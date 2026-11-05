package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"13s-patcher/internal/app"
	"13s-patcher/internal/fixture"
)

// brandedService builds a service over a synthetic fixture with an explicit
// version.txt at the fixture root, so branding resolves exactly as it does at a
// real checkout's repository root.
func brandedService(t *testing.T, version string) (*app.Service, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(f.Dir, "version.txt"), []byte(version), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return testService(t, f), f.Dir
}

// TestSidebarShowsBrandingAndFooter proves the dashboard renders the Unburger
// Mod branding beneath the menu and keeps the help footer and log visible at
// both a minimum and a large terminal size.
func TestSidebarShowsBrandingAndFooter(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{80, 24}, {120, 35}} {
		svc, _ := brandedService(t, "0.1.0\n")
		m := newModel(svc)
		m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m.Update(statusLoadedMsg{st: svc.Status()})
		body := m.renderBody()
		if !strings.Contains(body, "Unburger Mod v0.1.0") {
			t.Fatalf("size %dx%d: branding missing from the dashboard:\n%s", sz.w, sz.h, body)
		}
		if !strings.Contains(body, "help") {
			t.Fatalf("size %dx%d: the help footer is missing", sz.w, sz.h)
		}
		if !strings.Contains(body, "log") {
			t.Fatalf("size %dx%d: the log panel is missing", sz.w, sz.h)
		}
		if !strings.Contains(body, "actions") {
			t.Fatalf("size %dx%d: the actions menu is missing", sz.w, sz.h)
		}
	}
}

// TestBrandingReflectsVersionFileChange proves the displayed version follows
// version.txt with no source change: editing the file and refreshing the status
// updates the branding.
func TestBrandingReflectsVersionFileChange(t *testing.T) {
	svc, root := brandedService(t, "0.1.0\n")
	m := newModel(svc)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := m.modVersion; got != "0.1.0" {
		t.Fatalf("initial version = %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte("2.3.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A status refresh (r key, or the end of a task) re-reads the file.
	m.Update(statusLoadedMsg{st: svc.Status()})
	if got := m.modVersion; got != "2.3.4" {
		t.Fatalf("version did not follow version.txt: %q", got)
	}
	if !strings.Contains(m.renderBody(), "Unburger Mod v2.3.4") {
		t.Fatalf("branding did not update:\n%s", m.renderBody())
	}
}

// TestBrandingDiagnosticWhenVersionMissing proves a missing version.txt yields a
// diagnostic and never a fabricated version number.
func TestBrandingDiagnosticWhenVersionMissing(t *testing.T) {
	svc, _ := brandedService(t, "")
	m := newModel(svc)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 34})
	m.Update(statusLoadedMsg{st: svc.Status()})
	if m.modVersionErr == nil {
		t.Fatal("a missing version.txt must surface as an error, not a version")
	}
	if m.modVersion != "" {
		t.Fatalf("no version may be invented, got %q", m.modVersion)
	}
	body := m.renderBody()
	if !strings.Contains(body, "Unburger Mod vunknown") {
		t.Fatalf("the diagnostic branding is missing:\n%s", body)
	}
	if !strings.Contains(body, "version unavailable") {
		t.Fatalf("the overview diagnostic is missing:\n%s", body)
	}
	if strings.Contains(body, "v0.") {
		t.Fatalf("a version was fabricated:\n%s", body)
	}
}

// TestBrandingDiagnosticWhenVersionEmpty proves an empty file is reported the
// same way rather than rendering a blank label.
func TestBrandingDiagnosticWhenVersionEmpty(t *testing.T) {
	svc, _ := brandedService(t, "   \n")
	m := newModel(svc)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 34})
	m.Update(statusLoadedMsg{st: svc.Status()})
	if m.modVersionErr == nil {
		t.Fatal("an empty version.txt must surface as an error")
	}
	body := m.renderBody()
	if !strings.Contains(body, "Unburger Mod vunknown") {
		t.Fatalf("the diagnostic branding is missing:\n%s", body)
	}
}
