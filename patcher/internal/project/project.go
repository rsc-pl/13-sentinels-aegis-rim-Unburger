// Package project locates the surrounding project directory and derives the
// storage roots the integrated workflow uses. Discovery is generic: it walks
// upward from the working directory looking for the project's private data
// directory, and honours explicit overrides passed by the caller.
package project

import (
	"os"
	"path/filepath"
	"strings"
)

// PrivateDirName is the directory that marks a project checkout and holds all
// non-public data. The name is part of the documented user-state layout, so it
// is written directly; only its private subtrees are kept out of the public tree.
const PrivateDirName = ".local"

// AppDir is the per-tool directory name used for configuration and state.
const AppDir = "13s-patcher"

// TranslationDirName is the public dataset directory at the project root.
const TranslationDirName = "translations"

// Paths are the standard locations inside a project checkout.
type Paths struct {
	Root       string
	PrivateDir string
	StateDir   string
	ConfigPath string
	CacheDir   string
	ReportsDir string
	DerivedDir string

	RomsBase   string
	RomsUpdate string
	RomsExtra  string
	KeysDir    string

	TranslationsDir string
}

// Discover returns the project root: the nearest ancestor of startDir (or the
// explicit override) that contains the private data directory. The upward walk
// stops at the first directory holding VCS metadata so a nested checkout is
// never mis-attributed to its parent.
func Discover(startDir, override string) (string, bool) {
	if strings.TrimSpace(override) != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", false
		}
		if isDir(abs) {
			return abs, true
		}
		return "", false
	}
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", false
	}
	for {
		if isDir(filepath.Join(dir, PrivateDirName)) {
			return dir, true
		}
		if hasVCS(dir) {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// For builds the standard path set for a discovered project root.
func For(root string) Paths {
	priv := filepath.Join(root, PrivateDirName)
	state := filepath.Join(priv, AppDir)
	return Paths{
		Root:            root,
		PrivateDir:      priv,
		StateDir:        state,
		ConfigPath:      filepath.Join(state, "config.json"),
		CacheDir:        filepath.Join(state, "cache"),
		ReportsDir:      filepath.Join(state, "reports"),
		DerivedDir:      filepath.Join(state, "derived"),
		RomsBase:        filepath.Join(priv, "roms", "base-us"),
		RomsUpdate:      filepath.Join(priv, "roms", "update-us"),
		RomsExtra:       filepath.Join(priv, "roms", "base-jp"),
		KeysDir:         filepath.Join(priv, "keys"),
		TranslationsDir: filepath.Join(root, TranslationDirName),
	}
}

// SearchRoots are the directories scanned for verified inputs and existing
// extractions when no explicit path is configured.
func (p Paths) SearchRoots() []string {
	var out []string
	for _, d := range []string{p.RomsBase, p.RomsUpdate,
		filepath.Join(p.PrivateDir, "work"),
		filepath.Join(p.PrivateDir, "cache"),
		p.CacheDir,
		p.PrivateDir,
	} {
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// KeyFiles lists the conventional key file names inside a key directory, most
// specific first. Only presence is ever reported; contents are never read into
// a log or report.
func KeyFiles() []string { return []string{"prod.keys", "title.keys"} }

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func hasVCS(dir string) bool {
	for _, name := range []string{".git", ".hg", ".svn"} {
		if isDir(filepath.Join(dir, name)) || isFile(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
