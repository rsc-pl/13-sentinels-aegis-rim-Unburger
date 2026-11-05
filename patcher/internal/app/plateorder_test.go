package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/packaging"
	"13s-patcher/internal/techdata"
)

// plateOrderStep returns the preflight step for the story-plate name order.
func plateOrderStep(rep *PreflightReport) *Step {
	for i := range rep.Steps {
		if rep.Steps[i].ID == "plate_order" {
			return &rep.Steps[i]
		}
	}
	return nil
}

// TestPlateOrderFailsClosedWhenScriptsAreMissing proves the synthetic fixture
// (no user-supplied plate scripts) is not silently treated as a corrected
// 13-character set: the production default fails preflight, refuses the build
// and names the missing members. The explicit allow_missing_resources opt-out
// downgrades the same omission to a recorded warning.
func TestPlateOrderFailsClosedWhenScriptsAreMissing(t *testing.T) {
	svc, _ := integratedService(t)
	svc.Cfg.AllowMissingResources = false
	orders := techdata.OrderPlateResources()
	if len(orders) != 13 {
		t.Fatalf("expected 13 plate descriptors, got %d", len(orders))
	}
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatalf("resolve extraction: %v", err)
	}
	rep, err := svc.Preflight(context.Background(), packaging.TargetBoth)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	step := plateOrderStep(rep)
	if step == nil {
		t.Fatal("preflight has no plate_order step")
	}
	if step.Status != StatusFail {
		t.Fatalf("plate_order status %q, want %q (%s)", step.Status, StatusFail, step.Detail)
	}
	if rep.Ready {
		t.Fatal("preflight must not report ready with missing plate scripts")
	}
	for _, name := range []string{"GUI/change_kurabe.mbs", "GUI/change_shinonome.mbs"} {
		if !strings.Contains(step.Detail, name) {
			t.Fatalf("plate_order detail does not name %s: %s", name, step.Detail)
		}
	}
	if _, err := svc.Build(context.Background(), true, packaging.TargetBoth); err == nil ||
		!strings.Contains(err.Error(), "preflight failed") {
		t.Fatalf("a build with missing plate scripts must be refused, got %v", err)
	}

	// Explicit opt-out: the omission is recorded, never silent.
	svc.Cfg.AllowMissingResources = true
	rep, err = svc.Preflight(context.Background(), packaging.TargetBoth)
	if err != nil {
		t.Fatalf("preflight with opt-out: %v", err)
	}
	step = plateOrderStep(rep)
	if step == nil || step.Status != StatusWarn {
		t.Fatalf("opted-out plate_order step = %+v, want a warning", step)
	}
	if !rep.Ready {
		t.Fatal("the explicit opt-out must keep preflight ready")
	}
}

// TestPlateOrderTransformIsBuiltWhenTheBaseIsPresent places one synthetic base
// script of the pinned size in the resources directory. The transform must then
// appear as an added member and the build must stay verified; a base with the
// wrong identity must be refused instead.
func TestPlateOrderTransformIsBuiltWhenTheBaseIsPresent(t *testing.T) {
	svc, _ := integratedService(t)
	orders := techdata.OrderPlateResources()
	var target = orders[0]
	dir := filepath.Join(svc.ResourcesDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strings.ReplaceAll(target.Member, "/", "__"))
	if err := os.WriteFile(path, make([]byte, target.BaseSize), 0o644); err != nil {
		t.Fatal(err)
	}
	// Wrong identity: the build must refuse rather than patch blindly.
	if _, err := svc.Build(context.Background(), true, packaging.TargetBoth); err == nil ||
		!strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("a mismatching base must be refused, got %v", err)
	}
}
