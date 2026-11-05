package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/build"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/texpack"
)

// titleVersionRoot writes a version.txt into a fresh root and points the
// service at it, so the footer label resolves from the single authoritative
// source.
func titleVersionRoot(t *testing.T, svc *Service, version string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.Loc.Root = root
}

// TestTitleBrandingEnabledBuildCarriesCorrectedVersionedMember exercises the
// enabled path end to end: the composed member is the corrected encoder output
// (it re-verifies as a footer-only edit), the footer carries the actual
// version.txt label, and the built archive carries the member.
func TestTitleBrandingEnabledBuildCarriesCorrectedVersionedMember(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "9.9.9")
	on := true
	svc.Cfg.TitleBranding = &on
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}

	base := fixture.SynthTitleFTX()
	resrc, status, err := svc.buildResources()
	if err != nil {
		t.Fatal(err)
	}
	var title *ResourceStatus
	var logical []byte
	for i := range status {
		if status[i].Member == texpack.TitleMember {
			title = &status[i]
		}
	}
	for _, r := range resrc {
		if r.Member == texpack.TitleMember {
			logical = r.Logical
		}
	}
	if title == nil || logical == nil {
		t.Fatalf("the title resource was not produced: %+v", status)
	}
	if !strings.Contains(title.Detail, "Unburger Mod v9.9.9") {
		t.Fatalf("the footer label must come from version.txt: %q", title.Detail)
	}
	// The composed bytes must be a faithful, footer-only edit: only BC7 mode-6
	// blocks inside the footer rectangle change.
	if err := texpack.VerifyTitleFooter(base, logical); err != nil {
		t.Fatalf("the enabled member is not the corrected footer-only edit: %v", err)
	}

	res, err := svc.Build(ctx, false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	tb := res.Components.TitleBranding
	if !tb.Enabled || !tb.Included {
		t.Fatalf("the build must record the enabled, included title branding: %+v", tb)
	}
	if tb.Footer != "Unburger Mod v9.9.9" {
		t.Fatalf("the report footer label is wrong: %q", tb.Footer)
	}
	if tb.Storage != "raw" || tb.StorageLabel() != "RAW" {
		t.Fatalf("the title member must be stored raw: %+v", tb)
	}
	if title.Storage != "raw" {
		t.Fatalf("the resource status must record the raw storage: %+v", title)
	}
	if tb.RuntimeVerified {
		t.Fatalf("the shipped composition must not claim the historical runtime confirmation: %+v", tb)
	}
	if tb.Composition != CompositionShipped {
		t.Fatalf("the build must classify the shipped face/style as %q, got %q", CompositionShipped, tb.Composition)
	}
	if tb.FontSHA256 != texpack.ShippedFontSHA256 {
		t.Fatalf("the build must record the shipped face digest: %q", tb.FontSHA256)
	}
	if tb.RendererVersion != texpack.TitleFooterRendererVersion {
		t.Fatalf("the report must carry the renderer version: %+v", tb)
	}
	if tb.FontSHA256 == "" || tb.CacheIdentity == "" {
		t.Fatalf("the report must carry the font digest and cache identity: %+v", tb)
	}
	// The rebuilt archive carries GUI/title.ftx with exactly the composed bytes.
	ins, err := build.Inspect(res.Archive.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	var found *build.InspectionMember
	for i := range ins.Members {
		if strings.EqualFold(ins.Members[i].Path, texpack.TitleMember) {
			found = &ins.Members[i]
		}
	}
	if found == nil {
		t.Fatalf("the enabled build must carry %s", texpack.TitleMember)
	}
	if found.Compressed || found.StoredSize != found.ExtractSize {
		t.Fatalf("the archived title member must be stored raw (FileSize == ExtractSize): %+v", found)
	}
	if found.LogicalSHA256 != title.LogicalSHA256 {
		t.Fatalf("the archived member differs from the composed member: %s != %s", found.LogicalSHA256, title.LogicalSHA256)
	}
}

// TestTitleBrandingDisabledBuildExcludesMember proves the disabled build adds
// no GUI/title.ftx member and reports the state as disabled.
func TestTitleBrandingDisabledBuildExcludesMember(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.0.0")
	off := false
	svc.Cfg.TitleBranding = &off
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if res.Components.TitleBranding.Enabled {
		t.Fatalf("a disabled build must report disabled: %+v", res.Components.TitleBranding)
	}
	if res.Components.TitleBranding.Included {
		t.Fatalf("a disabled build must not include the member: %+v", res.Components.TitleBranding)
	}
	ins, err := build.Inspect(res.Archive.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ins.Members {
		if strings.EqualFold(m.Path, texpack.TitleMember) {
			t.Fatalf("the disabled build must not carry %s", texpack.TitleMember)
		}
	}
}

// TestTitleBrandingReportsTheRenderingFace proves the build report records
// which face drew the footer label and, when the bundled face cannot cover the
// label, names the runes it lacks: the fallback is never silent.
func TestTitleBrandingReportsTheRenderingFace(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.2.3")
	on := true
	svc.Cfg.TitleBranding = &on
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	_, status, err := svc.buildResources()
	if err != nil {
		t.Fatal(err)
	}
	var title *ResourceStatus
	for i := range status {
		if status[i].Member == texpack.TitleMember {
			title = &status[i]
		}
	}
	if title == nil {
		t.Fatal("the title resource was not composed")
	}
	if !strings.Contains(title.Detail, "rendered with the") {
		t.Fatalf("the report must name the face that drew the footer: %q", title.Detail)
	}
	assets, aerr := svc.LoadTitleAssets()
	if aerr != nil {
		t.Fatal(aerr)
	}
	_, fallback := texpack.TitleLabelRendering(assets.FontBytes, "Unburger Mod v1.2.3")
	if fallback != "" && !strings.Contains(title.Detail, fallback) {
		t.Fatalf("the report must carry the fallback note %q: %q", fallback, title.Detail)
	}
}

// TestTitleBrandingMissingBaseFailsClosedActionably proves an enabled build
// without the user-supplied atlas fails preflight and the build with a message
// that names the member, the resources directory and the search roots. It is
// never silently disabled.
func TestTitleBrandingMissingBaseFailsClosedActionably(t *testing.T) {
	svc, _ := integratedService(t)
	on := true
	svc.Cfg.TitleBranding = &on
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(svc.StateDir(), ResourcesDirName, "GUI__title.ftx")
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	// The build refuses.
	_, err := svc.Build(ctx, true, packaging.TargetBoth)
	if err == nil {
		t.Fatal("a build with a missing title base must be refused")
	}
	for _, want := range []string{"GUI__title.ftx", ResourcesDirName, "title texture atlas not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name %q: %v", want, err)
		}
	}
	// Preflight surfaces the same failure instead of silently disabling it.
	pf, perr := svc.Preflight(ctx, packaging.TargetBoth)
	if perr != nil {
		t.Fatal(perr)
	}
	if pf.Ready {
		t.Fatal("preflight must not be ready while the enabled title base is missing")
	}
	var step string
	for _, s := range pf.Steps {
		if s.ID == "title_branding" {
			step = s.Status + " " + s.Detail
		}
	}
	if !strings.HasPrefix(step, "FAIL") || !strings.Contains(step, "GUI__title.ftx") {
		t.Fatalf("preflight must report an actionable title-branding failure, got %q", step)
	}
}

// TestTitleBrandingVersionChangeChangesBuiltMember proves a version.txt edit
// changes the member actually placed in the archive, not just a label.
func TestTitleBrandingVersionChangeChangesBuiltMember(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.0.0")
	on := true
	svc.Cfg.TitleBranding = &on
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	memberSHA := func() string {
		t.Helper()
		_, status, err := svc.buildResources()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range status {
			if s.Member == texpack.TitleMember {
				return s.LogicalSHA256
			}
		}
		t.Fatal("title member not composed")
		return ""
	}
	sha1 := memberSHA()
	if err := os.WriteFile(filepath.Join(svc.Loc.Root, "version.txt"), []byte("2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha2 := memberSHA()
	if sha1 == "" || sha2 == "" || sha1 == sha2 {
		t.Fatalf("changing version.txt must change the composed member (%s == %s)", sha1, sha2)
	}
}

// TestValidateReportsTitleBranding proves the validation report carries the
// effective branding state and the versioned footer.
func TestValidateReportsTitleBranding(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "3.1.4")
	on := true
	svc.Cfg.TitleBranding = &on
	vr, err := svc.Validate(context.Background(), packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if !vr.Ready {
		t.Fatalf("validation not ready: %+v", vr.Checks)
	}
	if !vr.TitleBranding.Enabled || !vr.TitleBranding.Included {
		t.Fatalf("validation must report the enabled title branding: %+v", vr.TitleBranding)
	}
	if vr.TitleBranding.Footer != "Unburger Mod v3.1.4" {
		t.Fatalf("validation footer label is wrong: %q", vr.TitleBranding.Footer)
	}
	if !strings.Contains(vr.TitleBranding.RuntimeStatus, "RUNTIME CONFIRMED") {
		t.Fatalf("validation must carry the confirmed runtime status: %q", vr.TitleBranding.RuntimeStatus)
	}
}

// TestInspectDetectsTitleMember proves inspection reports title inclusion from
// the archive bytes, independently of the current settings.
func TestInspectDetectsTitleMember(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.0.0")
	ctx := context.Background()
	if _, err := svc.ResolveExtraction(ctx); err != nil {
		t.Fatal(err)
	}
	on := true
	svc.Cfg.TitleBranding = &on
	res, err := svc.Build(ctx, false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Artifacts {
		ins, err := svc.InspectOutput(ctx, a.Path, packaging.TargetBoth)
		if err != nil {
			t.Fatal(err)
		}
		if !ins.TitleIncluded {
			t.Fatalf("inspection of %s did not detect the title member", a.Path)
		}
	}
	// A disabled build's archive must report no title member.
	off := false
	svc.Cfg.TitleBranding = &off
	res2, err := svc.Build(ctx, true, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res2.Artifacts {
		ins, err := svc.InspectOutput(ctx, a.Path, packaging.TargetBoth)
		if err != nil {
			t.Fatal(err)
		}
		if ins.TitleIncluded {
			t.Fatalf("inspection of %s wrongly detected the title member", a.Path)
		}
	}
}
