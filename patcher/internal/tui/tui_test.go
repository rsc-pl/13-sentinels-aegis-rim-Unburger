package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"13s-patcher/internal/app"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/packaging"
)

func testService(t *testing.T, f *fixture.Fixture) *app.Service {
	t.Helper()
	svc, err := app.New(app.Options{
		ConfigPath:    filepath.Join(f.Dir, "config.json"),
		WorkDir:       f.Dir,
		SourceArchive: f.SourceArchive,
		PatchDataDir:  f.BundleDir,
		OutputDir:     f.OutputDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The live title-footer face/style for production is resolved from the
	// project root; the tests use the module's shipped files.
	svc.TitleAssetsDir = filepath.Join("..", "..", "assets", "fonts")
	return svc
}

func TestTerminalCapableForcedOff(t *testing.T) {
	t.Setenv("PATCHER_TUI", "0")
	if TerminalCapable() {
		t.Fatal("PATCHER_TUI=0 must disable the TUI")
	}
}

func TestTerminalCapableInNonTTY(t *testing.T) {
	t.Setenv("PATCHER_TUI", "")
	t.Setenv("CI", "")
	// Tests are not attached to a terminal, so this must be false.
	if TerminalCapable() {
		t.Fatal("non-TTY must not be terminal-capable")
	}
}

func TestModelNavigationAndRender(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// status message populates the dashboard
	m.Update(statusLoadedMsg{st: m.svc.Status()})
	body := m.renderBody()
	if body == "" {
		t.Fatal("empty body")
	}

	start := m.focus
	m.Update(tea.KeyPressMsg{Code: 'j'})
	if m.focus == start {
		t.Fatal("down did not move focus")
	}
	m.Update(tea.KeyPressMsg{Code: 'k'})
	if m.focus != start {
		t.Fatal("up did not restore focus")
	}
	m.Update(tea.KeyPressMsg{Code: '?'})
	if !m.showHelp {
		t.Fatal("help not toggled")
	}
	m.Update(tea.KeyPressMsg{Code: '?'})
	if m.showHelp {
		t.Fatal("help not toggled off")
	}
	first := m.logView.Height()
	m.Update(tea.KeyPressMsg{Code: 'L'})
	if !m.logFullscreen {
		t.Fatal("L did not expand the log")
	}
	if m.logView.Height() <= first {
		t.Fatalf("full-screen log did not grow (%d -> %d)", first, m.logView.Height())
	}
	m.Update(tea.KeyPressMsg{Code: 'L'})
	if m.logFullscreen {
		t.Fatal("L did not collapse the log again")
	}
}

func TestSmallTerminal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
	if body := m.renderBody(); body == "" {
		t.Fatal("no body on a small terminal")
	}
}

func TestSettingsViewRenders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))
	m.setFocusByMode(viewSettings)
	m.refreshContent()
	if m.settings.View(60) == "" {
		t.Fatal("settings view empty")
	}
}

func TestQuitAndCancelKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))

	// Start a task, then cancel it with esc.
	cmd := m.startTask("preflight")
	if cmd == nil {
		t.Fatal("startTask returned no command")
	}
	if !m.busy || m.cancel == nil {
		t.Fatal("task did not start")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.busy != true {
		t.Fatal("esc should only request cancellation")
	}
	// Complete the task synchronously.
	m.Update(taskDoneMsg{name: "Preflight", summary: "done"})
	if m.busy {
		t.Fatal("task did not finish")
	}

	// q quits.
	m.Update(tea.KeyPressMsg{Code: 'q'})
	if !m.quit {
		t.Fatal("q did not quit")
	}
}

func TestStatusErrorRenders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))
	m.Update(statusLoadedMsg{err: os.ErrNotExist})
	if m.renderBody() == "" {
		t.Fatal("no body after a status error")
	}
}

// TestEscReturnsFromSettingsAndLogs proves the pane focus is never trapped: esc
// leaves Settings and Logs and returns to the overview.
func TestEscReturnsFromSettingsAndLogs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))

	// Settings: navigation mode, esc leaves immediately.
	m.setFocusByMode(viewSettings)
	if !m.showSettings() {
		t.Fatal("settings pane not focused")
	}
	if m.settings.editing() {
		t.Fatal("settings must start in navigation mode, not editing")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.showSettings() {
		t.Fatal("esc did not leave the settings pane")
	}
	if m.menu[m.focus].mode != viewOverview {
		t.Fatalf("esc did not return to the overview (now %v)", m.menu[m.focus].mode)
	}

	// Settings: editing mode needs two esc presses (stop editing, then leave).
	m.setFocusByMode(viewSettings)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.settings.editing() {
		t.Fatal("enter did not start editing")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.settings.editing() {
		t.Fatal("esc did not stop editing")
	}
	if !m.showSettings() {
		t.Fatal("the first esc must not leave the pane")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.menu[m.focus].mode != viewOverview {
		t.Fatal("the second esc did not return to the overview")
	}

	// Logs: esc leaves.
	m.setFocusByMode(viewLogs)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.menu[m.focus].mode != viewOverview {
		t.Fatal("esc did not leave the logs pane")
	}
}

// TestSettingsKeepsTypedBuffer proves leaving the pane does not clear what was
// typed, and that ctrl+s saves it.
func TestSettingsKeepsTypedBuffer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := testService(t, f)
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "abc" {
		m.settings.UpdateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	typed := m.settings.field["target"].Value()
	if !strings.HasSuffix(typed, "abc") {
		t.Fatalf("typed text not captured: %q", typed)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc}) // stop editing
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc}) // leave pane
	if got := m.settings.field["target"].Value(); !strings.HasSuffix(got, "abc") {
		t.Fatalf("leaving the pane discarded the typed buffer: %q", got)
	}
}

// TestCancelIsDistinctFromBack proves esc cancels a running task instead of
// changing the pane, and that esc only navigates back when idle.
func TestCancelIsDistinctFromBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))
	if cmd := m.startTask("preflight"); cmd == nil {
		t.Fatal("task did not start")
	}
	if !m.busy {
		t.Fatal("model not busy")
	}
	pane := m.menu[m.focus].mode
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.busy {
		t.Fatal("esc must only request cancellation while a task runs")
	}
	if m.menu[m.focus].mode != pane {
		t.Fatal("esc must not navigate while a task runs")
	}
	m.Update(taskDoneMsg{name: "Preflight", summary: "done"})
	if m.busy {
		t.Fatal("task did not finish")
	}
	// now idle: esc navigates back
	m.setFocusByMode(viewBuild)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.menu[m.focus].mode != viewOverview {
		t.Fatal("idle esc did not return to the overview")
	}
}

// TestTargetSelector covers the 1/2/3 target keys.
func TestTargetSelector(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testService(t, f))
	if m.target != packaging.TargetBoth {
		t.Fatalf("the default target must be both, got %q", m.target)
	}
	m.Update(tea.KeyPressMsg{Code: '2'})
	if m.target != packaging.TargetEmulator {
		t.Fatalf("key 2 did not select the emulator target (%q)", m.target)
	}
	m.Update(tea.KeyPressMsg{Code: '3'})
	if m.target != packaging.TargetBoth {
		t.Fatalf("key 3 did not select both targets (%q)", m.target)
	}
	m.Update(tea.KeyPressMsg{Code: '1'})
	if m.target != packaging.TargetAtmosphere {
		t.Fatalf("key 1 did not select the Atmosphère target (%q)", m.target)
	}
	if m.renderBody() == "" {
		t.Fatal("no body rendered")
	}
}
