package trdsql

import (
	"bufio"
	"bytes"
	"io"
)

// yamlDocument is one YAML document together with the physical line range
// it occupies in the source stream.
type yamlDocument struct {
	data    []byte
	startLn int
	endLn   int
}

// yamlDocSplitter reads a YAML stream and yields one document at a time.
// Documents are separated by line-start "---" (and ended by "..."), which
// is the only robust way to recover physical line positions because the
// goccy YAML decoder does not report its input offset. Blank lines and
// document markers are accounted for, so reported line numbers match the
// source file exactly.
type yamlDocSplitter struct {
	reader *bufio.Reader
	docs   []yamlDocument
	index  int
	err    error
}

// newYAMLDocSplitter wraps r and parses document boundaries lazily.
func newYAMLDocSplitter(r io.Reader) *yamlDocSplitter {
	return &yamlDocSplitter{reader: bufio.NewReader(r)}
}

// preload scans the stream into documents with their physical line ranges.
func (s *yamlDocSplitter) preload() error {
	var (
		buf        bytes.Buffer
		docStartLn int
		hasContent bool
	)
	physical := 0
	flush := func(endLn int) {
		if !hasContent {
			return
		}
		s.docs = append(s.docs, yamlDocument{
			data:    append([]byte(nil), buf.Bytes()...),
			startLn: docStartLn,
			endLn:   endLn,
		})
		buf.Reset()
		hasContent = false
	}

	for {
		line, err := s.reader.ReadBytes('\n')
		if len(line) > 0 {
			physical++
			trimmed := bytes.TrimRight(line, "\r\n")
			switch {
			case bytes.HasPrefix(trimmed, []byte("---")) && isDocMarker(trimmed):
				flush(physical - 1)
				docStartLn = physical + 1
			case bytes.HasPrefix(trimmed, []byte("...")) && isDocMarker(trimmed):
				flush(physical)
				docStartLn = physical + 1
			default:
				if !hasContent && len(bytes.TrimSpace(line)) > 0 {
					docStartLn = physical
				}
				if len(bytes.TrimSpace(line)) > 0 {
					hasContent = true
				}
				buf.Write(line)
			}
		}
		if err != nil {
			flush(physical)
			if err == io.EOF {
				s.err = io.EOF
				return nil
			}
			s.err = err
			return err
		}
	}
}

// next returns the next document, or io.EOF when the stream is exhausted.
func (s *yamlDocSplitter) next() (yamlDocument, error) {
	if s.index < len(s.docs) {
		doc := s.docs[s.index]
		s.index++
		return doc, nil
	}
	if s.err != nil {
		return yamlDocument{}, s.err
	}
	return yamlDocument{}, io.EOF
}

// isDocMarker reports whether line is a bare document marker ("---" or
// "...") optionally followed by trailing spaces or a comment.
func isDocMarker(line []byte) bool {
	rest := bytes.TrimSpace(line[3:])
	return len(rest) == 0 || rest[0] == '#'
}
