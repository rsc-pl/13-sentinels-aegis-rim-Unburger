// Package config resolves and persists the user's settings.
//
// Two storage modes exist:
//
//   - project mode: when the tool runs inside a project checkout (a directory
//     tree that contains the project's private data directory), the primary
//     settings file and all derived state live inside that private directory.
//     No per-user file is created and no XDG location is touched.
//   - user mode: a fresh checkout without a private directory keeps the
//     per-user settings file (XDG on Linux/macOS, AppData on Windows) with an
//     optional per-directory override file.
//
// The mode is resolved once at start-up; exactly one settings file is
// authoritative in each mode.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/project"
)

// AppDir is the per-user configuration subdirectory name.
const AppDir = project.AppDir

// FileName is the settings file name.
const FileName = "config.json"

// LocalName is the per-directory override file name.
const LocalName = ".13s-patcher.json"

// Mode names the active storage mode.
const (
	ModeProject = "project"
	ModeUser    = "user"
)

// Config holds every user setting. Relative paths are resolved against the
// working directory. Nothing secret is stored here: keys are never read or
// recorded, only the directory that holds them.
type Config struct {
	// --- inputs ---
	// SourceArchive is an explicit effective update overlay archive.
	SourceArchive string `json:"source_archive,omitempty"`
	// PatchDataDir is an explicit patch-data bundle directory. When set, the
	// bundle is consumed as-is instead of deriving one from the public dataset.
	PatchDataDir string `json:"patch_data_dir,omitempty"`
	// RomsBase / RomsUpdate / RomsExtra are the directories holding the
	// local containers.
	RomsBase   string `json:"roms_base,omitempty"`
	RomsUpdate string `json:"roms_update,omitempty"`
	RomsExtra  string `json:"roms_extra,omitempty"`
	// KeysDir holds the user's key files. Only presence is ever checked.
	KeysDir string `json:"keys_dir,omitempty"`
	// BaseBanksDir overrides the base bank directory used for members the
	// overlay does not carry.
	BaseBanksDir string `json:"base_banks_dir,omitempty"`
	// TranslationsDir is the public dataset directory.
	TranslationsDir string `json:"translations_dir,omitempty"`

	// --- state ---
	// StateDir overrides where derived state (cache, reports, derived bundles)
	// is written.
	StateDir string `json:"state_dir,omitempty"`
	CacheDir string `json:"cache_dir,omitempty"`

	// --- extraction ---
	// ExtractorCommand is the argv template of the decode helper, using the
	// placeholders {base} {update} {keys} {dest} {overlay} {banks}.
	ExtractorCommand []string `json:"extractor_command,omitempty"`

	// --- output ---
	OutputDir  string `json:"output_dir,omitempty"`
	OutputName string `json:"output_name,omitempty"`
	Encoder    string `json:"encoder,omitempty"`
	// Target selects the installation layouts to produce: atmosphere,
	// emulator or both.
	Target string `json:"target,omitempty"`
	// ReleaseVersion is the public release label used in output file names.
	ReleaseVersion string `json:"release_version,omitempty"`
	// PlateBase is an optional explicit path to a replacement background image
	// for the added presentation page. When empty the shipped project asset is
	// used; a file in the tool's resource directory also overrides it.
	PlateBase string `json:"plate_base,omitempty"`
	// AllowMissingResources lets a build proceed when an added resource cannot
	// be produced. It is off by default so a component is never dropped
	// silently.
	AllowMissingResources bool `json:"allow_missing_resources,omitempty"`
	// TitleBranding controls the title-screen footer branding resource. It is a
	// pointer so an explicitly saved true/false is always honoured and never
	// silently overwritten, while an omitted key means ENABLED: the footer is
	// part of a normal build. Set title_branding to false to withhold it.
	TitleBranding *bool `json:"title_branding,omitempty"`

	// --- results ---
	LastBuild      string `json:"last_build,omitempty"`
	LastValidation string `json:"last_validation,omitempty"`

	// Path is where this config was loaded from (not persisted).
	Path string `json:"-"`
}

// Location describes the resolved settings file and storage roots.
type Location struct {
	Path       string
	Mode       string
	Root       string
	PrivateDir string
	StateDir   string
	// SeedFrom is an existing per-directory override that should be migrated
	// into the authoritative file (project mode, first run only).
	SeedFrom string
}

// Dir returns the per-user configuration directory.
func Dir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, AppDir), nil
	}
	if runtime.GOOS == "windows" {
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, AppDir), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("cannot determine the user config directory: %w", err)
	}
	return filepath.Join(home, ".config", AppDir), nil
}

// DefaultPath returns the per-user settings file path.
func DefaultPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, FileName), nil
}

// LocalPath returns the per-directory override path for dir.
func LocalPath(dir string) string { return filepath.Join(dir, LocalName) }

// ResolveLocation picks the settings file and storage roots.
//
// Precedence: an explicit path, then the project-private settings file, then a
// per-directory override, then the per-user default.
func ResolveLocation(explicit, workDir, projectRoot string) (Location, error) {
	root, inProject := project.Discover(workDir, projectRoot)
	if explicit != "" {
		loc := Location{Path: explicit, Mode: ModeUser}
		loc.StateDir = filepath.Dir(explicit)
		if inProject {
			p := project.For(root)
			loc.Mode, loc.Root, loc.PrivateDir, loc.StateDir = ModeProject, root, p.PrivateDir, p.StateDir
		}
		return loc, nil
	}
	if inProject {
		p := project.For(root)
		loc := Location{Path: p.ConfigPath, Mode: ModeProject, Root: root, PrivateDir: p.PrivateDir, StateDir: p.StateDir}
		if workDir != "" {
			if local := LocalPath(workDir); fileExists(local) && !fileExists(p.ConfigPath) {
				loc.SeedFrom = local
			}
		}
		return loc, nil
	}
	if workDir != "" {
		if local := LocalPath(workDir); fileExists(local) {
			return Location{Path: local, Mode: ModeUser, StateDir: workDir}, nil
		}
	}
	p, err := DefaultPath()
	if err != nil {
		return Location{}, err
	}
	return Location{Path: p, Mode: ModeUser, StateDir: filepath.Dir(p)}, nil
}

// ResolvePath is the historical helper (explicit, then per-directory override,
// then per-user default). Prefer ResolveLocation.
func ResolvePath(explicit, dir string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if dir != "" {
		lp := LocalPath(dir)
		if fi, err := os.Stat(lp); err == nil && !fi.IsDir() {
			return lp, nil
		}
	}
	return DefaultPath()
}

// Load reads the settings file at path. A missing file yields a zero Config with
// Exists()==false.
func Load(path string) (Config, error) {
	c := Config{Path: path}
	if path == "" {
		return c, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	return c, nil
}

// Exists reports whether the loaded config came from a real file.
func (c Config) Exists() bool { return c.Path != "" && fileExists(c.Path) }

// TitleBrandingEnabled reports whether the title-screen footer branding
// resource should be produced. The footer is included in a normal build: an
// omitted key means enabled. An explicitly saved title_branding=false is
// honoured and never overwritten.
func (c Config) TitleBrandingEnabled() bool { return c.TitleBranding == nil || *c.TitleBranding }

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Save writes the config atomically to path (or the config's own path).
func (c Config) Save(path string) error {
	if path == "" {
		path = c.Path
	}
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	return atomic.WriteJSON(path, c, atomic.Options{Overwrite: true})
}
