package build_test

import (
	"os"
	"testing"

	"13s-patcher/internal/cpk"
	"13s-patcher/internal/fmsb"
	"13s-patcher/internal/translations"
)

// This test is an acceptance harness: when the environment points at a real
// dataset and a real built archive, it re-reads the archive from disk and
// checks that every exported change is present with exactly the dataset's text.
// Without the environment variables it skips, so the unit suite stays offline
// and synthetic.
//
//	PATCHER_VERIFY_DATASET=<dataset dir>
//	PATCHER_VERIFY_ARCHIVE=<built archive>
func TestDatasetAppliedToArchive(t *testing.T) {
	dsDir := os.Getenv("PATCHER_VERIFY_DATASET")
	archivePath := os.Getenv("PATCHER_VERIFY_ARCHIVE")
	if dsDir == "" || archivePath == "" {
		t.Skip("set PATCHER_VERIFY_DATASET and PATCHER_VERIFY_ARCHIVE to run the acceptance check")
	}
	ds, err := translations.Load(dsDir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range ds.Records {
		m := archive.MemberByPath(r.BankKey)
		if m == nil {
			t.Fatalf("built archive is missing member %q", r.BankKey)
		}
		logical, err := archive.Logical(m)
		if err != nil {
			t.Fatalf("%s: %v", r.BankKey, err)
		}
		bank, err := fmsb.Parse(logical)
		if err != nil {
			t.Fatalf("%s: %v", r.BankKey, err)
		}
		if r.RecordIndex >= len(bank.Strings) {
			t.Fatalf("%s#%d: out of range (%d)", r.BankKey, r.RecordIndex, len(bank.Strings))
		}
		if got := string(bank.Strings[r.RecordIndex]); got != r.Text {
			t.Fatalf("%s#%d: built text differs from the dataset\n got %q\nwant %q", r.BankKey, r.RecordIndex, got, r.Text)
		}
		checked++
	}
	if checked != len(ds.Records) {
		t.Fatalf("checked %d of %d records", checked, len(ds.Records))
	}
	t.Logf("verified %d dataset record(s) inside %s (%d members)", checked, archivePath, len(archive.Members))
}
