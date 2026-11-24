package trdsql

// Convert JSON to a table.
// Supports the following JSON container types.
// * Array ([{c1: 1}, {c1: 2}, {c1: 3}])
// * Multiple JSON ({c1: 1}\n {c1: 2}\n {c1: 3}\n)

// Make a table from json
// or make the result of json filter by jq.
import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"

	"github.com/itchyny/gojq"
)

// JSONReader parses JSON objects and arrays as tabular input.
type JSONReader struct {
	reader     *json.Decoder
	query      *gojq.Query
	already    map[string]bool
	inNULL     string
	preRead    []map[string]any
	preReadPos []rowPosition
	names      []string
	types      []string
	limitRead  bool
	needNULL   bool
	lastPos    rowPosition
	*offsetLineReader
}

// NewJSONReader returns a JSONReader configured with input options and jq filter.
func NewJSONReader(reader io.Reader, opts *ReadOpts) (*JSONReader, error) {
	r := &JSONReader{}
	r.offsetLineReader = newOffsetLineReader(reader)
	r.reader = json.NewDecoder(r.offsetLineReader)
	r.reader.UseNumber()
	r.already = make(map[string]bool)
	var top any

	if opts.InJQuery != "" {
		query, err := jqParse(opts.InJQuery)
		if err != nil {
			return nil, err
		}
		r.query = query
	}

	r.limitRead = opts.InLimitRead
	r.needNULL = opts.InNeedNULL
	r.inNULL = opts.InNULL

	for i := 0; i < opts.InPreRead; i++ {
		startOffset := r.reader.InputOffset()
		if err := r.reader.Decode(&top); err != nil {
			if !errors.Is(err, io.EOF) {
				return r, fmt.Errorf("%w: %s", ErrInvalidJSON, err)
			}
			debug.Print(err.Error())
			return r, nil
		}
		r.trackDoc(startOffset, r.reader.InputOffset())

		if r.query != nil {
			if err := r.jqueryRun(top); err != nil {
				return nil, err
			}
			r.markPreReadPosition()
			return r, nil
		}

		if err := r.readAhead(top); err != nil {
			return nil, err
		}
		r.markPreReadPosition()
	}

	return r, nil
}

// trackDoc records the physical line span of the JSON document occupying
// byte range [startOffset, endOffset). Rows expanded from one document (a
// JSON array or a jq result) inherit the document's span; JSONL documents
// occupy one line each.
func (r *JSONReader) trackDoc(startOffset, endOffset int64) {
	startLine := r.contentStartLine(startOffset)
	// The end offset is the first byte after the document. When it is the
	// newline terminating the last record line, the document ends on the
	// current line; when it starts the next line already, use that offset.
	endLine := r.lineAt(endOffset)
	if endLine > startLine && r.isLineStart(endOffset) {
		endLine--
	}
	r.lastPos = rowPosition{line: startLine, lineEnd: endLine}
	// Prune only after lookups: the entry at startOffset itself is needed.
	r.pruneBefore(startOffset + 1)
}

// contentStartLine resolves the physical line where document content
// begins. The decoder's start offset may sit inside the whitespace gap
// between top-level documents; every line terminator in that gap produces a
// consecutive line-start entry, so walk the run of entries whose offsets
// advance one byte at a time from startOffset. The last entry in the run is
// the start of the first content line.
func (r *JSONReader) contentStartLine(startOffset int64) int {
	if startOffset == 0 || !r.isLineStart(startOffset) && !r.isLineStart(startOffset+1) && !r.isLineStart(startOffset+2) {
		return r.lineAt(startOffset)
	}
	// Locate the first entry at or after startOffset.
	idx := sort.Search(len(r.entries), func(i int) bool {
		return r.entries[i].offset >= startOffset
	})
	want := startOffset
	line := r.lineAt(startOffset)
	for j := idx; j < len(r.entries); j++ {
		if r.entries[j].offset != want+1 {
			break
		}
		want++
		line = r.entries[j].lineNo
	}
	if line == r.lineAt(startOffset) && r.isLineStart(startOffset) {
		// startOffset itself begins a line (JSONL without preceding gap).
		line++
	}
	return line
}

// markPreReadPosition attaches the current document's physical span to all
// rows buffered during the most recent readAhead call.
func (r *JSONReader) markPreReadPosition() {
	for len(r.preReadPos) < len(r.preRead) {
		r.preReadPos = append(r.preReadPos, r.lastPos)
	}
}

// RowPosition returns the physical line range of the last decoded document.
func (r *JSONReader) RowPosition() rowPosition {
	return r.lastPos
}

// jqParse parses a string and returns a *gojq.Query.
func jqParse(q string) (*gojq.Query, error) {
	if q == "" {
		return nil, nil
	}
	str := trimQuoteAll(q)
	query, err := gojq.Parse(str)
	if err != nil {
		return nil, fmt.Errorf("%w gojq:(%s)", err, str)
	}
	debug.Print("Parsed gojq query: ", str)
	return query, nil
}

// Names returns column names.
func (r *JSONReader) Names() ([]string, error) {
	return r.names, nil
}

// Types returns column types.
// All JSON types return the DefaultDBType.
func (r *JSONReader) Types() ([]string, error) {
	r.types = make([]string, len(r.names))
	for i := 0; i < len(r.names); i++ {
		r.types[i] = DefaultDBType
	}
	return r.types, nil
}

// readAhead parses the top level of the JSON and stores it in preRead.
func (r *JSONReader) readAhead(top any) error {
	switch m := top.(type) {
	case []any:
		// []
		r.preRead = make([]map[string]any, 0, len(m))
		if r.reader.More() {
			pre, names, err := r.etcRow(m)
			if err != nil {
				return err
			}
			r.appendNames(names)
			r.preRead = append(r.preRead, pre)
			return nil
		}

		for _, v := range m {
			pre, names, err := r.topLevel(v)
			if err != nil {
				return err
			}
			r.appendNames(names)
			r.preRead = append(r.preRead, pre)
		}
		return nil
	default:
		pre, names, err := r.topLevel(m)
		if err != nil {
			return err
		}
		r.appendNames(names)
		r.preRead = append(r.preRead, pre)
	}
	return nil
}

// appendNames adds multiple names for the argument to be unique.
func (r *JSONReader) appendNames(names []string) {
	for _, name := range names {
		if !r.already[name] {
			r.already[name] = true
			r.names = append(r.names, name)
		}
	}
}

func (r *JSONReader) topLevel(top any) (map[string]any, []string, error) {
	switch obj := top.(type) {
	case map[string]any:
		return r.objectRow(obj)
	default:
		return r.etcRow(obj)
	}
}

// PreReadRow returns only columns that store preread rows.
// One json (not jsonl) returns all rows with preRead.
func (r *JSONReader) PreReadRow() [][]any {
	rows := make([][]any, len(r.preRead))
	for n, v := range r.preRead {
		rows[n] = make([]any, len(r.names))
		for i := range r.names {
			rows[n][i] = v[r.names[i]]
			if r.needNULL {
				rows[n][i] = replaceNULL(r.inNULL, rows[n][i])
			}
		}
	}
	return rows
}

// PreReadRowWithPosition returns the pre-read rows paired with the physical
// line span of the document each row came from.
func (r *JSONReader) PreReadRowWithPosition() []positionedRow {
	rows := r.PreReadRow()
	out := make([]positionedRow, len(rows))
	for n := range rows {
		pos := r.lastPos
		if n < len(r.preReadPos) {
			pos = r.preReadPos[n]
		}
		out[n] = positionedRow{row: rows[n], pos: pos}
	}
	return out
}

// ReadRow reads the rest of the row.
// Only jsonl requires ReadRow in json.
func (r *JSONReader) ReadRow(row []any) ([]any, error) {
	if r.limitRead {
		return nil, io.EOF
	}

	var data any
	startOffset := r.reader.InputOffset()
	if err := r.reader.Decode(&data); err != nil {
		return nil, err
	}
	r.trackDoc(startOffset, r.reader.InputOffset())

	if r.query != nil {
		return r.jqueryRunJsonl(row, data)
	}
	return r.rowParse(row, data), nil
}

func (r *JSONReader) rowParse(row []any, jsonRow any) []any {
	switch m := jsonRow.(type) {
	case map[string]any:
		for i := range r.names {
			row[i] = r.jsonString(m[r.names[i]])
		}
	default:
		for i := range r.names {
			row[i] = nil
		}
		row[0] = r.jsonString(jsonRow)
	}
	return row
}

func (r *JSONReader) objectRow(obj map[string]any) (map[string]any, []string, error) {
	// {"a":"b"} object
	names := make([]string, 0, len(obj))
	row := make(map[string]any)
	for k, v := range obj {
		names = append(names, k)
		row[k] = r.jsonString(v)
	}
	return row, names, nil
}

func (r *JSONReader) etcRow(val any) (map[string]any, []string, error) {
	// ex. array array
	// [["a"],
	//  ["b"]]
	var names []string
	k := "c1"
	names = append(names, k)
	row := make(map[string]any)
	row[k] = r.jsonString(val)
	return row, names, nil
}

// jqueryRun is a gojq.Run for json.
func (r *JSONReader) jqueryRun(top any) error {
	iter := r.query.Run(top)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return fmt.Errorf("%w gojq:(%s) ", err, r.query)
		}
		if err := r.readAhead(v); err != nil {
			return err
		}
	}
	return nil
}

// jqueryRunJsonl gojq.Run for rows of jsonl.
func (r *JSONReader) jqueryRunJsonl(row []any, jsonRow any) ([]any, error) {
	iter := r.query.Run(jsonRow)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			debug.Printf("%s gojq: %s", err.Error(), r.query)
			continue
		}
		row = r.rowParse(row, v)
	}
	return row, nil
}

// jsonString returns the string of the argument.
func (r *JSONReader) jsonString(val any) any {
	var str string
	switch val.(type) {
	case nil:
		return nil
	case map[string]any, []any:
		b, err := json.Marshal(val)
		if err != nil {
			log.Printf("ERROR: jsonString:%s", err)
		}
		str = ValString(b)
	default:
		str = ValString(val)
	}
	if r.needNULL {
		return replaceNULL(r.inNULL, str)
	}
	return str
}
