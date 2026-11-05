package build_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/build"
	"13s-patcher/internal/cpk"
	"13s-patcher/internal/fmsb"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/patchdata"
	"13s-patcher/internal/synth"
)

// addMemberFixture prepares a synthetic archive plus a base bank directory and a
// bundle whose operations target both an existing member and an absent one.
func addMemberFixture(t *testing.T) (archivePath, bankDir, bundleDir string, sourceSize int64, sourceSHA string) {
	t.Helper()
	dir := t.TempDir()
	archivePath = filepath.Join(dir, "overlay.CPK")
	bankDir = filepath.Join(dir, "banks")
	bundleDir = filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bankDir, 0o755); err != nil {
		t.Fatal(err)
	}

	archive, err := synth.Archive([]synth.Member{
		{Dir: "fms", Name: "a.fms", ID: 0, Logical: synth.Bank([]string{"alpha", "beta"})},
		{Dir: "fms", Name: "c.fms", ID: 1, Logical: synth.Bank([]string{"gamma"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	sourceSize = int64(len(archive))
	sourceSHA = hashutil.Bytes(archive)

	base := synth.Bank([]string{"delta", "epsilon"})
	if err := os.WriteFile(filepath.Join(bankDir, "Fms__B.fms"), base, 0o644); err != nil {
		t.Fatal(err)
	}

	in := patchdata.ExportInput{
		BundleID:      "fixture-add",
		BundleVersion: "1.0.0",
		FormatVersion: patchdata.SupportedFormatVersion,
		Sources: []patchdata.SourceSpec{{
			Role: build.RolePatchArchive, Size: sourceSize, SHA256: sourceSHA,
		}},
		Operations: []patchdata.ExportOperation{
			{ID: "edit-a", Member: "fms/a.fms", RecordIndex: 1, Original: "beta", Replacement: "beta revised", PreserveTokens: true},
			{ID: "add-b", Member: "fms/b.fms", RecordIndex: 0, Original: "delta", Replacement: "delta revised", PreserveTokens: true},
		},
		Output: patchdata.OutputSpec{PreserveUnmodified: true, MemberCountDelta: 1},
	}
	if _, err := patchdata.ExportBundle(bundleDir, in); err != nil {
		t.Fatal(err)
	}
	return archivePath, bankDir, bundleDir, sourceSize, sourceSHA
}

func TestBuildAddsMemberFromBaseBankDirectory(t *testing.T) {
	archivePath, bankDir, bundleDir, _, _ := addMemberFixture(t)
	out := filepath.Join(t.TempDir(), "out.CPK")
	res, err := build.Build(context.Background(), build.Config{
		SourceArchive: archivePath,
		BundleDir:     bundleDir,
		BankDir:       bankDir,
		OutputPath:    out,
		Encoder:       "chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.MemberCountBefore != 2 || res.Report.MemberCountAfter != 3 {
		t.Fatalf("member count %d -> %d", res.Report.MemberCountBefore, res.Report.MemberCountAfter)
	}
	if res.Report.BanksAdded != 1 {
		t.Fatalf("expected one added bank, got %d", res.Report.BanksAdded)
	}
	if !res.Report.Verified || !res.Report.UnmodifiedIntact {
		t.Fatalf("build not verified: %+v", res.Report)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	added := archive.MemberByPath("fms/b.fms")
	if added == nil {
		t.Fatal("the added member is missing from the built archive")
	}
	logical, err := archive.Logical(added)
	if err != nil {
		t.Fatal(err)
	}
	bank, err := fmsb.Parse(logical)
	if err != nil {
		t.Fatal(err)
	}
	if len(bank.Strings) != 2 || string(bank.Strings[0]) != "delta revised" || string(bank.Strings[1]) != "epsilon" {
		t.Fatalf("added bank content wrong: %q", bank.Strings)
	}
	// the untouched base-bank member must survive byte-identical
	untouched := archive.MemberByPath("fms/c.fms")
	srcRaw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	srcArchive, err := cpk.Parse(srcRaw)
	if err != nil {
		t.Fatal(err)
	}
	orig := srcArchive.MemberByPath("fms/c.fms")
	if untouched == nil || orig == nil || string(untouched.Stored) != string(orig.Stored) {
		t.Fatal("an unmodified member changed")
	}
}

func TestBuildRefusesAbsentMemberWithoutBankDirectory(t *testing.T) {
	archivePath, _, bundleDir, _, _ := addMemberFixture(t)
	out := filepath.Join(t.TempDir(), "out.CPK")
	if _, err := build.Build(context.Background(), build.Config{
		SourceArchive: archivePath, BundleDir: bundleDir, OutputPath: out,
	}); err == nil {
		t.Fatal("a missing member must be refused when no base bank directory is supplied")
	}
}

func TestBuildRefusesWrongBaseBankContent(t *testing.T) {
	archivePath, bankDir, bundleDir, _, _ := addMemberFixture(t)
	// replace the base bank with content whose record 0 no longer matches
	if err := os.WriteFile(filepath.Join(bankDir, "Fms__B.fms"), synth.Bank([]string{"different"}), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.CPK")
	if _, err := build.Build(context.Background(), build.Config{
		SourceArchive: archivePath, BundleDir: bundleDir, BankDir: bankDir, OutputPath: out,
	}); err == nil {
		t.Fatal("a baseline mismatch in the base bank must be refused")
	}
}
