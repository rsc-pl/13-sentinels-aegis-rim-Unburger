package patchdata_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/patchdata"
)

func writeManifest(t *testing.T, dir string, m patchdata.Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, patchdata.ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func baseManifest() patchdata.Manifest {
	return patchdata.Manifest{
		SpecVersion:    patchdata.SpecVersion,
		BundleID:       "b1",
		BundleVersion:  "1.0.0",
		ReleaseVersion: "r1",
		FormatVersion:  patchdata.SupportedFormatVersion,
		Sources:        []patchdata.SourceSpec{{Role: "patch_archive", Size: 10, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Output:         patchdata.OutputSpec{PreserveUnmodified: true},
	}
}

func TestExportAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := patchdata.ExportInput{
		BundleID: "example-1", BundleVersion: "1.0.0", ReleaseVersion: "2026.1",
		Game:          patchdata.GameInfo{Title: "Example", Region: "US", BaseVersion: "1.10"},
		FormatVersion: patchdata.SupportedFormatVersion,
		Sources:       []patchdata.SourceSpec{{Role: "patch_archive", Size: 10, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Operations: []patchdata.ExportOperation{
			{ID: "op-1", Member: "Fms/A.fms", RecordIndex: 0, Original: "Hello", Replacement: "Hi", PreserveTokens: true},
			{ID: "op-2", Member: "Fms/B.fms", RecordIndex: 3, Original: "Bye", Replacement: "Farewell", PreserveTokens: true},
		},
	}
	b, err := patchdata.ExportBundle(dir, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.OperationCount() != 2 {
		t.Fatalf("operations = %d", b.OperationCount())
	}
	if b.Replacement["op-1"] != "Hi" || b.Replacement["op-2"] != "Farewell" {
		t.Fatalf("replacements not resolved: %+v", b.Replacement)
	}
	// The exported bundle must load cleanly from disk.
	b2, err := patchdata.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Manifest.BundleVersion != "1.0.0" || b2.OperationCount() != 2 {
		t.Fatalf("reload mismatch: %+v", b2.Manifest)
	}
	if _, ok := b2.SourceByRole("patch_archive"); !ok {
		t.Fatal("source role not found")
	}
	// Export must be deterministic.
	dir2 := t.TempDir()
	if _, err := patchdata.ExportBundle(dir2, in); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, patchdata.ManifestName))
	c, _ := os.ReadFile(filepath.Join(dir2, patchdata.ManifestName))
	if string(a) != string(c) {
		t.Fatal("export is not deterministic")
	}
}

func TestLoadDetectsTamperedContent(t *testing.T) {
	dir := t.TempDir()
	in := patchdata.ExportInput{
		BundleID: "b", BundleVersion: "1", FormatVersion: patchdata.SupportedFormatVersion,
		Sources:    []patchdata.SourceSpec{{Role: "patch_archive", Size: 1, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Operations: []patchdata.ExportOperation{{ID: "op-1", Member: "Fms/A.fms", RecordIndex: 0, Original: "a", Replacement: "bb", PreserveTokens: true}},
	}
	if _, err := patchdata.ExportBundle(dir, in); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "content", "op-1.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("tampered content accepted")
	}
}

func TestValidationRejections(t *testing.T) {
	dir := t.TempDir()
	m := baseManifest()

	// wrong spec version
	m.SpecVersion = 99
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("bad spec version accepted")
	}

	// duplicate operation ids
	m = baseManifest()
	m.Operations = []patchdata.Operation{
		{ID: "dup", Kind: patchdata.KindReplaceTextRecord, Member: "Fms/A.fms", RecordIndex: 0, OriginalSHA256: hex64(), Replacement: "x"},
		{ID: "dup", Kind: patchdata.KindReplaceTextRecord, Member: "Fms/A.fms", RecordIndex: 1, OriginalSHA256: hex64(), Replacement: "y"},
	}
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("duplicate ids accepted")
	}

	// unsupported kind
	m = baseManifest()
	m.Operations = []patchdata.Operation{{ID: "k", Kind: "nope", Member: "Fms/A.fms", RecordIndex: 0, OriginalSHA256: hex64(), Replacement: "x"}}
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("unsupported kind accepted")
	}

	// negative record index
	m = baseManifest()
	m.Operations = []patchdata.Operation{{ID: "n", Kind: patchdata.KindReplaceTextRecord, Member: "Fms/A.fms", RecordIndex: -1, OriginalSHA256: hex64(), Replacement: "x"}}
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("negative index accepted")
	}

	// missing replacement
	m = baseManifest()
	m.Operations = []patchdata.Operation{{ID: "m", Kind: patchdata.KindReplaceTextRecord, Member: "Fms/A.fms", RecordIndex: 0, OriginalSHA256: hex64()}}
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("empty replacement accepted")
	}

	// content path traversal
	m = baseManifest()
	m.ContentFiles = []patchdata.ContentFile{{Path: "../secret", SHA256: hex64(), Size: 1}}
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("content path traversal accepted")
	}

	// preserve_unmodified must be true
	m = baseManifest()
	m.Output = patchdata.OutputSpec{}
	writeManifest(t, dir, m)
	if _, err := patchdata.Load(dir); err == nil {
		t.Fatal("preserve_unmodified=false accepted")
	}
}

func hex64() string { return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" }
