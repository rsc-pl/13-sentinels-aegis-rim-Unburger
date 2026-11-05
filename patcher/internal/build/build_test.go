package build_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/build"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/patchdata"
)

func cfgFrom(f *fixture.Fixture, out string) build.Config {
	return build.Config{
		SourceArchive: f.SourceArchive,
		BundleDir:     f.BundleDir,
		OutputPath:    out,
		Encoder:       "chain",
	}
}

func TestBuildEndToEnd(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.OutputDir, "built.CPK")
	srcBefore, _ := os.ReadFile(f.SourceArchive)

	res, err := build.Build(context.Background(), cfgFrom(f, out))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Report.Verified || !res.Report.UnmodifiedIntact {
		t.Fatalf("report not verified: %+v", res.Report)
	}
	if res.Operations != f.Operations {
		t.Fatalf("operations = %d, want %d", res.Operations, f.Operations)
	}
	if res.MembersEdited != 2 {
		t.Fatalf("members edited = %d, want 2", res.MembersEdited)
	}
	if res.OutputSHA256 != hashutil.Bytes(res.Bytes) {
		t.Fatal("reported digest does not match the bytes")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	srcAfter, _ := os.ReadFile(f.SourceArchive)
	if string(srcBefore) != string(srcAfter) {
		t.Fatal("source archive was modified")
	}
	// The output must re-parse and every check must pass.
	ins, err := build.Inspect(out)
	if err != nil {
		t.Fatal(err)
	}
	if !ins.AllChecksOK {
		t.Fatalf("output checks failed: %v", ins.FailedChecks)
	}
	// Exactly two members must differ from the source.
	srcIns, err := build.Inspect(f.SourceArchive)
	if err != nil {
		t.Fatal(err)
	}
	diff := build.Compare(srcIns, ins)
	if len(diff.Changed) != 2 || len(diff.Added) != 0 || len(diff.Removed) != 0 || !diff.AllIntact {
		t.Fatalf("unexpected diff: %+v", diff)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := build.Build(context.Background(), cfgFrom(f, filepath.Join(f.OutputDir, "a.CPK")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := build.Build(context.Background(), cfgFrom(f, filepath.Join(f.OutputDir, "b.CPK")))
	if err != nil {
		t.Fatal(err)
	}
	if a.OutputSHA256 != b.OutputSHA256 {
		t.Fatalf("build is not deterministic: %s vs %s", a.OutputSHA256, b.OutputSHA256)
	}
}

func TestBuildRefusesOverwriteWithoutForce(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.OutputDir, "built.CPK")
	if _, err := build.Build(context.Background(), cfgFrom(f, out)); err != nil {
		t.Fatal(err)
	}
	cfg := cfgFrom(f, out)
	if _, err := build.Build(context.Background(), cfg); err == nil {
		t.Fatal("build overwrote an existing output without --force")
	}
	cfg.Overwrite = true
	if _, err := build.Build(context.Background(), cfg); err != nil {
		t.Fatalf("build with overwrite failed: %v", err)
	}
}

func TestBuildCancelledLeavesNoArtifact(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.OutputDir, "cancelled.CPK")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := cfgFrom(f, out)
	cfg.Ctx = ctx
	if _, err := build.Build(ctx, cfg); !errors.Is(err, build.ErrCancelled) {
		t.Fatalf("want ErrCancelled, got %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("cancelled build left an artifact")
	}
}

func TestBuildRefusesNoOpAndTokenChanges(t *testing.T) {
	dir := t.TempDir()
	f, err := fixture.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		op   patchdata.ExportOperation
	}{
		{"no-op", patchdata.ExportOperation{ID: "n", Member: "Fms/Greeting.fms", RecordIndex: 0, Original: "Hello, pilot.", Replacement: "Hello, pilot.", PreserveTokens: true}},
		{"token-drop", patchdata.ExportOperation{ID: "tok", Member: "Fms/Greeting.fms", RecordIndex: 1, Original: "@#(1)Begin@/b", Replacement: "Start", PreserveTokens: true}},
		{"wrong-baseline", patchdata.ExportOperation{ID: "wb", Member: "Fms/Greeting.fms", RecordIndex: 0, Original: "NOT THE BASELINE", Replacement: "something", PreserveTokens: true}},
		{"missing-member", patchdata.ExportOperation{ID: "mm", Member: "Fms/Missing.fms", RecordIndex: 0, Original: "x", Replacement: "y", PreserveTokens: true}},
	}
	for i, c := range cases {
		bdir := filepath.Join(dir, "bundle-case")
		_ = os.RemoveAll(bdir)
		in := patchdata.ExportInput{
			BundleID: "case", BundleVersion: "1", FormatVersion: patchdata.SupportedFormatVersion,
			Sources:    []patchdata.SourceSpec{{Role: "patch_archive", Size: f.SourceSize, SHA256: f.SourceSHA256}},
			Operations: []patchdata.ExportOperation{c.op},
		}
		if _, err := patchdata.ExportBundle(bdir, in); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "out", c.name+".CPK")
		_, err := build.Build(context.Background(), build.Config{SourceArchive: f.SourceArchive, BundleDir: bdir, OutputPath: out})
		if err == nil {
			t.Errorf("case %d (%s): expected failure", i, c.name)
		}
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Errorf("case %d (%s): failed build left an artifact", i, c.name)
		}
	}
}

func TestBuildRejectsWrongOutputDigestConstraint(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	in := patchdata.ExportInput{
		BundleID: "pin", BundleVersion: "1", FormatVersion: patchdata.SupportedFormatVersion,
		Sources:    []patchdata.SourceSpec{{Role: "patch_archive", Size: f.SourceSize, SHA256: f.SourceSHA256}},
		Operations: []patchdata.ExportOperation{{ID: "op", Member: "Fms/Menu.fms", RecordIndex: 0, Original: "Start", Replacement: "Begin", PreserveTokens: true}},
		Output:     patchdata.OutputSpec{PreserveUnmodified: true, ExpectedSHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
	}
	if _, err := patchdata.ExportBundle(dir, in); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.CPK")
	if _, err := build.Build(context.Background(), build.Config{SourceArchive: f.SourceArchive, BundleDir: dir, OutputPath: out}); err == nil {
		t.Fatal("wrong pinned output digest accepted")
	}
}

func TestBuildRejectsWrongSource(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A different archive with a different digest must be refused.
	other := filepath.Join(t.TempDir(), "other.CPK")
	if err := os.WriteFile(other, []byte("not the archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.OutputDir, "x.CPK")
	if _, err := build.Build(context.Background(), build.Config{SourceArchive: other, BundleDir: f.BundleDir, OutputPath: out}); err == nil {
		t.Fatal("wrong source accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("rejected build left an artifact")
	}
}
