package trdsql

import (
	"io"
	"log"
)

// multiFileReader chains several Readers into one. It is used when a single
// logical table reference expands to multiple concrete files: each child
// reader carries its own source provenance, while the database layer sees
// one continuous row stream and therefore one prepared insert statement.
type multiFileReader struct {
	readers []Reader
	closers []io.Closer
	index   int
	closed  bool
}

// newMultiFileReader returns a Reader concatenating the given readers.
// closers, when non-nil, are closed once after the last reader is consumed.
func newMultiFileReader(readers []Reader, closers ...io.Closer) *multiFileReader {
	return &multiFileReader{readers: readers, closers: closers}
}

// Names returns the column names of the first reader; all files expanded
// from one table reference share the same schema (including provenance
// columns appended in the same order).
func (r *multiFileReader) Names() ([]string, error) {
	if r.index >= len(r.readers) {
		return r.readers[0].Names()
	}
	return r.readers[r.index].Names()
}

// Types returns the column types of the first reader.
func (r *multiFileReader) Types() ([]string, error) {
	if r.index >= len(r.readers) {
		return r.readers[0].Types()
	}
	return r.readers[r.index].Types()
}

// PreReadRow returns the pre-read rows of every child reader in order, so
// no rows buffered during schema detection are lost at file boundaries.
func (r *multiFileReader) PreReadRow() [][]any {
	var rows [][]any
	for _, reader := range r.readers {
		rows = append(rows, reader.PreReadRow()...)
	}
	return rows
}

// ReadRow reads across child readers, advancing to the next file whenever
// the current one reaches EOF.
func (r *multiFileReader) ReadRow(row []any) ([]any, error) {
	for r.index < len(r.readers) {
		got, err := r.readers[r.index].ReadRow(row)
		if err == nil {
			if len(got) == 0 {
				continue
			}
			return got, nil
		}
		if err != io.EOF {
			return got, err
		}
		r.index++
	}
	r.close()
	return nil, io.EOF
}

// close releases every underlying file exactly once.
func (r *multiFileReader) close() {
	if r.closed {
		return
	}
	r.closed = true
	for _, c := range r.closers {
		if err := c.Close(); err != nil {
			log.Printf("file close:%s", err)
		}
	}
}

var _ Reader = (*multiFileReader)(nil)
