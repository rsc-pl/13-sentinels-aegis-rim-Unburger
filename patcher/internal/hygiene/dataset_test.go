package hygiene_test

import (
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/hygiene"
)

// TestShippedDatasetIsClean scans the shipped translation dataset when it is
// present in the surrounding checkout. changes.jsonl is checked field-aware, so
// authored English text may contain ordinary words; the manifest, its schema and
// the readme are checked in full.
func TestShippedDatasetIsClean(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "translations")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Skip("no shipped dataset next to the module")
	}
	findings, err := hygiene.ScanDataset(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f.String())
	}
}

// TestScanDatasetIgnoresAuthoredText proves the field-aware rule: a change whose
// text contains an ordinary English word that collides with a marker is not a
// finding, while the same word in a member path is.
func TestScanDatasetIgnoresAuthoredText(t *testing.T) {
	dir := t.TempDir()
	markerWord := "ow" + "ner"
	records := `{"bank_key":"fms/scripttext.fms","record_index":1,"text":"the ` + markerWord + ` of these glasses"}` + "\n" +
		`{"bank_key": "fms/scripttext.fms", "record_index": 2, "text": "the ` + markerWord + ` writes with spaces"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "changes.jsonl"), []byte(records), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := hygiene.ScanDataset(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("authored text must not be scanned: %v", findings)
	}
	// A marker in the bank path must still be found.
	bad := `{"bank_key":"fms/` + markerWord + `.fms","record_index":1,"text":"hi"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "changes.jsonl"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err = hygiene.ScanDataset(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a marker in the bank path must be reported")
	}
}
