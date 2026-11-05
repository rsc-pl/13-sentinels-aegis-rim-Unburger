package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/app"
	"13s-patcher/internal/fixture"
)

func newService(t *testing.T, f *fixture.Fixture) *app.Service {
	t.Helper()
	svc, err := app.New(app.Options{
		ConfigPath:    filepath.Join(f.Dir, "config.json"),
		WorkDir:       f.Dir,
		SourceArchive: f.SourceArchive,
		PatchDataDir:  f.BundleDir,
		OutputDir:     f.OutputDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.TitleBase(f.Dir); err != nil {
		t.Fatal(err)
	}
	// Point the live title-footer assets and the shipped presentation plate at
	// the module's files. Production resolves both from the project root; the
	// tests use the real shipped files.
	svc.TitleAssetsDir = filepath.Join("..", "..", "assets", "fonts")
	svc.PlateAssetsDir = filepath.Join("..", "..", "assets", "images")
	return svc
}

func TestStatusPreflightBuildValidateInspect(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, f)

	st := svc.Status()
	if !st.PatchData.Loaded || st.PatchData.Operations != f.Operations {
		t.Fatalf("status patch data wrong: %+v", st.PatchData)
	}
	if !st.Source.Supported || st.Source.Path == "" {
		t.Fatalf("status source wrong: %+v", st.Source)
	}

	pf, err := svc.Preflight(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !pf.Ready || pf.Failed != 0 {
		t.Fatalf("preflight not ready: %+v", pf.Steps)
	}

	res, err := svc.Build(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Archive.Report.Verified {
		t.Fatal("build not verified")
	}
	if _, err := os.Stat(res.Archive.OutputPath); err != nil {
		t.Fatal(err)
	}

	rep, err := svc.Validate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Ready {
		t.Fatalf("validation not ready: error=%q checks=%+v", rep.Error, rep.Checks)
	}

	ir, err := svc.InspectOutput(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ir.Ready {
		t.Fatalf("inspect not ready: %+v", ir.Checks)
	}

	// The last build must be recorded and reloadable.
	if bs, ok := svc.LoadLastBuild(); !ok || bs.Operations != f.Operations {
		t.Fatalf("last build not recorded: %+v ok=%v", bs, ok)
	}
}

func TestPreflightFailsWithoutSource(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, f)
	// Point at a directory with no archive.
	empty := t.TempDir()
	svc2, err := app.New(app.Options{ConfigPath: filepath.Join(empty, "c.json"), WorkDir: empty, PatchDataDir: f.BundleDir, OutputDir: filepath.Join(empty, "out")})
	if err != nil {
		t.Fatal(err)
	}
	pf, err := svc2.Preflight(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if pf.Ready {
		t.Fatal("preflight reported ready without a source archive")
	}
	if _, err := svc2.Build(context.Background(), false, ""); err == nil {
		t.Fatal("build succeeded without a source archive")
	}

	// Sanity: the original service is still ready.
	pf1, _ := svc.Preflight(context.Background(), "")
	if !pf1.Ready {
		t.Fatal("fixture service should be ready")
	}
}

func TestDoctor(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, f)
	rep := svc.Doctor()
	if rep.Failed != 0 {
		t.Fatalf("doctor reported failures: %+v", rep.Checks)
	}
	if rep.Passed == 0 {
		t.Fatal("doctor reported no passing checks")
	}
}

// TestOneOffOptionsDoNotPersist proves that the one-off CLI options folded
// into the service are ephemeral: a build and a validation honour them for the
// run, but the settings file keeps its durable values and still records the
// last build/validation results.
func TestOneOffOptionsDoNotPersist(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	if err := fixture.PlateBase(cfgDir); err != nil {
		t.Fatal(err)
	}
	if err := fixture.TitleBase(cfgDir); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	const durableOutput = "/durable/output"
	if err := os.WriteFile(cfgPath, []byte(`{"output_dir":"`+durableOutput+`","encoder":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oneOffOutput := t.TempDir()

	svc, err := app.New(app.Options{
		ConfigPath:    cfgPath,
		WorkDir:       f.Dir,
		SourceArchive: f.SourceArchive,
		PatchDataDir:  f.BundleDir,
		OutputDir:     oneOffOutput,
		Encoder:       "chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.TitleAssetsDir = filepath.Join("..", "..", "assets", "fonts")
	// The synthetic fixture has no game-derived plate scripts.
	svc.Cfg.AllowMissingResources = true

	// The one-off override must still apply to this run.
	if got := svc.OutputDirOrDefault(); got != oneOffOutput {
		t.Fatalf("one-off output not applied for the run: %q", got)
	}
	res, err := svc.Build(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	// The user-facing outputs are the per-target ZIP archives in the one-off
	// output directory. The rebuilt archive itself is an intermediate in the
	// work cache, not an output.
	if len(res.Artifacts) == 0 {
		t.Fatal("build produced no artifacts")
	}
	for _, a := range res.Artifacts {
		if filepath.Dir(a.Path) != oneOffOutput {
			t.Fatalf("artifact %s does not honour the one-off output directory %s", a.Path, oneOffOutput)
		}
	}
	if _, err := svc.Validate(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if got["output_dir"] != durableOutput {
		t.Fatalf("one-off -output was persisted: %v", got["output_dir"])
	}
	if got["encoder"] != "old" {
		t.Fatalf("one-off -encoder overwrote the durable value: %v", got["encoder"])
	}
	if v, ok := got["source_archive"]; ok {
		t.Fatalf("one-off -source was persisted: %v", v)
	}
	if v, ok := got["patch_data_dir"]; ok {
		t.Fatalf("one-off -patch-data was persisted: %v", v)
	}
	if got["last_build"] == nil || got["last_build"] == "" {
		t.Fatalf("last build must still be persisted: %v", got["last_build"])
	}
	if got["last_validation"] == nil || got["last_validation"] == "" {
		t.Fatalf("last validation must still be persisted: %v", got["last_validation"])
	}
}

// TestSetupPersistsDiscoveriesWithoutOneOffFlags proves setup still writes the
// fields it discovers while the one-off CLI overrides stay ephemeral.
func TestSetupPersistsDiscoveriesWithoutOneOffFlags(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A discoverable durable field: setup records the conventional keys dir.
	keysDir := filepath.Join(f.Dir, "keys")
	if err := os.MkdirAll(keysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	svc, err := app.New(app.Options{
		ConfigPath:   cfgPath,
		WorkDir:      f.Dir,
		PatchDataDir: f.BundleDir,
		OutputDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AutoConfigure(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["patch_data_dir"] != nil {
		t.Fatalf("one-off -patch-data must not be persisted by setup: %v", got["patch_data_dir"])
	}
	if got["output_dir"] != nil {
		t.Fatalf("one-off -output must not be persisted by setup: %v", got["output_dir"])
	}
	if got["keys_dir"] != keysDir {
		t.Fatalf("setup must persist the discovered keys_dir: got %v, want %q", got["keys_dir"], keysDir)
	}
}

func TestBuildRefusesOverwriteThroughService(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, f)
	if _, err := svc.Build(context.Background(), false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(context.Background(), false, ""); err == nil {
		t.Fatal("second build overwrote without force")
	}
	if _, err := svc.Build(context.Background(), true, ""); err != nil {
		t.Fatalf("forced build failed: %v", err)
	}
}
