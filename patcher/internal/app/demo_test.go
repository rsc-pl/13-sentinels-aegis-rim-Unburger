package app_test

import (
	"context"
	"path/filepath"
	"testing"

	"13s-patcher/internal/app"
	"13s-patcher/internal/fixture"
)

// TestCommittedDemoRunsEndToEnd exercises the synthetic demo tree that ships in
// examples/demo, proving the committed fixture is self-consistent.
func TestCommittedDemoRunsEndToEnd(t *testing.T) {
	demo, err := filepath.Abs(filepath.Join("..", "..", "examples", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	work := t.TempDir()
	cfgDir := t.TempDir()
	if err := fixture.PlateBase(cfgDir); err != nil {
		t.Fatal(err)
	}
	if err := fixture.TitleBase(cfgDir); err != nil {
		t.Fatal(err)
	}
	svc, err := app.New(app.Options{
		ConfigPath:    filepath.Join(cfgDir, "config.json"),
		WorkDir:       work,
		SourceArchive: filepath.Join(demo, "src", "GAME_PATCH.CPK"),
		PatchDataDir:  filepath.Join(demo, "patchdata"),
		OutputDir:     out,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.TitleAssetsDir = filepath.Join("..", "..", "assets", "fonts")
	// The synthetic demo tree ships no game-derived plate scripts.
	svc.Cfg.AllowMissingResources = true
	pf, err := svc.Preflight(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !pf.Ready {
		t.Fatalf("demo preflight not ready: %+v", pf.Steps)
	}
	res, err := svc.Build(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Archive.Report.Verified {
		t.Fatal("demo build not verified")
	}
	rep, err := svc.Validate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Ready {
		t.Fatalf("demo validation not ready: %q", rep.Error)
	}
}
