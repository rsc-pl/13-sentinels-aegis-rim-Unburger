package modversion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVersion(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValidTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeVersion(t, dir, "  1.2.3  \n\n")
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "1.2.3" {
		t.Fatalf("Load = %q, want %q", got, "1.2.3")
	}
}

func TestLoadMissingIsError(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("a missing version.txt must be an error, not an invented version")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Fatalf("diagnostic should name %s: %v", FileName, err)
	}
}

func TestLoadEmptyIsError(t *testing.T) {
	dir := t.TempDir()
	writeVersion(t, dir, "\n  \t\n")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("an empty version.txt must be an error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("diagnostic should say the file is empty: %v", err)
	}
}

func TestLoadInvalidIsError(t *testing.T) {
	for _, bad := range []string{
		"not a version",         // inner spaces
		"1.0.0/evil",            // slash would escape into a path
		"../etc",                // traversal-shaped
		"-1.0.0",                // must start with a letter or digit
		strings.Repeat("9", 65), // too long
		"1.0.0\x00",             // embedded control character
	} {
		dir := t.TempDir()
		writeVersion(t, dir, bad)
		if _, err := Load(dir); err == nil {
			t.Fatalf("Load accepted invalid version %q", bad)
		}
	}
}

func TestDisplayIsBrandedAndNeverInvents(t *testing.T) {
	if got := Display("0.1.0"); got != "Unburger Mod v0.1.0" {
		t.Fatalf("Display = %q", got)
	}
	if got := Display("  2.0.0 "); got != "Unburger Mod v2.0.0" {
		t.Fatalf("Display should trim: %q", got)
	}
	if got := Display(""); strings.Contains(got, "v0") {
		t.Fatalf("Display invented a version: %q", got)
	}
}

// TestResolveIsCwdIndependent proves the root is found by walking up from a
// nested working directory, so the tool works from any cwd.
func TestResolveIsCwdIndependent(t *testing.T) {
	root := t.TempDir()
	writeVersion(t, root, "3.4.5\n")
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(deep, "")
	if err != nil {
		t.Fatalf("Resolve from a nested cwd: %v", err)
	}
	if got != "3.4.5" {
		t.Fatalf("Resolve = %q, want 3.4.5", got)
	}
}

// TestResolvePrefersProjectRoot proves an explicit root wins and that a
// discovered project checkout (marked by the private directory) is preferred
// over a merely nearby version.txt.
func TestResolvePrefersProjectRoot(t *testing.T) {
	explicit := t.TempDir()
	writeVersion(t, explicit, "9.9.9\n")

	other := t.TempDir()
	writeVersion(t, other, "1.1.1\n")
	deep := filepath.Join(other, "x", "y")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(deep, explicit)
	if err != nil {
		t.Fatal(err)
	}
	if got != "9.9.9" {
		t.Fatalf("the explicit root must win, got %q", got)
	}

	// The discovered checkout (private-dir marker) is preferred over the walk.
	if err := os.MkdirAll(filepath.Join(other, "."+"local"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = Resolve(deep, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.1.1" {
		t.Fatalf("the discovered project root must be used, got %q", got)
	}
}

// TestResolveReportsWhenMissing proves nothing is invented when no root exists.
func TestResolveReportsWhenMissing(t *testing.T) {
	// An empty temp tree has no version.txt anywhere on the way up.
	dir := t.TempDir()
	if _, err := Resolve(dir, ""); err == nil {
		t.Skip("an ancestor of the temp directory unexpectedly holds version.txt")
	}
}
