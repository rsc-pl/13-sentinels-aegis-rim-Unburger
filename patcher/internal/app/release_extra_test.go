package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/packaging"
	"13s-patcher/internal/progress"
)

// TestBuildIntermediateIsNotAnOutput proves the rebuilt archive is an
// intermediate in the work cache, never a file in the output directory. This is
// the regression for the collision where a stale out/ROBO_PATCH_US.CPK blocked
// every repeated build.
func TestBuildIntermediateIsNotAnOutput(t *testing.T) {
	svc, _ := integratedService(t)
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(context.Background(), false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(res.Archive.OutputPath); got != svc.WorkCacheDir() {
		t.Fatalf("intermediate archive must live in the work cache %s, got %s", svc.WorkCacheDir(), got)
	}
	ents, err := os.ReadDir(svc.OutputDirOrDefault())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".CPK" || filepath.Ext(e.Name()) == ".cpk" {
			t.Fatalf("the output directory must not contain an intermediate archive: %s", e.Name())
		}
	}
	for _, a := range res.Artifacts {
		if filepath.Dir(a.Path) != svc.OutputDirOrDefault() {
			t.Fatalf("artifact %s is not in the output directory", a.Path)
		}
	}
}

// TestBuildRepeatedWithOverwriteIsDeterministic proves a repeated build needs no
// manual delete and produces byte-identical outputs.
func TestBuildRepeatedWithOverwriteIsDeterministic(t *testing.T) {
	svc, _ := integratedService(t)
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Build(context.Background(), false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Build(context.Background(), true, packaging.TargetBoth)
	if err != nil {
		t.Fatalf("repeated build failed: %v", err)
	}
	if len(first.Artifacts) != 2 || len(second.Artifacts) != 2 {
		t.Fatalf("expected two artifacts each, got %d and %d", len(first.Artifacts), len(second.Artifacts))
	}
	for i := range first.Artifacts {
		if first.Artifacts[i].SHA256 != second.Artifacts[i].SHA256 {
			t.Fatalf("target %s is not deterministic across repeated builds", first.Artifacts[i].Target)
		}
	}
}

// TestBuildRefusesOverwriteWithoutForceInIntegratedMode proves the service
// overwrite protection is unchanged: a second build without overwrite fails.
func TestBuildRefusesOverwriteWithoutForceInIntegratedMode(t *testing.T) {
	svc, _ := integratedService(t)
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(context.Background(), false, packaging.TargetBoth); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(context.Background(), false, packaging.TargetBoth); err == nil {
		t.Fatal("a second build must refuse to overwrite existing archives without the overwrite decision")
	}
}

// TestInspectDestinations classifies missing, identical and unverifiable
// archives.
func TestInspectDestinations(t *testing.T) {
	svc, _ := integratedService(t)
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	states, err := svc.InspectDestinations(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("expected two destinations, got %d", len(states))
	}
	for _, st := range states {
		if st.Exists || st.Kind != DestMissing {
			t.Fatalf("fresh destination must be missing: %+v", st)
		}
	}
	if _, err := svc.Build(ctx, false, packaging.TargetBoth); err != nil {
		t.Fatal(err)
	}
	states, err = svc.InspectDestinations(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if !st.Exists || !st.Valid || st.Kind != DestIdentical {
			t.Fatalf("archives built from the current dataset must be identical-valid: %+v", st)
		}
	}
	// Corrupt one archive: it can no longer be verified as one this tool made.
	if err := os.WriteFile(states[0].Path, []byte("not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	states, err = svc.InspectDestinations(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, st := range states {
		if st.Path == states[0].Path || st.Kind == DestUnverifiable {
			found = true
		}
	}
	if !found {
		t.Fatalf("a corrupted archive must be classified unverifiable: %+v", states)
	}
}

// TestBuildKeepingExistingReusesAndBuildsOnlyMissing covers the "Keep existing"
// decision: valid archives are reused, missing ones are built.
func TestBuildKeepingExistingReusesAndBuildsOnlyMissing(t *testing.T) {
	svc, _ := integratedService(t)
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(ctx, false, packaging.TargetBoth); err != nil {
		t.Fatal(err)
	}
	// Everything exists: nothing must be rebuilt.
	keep, err := svc.BuildKeepingExisting(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if keep.Built {
		t.Fatal("a keep decision with all targets valid must not rebuild")
	}
	if len(keep.Reused) != 2 || len(keep.Artifacts) != 2 {
		t.Fatalf("expected two reused artifacts, got reused=%v artifacts=%d", keep.Reused, len(keep.Artifacts))
	}
	// Remove one archive: only it must be built, the other reused.
	emulator := filepath.Join(svc.OutputDirOrDefault(), packaging.ArchiveName(packaging.TargetEmulator, svc.Cfg.ReleaseVersion))
	if err := os.Remove(emulator); err != nil {
		t.Fatal(err)
	}
	keep, err = svc.BuildKeepingExisting(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if !keep.Built {
		t.Fatal("a missing target must be built")
	}
	if len(keep.Reused) != 1 || keep.Reused[0] != packaging.TargetAtmosphere {
		t.Fatalf("the existing atmosphere archive must be reused, got %v", keep.Reused)
	}
	if len(keep.Artifacts) != 1 || keep.Artifacts[0].Target != packaging.TargetEmulator {
		t.Fatalf("only the missing emulator archive must be built, got %+v", keep.Artifacts)
	}
}

// TestBuildCancelledLeavesExistingOutputsIntact proves a cancelled build never
// truncates or replaces valid outputs.
func TestBuildCancelledLeavesExistingOutputsIntact(t *testing.T) {
	svc, _ := integratedService(t)
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, a := range res.Artifacts {
		before[a.Path] = a.SHA256
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.Build(cancelCtx, true, packaging.TargetBoth); err == nil {
		t.Fatal("a cancelled build must return an error")
	}
	states, err := svc.InspectDestinations(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if want, ok := before[st.Path]; ok && st.SHA256 != want {
			t.Fatalf("cancelled build changed %s (%s -> %s)", st.Path, want, st.SHA256)
		}
	}
}

// TestCancelDuringPackagingLeavesOutputsIntact cancels the build the moment
// packaging begins and proves the existing outputs are untouched.
func TestCancelDuringPackagingLeavesOutputsIntact(t *testing.T) {
	svc, _ := integratedService(t)
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, a := range res.Artifacts {
		before[a.Path] = a.SHA256
	}

	cctx, cancel := context.WithCancel(ctx)
	svc.Progress = func(e progress.Event) {
		if e.Stage == progress.StagePackage {
			cancel()
		}
	}
	_, err = svc.Build(cctx, true, packaging.TargetBoth)
	svc.Progress = nil
	if err == nil {
		t.Fatal("a build cancelled during packaging must fail")
	}
	states, err := svc.InspectDestinations(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if want, ok := before[st.Path]; ok && st.SHA256 != want {
			t.Fatalf("cancelled packaging changed %s", st.Path)
		}
	}
}

// TestFailedBuildKeepsExistingOutputs proves a build that fails after a
// previous success leaves the existing valid archives byte-identical.
func TestFailedBuildKeepsExistingOutputs(t *testing.T) {
	svc, _ := integratedService(t)
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, a := range res.Artifacts {
		before[a.Path] = a.SHA256
	}
	// Force the next build to fail closed. An explicit but absent override
	// path takes precedence over the shipped asset, exactly as documented.
	// This test exercises the production default, so undo the synthetic
	// fixture's allow-missing setting.
	svc.Cfg.AllowMissingResources = false
	svc.Cfg.PlateBase = filepath.Join(t.TempDir(), "absent-plate.png")
	if _, err := svc.Build(ctx, true, packaging.TargetBoth); err == nil {
		t.Fatal("the build should have failed without the added resource")
	}
	states, err := svc.InspectDestinations(ctx, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if want, ok := before[st.Path]; ok && st.SHA256 != want {
			t.Fatalf("failed build changed %s", st.Path)
		}
	}
}

// TestInspectRecognisesEmulatorArchiveWithBothTargets is the regression for the
// inspect target detection: with both targets configured, inspecting the
// emulator archive must report the emulator layout instead of falling back to
// the first layout whose bytes merely reopen.
func TestInspectRecognisesEmulatorArchiveWithBothTargets(t *testing.T) {
	svc, _ := integratedService(t)
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(ctx, false, packaging.TargetBoth); err != nil {
		t.Fatal(err)
	}
	emu := filepath.Join(svc.OutputDirOrDefault(), packaging.ArchiveName(packaging.TargetEmulator, svc.ReleaseLabel()))
	res, err := svc.InspectOutput(ctx, emu, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != packaging.TargetEmulator {
		t.Fatalf("inspect detected target %q for the emulator archive", res.Target)
	}
	if !res.Ready {
		t.Fatalf("emulator archive did not validate: %+v", res.Checks)
	}
	if res.ArchiveSHA == "" || res.PatchSHA == "" {
		t.Fatalf("inspect did not report the payload digests: %+v", res)
	}
}

// TestBuildEmitsProgressEvents proves the shared service reports real stages,
// with determinate counts where they exist and no fabricated totals.
func TestBuildEmitsProgressEvents(t *testing.T) {
	svc, _ := integratedService(t)
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	var events []progress.Event
	svc.Progress = func(e progress.Event) { events = append(events, e) }
	if _, err := svc.Build(context.Background(), false, packaging.TargetBoth); err != nil {
		t.Fatal(err)
	}
	svc.Progress = nil
	seen := map[progress.Stage]bool{}
	determinate := 0
	for _, e := range events {
		seen[e.Stage] = true
		if e.Determinate() {
			determinate++
			if e.Completed > e.Total {
				t.Fatalf("completed exceeds total for %+v", e)
			}
		}
		if e.Completed < 0 || e.Total < 0 || e.Bytes < 0 {
			t.Fatalf("negative counters in %+v", e)
		}
	}
	for _, want := range []progress.Stage{
		progress.StageTranslations, progress.StageExtraction, progress.StagePlan,
		progress.StageFMSB, progress.StageCPK, progress.StagePackage, progress.StageVerify,
	} {
		if !seen[want] {
			t.Fatalf("stage %s was never reported (seen: %v)", want, seen)
		}
	}
	if determinate == 0 {
		t.Fatal("no determinate progress event was reported")
	}
}
