package app

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"13s-patcher/internal/config"
	"13s-patcher/internal/extraction"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/gameprofile"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/project"
	"13s-patcher/internal/synth"
	"13s-patcher/internal/techdata"
	"13s-patcher/internal/texpack"
	"13s-patcher/internal/translations"
)

// writeTitleBase places the synthetic title atlas in the service's resource
// directory. The presentation plate is not written: the whole-flow tests use
// the shipped project asset, exactly like a normal build.
func writeTitleBase(t *testing.T, stateDir string) {
	t.Helper()
	dir := filepath.Join(stateDir, ResourcesDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "GUI__title.ftx"), fixture.SynthTitleFTX(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSyntheticPlateOverride writes a synthetic plate into the resource
// directory. This is the optional-override path, not the standard build: the
// shipped public asset is the default.
func writeSyntheticPlateOverride(t *testing.T, stateDir string) {
	t.Helper()
	dir := filepath.Join(stateDir, ResourcesDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 2560, 1440))
	for y := 0; y < 1440; y++ {
		for x := 0; x < 2560; x++ {
			o := img.PixOffset(x, y)
			img.Pix[o] = uint8(x % 256)
			img.Pix[o+1] = uint8(y % 256)
			img.Pix[o+2] = uint8((x + y) % 256)
			img.Pix[o+3] = 255
		}
	}
	f, err := os.Create(filepath.Join(dir, "plate_base.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = color.NRGBA{}
}

// integratedService builds a service over synthetic inputs.
func integratedService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	overlay, err := synth.Archive([]synth.Member{
		{Dir: "fms", Name: "scripttext.fms", ID: 0, Logical: synth.Bank([]string{"Alpha", "Beta"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(dir, "overlay.cpk")
	if err := os.WriteFile(overlayPath, overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	banksDir := filepath.Join(dir, "banks")
	if err := os.MkdirAll(banksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(banksDir, "Fms__SysMsg.fms"), synth.Bank([]string{"Gamma"}), 0o644); err != nil {
		t.Fatal(err)
	}
	dsDir := filepath.Join(dir, "translations")
	recs := []translations.Record{
		{BankKey: "fms/scripttext.fms", RecordIndex: 1, Text: "Beta revised"},
		{BankKey: "fms/sysmsg.fms", RecordIndex: 0, Text: "Gamma revised"},
	}
	if err := translations.Write(dsDir, translations.BuildManifest(gameprofile.Game(), gameprofile.OverlayTarget(), recs), recs); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "cache")
	stateDir := filepath.Join(dir, "state")
	outDir := filepath.Join(dir, "out")
	extractionHook = func(r *extraction.Request) {
		r.ExpectOverlaySize = int64(len(overlay))
		r.ExpectOverlaySHA256 = hashutil.Bytes(overlay)
		r.OverlayPath = overlayPath
		r.BaseBanksDir = banksDir
		// The synthetic fixture has no game-derived base members; the title
		// atlas is placed in the resource directory. The presentation plate
		// comes from the shipped public asset.
		r.BaseMembers = nil
		r.SearchRoots = nil
	}
	t.Cleanup(func() { extractionHook = nil })
	writeTitleBase(t, stateDir)
	on := true
	svc := &Service{
		Cfg: config.Config{
			TranslationsDir: dsDir,
			CacheDir:        cacheDir,
			StateDir:        stateDir,
			OutputDir:       outDir,
			Encoder:         "chain",
			Target:          string(packaging.TargetBoth),
			// The whole-flow tests exercise the complete component set, so the
			// experimental title branding is switched on explicitly here. The
			// default-off behaviour has its own test.
			TitleBranding: &on,
			// The synthetic fixture has no game-derived plate scripts. Allow
			// their omission explicitly so the whole-flow tests keep testing
			// the rest of the pipeline; the production default is fail-closed
			// and has its own plate-order tests.
			AllowMissingResources: true,
		},
		WorkDir:        dir,
		Loc:            config.Location{Mode: config.ModeUser, StateDir: stateDir},
		TitleAssetsDir: titleAssetsDir(t),
		PlateAssetsDir: plateAssetsDir(t),
	}
	return svc, dir
}

// titleAssetsDir is the module's live display-face/style directory, addressed
// relative to this package's test working directory. Production resolves it
// from the project root; tests point at the real shipped files.
func titleAssetsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "assets", "fonts")
}

// plateAssetsDir is the module's shipped presentation-plate directory.
func plateAssetsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "assets", "images")
}

// TestIntegratedBuildProducesBothTargets exercises the whole flow with
// synthetic inputs: one rebuild, two target archives, byte-identical across
// repeated runs.
func TestIntegratedBuildProducesBothTargets(t *testing.T) {
	svc, _ := integratedService(t)

	pf0, err := svc.Preflight(context.Background(), packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if pf0.Ready {
		t.Fatal("preflight must not be ready before the cache is prepared")
	}
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	pf, err := svc.Preflight(context.Background(), packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if !pf.Ready || pf.Failed != 0 {
		t.Fatalf("preflight not ready: %+v", pf.Steps)
	}
	if pf.Derived == nil || pf.Derived.Operations != 2 || pf.Derived.AddedBanks != 1 || pf.Derived.EditedBanks != 1 {
		t.Fatalf("unexpected plan: %+v", pf.Derived)
	}

	res, err := svc.Build(context.Background(), false, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	cpk := res.Archive
	if cpk.Operations != 2 || cpk.MembersEdited != 2 || cpk.Report.Verified != true {
		t.Fatalf("unexpected build result: ops=%d members=%d verified=%v", cpk.Operations, cpk.MembersEdited, cpk.Report.Verified)
	}
	// one added text bank plus two added resources (the manual page and the
	// title-screen footer branding)
	if cpk.Report.MemberCountAfter != cpk.Report.MemberCountBefore+3 {
		t.Fatalf("member count %d -> %d", cpk.Report.MemberCountBefore, cpk.Report.MemberCountAfter)
	}
	if _, err := os.Stat(cpk.OutputPath); err != nil {
		t.Fatal(err)
	}
	if !fileIsFile(svc.Cfg.LastBuild) {
		t.Fatalf("last build summary missing (%s)", svc.Cfg.LastBuild)
	}

	// both targets produced and validated
	if len(res.Artifacts) != 2 {
		t.Fatalf("expected two artifacts, got %d", len(res.Artifacts))
	}
	for _, a := range res.Artifacts {
		if !a.Validated {
			t.Fatalf("artifact %s not validated", a.Target)
		}
		if _, err := os.Stat(a.Path); err != nil {
			t.Fatal(err)
		}
	}
	if !res.Packaging.Determinism {
		t.Fatal("packaging is not deterministic")
	}
	// the same archive bytes must appear in both targets
	var seen []string
	for _, a := range res.Artifacts {
		for _, e := range a.Entries {
			if strings.HasSuffix(e.Path, ".CPK") {
				seen = append(seen, e.SHA256)
			}
		}
	}
	if len(seen) != 2 || seen[0] != seen[1] {
		t.Fatalf("target archives do not carry identical archive bytes: %v", seen)
	}
	// Two composed resources, plus one descriptor per protagonist story plate.
	// The synthetic fixture has no user-supplied plate scripts, so those must be
	// recorded as absent with an explicit reason rather than silently dropped.
	plates := len(techdata.OrderPlateResources())
	if len(res.Components.AddedResources) != 2+plates {
		t.Fatalf("added resources missing from the components: %+v", res.Components.AddedResources)
	}
	for _, r := range res.Components.AddedResources {
		if r.Kind == "order_plate" {
			if r.Present {
				t.Fatalf("plate script %s cannot be present in the synthetic fixture: %+v", r.Member, r)
			}
			if r.Error == "" {
				t.Fatalf("plate script omission %s must be recorded: %+v", r.Member, r)
			}
			continue
		}
		if !r.Present {
			t.Fatalf("added resource %s not present: %+v", r.Member, r)
		}
	}

	// second run: cache must be reused and the outputs must be identical
	svc2 := &Service{Cfg: svc.Cfg, WorkDir: svc.WorkDir, Loc: svc.Loc, TitleAssetsDir: svc.TitleAssetsDir, PlateAssetsDir: svc.PlateAssetsDir}
	res2, err := svc2.Build(context.Background(), true, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Archive.OutputSHA256 != res.Archive.OutputSHA256 {
		t.Fatal("the build is not deterministic across runs")
	}
	for i := range res2.Artifacts {
		if res2.Artifacts[i].SHA256 != res.Artifacts[i].SHA256 {
			t.Fatalf("archive %s is not deterministic across runs", res2.Artifacts[i].Target)
		}
	}
	st, err := svc2.VerifyExtraction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(st.Notes, "; "), "reused verified cache") {
		t.Fatalf("expected the cache to be reused, got %v", st.Notes)
	}
}

// TestBuildTargetsProduceExactLayout checks the two layouts individually.
func TestBuildTargetsProduceExactLayout(t *testing.T) {
	svc, _ := integratedService(t)
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target packaging.Target
		want   []string
	}{
		{packaging.TargetAtmosphere, []string{
			"atmosphere/contents/" + techdata.TitleID() + "/romfs/" + packaging.RomfsArchiveName,
			"atmosphere/" + packaging.AtmospherePatchDir + "/" + techdata.BuildIDUpper() + ".ips",
		}},
		{packaging.TargetEmulator, []string{
			packaging.ModFolder + "/exefs/" + techdata.BuildIDUpper() + ".ips",
			packaging.ModFolder + "/romfs/" + packaging.RomfsArchiveName,
		}},
	} {
		res, err := svc.Build(context.Background(), true, tc.target)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Artifacts) != 1 {
			t.Fatalf("target %s produced %d artifacts", tc.target, len(res.Artifacts))
		}
		var got []string
		for _, e := range res.Artifacts[0].Entries {
			got = append(got, e.Path)
		}
		sort.Strings(got)
		sort.Strings(tc.want)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("target %s entries %v, want %v", tc.target, got, tc.want)
		}
	}
}

// TestBuildFailsClosedWithoutAddedResource proves a missing component fails the
// build instead of being dropped. The component is made genuinely unavailable by
// pointing the shipped-asset lookup at an empty directory; the game-derived
// fail-closed paths (plate scripts, title atlas) have their own tests.
func TestBuildFailsClosedWithoutAddedResource(t *testing.T) {
	svc, dir := integratedService(t)
	// This test proves the production default (no explicit opt-out), so undo
	// the synthetic fixture's allow-missing setting.
	svc.Cfg.AllowMissingResources = false
	// No shipped asset and no resource-directory override.
	svc.PlateAssetsDir = t.TempDir()
	if _, _, err := svc.resolvePlateBase("bg/ED00.ftx"); err == nil {
		t.Fatal("the shipped asset lookup must fail when the asset is unavailable")
	}
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(context.Background(), true, packaging.TargetBoth); err == nil {
		t.Fatal("a build without the added resource must be refused")
	}
	// The missing compose resource itself must fail closed; the plate-order
	// preflight may refuse the same build first, so exercise the resource
	// assembly directly as well.
	if _, _, err := svc.buildResources(); err == nil ||
		!strings.Contains(err.Error(), "added resources could not be produced") {
		t.Fatalf("unexpected resource error: %v", err)
	}
	// the explicit opt-out lets the build proceed and records the omission
	svc2 := &Service{Cfg: svc.Cfg, WorkDir: dir, Loc: svc.Loc, TitleAssetsDir: svc.TitleAssetsDir, PlateAssetsDir: svc.PlateAssetsDir}
	svc2.Cfg.AllowMissingResources = true
	res, err := svc2.Build(context.Background(), true, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Components.AddedResources) != 2+len(techdata.OrderPlateResources()) {
		t.Fatalf("the omission must be recorded: %+v", res.Components.AddedResources)
	}
	var missingRecorded bool
	for _, r := range res.Components.AddedResources {
		if r.Member == "Manual/Manual25.ftx" && !r.Present {
			missingRecorded = true
		}
		if r.Member == "GUI/title.ftx" && !r.Present {
			t.Fatalf("the title footer resource is present and must not be dropped: %+v", r)
		}
	}
	if !missingRecorded {
		t.Fatalf("the omitted manual resource must be recorded as missing: %+v", res.Components.AddedResources)
	}
}

// TestTitleBrandingOptOutWithholdsTheResource proves the explicit, reversible
// opt-out: with title_branding=false the runtime-failing title resource is not
// added, the build still succeeds, the omission is recorded, and the resource
// status keeps the recorded runtime failure. This is the
// last-known-good build shape while the regression is unresolved.
func TestTitleBrandingOptOutWithholdsTheResource(t *testing.T) {
	svc, dir := integratedService(t)
	off := false
	svc.Cfg.TitleBranding = &off
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(context.Background(), true, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive.Report.MemberCountAfter != res.Archive.Report.MemberCountBefore+2 {
		t.Fatalf("with the title branding withheld the manual page is the only added resource: %d -> %d",
			res.Archive.Report.MemberCountBefore, res.Archive.Report.MemberCountAfter)
	}
	var title *ResourceStatus
	for i := range res.Components.AddedResources {
		if res.Components.AddedResources[i].Member == texpack.TitleMember {
			title = &res.Components.AddedResources[i]
		}
	}
	if title == nil {
		t.Fatal("the withheld resource must still be recorded")
	}
	if title.Present {
		t.Fatalf("the title branding must be withheld: %+v", title)
	}
	if !strings.Contains(title.Detail, "title_branding=false") {
		t.Fatalf("the omission must name the setting that caused it: %q", title.Detail)
	}
	if !strings.Contains(title.RuntimeStatus, "RUNTIME CONFIRMED") {
		t.Fatalf("the current implementation must record its confirmed runtime state: %q", title.RuntimeStatus)
	}
	if !strings.Contains(title.RuntimeStatus, "FAILED") {
		t.Fatalf("the earlier runtime failure must survive the opt-out: %q", title.RuntimeStatus)
	}
	if got := strings.Join(res.Components.Notes, "; "); !strings.Contains(got, "WITHHELD") {
		t.Fatalf("the component notes must state the withholding: %v", res.Components.Notes)
	}
	// The opt-out must not touch the executable patch or the dataset.
	if res.Components.ExecutablePatchSHA != techdata.ExecutablePatchSHA256() {
		t.Fatal("the opt-out must not change the executable patch")
	}
	_ = dir
}

// TestTitleBrandingDefaultIsIncluded proves the new default: a settings file
// that does not mention title_branding includes the footer, so a normal build
// carries the member. An explicit false is the only way to withhold it.
func TestTitleBrandingDefaultIsIncluded(t *testing.T) {
	svc, _ := integratedService(t)
	svc.Cfg.TitleBranding = nil // no key in the settings file
	if !svc.Cfg.TitleBrandingEnabled() {
		t.Fatal("the missing title_branding key must default to on")
	}
	if _, err := svc.ResolveExtraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(context.Background(), true, packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive.Report.MemberCountAfter != res.Archive.Report.MemberCountBefore+3 {
		t.Fatalf("the default build must add the text bank, the manual page and the title footer: %d -> %d",
			res.Archive.Report.MemberCountBefore, res.Archive.Report.MemberCountAfter)
	}
	var title *ResourceStatus
	for i := range res.Components.AddedResources {
		if res.Components.AddedResources[i].Member == texpack.TitleMember {
			title = &res.Components.AddedResources[i]
		}
	}
	if title == nil || !title.Present {
		t.Fatalf("the title footer must be included by default: %+v", title)
	}
	if !res.Components.TitleBranding.Enabled || !res.Components.TitleBranding.Included {
		t.Fatalf("the default build must report the footer enabled and included: %+v", res.Components.TitleBranding)
	}
	// The shipped face/style is a release composition, not the composition the
	// 2026-10-19 Eden run confirmed. The build must say so instead of claiming
	// the historical confirmation.
	tb := res.Components.TitleBranding
	if tb.RuntimeVerified {
		t.Fatalf("the shipped composition must not claim the historical runtime confirmation: %+v", tb)
	}
	if tb.Composition != CompositionShipped {
		t.Fatalf("the build must classify the shipped face/style as %q, got %q", CompositionShipped, tb.Composition)
	}
	if tb.FontSHA256 != texpack.ShippedFontSHA256 {
		t.Fatalf("the build must record the shipped face digest: %q", tb.FontSHA256)
	}
}

// TestTitleBrandingTracksModVersion proves the composed title texture changes
// when version.txt changes, and that the version is never hardcoded.
func TestTitleBrandingTracksModVersion(t *testing.T) {
	svc, _ := integratedService(t)
	root := t.TempDir()
	writeVersion := func(v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc.Loc.Root = root

	titleSHA := func() (string, string) {
		t.Helper()
		_, status, err := svc.buildResources()
		if err != nil {
			t.Fatal(err)
		}
		var sha, detail string
		for _, s := range status {
			if s.Member == texpack.TitleMember {
				sha = s.LogicalSHA256
				detail = s.Detail
			}
		}
		if sha == "" {
			t.Fatalf("title resource was not composed: %+v", status)
		}
		return sha, detail
	}

	writeVersion("0.1.0")
	sha1, detail1 := titleSHA()
	if !strings.Contains(detail1, "Unburger Mod v0.1.0") {
		t.Fatalf("label does not carry the version: %q", detail1)
	}
	writeVersion("0.2.0")
	sha2, detail2 := titleSHA()
	if !strings.Contains(detail2, "Unburger Mod v0.2.0") {
		t.Fatalf("label does not carry the new version: %q", detail2)
	}
	if sha1 == sha2 {
		t.Fatal("changing version.txt must change the composed title texture")
	}
}

// TestIntegratedPreflightFailsClosedWithoutInputs proves a missing dataset and
// an unprepared cache produce actionable failures rather than a guess.
func TestIntegratedPreflightFailsClosedWithoutInputs(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{
		Cfg:     config.Config{TranslationsDir: filepath.Join(dir, "absent"), CacheDir: filepath.Join(dir, "cache"), StateDir: filepath.Join(dir, "state")},
		WorkDir: dir,
		Loc:     config.Location{Mode: config.ModeUser, StateDir: filepath.Join(dir, "state")},
	}
	pf, err := svc.Preflight(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if pf.Ready || pf.Failed == 0 {
		t.Fatalf("preflight must fail without inputs: %+v", pf.Steps)
	}
	if pf.Translations == nil || !strings.Contains(pf.Translations.Error, "translation dataset missing/invalid") {
		t.Fatalf("the dataset must fail closed with the canonical message: %+v", pf.Translations)
	}
	if _, err := svc.Build(context.Background(), false, ""); err == nil {
		t.Fatal("a build without inputs must be refused")
	}
}

// TestFreshCheckoutUsesDatasetOnlyAndReportsMissingSources models a fresh
// checkout: the dataset is present, no private sources and no cache exist. The
// tool must still load the dataset, report the cache as not prepared, and ask
// for nothing it can discover itself.
func TestFreshCheckoutUsesDatasetOnlyAndReportsMissingSources(t *testing.T) {
	dir := t.TempDir()
	dsDir := filepath.Join(dir, project.TranslationDirName)
	recs := []translations.Record{{BankKey: "fms/scripttext.fms", RecordIndex: 0, Text: "changed"}}
	if err := translations.Write(dsDir, translations.BuildManifest(gameprofile.Game(), gameprofile.OverlayTarget(), recs), recs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dsDir, translations.ChangesFile)); err != nil {
		t.Fatalf("changes.jsonl must be the dataset file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dsDir, "records.jsonl")); err == nil {
		t.Fatal("records.jsonl must not exist")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	svc := &Service{
		Cfg:     config.Config{CacheDir: filepath.Join(stateDir, "cache"), StateDir: stateDir, RomsBase: filepath.Join(dir, "absent-base")},
		WorkDir: dir,
		Loc:     config.Location{Mode: config.ModeUser, StateDir: stateDir},
	}
	pf, err := svc.Preflight(context.Background(), packaging.TargetBoth)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Translations == nil || !pf.Translations.Loaded || pf.Translations.Records != 1 {
		t.Fatalf("the dataset must load in a fresh checkout: %+v", pf.Translations)
	}
	if pf.Cache == nil || pf.Cache.Ready {
		t.Fatalf("the cache must be reported as not prepared: %+v", pf.Cache)
	}
	if pf.Ready {
		t.Fatal("preflight must not be ready without prepared inputs")
	}
	if len(pf.Sources.Missing) == 0 {
		t.Fatal("missing containers must be reported")
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); err == nil {
		t.Fatal("a read-only preflight must not create an output directory")
	}
	if pf.Target != packaging.TargetBoth {
		t.Fatalf("target must default to both, got %q", pf.Target)
	}
}

// The shipped presentation plate and the manual page it composes into. These
// pins make an accidental replacement of the public asset or a change to the
// composition visible, while the separate override test proves the optional
// user path.
const (
	shippedPlateSHA256      = "144df60eefd0746e24aa946433c64b7ff1c263adfc4489445ca5e23663dea6cf"
	shippedManualPageSHA256 = "2389f23fb411fa2f9a78ca8ef1701b26e18aceaa26775766bcea4721c94079f4"
)

func manualPageStatus(status []ResourceStatus) *ResourceStatus {
	for i := range status {
		if status[i].Member == "Manual/Manual25.ftx" {
			return &status[i]
		}
	}
	return nil
}

// TestShippedPresentationPlateIsTheDefault proves the standard build no longer
// needs a user-supplied image: the public asset is resolved by default and
// produces the accepted end-of-route page.
func TestShippedPresentationPlateIsTheDefault(t *testing.T) {
	svc, _ := integratedService(t)
	path := svc.ShippedPlatePath()
	if path == "" {
		t.Fatal("the shipped presentation plate was not found")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := hashutil.Bytes(raw); got != shippedPlateSHA256 {
		t.Fatalf("shipped plate digest %s, want %s", got, shippedPlateSHA256)
	}
	basePath, how, err := svc.resolvePlateBase("bg/ED00.ftx")
	if err != nil {
		t.Fatal(err)
	}
	if how != "shipped project asset" {
		t.Fatalf("default resolution reported %q (%s), want the shipped asset", how, basePath)
	}
	_, status, err := svc.buildResources()
	if err != nil {
		t.Fatal(err)
	}
	st := manualPageStatus(status)
	if st == nil || !st.Present {
		t.Fatalf("the added manual page was not composed: %+v", status)
	}
	if !strings.Contains(st.Detail, "shipped project asset") {
		t.Fatalf("the manual page did not record the shipped asset: %q", st.Detail)
	}
	if st.LogicalSHA256 != shippedManualPageSHA256 {
		t.Fatalf("composed page digest %s, want %s", st.LogicalSHA256, shippedManualPageSHA256)
	}
}

// TestResourceDirectoryPlateOverridesShippedAsset proves a user can still
// replace the presentation plate deliberately; it is an optional override, not
// a requirement.
func TestResourceDirectoryPlateOverridesShippedAsset(t *testing.T) {
	svc, _ := integratedService(t)
	writeSyntheticPlateOverride(t, svc.StateDir())
	path, how, err := svc.resolvePlateBase("bg/ED00.ftx")
	if err != nil {
		t.Fatal(err)
	}
	if how != "resource directory" {
		t.Fatalf("override resolution reported %q (%s)", how, path)
	}
	_, status, err := svc.buildResources()
	if err != nil {
		t.Fatal(err)
	}
	st := manualPageStatus(status)
	if st == nil || !st.Present {
		t.Fatalf("the added manual page was not composed: %+v", status)
	}
	if st.LogicalSHA256 == shippedManualPageSHA256 {
		t.Fatal("the optional override must change the composed page")
	}
	// An explicit but absent path still fails closed instead of silently
	// falling back to the shipped asset.
	svc.Cfg.PlateBase = filepath.Join(t.TempDir(), "absent-plate.png")
	if _, _, err := svc.resolvePlateBase("bg/ED00.ftx"); err == nil {
		t.Fatal("a configured but absent override must fail closed")
	}
}
