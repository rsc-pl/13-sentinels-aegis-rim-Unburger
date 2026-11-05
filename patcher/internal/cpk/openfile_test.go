package cpk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/cpk"
	"13s-patcher/internal/synth"
)

// TestOpenFileMatchesParse proves the on-disk reader exposes the same member
// inventory and logical bytes as the in-memory parser, including a compressed
// member, and refuses to rebuild a file-backed archive.
func TestOpenFileMatchesParse(t *testing.T) {
	bank := synth.Bank([]string{"Alpha", strings.Repeat("compressed-payload ", 40), "Beta"})
	raw, err := synth.Archive([]synth.Member{
		{Dir: "Fms", Name: "ScriptText.fms", Logical: bank, Compress: true, ID: 3},
		{Dir: "GUI", Name: "title.ftx", Logical: []byte("raw-title-bytes"), ID: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "base.cpk")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	mem, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := cpk.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if disk.RowCount() != mem.RowCount() {
		t.Fatalf("row count %d != %d", disk.RowCount(), mem.RowCount())
	}
	for _, p := range []string{"Fms/ScriptText.fms", "GUI/title.ftx"} {
		a, b := disk.MemberByPath(p), mem.MemberByPath(p)
		if a == nil || b == nil {
			t.Fatalf("%s missing (disk=%v memory=%v)", p, a != nil, b != nil)
		}
		if a.FileSize != b.FileSize || a.ExtractSize != b.ExtractSize || a.FileOffset != b.FileOffset {
			t.Fatalf("%s identity differs: %+v vs %+v", p, a, b)
		}
		la, err := disk.Logical(a)
		if err != nil {
			t.Fatal(err)
		}
		lb, err := mem.Logical(b)
		if err != nil {
			t.Fatal(err)
		}
		if string(la) != string(lb) {
			t.Fatalf("%s logical bytes differ", p)
		}
	}
	if _, err := disk.Build(nil); err == nil {
		t.Fatal("rebuilding a file-backed archive must be refused")
	}
}
