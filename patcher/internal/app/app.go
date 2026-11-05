// Package app is the shared service layer used by both the CLI and the TUI.
// Every operation (discovery, preflight, build, validation, inspection) is
// implemented exactly once here; the CLI and the TUI only format and drive
// these methods. Long operations accept a context and never leave partial
// artifacts.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/build"
	"13s-patcher/internal/config"
	"13s-patcher/internal/extraction"
	"13s-patcher/internal/gameprofile"
	"13s-patcher/internal/localsources"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/patchdata"
	"13s-patcher/internal/progress"
	"13s-patcher/internal/project"
	"13s-patcher/internal/source"
	"13s-patcher/internal/techdata"
	"13s-patcher/internal/translations"
)

// ErrCancelled is returned by a cancelled long operation.
var ErrCancelled = errors.New("operation cancelled")

// Options configures a Service. Explicit fields override the config file.
type Options struct {
	ConfigPath    string
	WorkDir       string
	ProjectRoot   string
	SourceArchive string
	PatchDataDir  string
	OutputDir     string
	Encoder       string
}

// Service bundles the resolved configuration and the pipeline operations.
type Service struct {
	Cfg     config.Config
	CfgPath string
	WorkDir string
	Loc     config.Location

	// ephemeral remembers which settings a one-off CLI option replaced and what
	// the persisted value was. The overrides are restored before the settings
	// file is written, so a one-off flag can never leak into it.
	ephemeral ephemeralOverrides

	// TitleAssetsDir optionally overrides where the title-footer display face
	// and its style file are read from. It is empty in production, where the
	// directory is resolved relative to the project root; tests set it to the
	// module's assets/fonts so the live files are exercised.
	TitleAssetsDir string

	// PlateAssetsDir optionally overrides where the shipped presentation
	// plate is read from. It is empty in production, where the directory is
	// resolved relative to the project root; tests set it to the module's
	// assets/images so the shipped asset is exercised directly.
	PlateAssetsDir string

	// Logf, when set, receives human-readable progress lines.
	Logf func(string)

	// Progress, when set, receives structured progress events for long
	// operations. It is optional and independent of Logf.
	Progress progress.Func
}

func (s *Service) emit(e progress.Event) { s.Progress.Emit(e) }

// ephemeralOverrides records the settings that a one-off CLI option replaced
// for this run only, together with the values that were persisted before the
// run. Only these four fields can come from Options; -target is handled by the
// caller and is never folded into the settings file this way.
type ephemeralOverrides struct {
	sourceArchive string
	patchDataDir  string
	outputDir     string
	encoder       string

	setSourceArchive bool
	setPatchDataDir  bool
	setOutputDir     bool
	setEncoder       bool
}

// persisted returns a copy of c with the one-off overrides replaced by the
// values that were persisted before the run. Durable settings and the last
// build/validation results are kept exactly as they are.
func (e ephemeralOverrides) persisted(c config.Config) config.Config {
	if e.setSourceArchive {
		c.SourceArchive = e.sourceArchive
	}
	if e.setPatchDataDir {
		c.PatchDataDir = e.patchDataDir
	}
	if e.setOutputDir {
		c.OutputDir = e.outputDir
	}
	if e.setEncoder {
		c.Encoder = e.encoder
	}
	return c
}

// New loads the configuration and builds a Service.
func New(opts Options) (*Service, error) {
	wd := opts.WorkDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	wd, err := filepath.Abs(wd)
	if err != nil {
		return nil, err
	}
	loc, err := config.ResolveLocation(opts.ConfigPath, wd, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(loc.Path)
	if err != nil {
		return nil, err
	}
	// One-time migration: a per-directory override in a project checkout is
	// adopted into the project-private settings file, which then becomes the
	// single authoritative source. The old file is left untouched.
	if !cfg.Exists() && loc.SeedFrom != "" {
		if seed, serr := config.Load(loc.SeedFrom); serr == nil {
			seed.Path = loc.Path
			cfg = seed
			if derr := os.MkdirAll(filepath.Dir(loc.Path), 0o755); derr == nil {
				_ = cfg.Save(loc.Path)
			}
		}
	}
	s := &Service{Cfg: cfg, CfgPath: loc.Path, WorkDir: wd, Loc: loc}
	if opts.SourceArchive != "" {
		s.ephemeral.sourceArchive, s.ephemeral.setSourceArchive = s.Cfg.SourceArchive, true
		s.Cfg.SourceArchive = opts.SourceArchive
	}
	if opts.PatchDataDir != "" {
		s.ephemeral.patchDataDir, s.ephemeral.setPatchDataDir = s.Cfg.PatchDataDir, true
		s.Cfg.PatchDataDir = opts.PatchDataDir
	}
	if opts.OutputDir != "" {
		s.ephemeral.outputDir, s.ephemeral.setOutputDir = s.Cfg.OutputDir, true
		s.Cfg.OutputDir = opts.OutputDir
	}
	if opts.Encoder != "" {
		s.ephemeral.encoder, s.ephemeral.setEncoder = s.Cfg.Encoder, true
		s.Cfg.Encoder = opts.Encoder
	}
	return s, nil
}

func (s *Service) log(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(fmt.Sprintf(format, args...))
	}
}

// Save persists the current configuration. It is the explicit durable write
// used by the settings pane and by setup: every field currently held is stored.
func (s *Service) Save() error {
	if s.Loc.Mode == config.ModeProject {
		if err := os.MkdirAll(filepath.Dir(s.CfgPath), 0o755); err != nil {
			return err
		}
	}
	return s.Cfg.Save(s.CfgPath)
}

// savePersisted writes the settings file with any one-off CLI overrides restored
// to their persisted values. A one-off flag therefore stays ephemeral for this
// run, while durable results (discoveries, last build/validation) are still
// written. Use it on every implicit save; use Save for explicit durable writes.
func (s *Service) savePersisted() error {
	if s.Loc.Mode == config.ModeProject {
		if err := os.MkdirAll(filepath.Dir(s.CfgPath), 0o755); err != nil {
			return err
		}
	}
	return s.ephemeral.persisted(s.Cfg).Save(s.CfgPath)
}

// InProject reports whether a project checkout was detected.
func (s *Service) InProject() bool { return s.Loc.Mode == config.ModeProject }

func (s *Service) resolve(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(s.WorkDir, p))
}

// ---------------------------------------------------------------------------
// storage roots
// ---------------------------------------------------------------------------

// StateDir is where derived state (cache, reports, derived bundles) lives.
func (s *Service) StateDir() string {
	if s.Cfg.StateDir != "" {
		return s.resolve(s.Cfg.StateDir)
	}
	if s.Loc.StateDir != "" {
		return s.Loc.StateDir
	}
	return s.WorkDir
}

// CacheDir is the verified extraction cache.
func (s *Service) CacheDir() string {
	if s.Cfg.CacheDir != "" {
		return s.resolve(s.Cfg.CacheDir)
	}
	return filepath.Join(s.StateDir(), "cache")
}

// ReportsDir holds generated reports.
func (s *Service) ReportsDir() string { return filepath.Join(s.StateDir(), "reports") }

// WorkCacheDir holds intermediate build artifacts (the rebuilt archive the
// packaging step consumes). They are not user-facing outputs, so a repeated
// build never collides with its own intermediate and never needs a delete.
func (s *Service) WorkCacheDir() string { return filepath.Join(s.StateDir(), "work") }

// BuildArchivePath is the intermediate rebuilt archive. It lives in the work
// cache: the ZIP archives in the output directory are the primary outputs.
func (s *Service) BuildArchivePath() string {
	return filepath.Join(s.WorkCacheDir(), s.OutputFileName())
}

// DerivedDir holds derived patch-data bundles.
func (s *Service) DerivedDir() string { return filepath.Join(s.StateDir(), "derived") }

// TranslationsDir is the public translation dataset directory.
func (s *Service) TranslationsDir() string {
	if s.Cfg.TranslationsDir != "" {
		return s.resolve(s.Cfg.TranslationsDir)
	}
	if s.InProject() && s.Loc.Root != "" {
		return project.For(s.Loc.Root).TranslationsDir
	}
	d := filepath.Join(s.WorkDir, project.TranslationDirName)
	return d
}

// RomsDirs returns the directories scanned for local containers.
func (s *Service) RomsDirs() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs := s.resolve(p)
		for _, x := range out {
			if x == abs {
				return
			}
		}
		out = append(out, abs)
	}
	if s.InProject() {
		p := project.For(s.Loc.Root)
		add(p.RomsBase)
		add(p.RomsUpdate)
		if s.Cfg.RomsBase != "" {
			add(s.Cfg.RomsBase)
		}
		if s.Cfg.RomsUpdate != "" {
			add(s.Cfg.RomsUpdate)
		}
		add(p.RomsExtra)
		if s.Cfg.RomsExtra != "" {
			add(s.Cfg.RomsExtra)
		}
		return out
	}
	add(s.Cfg.RomsBase)
	add(s.Cfg.RomsUpdate)
	add(s.Cfg.RomsExtra)
	return out
}

// KeysDir is the directory holding the user's key files.
func (s *Service) KeysDir() string {
	if s.Cfg.KeysDir != "" {
		return s.resolve(s.Cfg.KeysDir)
	}
	if s.InProject() {
		return project.For(s.Loc.Root).KeysDir
	}
	return filepath.Join(s.WorkDir, "keys")
}

// RequiredBaseMembers returns the game-derived members this build needs for the
// current settings: every plate script always, and the title atlas only while
// the title-screen footer branding is enabled. A disabled resource must not
// make the cache demand an input the build will not use.
func (s *Service) RequiredBaseMembers() []string {
	all := techdata.BaseMemberPaths()
	if s.Cfg.TitleBrandingEnabled() {
		return all
	}
	titleHint := ""
	if _, hint, _, _, ok := TitleResource(); ok {
		titleHint = hint
	}
	out := make([]string, 0, len(all))
	for _, m := range all {
		if titleHint != "" && strings.EqualFold(m, titleHint) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SearchRoots are scanned for existing extractions and verified artifacts.
func (s *Service) SearchRoots() []string {
	var out []string
	if s.InProject() {
		out = append(out, project.For(s.Loc.Root).SearchRoots()...)
	}
	out = append(out, s.RomsDirs()...)
	out = append(out, s.StateDir())
	out = append(out, s.CacheDir())
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------------------
// legacy bundle-mode accessors
// ---------------------------------------------------------------------------

// BundleDir resolves the patch-data bundle directory.
func (s *Service) BundleDir() string { return s.resolve(s.Cfg.PatchDataDir) }

// SourceArchive resolves the source archive path.
func (s *Service) SourceArchive() string { return s.resolve(s.Cfg.SourceArchive) }

// OutputDirOrDefault resolves the configured output directory. In a project
// checkout the default is the private state directory, so generated archives
// never land in the public tree.
func (s *Service) OutputDirOrDefault() string {
	if s.Cfg.OutputDir != "" {
		return s.resolve(s.Cfg.OutputDir)
	}
	if s.InProject() {
		return filepath.Join(s.StateDir(), "out")
	}
	return filepath.Join(s.WorkDir, "out")
}

// OutputFileName returns the built archive file name. When not configured it is
// derived from the source archive name, except in the integrated flow where the
// loader expects a fixed name and that name is used directly.
func (s *Service) OutputFileName() string {
	if s.Cfg.OutputName != "" {
		return s.Cfg.OutputName
	}
	if s.Cfg.PatchDataDir == "" {
		if p := s.SourcePath(); p != "" {
			if id, err := source.Identify(p); err == nil && id.SHA256 == overlayDeploySHA256 {
				return overlayDeployName
			}
		}
	}
	src := s.SourcePath()
	if src != "" {
		base := filepath.Base(src)
		ext := filepath.Ext(base)
		stem := base
		if ext != "" {
			stem = base[:len(base)-len(ext)]
		} else {
			ext = ".CPK"
		}
		if stem == "" {
			stem = "patched"
		}
		return stem + ".patched" + ext
	}
	return "patched.CPK"
}

// OutputPath resolves the historical standalone-archive destination. The
// integrated build writes its intermediate archive to BuildArchivePath (the
// work cache) and its user-facing outputs as per-target ZIP archives; this path
// is kept for explicit single-archive use and reports.
func (s *Service) OutputPath() string {
	return filepath.Join(s.OutputDirOrDefault(), s.OutputFileName())
}

// DestinationArchives lists the per-target ZIP output paths for a target.
func (s *Service) DestinationArchives(target packaging.Target) []string {
	if target == "" {
		target = s.Target()
	}
	var out []string
	for _, t := range target.Targets() {
		out = append(out, filepath.Join(s.OutputDirOrDefault(), packaging.ArchiveName(t, s.ReleaseLabel())))
	}
	return out
}

// overlayDeployName is the archive name the loader expects for the update
// overlay, and overlayDeploySHA256 pins the accepted revision. The name is part
// of the public installed layout, so it is written directly.
const (
	overlayDeployName   = "ROBO_PATCH_US.CPK"
	overlayDeploySHA256 = gameprofile.OverlaySHA256
)

// SourcePath returns the effective overlay archive path, when known: an
// explicit configuration wins, otherwise the verified cache.
func (s *Service) SourcePath() string {
	if p := s.SourceArchive(); p != "" {
		return p
	}
	if p, _, ok := extraction.ReadCachedOverlay(s.CacheDir()); ok {
		return p
	}
	return ""
}

// FindBundleDir auto-detects a bundle directory under dir (one or two levels).
func FindBundleDir(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	if fi, err := os.Stat(filepath.Join(dir, patchdata.ManifestName)); err == nil && !fi.IsDir() {
		return dir, true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var found []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if fi, err := os.Stat(filepath.Join(sub, patchdata.ManifestName)); err == nil && !fi.IsDir() {
			found = append(found, sub)
		}
	}
	sort.Strings(found)
	if len(found) == 1 {
		return found[0], true
	}
	return "", false
}

// LoadBundle resolves and loads an explicitly configured bundle.
func (s *Service) LoadBundle() (*patchdata.Bundle, error) {
	dir := s.BundleDir()
	if dir == "" {
		return nil, fmt.Errorf("no patch-data bundle configured (set patch_data_dir or pass --patch-data)")
	}
	return patchdata.Load(dir)
}

// LocateSource resolves the source archive, honouring the bundle's path hint.
func (s *Service) LocateSource(bundle *patchdata.Bundle) (string, []source.Candidate, error) {
	if p := s.SourceArchive(); p != "" {
		return p, nil, nil
	}
	if p := s.SourcePath(); p != "" {
		return p, nil, nil
	}
	hint := ""
	if bundle != nil {
		if spec, ok := bundle.SourceByRole(build.RolePatchArchive); ok {
			hint = spec.PathHint
		}
	}
	cands, err := source.FindArchive(s.WorkDir, hint)
	if err != nil {
		return "", nil, nil
	}
	if len(cands) == 1 || (len(cands) > 1 && cands[0].Hint) {
		return cands[0].Path, cands, nil
	}
	return "", cands, nil
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

// SourceStatus describes the source archive state.
type SourceStatus struct {
	Path      string           `json:"path"`
	Exists    bool             `json:"exists"`
	Required  string           `json:"required_sha256,omitempty"`
	Identity  *source.Identity `json:"identity,omitempty"`
	Supported bool             `json:"supported"`
	Error     string           `json:"error,omitempty"`
}

// BundleStatus describes the patch-data bundle state.
type BundleStatus struct {
	Dir            string `json:"dir"`
	Exists         bool   `json:"exists"`
	Loaded         bool   `json:"loaded"`
	BundleID       string `json:"bundle_id,omitempty"`
	BundleVersion  string `json:"bundle_version,omitempty"`
	ReleaseVersion string `json:"release_version,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	Operations     int    `json:"operations"`
	Error          string `json:"error,omitempty"`
}

// TranslationStatus describes the public dataset.
type TranslationStatus struct {
	Dir           string `json:"dir"`
	Loaded        bool   `json:"loaded"`
	Schema        string `json:"schema,omitempty"`
	Records       int    `json:"records"`
	Banks         int    `json:"banks"`
	ChangesSHA256 string `json:"changes_sha256,omitempty"`
	TargetSHA256  string `json:"target_sha256,omitempty"`
	TargetSize    int64  `json:"target_size,omitempty"`
	Error         string `json:"error,omitempty"`
}

// CacheStatus describes the verified extraction cache.
type CacheStatus struct {
	Dir         string `json:"dir"`
	Ready       bool   `json:"ready"`
	OverlayPath string `json:"overlay_path,omitempty"`
	OverlaySHA  string `json:"overlay_sha256,omitempty"`
	BankCount   int    `json:"bank_count,omitempty"`
	MemberCount int    `json:"member_count,omitempty"`
	Source      string `json:"source,omitempty"`
	Error       string `json:"error,omitempty"`
}

// KeysStatus reports key-file presence only. Contents are never read.
type KeysStatus struct {
	Dir     string   `json:"dir"`
	Present []string `json:"present"`
	Missing []string `json:"missing"`
}

// BuildSummary is the persisted last-build summary.
type BuildSummary struct {
	OutputPath    string `json:"output_path"`
	OutputSHA256  string `json:"output_sha256"`
	OutputSize    int64  `json:"output_size"`
	BundleID      string `json:"bundle_id"`
	BundleVersion string `json:"bundle_version"`
	Operations    int    `json:"operations"`
	MembersEdited int    `json:"members_edited"`
	BanksAdded    int    `json:"banks_added"`
	Verified      bool   `json:"verified"`
	WhenUTC       string `json:"when_utc,omitempty"`
	ReportPath    string `json:"report_path,omitempty"`
}

// Status is the dashboard summary.
type Status struct {
	Tool string `json:"tool"`
	// Version is the patcher application version (internal/build).
	Version string `json:"version"`
	// ModVersion is the Unburger Mod version from the repository-root
	// version.txt; ModVersionError carries the diagnostic when it cannot be
	// loaded. The two are kept distinct so the app and the mod can version
	// independently.
	ModVersion      string               `json:"mod_version,omitempty"`
	ModVersionError string               `json:"mod_version_error,omitempty"`
	Mode            string               `json:"mode"`
	ProjectRoot     string               `json:"project_root,omitempty"`
	ConfigPath      string               `json:"config_path"`
	ConfigExists    bool                 `json:"config_exists"`
	WorkDir         string               `json:"work_dir"`
	Translations    TranslationStatus    `json:"translations"`
	Cache           CacheStatus          `json:"cache"`
	Sources         *localsources.Report `json:"sources,omitempty"`
	Keys            KeysStatus           `json:"keys"`
	Source          SourceStatus         `json:"source"`
	PatchData       BundleStatus         `json:"patch_data"`
	Target          string               `json:"target"`
	OutputDir       string               `json:"output_dir"`
	OutputPath      string               `json:"output_path"`
	OutputExists    bool                 `json:"output_exists"`
	Outputs         []packaging.Artifact `json:"outputs,omitempty"`
	LastBuild       *BuildSummary        `json:"last_build,omitempty"`
	LastValidation  string               `json:"last_validation,omitempty"`
	Warnings        []string             `json:"warnings,omitempty"`
	Notes           []string             `json:"notes,omitempty"`
}

// Status computes the current dashboard status. It never writes anything.
func (s *Service) Status() *Status {
	st := &Status{
		Tool: build.ToolName, Version: build.VersionString(),
		Mode: s.Loc.Mode, ProjectRoot: s.Loc.Root,
		ConfigPath: s.CfgPath, ConfigExists: s.Cfg.Exists(), WorkDir: s.WorkDir,
		OutputDir: s.OutputDirOrDefault(), OutputPath: s.BuildArchivePath(),
		Target:         string(s.Target()),
		LastValidation: s.Cfg.LastValidation,
	}
	st.ModVersion, st.ModVersionError = s.ModVersionStatus()
	st.Outputs = s.OutputArtifacts()
	if fileIsFile(st.OutputPath) {
		st.OutputExists = true
	}
	if s.Cfg.LastBuild != "" && fileIsFile(s.Cfg.LastBuild) {
		if bs, err := readBuildSummary(s.Cfg.LastBuild); err == nil {
			st.LastBuild = bs
		}
	}

	// translations dataset
	st.Translations.Dir = s.TranslationsDir()
	if ds, err := translations.Load(st.Translations.Dir); err == nil {
		st.Translations.Loaded = true
		st.Translations.Schema = ds.Manifest.Schema
		st.Translations.Records = len(ds.Records)
		st.Translations.Banks = ds.Manifest.BankCount
		st.Translations.ChangesSHA256 = ds.Manifest.ChangesSHA256
		st.Translations.TargetSHA256 = ds.Manifest.TargetSource.SHA256
		st.Translations.TargetSize = ds.Manifest.TargetSource.Size
	} else {
		st.Translations.Error = err.Error()
		st.Warnings = append(st.Warnings, "translations: "+err.Error())
	}

	// verified cache
	st.Cache.Dir = s.CacheDir()
	if state, err := extraction.Resolve(&extraction.Request{
		CacheDir:     s.CacheDir(),
		SearchRoots:  nil, // status never scans or populates; verify the cache only
		BaseMembers:  s.RequiredBaseMembers(),
		MinBankCount: 1,
		VerifyOnly:   true,
	}); err == nil {
		st.Cache.Ready = true
		st.Cache.OverlayPath = state.OverlayPath
		st.Cache.OverlaySHA = state.OverlaySHA256
		st.Cache.BankCount = state.BankCount
		st.Cache.MemberCount = state.MemberCount
	} else {
		st.Cache.Error = "not populated"
	}

	// local containers
	rep, derr := s.DiscoverSources()
	if derr != nil {
		st.Warnings = append(st.Warnings, "sources: "+derr.Error())
	} else {
		st.Sources = rep
		if len(rep.Missing) > 0 {
			st.Warnings = append(st.Warnings, fmt.Sprintf("missing local containers: %v", rep.Missing))
		}
	}

	// key presence only
	st.Keys = s.KeysPresence()

	// explicit bundle mode
	if s.Cfg.PatchDataDir != "" {
		bundle, berr := s.LoadBundle()
		if berr != nil {
			st.PatchData.Dir = s.BundleDir()
			st.PatchData.Error = berr.Error()
		} else {
			st.PatchData = BundleStatus{
				Dir: s.BundleDir(), Exists: true, Loaded: true,
				BundleID: bundle.Manifest.BundleID, BundleVersion: bundle.Manifest.BundleVersion,
				ReleaseVersion: bundle.Manifest.ReleaseVersion, ManifestSHA256: bundle.ManifestSHA256,
				Operations: bundle.OperationCount(),
			}
		}
	}

	srcPath := s.SourcePath()
	st.Source.Path = srcPath
	if srcPath == "" {
		st.Warnings = append(st.Warnings, "no effective overlay archive resolved (run setup or preflight for details)")
	} else if !fileIsFile(srcPath) {
		st.Source.Error = "file not found"
	} else {
		st.Source.Exists = true
		if id, err := source.Identify(srcPath); err == nil {
			st.Source.Identity = id
			st.Source.Supported = id.Supported
		} else {
			st.Source.Error = err.Error()
		}
	}
	if st.PatchData.Error != "" {
		st.Warnings = append(st.Warnings, "patch data: "+st.PatchData.Error)
	}
	if st.Source.Error != "" {
		st.Warnings = append(st.Warnings, "game files: "+st.Source.Error)
	}
	return st
}

func readBuildSummary(path string) (*BuildSummary, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bs BuildSummary
	if err := jsonUnmarshal(raw, &bs); err != nil {
		return nil, err
	}
	return &bs, nil
}

// KeysPresence reports which conventional key files exist. Contents are never
// read, logged or reported.
func (s *Service) KeysPresence() KeysStatus {
	out := KeysStatus{Dir: s.KeysDir()}
	for _, name := range project.KeyFiles() {
		if fileIsFile(filepath.Join(out.Dir, name)) {
			out.Present = append(out.Present, name)
		} else {
			out.Missing = append(out.Missing, name)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

// DoctorCheck is one environment check.
type DoctorCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// DoctorReport is the environment report.
type DoctorReport struct {
	Tool       string        `json:"tool"`
	Version    string        `json:"version"`
	GoOS       string        `json:"goos"`
	Mode       string        `json:"mode"`
	ConfigPath string        `json:"config_path"`
	Checks     []DoctorCheck `json:"checks"`
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	Warned     int           `json:"warned"`
}

// Doctor runs environment and configuration checks.
func (s *Service) Doctor() *DoctorReport {
	rep := &DoctorReport{Tool: build.ToolName, Version: build.VersionString(), GoOS: goos(), Mode: s.Loc.Mode, ConfigPath: s.CfgPath}
	add := func(id, title, status, detail string) {
		rep.Checks = append(rep.Checks, DoctorCheck{ID: id, Title: title, Status: status, Detail: detail})
		switch status {
		case StatusPass:
			rep.Passed++
		case StatusFail:
			rep.Failed++
		case StatusWarn:
			rep.Warned++
		}
	}
	if s.InProject() {
		add("storage.mode", "storage mode", StatusPass, "project checkout: "+s.Loc.PrivateDir)
	} else {
		add("storage.mode", "storage mode", StatusPass, "per-user settings ("+s.Loc.Path+")")
	}
	if s.Cfg.Exists() {
		add("config.file", "settings file", StatusPass, s.CfgPath)
	} else {
		add("config.file", "settings file", StatusWarn, "not created yet: "+s.CfgPath)
	}
	if _, err := os.Stat(s.WorkDir); err != nil {
		add("workdir", "working directory", StatusFail, err.Error())
	} else {
		add("workdir", "working directory", StatusPass, s.WorkDir)
	}
	if ds, err := translations.Load(s.TranslationsDir()); err != nil {
		add("translations", "translation dataset", StatusWarn, err.Error())
	} else {
		add("translations", "translation dataset", StatusPass,
			fmt.Sprintf("%d record(s) in %d bank(s) · %s", len(ds.Records), ds.Manifest.BankCount, shortSHA(ds.Manifest.ChangesSHA256)))
	}
	if st, err := extraction.Resolve(&extraction.Request{CacheDir: s.CacheDir(), VerifyOnly: true}); err == nil {
		add("cache", "verified cache", StatusPass, fmt.Sprintf("%s · %d bank(s)", st.OverlayPath, st.BankCount))
	} else {
		add("cache", "verified cache", StatusWarn, err.Error())
	}
	if k := s.KeysPresence(); len(k.Present) > 0 {
		add("keys", "key files (presence only)", StatusPass, fmt.Sprintf("%d/%d present in %s", len(k.Present), len(project.KeyFiles()), k.Dir))
	} else {
		add("keys", "key files (presence only)", StatusWarn, "none found in "+k.Dir)
	}
	if rep, err := s.DiscoverSources(); err != nil {
		add("sources", "local containers", StatusFail, err.Error())
	} else if len(rep.Missing) > 0 {
		add("sources", "local containers", StatusWarn, fmt.Sprintf("missing: %v", rep.Missing))
	} else {
		add("sources", "local containers", StatusPass, fmt.Sprintf("%d identified", len(rep.Found)))
	}
	if tools := extraction.ProbeTools(); tools.Hactool || tools.Hactoolnet || tools.Nstool || tools.Python {
		add("tools", "decryption helpers", StatusPass, fmt.Sprintf("%s", toolNames(tools)))
	} else {
		add("tools", "decryption helpers", StatusWarn, "none found; an existing cache or extraction is required")
	}
	if od := s.OutputDirOrDefault(); dirExists(od) {
		add("output.dir", "output directory", StatusPass, od)
	} else {
		add("output.dir", "output directory", StatusWarn, "will be created: "+od)
	}
	return rep
}

func toolNames(t extraction.Tools) string {
	var out []string
	if t.Hactool {
		out = append(out, "hactool")
	}
	if t.Hactoolnet {
		out = append(out, "hactoolnet")
	}
	if t.Nstool {
		out = append(out, "nstool")
	}
	if t.Python {
		out = append(out, "python3")
	}
	if len(out) == 0 {
		return "none"
	}
	sort.Strings(out)
	return fmt.Sprint(out)
}

// Status constants used across reports.
const (
	StatusPass = "PASS"
	StatusFail = "FAIL"
	StatusWarn = "WARN"
)
