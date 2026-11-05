package project_test

import (
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/project"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscoverFindsProjectRoot(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, project.PrivateDirName)
	mkdir(t, root, ".git")
	deep := mkdir(t, root, "a", "b", "c")
	got, ok := project.Discover(deep, "")
	if !ok || got != root {
		t.Fatalf("Discover = %q, %v; want %q, true", got, ok, root)
	}
}

func TestDiscoverStopsAtRepositoryBoundary(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, ".git")
	deep := mkdir(t, root, "nested", "deep")
	if got, ok := project.Discover(deep, ""); ok {
		t.Fatalf("expected no project root, got %q", got)
	}
}

func TestDiscoverHonoursOverride(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, project.PrivateDirName)
	elsewhere := t.TempDir()
	got, ok := project.Discover(elsewhere, root)
	if !ok || got != root {
		t.Fatalf("override ignored: %q, %v", got, ok)
	}
	if _, ok := project.Discover(elsewhere, filepath.Join(elsewhere, "missing")); ok {
		t.Fatal("a non-existent override must not be accepted")
	}
}

func TestForBuildsStandardPaths(t *testing.T) {
	root := t.TempDir()
	p := project.For(root)
	if p.Root != root {
		t.Fatalf("root = %q", p.Root)
	}
	if p.PrivateDir != filepath.Join(root, project.PrivateDirName) {
		t.Fatalf("private dir = %q", p.PrivateDir)
	}
	if p.ConfigPath != filepath.Join(p.StateDir, "config.json") {
		t.Fatalf("config path = %q", p.ConfigPath)
	}
	if p.TranslationsDir != filepath.Join(root, project.TranslationDirName) {
		t.Fatalf("translations dir = %q", p.TranslationsDir)
	}
	roots := p.SearchRoots()
	if len(roots) < 4 {
		t.Fatalf("expected several search roots, got %v", roots)
	}
	if roots[len(roots)-1] != p.PrivateDir {
		t.Fatalf("the private directory should be searched last, got %v", roots)
	}
}
