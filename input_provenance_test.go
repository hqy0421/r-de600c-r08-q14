package trdsql

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestOffsetLineReader(t *testing.T) {
	r := newOffsetLineReader(strings.NewReader("ab\ncde\nfg"))
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ab\ncde\nfg" {
		t.Fatalf("stream content changed: %q", data)
	}
	// line starts: 0 -> 1, 3 -> 2, 7 -> 3
	cases := []struct {
		offset int64
		want   int
	}{
		{0, 1}, {2, 1}, {3, 2}, {6, 2}, {7, 3}, {8, 3},
	}
	for _, c := range cases {
		if got := r.lineAt(c.offset); got != c.want {
			t.Errorf("lineAt(%d) = %d, want %d", c.offset, got, c.want)
		}
	}
	if !r.isLineStart(3) || r.isLineStart(4) {
		t.Error("isLineStart boundary wrong")
	}
}

func TestProvenanceReaderNames(t *testing.T) {
	inner := &CSVReader{
		names:   []string{"a", "b"},
		types:   []string{DefaultDBType, DefaultDBType},
		preRead: [][]string{{"1", "2"}},
	}
	r := newProvenanceReader(inner, "src.csv", "tbl*.csv")
	wantNames := []string{"a", "b",
		ProvenanceFileColumn, ProvenanceLineColumn,
		ProvenanceLineEndColumn, ProvenanceTableColumn}
	if got, _ := r.Names(); !reflect.DeepEqual(got, wantNames) {
		t.Errorf("Names() = %v, want %v", got, wantNames)
	}
	if got, _ := r.Types(); !reflect.DeepEqual(got,
		[]string{DefaultDBType, DefaultDBType, DefaultDBType, "int", "int", DefaultDBType}) {
		t.Errorf("Types() = %v", got)
	}
}

func TestProvenanceReaderNameCollision(t *testing.T) {
	inner := &CSVReader{
		names:   []string{ProvenanceFileColumn, ProvenanceLineColumn},
		types:   []string{DefaultDBType, DefaultDBType},
		preRead: [][]string{{"f", "9"}},
	}
	r := newProvenanceReader(inner, "src", "tbl")
	got, _ := r.Names()
	want := []string{
		ProvenanceFileColumn, ProvenanceLineColumn,
		ProvenanceFileColumn + "2", ProvenanceLineColumn + "2",
		ProvenanceLineEndColumn, ProvenanceTableColumn,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

// TestProvenanceReaderDataWidth pins the wrapper to a canonical data width,
// as happens when a glob expands to files with different column counts.
// Wider rows are truncated, narrower rows nil-padded, and the four
// provenance columns always remain the trailing four values.
func TestProvenanceReaderDataWidth(t *testing.T) {
	cases := []struct {
		name      string
		dataWidth int
		inner     [][]any
		wantData  []any
	}{
		{"wider row truncated", 2, [][]any{{"a", "b", "c"}}, []any{"a", "b"}},
		{"narrower row padded", 3, [][]any{{"a"}}, []any{"a", nil, nil}},
		{"equal width unchanged", 2, [][]any{{"a", "b"}}, []any{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &stubReader{
				names:    []string{"c1", "c2", "c3"},
				types:    []string{DefaultDBType, DefaultDBType, DefaultDBType},
				restRows: c.inner,
			}
			r := newProvenanceReader(stub, "f", "t")
			r.setDataWidth(c.dataWidth)

			pre := r.PreReadRow()
			if len(pre) != 0 {
				t.Fatalf("unexpected pre-read rows: %#v", pre)
			}
			got, err := r.ReadRow(make([]any, 3))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.dataWidth+provenanceColumnCount {
				t.Fatalf("row width = %d, want %d", len(got), c.dataWidth+provenanceColumnCount)
			}
			want := append(append([]any{}, c.wantData...), "f", 1, 1, "t")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ReadRow() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestProvenanceReaderRowsAndFallback(t *testing.T) {
	// CSVReader without position tracking would implement rowPositioner;
	// here use a tiny stub reader to verify the sequence fallback and that
	// provenance columns are appended to both pre-read and streamed rows.
	stub := &stubReader{
		names:    []string{"c1"},
		types:    []string{DefaultDBType},
		preRows:  [][]any{{"p1"}},
		restRows: [][]any{{"r1"}, {"r2"}},
	}
	r := newProvenanceReader(stub, "f", "t")

	pre := r.PreReadRow()
	wantPre := [][]any{{"p1", "f", 1, 1, "t"}}
	if !reflect.DeepEqual(pre, wantPre) {
		t.Errorf("PreReadRow() = %#v, want %#v", pre, wantPre)
	}
	var got [][]any
	row := make([]any, 1)
	for {
		gotRow, err := r.ReadRow(row)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, gotRow)
	}
	wantRest := [][]any{
		{"r1", "f", 2, 2, "t"},
		{"r2", "f", 3, 3, "t"},
	}
	if !reflect.DeepEqual(got, wantRest) {
		t.Errorf("ReadRow() = %#v, want %#v", got, wantRest)
	}
}

// stubReader is a minimal Reader without rowPositioner, used to exercise the
// sequence-number fallback.
type stubReader struct {
	names    []string
	types    []string
	preRows  [][]any
	restRows [][]any
	idx      int
}

func (s *stubReader) Names() ([]string, error) { return s.names, nil }
func (s *stubReader) Types() ([]string, error) { return s.types, nil }
func (s *stubReader) PreReadRow() [][]any      { return s.preRows }
func (s *stubReader) ReadRow(row []any) ([]any, error) {
	if s.idx >= len(s.restRows) {
		return nil, io.EOF
	}
	r := s.restRows[s.idx]
	s.idx++
	return r, nil
}
