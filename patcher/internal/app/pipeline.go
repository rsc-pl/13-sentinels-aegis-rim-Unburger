package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"13s-patcher/internal/build"
	"13s-patcher/internal/extraction"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/localsources"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/patchdata"
	"13s-patcher/internal/progress"
	"13s-patcher/internal/source"
	"13s-patcher/internal/techdata"
	"13s-patcher/internal/translations"
)

// Step is one preflight step.
type Step struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// PreflightReport is the pre-build validation report.
type PreflightReport struct {
	Tool         string               `json:"tool"`
	Version      string               `json:"version"`
	Mode         string               `json:"mode"`
	ConfigPath   string               `json:"config_path"`
	ProjectRoot  string               `json:"project_root,omitempty"`
	Steps        []Step               `json:"steps"`
	Passed       int                  `json:"passed"`
	Failed       int                  `json:"failed"`
	Warned       int                  `json:"warned"`
	Ready        bool                 `json:"ready"`
	Translations *TranslationStatus   `json:"translations,omitempty"`
	Cache        *CacheStatus         `json:"cache,omitempty"`
	Sources      *localsources.Report `json:"sources,omitempty"`
	Keys         *KeysStatus          `json:"keys,omitempty"`
	Derived      *DerivedStats        `json:"derived,omitempty"`
	Source       SourceStatus         `json:"source"`
	PatchData    BundleStatus         `json:"patch_data"`
	Target       packaging.Target     `json:"target"`
	OutputPath   string               `json:"output_path"`
	OutputDir    string               `json:"output_dir"`
	Outputs      []string             `json:"outputs,omitempty"`
	Warnings     []string             `json:"warnings,omitempty"`
}

func (r *PreflightReport) add(id, title, status, detail string) {
	r.Steps = append(r.Steps, Step{ID: id, Title: title, Status: status, Detail: detail})
	switch status {
	case StatusPass:
		r.Passed++
	case StatusFail:
		r.Failed++
	case StatusWarn:
		r.Warned++
	}
}

func (r *PreflightReport) finish() { r.Ready = r.Failed == 0 }

// prepareResult carries everything a build needs after preparation.
type prepareResult struct {
	Report    *PreflightReport
	Dataset   *translations.Dataset
	State     *extraction.State
	Plan      *patchdata.ExportInput
	Stats     *DerivedStats
	OutputDir string
}

// Preflight validates everything a build needs, without building and without
// populating the cache.
func (s *Service) Preflight(ctx context.Context, target packaging.Target) (*PreflightReport, error) {
	res, err := s.prepare(ctx, false)
	if res == nil {
		return nil, err
	}
	if res.Report != nil {
		res.Report.Target = s.effectiveTarget(target)
		for _, t := range res.Report.Target.Targets() {
			res.Report.Outputs = append(res.Report.Outputs, filepath.Join(s.OutputDirOrDefault(), packaging.ArchiveName(t, s.ReleaseLabel())))
		}
	}
	return res.Report, err
}

// effectiveTarget resolves an empty target against the configuration.
func (s *Service) effectiveTarget(t packaging.Target) packaging.Target {
	if t == "" {
		return s.Target()
	}
	return t
}

// prepare runs the shared preparation sequence. When populate is set it also
// populates the extraction cache and (for the integrated flow) plans the
// derived bundle.
func (s *Service) prepare(ctx context.Context, populate bool) (*prepareResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rep := &PreflightReport{
		Tool: build.ToolName, Version: build.VersionString(), Mode: s.Loc.Mode,
		ConfigPath: s.CfgPath, ProjectRoot: s.Loc.Root,
		Target:    s.Target(),
		OutputDir: s.OutputDirOrDefault(), OutputPath: s.BuildArchivePath(),
	}
	res := &prepareResult{Report: rep, OutputDir: rep.OutputDir}

	if s.Cfg.PatchDataDir != "" {
		return s.prepareBundle(ctx, res)
	}
	return s.prepareIntegrated(ctx, res, populate)
}

// ---------------------------------------------------------------------------
// integrated flow
// ---------------------------------------------------------------------------

func (s *Service) prepareIntegrated(ctx context.Context, res *prepareResult, populate bool) (*prepareResult, error) {
	rep := res.Report
	s.emit(progress.Event{Stage: progress.StageInputs, Status: progress.StatusStart, Note: "settings"})
	rep.add("config", "settings loaded", StatusPass, fmt.Sprintf("%s (%s mode)", s.CfgPath, s.Loc.Mode))
	if s.InProject() {
		rep.add("storage", "project storage", StatusPass, s.Loc.PrivateDir)
	} else {
		rep.add("storage", "per-user storage", StatusPass, s.StateDir())
	}

	// 1. dataset
	ds, err := s.LoadTranslations()
	ts := &TranslationStatus{Dir: s.TranslationsDir()}
	if err != nil {
		ts.Error = err.Error()
		rep.Translations = ts
		rep.add("translations", "translation dataset", StatusFail, err.Error())
		rep.finish()
		return res, nil
	}
	ts.Loaded = true
	ts.Schema = ds.Manifest.Schema
	ts.Records = len(ds.Records)
	ts.Banks = ds.Manifest.BankCount
	ts.ChangesSHA256 = ds.Manifest.ChangesSHA256
	ts.TargetSHA256 = ds.Manifest.TargetSource.SHA256
	ts.TargetSize = ds.Manifest.TargetSource.Size
	rep.Translations = ts
	rep.add("translations", "translation dataset", StatusPass,
		fmt.Sprintf("%d record(s) in %d bank(s) · %s", ts.Records, ts.Banks, hashutil.Short(ts.ChangesSHA256)))
	s.emit(progress.Event{Stage: progress.StageTranslations, Status: progress.StatusDone,
		Completed: int64(ts.Records), Total: int64(ts.Records),
		Note: fmt.Sprintf("%d record(s) in %d bank(s)", ts.Records, ts.Banks)})
	res.Dataset = ds

	// 2. local containers
	srcRep, derr := s.DiscoverSources()
	if derr != nil {
		rep.add("sources", "local containers", StatusFail, derr.Error())
		s.emit(progress.Event{Stage: progress.StageDiscovery, Status: progress.StatusError, Error: derr.Error()})
	} else {
		rep.Sources = srcRep
		switch {
		case len(srcRep.Missing) > 0:
			rep.add("sources", "local containers", StatusWarn, fmt.Sprintf("missing: %v (only needed to rebuild the cache)", srcRep.Missing))
		default:
			rep.add("sources", "local containers", StatusPass, fmt.Sprintf("%d container(s) identified", len(srcRep.Found)))
		}
		s.emit(progress.Event{Stage: progress.StageDiscovery, Status: progress.StatusDone,
			Completed: int64(len(srcRep.Found)), Total: int64(len(srcRep.Found) + len(srcRep.Missing)),
			Note: fmt.Sprintf("%d container(s) identified", len(srcRep.Found))})
	}

	// 3. keys (presence only)
	keys := s.KeysPresence()
	rep.Keys = &keys
	if len(keys.Present) > 0 {
		rep.add("keys", "key files (presence only)", StatusPass, fmt.Sprintf("%d present in %s", len(keys.Present), keys.Dir))
	} else {
		rep.add("keys", "key files (presence only)", StatusWarn, "none found in "+keys.Dir+" (only needed to rebuild the cache)")
	}

	// 4. verified cache / extraction inputs
	cache := &CacheStatus{Dir: s.CacheDir()}
	var st *extraction.State
	s.emit(progress.Event{Stage: progress.StageExtraction, Status: progress.StatusStart,
		Note: map[bool]string{true: "resolving build inputs", false: "verifying cache"}[populate]})
	if populate {
		st, err = s.ResolveExtraction(ctx)
	} else {
		st, err = s.VerifyExtraction(ctx)
	}
	if err != nil {
		cache.Error = err.Error()
		rep.Cache = cache
		hint := "run `setup`, `build` or `validate` to populate it"
		if populate {
			hint = "supply the local containers and keys, or an existing cache/extraction"
		}
		detail := fmt.Sprintf("%v (%s)", err, hint)
		if advice := s.ExtractionAdvice(); advice != "" {
			rep.Warnings = append(rep.Warnings, advice)
		}
		rep.add("cache", "verified build inputs", StatusFail, detail)
		rep.finish()
		return res, nil
	}
	cache.Ready = true
	cache.OverlayPath = st.OverlayPath
	cache.OverlaySHA = st.OverlaySHA256
	cache.BankCount = st.BankCount
	cache.MemberCount = st.MemberCount
	cache.Source = describeSource(st)
	rep.Cache = cache
	rep.add("cache", "verified build inputs", StatusPass,
		fmt.Sprintf("overlay %s · %d base bank(s) · %d base member(s) · %s", hashutil.Short(st.OverlaySHA256), st.BankCount, st.MemberCount, cache.Source))
	s.emit(progress.Event{Stage: progress.StageExtraction, Status: progress.StatusDone,
		Completed: 1, Total: 1, Note: fmt.Sprintf("overlay %s · %d base bank(s) · %d base member(s)", hashutil.Short(st.OverlaySHA256), st.BankCount, st.MemberCount)})
	res.State = st

	// 5. plan the derived bundle (read-only baseline validation)
	s.emit(progress.Event{Stage: progress.StagePlan, Status: progress.StatusStart,
		Note: fmt.Sprintf("%d record(s)", len(ds.Records))})
	plan, stats, perr := s.PlanDerivedBundle(ds, st)
	if perr != nil {
		rep.add("plan", "patch plan validated", StatusFail, perr.Error())
		s.emit(progress.Event{Stage: progress.StagePlan, Status: progress.StatusError, Error: perr.Error()})
		rep.finish()
		return res, nil
	}
	res.Plan = plan
	res.Stats = stats
	rep.Derived = stats
	rep.add("plan", "patch plan validated", StatusPass,
		fmt.Sprintf("%d operation(s) · %d bank(s) (%d edited, %d added) · %d token exception(s)",
			stats.Operations, stats.Members, stats.EditedBanks, stats.AddedBanks, stats.TokenExceptions))
	s.emit(progress.Event{Stage: progress.StagePlan, Status: progress.StatusDone,
		Completed: int64(stats.Operations), Total: int64(stats.Operations),
		Note: fmt.Sprintf("%d bank(s), %d edited, %d added", stats.Members, stats.EditedBanks, stats.AddedBanks)})

	// 5b. in-game title branding readiness. The footer is part of a normal
	// build; when it is enabled (the default) the live display face and style
	// and the user-supplied base atlas must resolve, so a missing one fails
	// preflight with the same actionable message the build uses rather than
	// being silently dropped.
	if _, _, _, _, ok := TitleResource(); ok {
		if !s.Cfg.TitleBrandingEnabled() {
			rep.add("title_branding", "in-game title branding", StatusPass,
				"disabled (title_branding=false); the footer member is not added")
		} else if _, aerr := s.LoadTitleAssets(); aerr != nil {
			rep.add("title_branding", "in-game title branding assets", StatusFail, aerr.Error())
			rep.Warnings = append(rep.Warnings, "in-game title branding is enabled but its display face or style could not be loaded")
		} else if p, how, terr := s.ResolveTitleBase(); terr != nil {
			rep.add("title_branding", "in-game title branding base", StatusFail, terr.Error())
			rep.Warnings = append(rep.Warnings, "in-game title branding is enabled but its base atlas is missing")
		} else {
			rep.add("title_branding", "in-game title branding base", StatusPass, p+" ("+how+")")
		}
	}

	// 5c. story-plate name order. The transform is applied to the user-supplied
	// base plate scripts. The production shape requires all 13 scripts: a
	// partial set would ship a build with only some protagonists corrected,
	// which is exactly the silent-incomplete result the project refuses. A
	// missing script therefore fails closed unless the settings explicitly
	// allow missing resources; then the omission is recorded as a warning and
	// in the per-resource build status. A present script with a wrong identity
	// always fails instead of being patched blindly.
	if orders := techdata.OrderPlateResources(); len(orders) > 0 {
		var missingPlates []string
		for _, r := range orders {
			if _, _, err := s.ResolvePlateBase(r); err != nil {
				missingPlates = append(missingPlates, r.Member)
			}
		}
		switch {
		case len(missingPlates) == 0:
			rep.add("plate_order", "story-plate name order", StatusPass,
				fmt.Sprintf("%d plate script(s) ready; surname placed in the upper slot, lower line left-aligned per plate", len(orders)))
		case s.Cfg.AllowMissingResources:
			rep.add("plate_order", "story-plate name order", StatusWarn,
				fmt.Sprintf("%d of %d plate script(s) not found and will be skipped (allow_missing_resources=true): %s",
					len(missingPlates), len(orders), strings.Join(missingPlates, ", ")))
			rep.Warnings = append(rep.Warnings, "story-plate name order is incomplete by explicit settings: the user-supplied GUI/change_*.mbs base scripts are not all present")
		default:
			rep.add("plate_order", "story-plate name order", StatusFail,
				fmt.Sprintf("%d of %d plate script(s) missing: %s; rebuild the verified cache from your own containers (both containers and the key files are required), or place the logical members in %s (or set allow_missing_resources=true for an explicitly partial build)",
					len(missingPlates), len(orders), strings.Join(missingPlates, ", "), s.ResourcesDir()))
			rep.Warnings = append(rep.Warnings, "story-plate name order is incomplete: the build is refused so a partial 13-character plate set is never shipped")
		}
	}

	// 6. output safety
	outPath := rep.OutputPath
	if sameFile(outPath, st.OverlayPath) {
		rep.add("output.distinct", "intermediate archive is separate", StatusFail, "intermediate path equals the overlay archive")
	} else if insideAny(outPath, s.ReadonlyRoots()) && !insideAny(outPath, s.AllowedWriteRoots()) {
		rep.add("output.distinct", "intermediate archive is separate", StatusFail, "intermediate path is inside a read-only location")
	} else {
		rep.add("output.distinct", "intermediate archive is separate", StatusPass, outPath)
	}
	od := s.OutputDirOrDefault()
	if dirExists(od) {
		rep.add("output.dir", "output directory", StatusPass, od)
	} else if dirExists(filepath.Dir(od)) {
		rep.add("output.dir", "output directory", StatusWarn, "will be created: "+od)
	} else {
		rep.add("output.dir", "output directory", StatusWarn, "will be created (with parents): "+od)
	}
	existing := s.existingDestinations(rep.Target)
	if len(existing) > 0 {
		rep.add("output.existing", "existing output archives", StatusWarn,
			strings.Join(baseNames(existing), ", ")+" (replace from the dashboard, or pass --force on the CLI)")
	} else {
		rep.add("output.existing", "existing output archives", StatusPass, "none")
	}
	rep.finish()
	return res, nil
}

// existingDestinations returns the configured archive outputs that already
// exist for a target.
func (s *Service) existingDestinations(target packaging.Target) []string {
	var out []string
	for _, p := range s.DestinationArchives(target) {
		if fileIsFile(p) {
			out = append(out, p)
		}
	}
	return out
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

func describeSource(st *extraction.State) string {
	switch {
	case st.FromCache:
		return "verified cache"
	default:
		return "resolved"
	}
}

// ---------------------------------------------------------------------------
// explicit-bundle flow (advanced/manual override)
// ---------------------------------------------------------------------------

func (s *Service) prepareBundle(ctx context.Context, res *prepareResult) (*prepareResult, error) {
	rep := res.Report
	rep.add("config", "settings loaded", StatusPass, s.CfgPath)
	bundle, err := s.LoadBundle()
	if err != nil {
		rep.PatchData = BundleStatus{Dir: s.BundleDir(), Error: err.Error()}
		rep.add("patch_data", "patch data bundle", StatusFail, err.Error())
		rep.finish()
		return res, nil
	}
	rep.PatchData = BundleStatus{
		Dir: s.BundleDir(), Exists: true, Loaded: true,
		BundleID: bundle.Manifest.BundleID, BundleVersion: bundle.Manifest.BundleVersion,
		ReleaseVersion: bundle.Manifest.ReleaseVersion, ManifestSHA256: bundle.ManifestSHA256,
		Operations: bundle.OperationCount(),
	}
	rep.add("patch_data", "patch data bundle", StatusPass,
		fmt.Sprintf("%s %s · %d operations · manifest %s", bundle.Manifest.BundleID,
			bundle.Manifest.BundleVersion, bundle.OperationCount(), hashutil.Short(bundle.ManifestSHA256)))
	if bundle.OperationCount() == 0 {
		rep.add("patch_data.ops", "bundle carries operations", StatusFail, "bundle has no operations")
	} else {
		rep.add("patch_data.ops", "bundle carries operations", StatusPass, fmt.Sprintf("%d operations", bundle.OperationCount()))
	}

	srcPath, cands, _ := s.LocateSource(bundle)
	if srcPath == "" {
		detail := "not found"
		if len(cands) > 0 {
			detail = fmt.Sprintf("%d candidates; set source_archive", len(cands))
		}
		rep.Source = SourceStatus{Path: "", Error: detail}
		rep.add("source.locate", "game archive located", StatusFail, detail)
		rep.finish()
		return res, nil
	}
	rep.Source.Path = srcPath
	rep.add("source.locate", "game archive located", StatusPass, srcPath)
	spec, ok := bundle.SourceByRole(build.RolePatchArchive)
	if !ok {
		rep.Source.Error = "bundle does not declare a patch_archive source"
		rep.add("source.identity", "game archive identity", StatusFail, rep.Source.Error)
		rep.finish()
		return res, nil
	}
	rep.Source.Required = spec.SHA256
	id, err := source.Match(srcPath, spec)
	rep.Source.Identity = id
	if err != nil {
		rep.Source.Error = err.Error()
		rep.add("source.identity", "game archive identity", StatusFail, err.Error())
	} else {
		rep.Source.Supported = true
		rep.add("source.identity", "game archive identity", StatusPass,
			fmt.Sprintf("%d members · sha256 %s", id.Members, hashutil.Short(id.SHA256)))
	}
	res.OutputDir = s.OutputDirOrDefault()
	rep.add("output.dir", "output directory", StatusPass, res.OutputDir)
	rep.finish()
	return res, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// LoadLastBuild reads the persisted last-build summary, if any.
func (s *Service) LoadLastBuild() (*BuildSummary, bool) {
	if !fileIsFile(s.Cfg.LastBuild) {
		return nil, false
	}
	bs, err := readBuildSummary(s.Cfg.LastBuild)
	if err != nil {
		return nil, false
	}
	return bs, true
}

// ReadonlyRoots are locations a build must never write into.
func (s *Service) ReadonlyRoots() []string {
	roots := []string{s.TranslationsDir(), s.CacheDir(), s.KeysDir()}
	roots = append(roots, s.RomsDirs()...)
	if s.InProject() && s.Loc.PrivateDir != "" {
		roots = append(roots, s.Loc.PrivateDir)
	}
	return dedupe(roots)
}

// AllowedWriteRoots are the tool's own state locations; they may sit inside a
// read-only root (the project's private directory) and are still writable.
func (s *Service) AllowedWriteRoots() []string {
	dirs := []string{s.StateDir(), s.DerivedDir(), s.WorkCacheDir(), s.ReportsDir()}
	if s.Cfg.OutputDir != "" {
		dirs = append(dirs, s.OutputDirOrDefault())
	}
	return dedupe(dirs)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return aa == bb
}

func insideDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	pa, err1 := filepath.Abs(path)
	da, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(da, pa)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func insideAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if insideDir(path, d) {
			return true
		}
	}
	return false
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "bundle"
	}
	return string(out)
}

// WriteReport writes a JSON report atomically, creating parent directories.
func (s *Service) WriteReport(path string, v any) error {
	if path == "" {
		return fmt.Errorf("empty report path")
	}
	return writeJSON(path, v)
}

// BundlePath is a convenience accessor for the loaded bundle directory string.
func (s *Service) BundlePath() string { return s.BundleDir() }
