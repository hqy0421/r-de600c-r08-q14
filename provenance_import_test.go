package trdsql

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProvenanceImport exercises the audit requirement: rows imported with
// provenance carry the concrete source file, exact physical line range and
// the shared logical table identity, all as ordinary SQL columns.
func TestProvenanceImport(t *testing.T) {
	db := newDBTestSqlite3()
	if db == nil {
		t.SkipNow()
	}
	var err error
	db.Tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Tx.Commit()
		_ = db.Disconnect()
	}()

	logical := filepath.Join(dataDir, "tt*.csv")
	tableName, err := ImportFile(db, logical, NewReadOpts(InProvenance(true)))
	if err != nil {
		t.Fatal(err)
	}
	if tableName != db.QuotedName(logical) {
		t.Fatalf("table name = %q, want %q", tableName, db.QuotedName(logical))
	}

	rows, err := db.Select(
		"SELECT " + db.QuotedName(ProvenanceFileColumn) +
			", " + db.QuotedName(ProvenanceLineColumn) +
			", " + db.QuotedName(ProvenanceTableColumn) +
			" FROM " + tableName +
			" ORDER BY " + db.QuotedName(ProvenanceFileColumn))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type got struct {
		file  string
		line  int
		table string
	}
	var actual []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.file, &g.line, &g.table); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, g)
	}
	// Each globbed file has one record on physical line 1; the logical table
	// is the glob reference for every row.
	for i, g := range actual {
		wantFile := filepath.Join(dataDir, "tt"+string(rune('1'+i))+".csv")
		if g.file != wantFile {
			t.Errorf("row %d file = %q, want %q", i, g.file, wantFile)
		}
		if g.line != 1 {
			t.Errorf("row %d line = %d, want 1", i, g.line)
		}
		if g.table != logical {
			t.Errorf("row %d table = %q, want %q", i, g.table, logical)
		}
	}
}

// TestProvenanceMultiLineQuote proves physical lines survive quoted
// newlines: the final record spans two lines.
func TestProvenanceMultiLineQuote(t *testing.T) {
	db := newDBTestSqlite3()
	if db == nil {
		t.SkipNow()
	}
	var err error
	db.Tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Tx.Commit()
		_ = db.Disconnect()
	}()

	tableName, err := ImportFile(db, filepath.Join(dataDir, "test_quote2.csv"),
		NewReadOpts(InProvenance(true), InFormat(CSV)))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Select(
		"SELECT " + db.QuotedName(ProvenanceLineColumn) +
			", " + db.QuotedName(ProvenanceLineEndColumn) +
			" FROM " + tableName)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var starts, ends []int
	for rows.Next() {
		var s, e int
		if err := rows.Scan(&s, &e); err != nil {
			t.Fatal(err)
		}
		starts = append(starts, s)
		ends = append(ends, e)
	}
	wantStarts := []int{1, 2, 3, 4}
	wantEnds := []int{1, 2, 3, 5}
	if !equalInts(starts, wantStarts) || !equalInts(ends, wantEnds) {
		t.Errorf("line spans = %v/%v, want %v/%v", starts, ends, wantStarts, wantEnds)
	}
}

// TestProvenanceGroupBy confirms the source columns are usable in SQL
// aggregation, the auditor's primary workflow.
func TestProvenanceGroupBy(t *testing.T) {
	db := newDBTestSqlite3()
	if db == nil {
		t.SkipNow()
	}
	var err error
	db.Tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Tx.Commit()
		_ = db.Disconnect()
	}()

	logical := filepath.Join(dataDir, "tt*.csv")
	tableName, err := ImportFile(db, logical, NewReadOpts(InProvenance(true)))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Select(
		"SELECT count(*) FROM " + tableName +
			" GROUP BY " + db.QuotedName(ProvenanceTableColumn))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	rows.Next()
	var n int
	if err := rows.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("grouped count = %d, want 3 rows across the glob", n)
	}
}

// TestProvenanceOffByDefault ensures ordinary imports do not gain columns.
func TestProvenanceOffByDefault(t *testing.T) {
	db := newDBTestSqlite3()
	if db == nil {
		t.SkipNow()
	}
	var err error
	db.Tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Tx.Commit()
		_ = db.Disconnect()
	}()
	tableName, err := ImportFile(db, filepath.Join(dataDir, "test.csv"), NewReadOpts())
	if err != nil {
		t.Fatal(err)
	}
	cols, err := tableColumns(db, tableName)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if c == ProvenanceFileColumn || c == ProvenanceLineColumn {
			t.Errorf("provenance column %q present without InProvenance", c)
		}
	}
}

// TestProvenanceHeterogeneousWidth covers a glob of files whose column
// counts differ. The table is created from the first file's schema, so rows
// of later files must be projected onto it: surplus columns dropped, missing
// columns nil-padded — while every provenance column survives. This mirrors
// the truncation behavior of the non-provenance concatenated import instead
// of failing the bulk insert with a placeholder-count mismatch.
func TestProvenanceHeterogeneousWidth(t *testing.T) {
	dir := t.TempDir()
	// First file defines a two-column schema.
	if err := os.WriteFile(filepath.Join(dir, "w1.csv"),
		[]byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Second file is wider: its third column must be dropped.
	if err := os.WriteFile(filepath.Join(dir, "w2.csv"),
		[]byte("a,b,c\n3,4,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Third file is narrower: its missing third position stays NULL only if
	// the first file had been wider; here it must simply import unchanged.
	if err := os.WriteFile(filepath.Join(dir, "w3.csv"),
		[]byte("a,b\n6,7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := newDBTestSqlite3()
	if db == nil {
		t.SkipNow()
	}
	var err error
	db.Tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Tx.Commit()
		_ = db.Disconnect()
	}()

	logical := filepath.Join(dir, "w*.csv")
	tableName, err := ImportFile(db, logical, NewReadOpts(InProvenance(true), InHeader(true)))
	if err != nil {
		t.Fatalf("heterogeneous import failed: %v", err)
	}

	// The table must expose exactly the first file's data columns plus the
	// four provenance columns; the wider file's "c" must not leak in.
	cols, err := tableColumns(db, tableName)
	if err != nil {
		t.Fatal(err)
	}
	wantCols := []string{"a", "b",
		ProvenanceFileColumn, ProvenanceLineColumn,
		ProvenanceLineEndColumn, ProvenanceTableColumn}
	if len(cols) != len(wantCols) {
		t.Fatalf("columns = %v, want %v", cols, wantCols)
	}
	for i, c := range wantCols {
		if cols[i] != c {
			t.Errorf("column %d = %q, want %q (full: %v)", i, cols[i], c, cols)
		}
	}

	rows, err := db.Select(
		"SELECT " + db.QuotedName("a") + ", " + db.QuotedName("b") +
			", " + db.QuotedName(ProvenanceFileColumn) +
			", " + db.QuotedName(ProvenanceLineColumn) +
			" FROM " + tableName +
			" ORDER BY " + db.QuotedName(ProvenanceFileColumn))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type rec struct {
		a, b, file string
		line       int
	}
	var got []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.a, &r.b, &r.file, &r.line); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := []rec{
		{"1", "2", filepath.Join(dir, "w1.csv"), 2},
		{"3", "4", filepath.Join(dir, "w2.csv"), 2},
		{"6", "7", filepath.Join(dir, "w3.csv"), 2},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// TestProvenanceNarrowerAfterWider proves nil-padding: when the first file
// defines three columns, a later two-column file must load with a NULL in
// the trailing data column, still carrying intact provenance columns.
func TestProvenanceNarrowerAfterWider(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "n1.csv"),
		[]byte("a,b,c\n1,2,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "n2.csv"),
		[]byte("a,b\n4,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := newDBTestSqlite3()
	if db == nil {
		t.SkipNow()
	}
	var err error
	db.Tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Tx.Commit()
		_ = db.Disconnect()
	}()

	tableName, err := ImportFile(db, filepath.Join(dir, "n*.csv"),
		NewReadOpts(InProvenance(true), InHeader(true)))
	if err != nil {
		t.Fatalf("narrow-after-wide import failed: %v", err)
	}
	rows, err := db.Select(
		"SELECT " + db.QuotedName("a") + ", " + db.QuotedName("b") +
			", " + db.QuotedName("c") +
			", " + db.QuotedName(ProvenanceFileColumn) +
			" FROM " + tableName +
			" ORDER BY " + db.QuotedName(ProvenanceFileColumn))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type rec struct {
		a, b string
		c    *string
		file string
	}
	var got []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.a, &r.b, &r.c, &r.file); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %#v, want 2 records", got)
	}
	if got[0].c == nil || *got[0].c != "3" {
		t.Errorf("first row c = %v, want 3", got[0].c)
	}
	if got[1].c != nil {
		t.Errorf("narrow row c = %v, want NULL", *got[1].c)
	}
	if got[1].a != "4" || got[1].b != "5" {
		t.Errorf("narrow row data = (%q,%q), want (4,5)", got[1].a, got[1].b)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tableColumns returns the column names of a temporary table. It uses the
// sqlite PRAGMA, which is the default driver for these tests.
func tableColumns(db *DB, tableName string) ([]string, error) {
	unquoted := tableName
	if len(unquoted) >= 2 {
		unquoted = unquoted[1 : len(unquoted)-1]
	}
	rows, err := db.Select("PRAGMA table_info(" + db.QuotedName(unquoted) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, nil
}
