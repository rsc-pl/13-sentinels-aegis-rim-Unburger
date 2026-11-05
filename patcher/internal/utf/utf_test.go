package utf_test

import (
	"testing"

	"13s-patcher/internal/utf"
)

func TestTableRoundTrip(t *testing.T) {
	tbl := &utf.Table{Name: []byte("T")}
	tbl.Cols = append(tbl.Cols,
		utf.Column{Flags: 0x50 | utf.TyStr, Name: []byte("Name")},
		utf.Column{Flags: 0x50 | utf.TyU32, Name: []byte("Value")},
		utf.Column{Flags: 0x50 | utf.TyU64, Name: []byte("Big")},
	)
	tbl.Rows = append(tbl.Rows, []utf.Value{
		{Type: utf.TyStr, S: []byte("alpha")},
		{Type: utf.TyU32, U: 42},
		{Type: utf.TyU64, U: 1 << 40},
	})
	tbl.Rows = append(tbl.Rows, []utf.Value{
		{Type: utf.TyStr, S: []byte("alpha")}, // duplicate string must de-duplicate
		{Type: utf.TyU32, U: 7},
		{Type: utf.TyU64, U: 9},
	})
	raw, err := utf.Build(tbl)
	if err != nil {
		t.Fatal(err)
	}
	got, err := utf.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount() != 2 {
		t.Fatalf("rows = %d, want 2", got.RowCount())
	}
	if got.ColIndex("Value") != 1 {
		t.Fatalf("ColIndex(Value) = %d", got.ColIndex("Value"))
	}
	if string(got.Rows[0][0].S) != "alpha" || got.Rows[0][1].U != 42 || got.Rows[1][2].U != 9 {
		t.Fatalf("round-trip mismatch: %+v", got.Rows)
	}
	// Rebuilding the parsed table must be byte-identical.
	raw2, err := utf.Build(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(raw2) {
		t.Fatal("rebuild is not byte-identical")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := utf.Parse([]byte("nope")); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := utf.Parse(nil); err == nil {
		t.Fatal("nil accepted")
	}
}
