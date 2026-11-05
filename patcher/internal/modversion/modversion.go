// Package modversion loads the single authoritative mod version from the
// repository-root version.txt.
//
// The value is the Unburger Mod's own version. It is deliberately independent
// of the patcher application version (internal/build), of the translation
// dataset schema, of the supported game update revision and of any toolchain
// version, so each can move without disturbing the others.
//
// Nothing here ever invents a version: a missing, empty or malformed file is
// reported as an error carrying an actionable path, and callers render a
// diagnostic instead of a made-up label.
package modversion

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"13s-patcher/internal/project"
)

// FileName is the authoritative mod-version file, created at the repository
// root. It is plain text that the project maintainer edits directly.
const FileName = "version.txt"

// Prefix is the branding prefix rendered before the version.
const Prefix = "Unburger Mod v"

// maxLen bounds an accepted version label.
const maxLen = 64

// validVersion accepts an ordinary version label: it must start with a letter
// or digit and contain only letters, digits, dots, dashes, underscores and plus
// signs. Anything else (spaces, slashes, control characters) is rejected rather
// than silently carried into a file name or a report.
var validVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`)

// Display renders the branded label for a version. An empty version renders the
// explicit "unknown" marker rather than a fabricated number.
func Display(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return Prefix + "unknown"
	}
	return Prefix + v
}

// Load reads and validates version.txt inside dir.
func Load(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("mod version: no project root to read %s from", FileName)
	}
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("mod version: %s not found (create it at the project root with the Unburger Mod version)", path)
		}
		return "", fmt.Errorf("mod version: cannot read %s: %w", path, err)
	}
	return parse(raw, path)
}

// parse trims and validates the raw file contents.
func parse(raw []byte, path string) (string, error) {
	v := strings.TrimSpace(string(raw))
	if v == "" {
		return "", fmt.Errorf("mod version: %s is empty (write the Unburger Mod version, e.g. 0.1.0)", path)
	}
	if len(v) > maxLen {
		return "", fmt.Errorf("mod version: %s holds %d characters (limit %d)", path, len(v), maxLen)
	}
	if !validVersion.MatchString(v) {
		return "", fmt.Errorf("mod version: %q in %s is not a valid version (use letters, digits, dot, dash, underscore or plus)", v, path)
	}
	return v, nil
}

// FindRoot locates the directory that holds version.txt. An explicit project
// root is honoured first, then the project checkout discovered from startDir,
// then an upward walk from startDir, and finally an upward walk from the
// executable's directory. It returns "" when nothing is found.
func FindRoot(startDir, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		if abs, err := filepath.Abs(explicit); err == nil && hasVersionFile(abs) {
			return abs
		}
	}
	if strings.TrimSpace(startDir) == "" {
		if wd, err := os.Getwd(); err == nil {
			startDir = wd
		}
	}
	if strings.TrimSpace(startDir) != "" {
		if root, ok := project.Discover(startDir, ""); ok && hasVersionFile(root) {
			return root
		}
	}
	if root := walkUp(startDir); root != "" {
		return root
	}
	if exe, err := os.Executable(); err == nil {
		if root := walkUp(filepath.Dir(exe)); root != "" {
			return root
		}
	}
	return ""
}

// Resolve locates version.txt and returns its validated value.
func Resolve(startDir, explicit string) (string, error) {
	root := FindRoot(startDir, explicit)
	if root == "" {
		where := startDir
		if strings.TrimSpace(where) == "" {
			where = "the working directory"
		}
		return "", fmt.Errorf("mod version: %s not found near %s or the executable (create it at the project root)", FileName, where)
	}
	return Load(root)
}

// walkUp returns the nearest ancestor of dir (including dir itself) that holds
// version.txt, or "".
func walkUp(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if hasVersionFile(abs) {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

func hasVersionFile(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil && !fi.IsDir()
}
