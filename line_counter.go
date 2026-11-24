package trdsql

// lineNumberReader counts physical lines of records read from a
// bufio.Reader. Unlike counting newlines at the underlying io.Reader
// (which is defeated by bufio over-reading into its buffer), the counter
// advances only when a record boundary is actually consumed, so the
// reported numbers always match the lines the parser has reached.
//
// A physical line is delimited by '\n'; a CRLF sequence counts as one
// line break. Blank lines increment the counter as well and are simply
// skipped by formats that ignore them.
type lineNumberReader struct {
	// physicalLine is the 1-based physical line on which the most recently
	// completed record ends.
	physicalLine int
}

// beginLine returns the physical line where the next record starts.
func (l *lineNumberReader) beginLine() int {
	return l.physicalLine + 1
}

// finishRecord marks a record whose last physical line is the current one.
func (l *lineNumberReader) finishRecord(startLine int) rowPosition {
	if l.physicalLine < startLine {
		l.physicalLine = startLine
	}
	return rowPosition{line: startLine, lineEnd: l.physicalLine}
}

// consumeLine accounts for one physical line consumed by the parser.
func (l *lineNumberReader) consumeLine() {
	l.physicalLine++
}
