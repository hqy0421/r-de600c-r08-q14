package trdsql

import (
	"strings"
	"testing"
)

func TestCSVReaderRowPosition(t *testing.T) {
	// A quoted record spans two physical lines.
	input := "1,a\n2,b\n3,\"x\ny\"\n4,d\n"
	r, err := NewCSVReader(strings.NewReader(input), NewReadOpts())
	if err != nil {
		t.Fatal(err)
	}
	// The default pre-read consumes the first record at construction.
	pre := r.PreReadRow()
	if len(pre) != 1 || r.RowPosition() != (rowPosition{line: 1, lineEnd: 1}) {
		t.Fatalf("pre-read position = %+v", r.RowPosition())
	}
	want := []rowPosition{
		{line: 2, lineEnd: 2},
		{line: 3, lineEnd: 4},
		{line: 5, lineEnd: 5},
	}
	row := make([]any, 2)
	for i, w := range want {
		got, err := r.ReadRow(row)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if len(got) == 0 {
			t.Fatalf("row %d empty", i)
		}
		if pos := r.RowPosition(); pos != w {
			t.Errorf("row %d position = %+v, want %+v", i, pos, w)
		}
	}
}

func TestCSVReaderRowPositionHeader(t *testing.T) {
	input := "id,name\n1,a\n2,b\n"
	opts := NewReadOpts(InHeader(true))
	r, err := NewCSVReader(strings.NewReader(input), opts)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]any, 2)
	got, err := r.ReadRow(row)
	if err != nil || len(got) == 0 {
		t.Fatalf("first data row: %v %v", got, err)
	}
	if pos := r.RowPosition(); pos != (rowPosition{line: 2, lineEnd: 2}) {
		t.Errorf("position after header = %+v, want line 2", pos)
	}
}
