package trdsql

import (
	"io"
	"sort"
)

// offsetLineReader wraps an io.Reader and records, for every newline seen,
// the absolute byte offset just after it and the physical line number that
// begins there. It answers line numbers for arbitrary byte offsets, which
// stays correct even when a decoder (encoding/json, a YAML parser) reads far
// ahead of the record it is returning: the decoder's stream offset is mapped
// back onto the recorded lines rather than counting lines greedily at read
// time.
//
// Retired index entries are pruned while keeping a bounded window, so a
// long stream does not retain an entry per line for the whole file.
type offsetLineReader struct {
	r io.Reader

	absPos    int64
	lineCount int
	entries   []lineEntry
}

// lineEntry maps the byte offset at the beginning of a physical line to
// that 1-based line number.
type lineEntry struct {
	offset int64
	lineNo int
}

// newOffsetLineReader wraps r with an index positioned before line 1.
func newOffsetLineReader(r io.Reader) *offsetLineReader {
	return &offsetLineReader{
		r:       r,
		entries: []lineEntry{{offset: 0, lineNo: 1}},
	}
}

// Read forwards to the wrapped reader and indexes newline bytes.
func (o *offsetLineReader) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == '\n' {
			o.lineCount++
			o.entries = append(o.entries, lineEntry{
				offset: o.absPos + int64(i) + 1,
				lineNo: o.lineCount + 1,
			})
		}
	}
	o.absPos += int64(n)
	return n, err
}

// lineAt returns the 1-based physical line containing byte offset.
func (o *offsetLineReader) lineAt(offset int64) int {
	idx := sort.Search(len(o.entries), func(i int) bool {
		return o.entries[i].offset > offset
	})
	return o.entries[idx-1].lineNo
}

// isLineStart reports whether offset begins a physical line.
func (o *offsetLineReader) isLineStart(offset int64) bool {
	idx := sort.Search(len(o.entries), func(i int) bool {
		return o.entries[i].offset >= offset
	})
	return idx < len(o.entries) && o.entries[idx].offset == offset
}

// pruneBefore discards index entries at offsets strictly below the
// argument, retaining the entry of the line immediately preceding it so
// later lineAt lookups still resolve correctly.
func (o *offsetLineReader) pruneBefore(offset int64) {
	keep := sort.Search(len(o.entries), func(i int) bool {
		return o.entries[i].offset >= offset
	})
	if keep > 1 {
		o.entries = o.entries[keep-1:]
	}
}
