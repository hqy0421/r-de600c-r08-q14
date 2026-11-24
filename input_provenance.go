package trdsql

// Provenance column names. They are reserved names: when provenance is
// enabled and an input already uses one of them, a numeric suffix is added
// so the provenance columns remain addressable and unambiguous.
const (
	// ProvenanceFileColumn is the source file name (concrete file after
	// glob expansion, or "-" for stdin).
	ProvenanceFileColumn = "trdsql_file"
	// ProvenanceLineColumn is the 1-based physical line where a record
	// starts in its source file.
	ProvenanceLineColumn = "trdsql_line"
	// ProvenanceLineEndColumn is the 1-based physical line where a record
	// ends. It equals the start line for single-line records and is larger
	// for records spanning quoted newlines.
	ProvenanceLineEndColumn = "trdsql_line_end"
	// ProvenanceTableColumn is the logical table identity: the table name
	// as referenced in the SQL statement. Rows of every file expanded from
	// the same glob share this value.
	ProvenanceTableColumn = "trdsql_table"
)

// provenanceColumnCount is the number of provenance columns appended to
// every row, in order: file, start line, end line, logical table.
const provenanceColumnCount = 4

// rowPosition is the physical location of one record in its source file.
// All line numbers are 1-based.
type rowPosition struct {
	// line is the physical line where the record starts.
	line int
	// lineEnd is the physical line where the record ends.
	lineEnd int
}

// positionedRow pairs a pre-read record with the physical position captured
// when it was parsed. Readers that expand one parsed document into several
// rows (JSON arrays, jq results) return these inside PreReadRow so the
// provenance wrapper can attribute each row correctly instead of reusing
// the reader's single "last position".
type positionedRow struct {
	row []any
	pos rowPosition
}

// positionedPreReader is implemented by readers whose pre-read rows carry
// their own physical positions.
type positionedPreReader interface {
	PreReadRowWithPosition() []positionedRow
}

// rowPositioner is implemented by readers that can report the physical
// line range of the record returned by the last ReadRow/PreReadRow call.
// Readers that cannot determine physical positions (for example fixed
// width readers that pre-scan the input) do not implement it; in that
// case the record sequence number is used as a best-effort position.
type rowPositioner interface {
	RowPosition() rowPosition
}

// provenanceReader wraps a Reader and appends source provenance columns
// to every emitted row: source file, physical start line, physical end
// line and logical table identity.
type provenanceReader struct {
	reader Reader

	fileName  string
	tableName string

	names []string
	types []string

	// originRow is reused when reading from the wrapped reader. It must be
	// sized to the wrapped reader's column count: the buffer handed to
	// ReadRow by the database layer is sized to this wrapper's wider schema
	// and would otherwise leave stale trailing slots for the inner reader.
	originRow []any

	// dataWidth, when positive, is the canonical number of data (non
	// provenance) columns shared by every file expanded from one table
	// reference. Rows of files with a different width are projected to it
	// before the provenance columns are appended: surplus columns are
	// truncated and missing columns are nil-padded. A zero value means the
	// wrapped reader's own width is used (single-file imports).
	dataWidth int

	// positions holds the physical location captured for each pre-read row,
	// in emission order. A reader buffers records during construction but
	// exposes its current position only via RowPosition, so the position is
	// snapshot while the pre-read rows are emitted rather than read later.
	positions []rowPosition

	// recordSeq counts records emitted so far and is used as a fallback
	// position when the wrapped reader cannot report physical lines.
	recordSeq int
}

// newProvenanceReader wraps r, attaching the given source file name and
// logical table name to every row.
func newProvenanceReader(r Reader, fileName, tableName string) *provenanceReader {
	columnNum := 0
	if names, err := r.Names(); err == nil {
		columnNum = len(names)
	}
	p := &provenanceReader{
		reader:    r,
		fileName:  fileName,
		tableName: tableName,
		originRow: make([]any, columnNum),
	}
	p.names = p.buildNames()
	p.types = p.buildTypes()
	return p
}

// setDataWidth pins the number of data columns emitted by this wrapper to
// width, matching the schema derived from the first file of a multi-file
// import. It must be called before any rows are read.
func (r *provenanceReader) setDataWidth(width int) {
	if width > 0 {
		r.dataWidth = width
	}
}

// normalizeData projects a data row onto the canonical data width. Wider
// rows are truncated (mirroring the single-stream import, which drops fields
// beyond the table schema) and narrower rows are nil-padded.
func (r *provenanceReader) normalizeData(row []any) []any {
	if r.dataWidth <= 0 || len(row) == r.dataWidth {
		return row
	}
	if len(row) > r.dataWidth {
		return row[:r.dataWidth]
	}
	out := make([]any, r.dataWidth)
	copy(out, row)
	return out
}

// uniqueName returns name, or name suffixed with a number if name already
// exists in used.
func uniqueName(name string, used map[string]bool) string {
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := name + itoa(i)
		if !used[candidate] {
			return candidate
		}
	}
}

func (r *provenanceReader) buildNames() []string {
	names, _ := r.reader.Names()
	used := make(map[string]bool, len(names)+4)
	for _, name := range names {
		used[name] = true
	}
	extra := []string{
		uniqueName(ProvenanceFileColumn, used),
	}
	used[extra[0]] = true
	extra = append(extra, uniqueName(ProvenanceLineColumn, used))
	used[extra[1]] = true
	extra = append(extra, uniqueName(ProvenanceLineEndColumn, used))
	used[extra[2]] = true
	extra = append(extra, uniqueName(ProvenanceTableColumn, used))
	return append(append([]string{}, names...), extra...)
}

func (r *provenanceReader) buildTypes() []string {
	types, _ := r.reader.Types()
	out := append([]string{}, types...)
	return append(out, DefaultDBType, "int", "int", DefaultDBType)
}

// Names returns the original column names plus the provenance columns.
func (r *provenanceReader) Names() ([]string, error) {
	return r.names, nil
}

// Types returns the original column types plus provenance column types.
func (r *provenanceReader) Types() ([]string, error) {
	return r.types, nil
}

// currentPosition returns the physical position of the wrapped reader's
// most recently emitted record, or a sequence-based fallback.
func (r *provenanceReader) currentPosition() rowPosition {
	r.recordSeq++
	pos := rowPosition{line: r.recordSeq, lineEnd: r.recordSeq}
	if rp, ok := r.reader.(rowPositioner); ok {
		if p := rp.RowPosition(); p.line > 0 {
			pos = p
		}
	}
	return pos
}

// provenanceValues builds the four provenance columns for a record at pos.
func (r *provenanceReader) provenanceValues(pos rowPosition) []any {
	return []any{
		r.fileName,
		pos.line,
		pos.lineEnd,
		r.tableName,
	}
}

// PreReadRow returns the pre-read rows with provenance columns appended.
// Readers implementing positionedPreReader provide a per-row physical
// position captured at parse time; otherwise the wrapped reader's current
// position is snapshot for each emitted row.
func (r *provenanceReader) PreReadRow() [][]any {
	if pr, ok := r.reader.(positionedPreReader); ok {
		prs := pr.PreReadRowWithPosition()
		out := make([][]any, len(prs))
		r.positions = make([]rowPosition, len(prs))
		for i, pr := range prs {
			r.recordSeq++
			r.positions[i] = pr.pos
			data := r.normalizeData(pr.row)
			out[i] = append(append([]any{}, data...), r.provenanceValues(pr.pos)...)
		}
		return out
	}

	rows := r.reader.PreReadRow()
	r.positions = make([]rowPosition, len(rows))
	out := make([][]any, len(rows))
	for i := range rows {
		pos := r.currentPosition()
		r.positions[i] = pos
		data := r.normalizeData(rows[i])
		out[i] = append(append([]any{}, data...), r.provenanceValues(pos)...)
	}
	return out
}

// ReadRow reads the next row with provenance columns appended.
// The passed-in buffer belongs to the database layer and is sized to this
// wrapper's schema; the wrapped reader receives a separate, correctly
// sized buffer instead.
func (r *provenanceReader) ReadRow(_ []any) ([]any, error) {
	origin, err := r.reader.ReadRow(r.originRow)
	if err != nil {
		return nil, err
	}
	if len(origin) == 0 {
		return nil, nil
	}
	pos := r.currentPosition()
	data := r.normalizeData(origin)
	return append(append([]any{}, data...), r.provenanceValues(pos)...), nil
}

// itoa is a small allocation-free integer to string helper sufficient for
// short suffix numbers.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// ensure provenanceReader satisfies the Reader interface.
var _ Reader = (*provenanceReader)(nil)
