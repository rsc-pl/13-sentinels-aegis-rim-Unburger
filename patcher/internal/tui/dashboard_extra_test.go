package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"13s-patcher/internal/app"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/progress"
)

func newTestModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newModel(testService(t, f))
}

func existingDest() []app.DestinationState {
	return []app.DestinationState{{
		Target: packaging.TargetAtmosphere, Label: "Atmosphère",
		Path: "/tmp/out/13-Sentinels-Patch-Atmosphere.zip", Exists: true,
		Size: 1234, SHA256: strings.Repeat("ab", 32), Kind: app.DestDifferent, Valid: true,
		Detail: "valid archive, differs from the last recorded build",
	}}
}

// TestOverwriteModalSafeDefaultAndCancel proves the dialog opens with the safe
// default and esc cancels without starting anything.
func TestOverwriteModalSafeDefaultAndCancel(t *testing.T) {
	m := newTestModel(t)
	m.setFocusByMode(viewBuild)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	m.Update(destinationsMsg{taskID: "build", states: existingDest()})
	if !m.modal {
		t.Fatal("an existing output must open the overwrite dialog")
	}
	if m.choice != choiceCancel {
		t.Fatalf("the safe default must be Cancel, got %v", m.choice)
	}
	before := m.target
	m.Update(tea.KeyPressMsg{Code: '1'})
	if m.target != before {
		t.Fatal("keys must be isolated while the dialog is open")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal {
		t.Fatal("esc did not close the dialog")
	}
	if m.busy {
		t.Fatal("esc must not start a build")
	}
}

// TestOverwriteModalChooseReplaceAndKeep covers switching the choice and
// activating it.
func TestOverwriteModalChooseReplaceAndKeep(t *testing.T) {
	m := newTestModel(t)
	m.setFocusByMode(viewBuild)

	m.Update(destinationsMsg{taskID: "build", states: existingDest()})
	// Cancel -> right -> Replace.
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.choice != choiceReplace {
		t.Fatalf("right did not select Replace, got %v", m.choice)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modal {
		t.Fatal("enter did not close the dialog")
	}
	if !m.busy || !m.approvedOver {
		t.Fatalf("Replace must start a build with overwrite approved (busy=%v approved=%v)", m.busy, m.approvedOver)
	}
	m.Update(taskDoneMsg{name: "Build Patch", summary: "done"})

	// Cancel -> left -> Keep.
	m.Update(destinationsMsg{taskID: "build", states: existingDest()})
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.choice != choiceKeep {
		t.Fatalf("left did not select Keep, got %v", m.choice)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.busy {
		t.Fatal("Keep must start the keep task")
	}
	if !strings.Contains(m.taskTitle, "keep") {
		t.Fatalf("Keep must run the keep-existing task, got %q", m.taskTitle)
	}
	m.Update(taskDoneMsg{name: m.taskTitle, summary: "done"})
}

// TestNoDestinationsSkipsModal proves a clean output directory builds directly.
func TestNoDestinationsSkipsModal(t *testing.T) {
	m := newTestModel(t)
	m.setFocusByMode(viewBuild)
	m.Update(destinationsMsg{taskID: "build", states: []app.DestinationState{{Target: packaging.TargetBoth, Kind: app.DestMissing}}})
	if m.modal {
		t.Fatal("no existing output must not open the dialog")
	}
	if !m.busy {
		t.Fatal("the build must start when nothing exists")
	}
}

// TestProgressStateAndRendering covers determinate, indeterminate and burst
// updates and that no percentage is fabricated.
func TestProgressStateAndRendering(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	m.Update(statusLoadedMsg{st: m.svc.Status()})
	m.setFocusByMode(viewBuild)
	if cmd := m.startTask("preflight"); cmd == nil {
		t.Fatal("task did not start")
	}
	m.Update(progressMsg(progress.Event{Op: "build", Stage: progress.StageFMSB, Status: progress.StatusProgress,
		Item: "Fms/ScriptText.fms", Completed: 3, Total: 12}))
	if m.prog.total != 12 || m.prog.completed != 3 {
		t.Fatalf("determinate progress not tracked: %+v", m.prog)
	}
	if got := m.prog.stageFrac; got < 0.24 || got > 0.26 {
		t.Fatalf("stage fraction wrong: %v", got)
	}
	// Indeterminate: a real spinner, no fabricated percentage.
	m.Update(progressMsg(progress.Event{Op: "build", Stage: progress.StageExtraction, Status: progress.StatusProgress, Note: "resolving"}))
	if m.prog.total != 0 || m.prog.stageFrac != 0 {
		t.Fatalf("indeterminate stage must have no fraction: %+v", m.prog)
	}
	// Burst: many updates in a row must not break the model.
	for i := 0; i < 500; i++ {
		m.Update(progressMsg(progress.Event{Op: "build", Stage: progress.StageFMSB, Status: progress.StatusProgress, Completed: int64(i), Total: 500}))
	}
	body := m.renderBody()
	if !strings.Contains(body, "rebuilding text banks") {
		t.Fatal("progress stage label missing from the build pane")
	}
	if !strings.Contains(body, "%") {
		t.Fatal("overall percentage missing")
	}
	// cancel mid-task
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.busy {
		t.Fatal("esc must only request cancellation while a task runs")
	}
	m.Update(taskDoneMsg{name: "Preflight", summary: "done"})
	if m.busy {
		t.Fatal("task did not finish")
	}
}

// TestLayoutFitsEverySize proves the header, body, log and footer always fit the
// terminal, and that resizing during a task keeps them within bounds.
func TestLayoutFitsEverySize(t *testing.T) {
	sizes := []struct{ w, h int }{
		{80, 24}, {100, 30}, {120, 35}, {160, 40}, {200, 35},
	}
	for _, sz := range sizes {
		m := newTestModel(t)
		m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m.Update(statusLoadedMsg{st: m.svc.Status()})
		m.setFocusByMode(viewBuild)
		if cmd := m.startTask("preflight"); cmd == nil {
			t.Fatal("task did not start")
		}
		m.Update(progressMsg(progress.Event{Stage: progress.StageFMSB, Status: progress.StatusProgress, Completed: 1, Total: 4}))
		body := m.renderBody()
		if got := lipgloss.Height(body); got != sz.h {
			t.Fatalf("size %dx%d: rendered height %d != terminal height", sz.w, sz.h, got)
		}
		if m.logView.Height() < 1 || m.content.Height() < 1 {
			t.Fatalf("size %dx%d: a pane lost its height (log=%d content=%d)", sz.w, sz.h, m.logView.Height(), m.content.Height())
		}
		if !strings.Contains(body, "help") && !strings.Contains(body, "quit") {
			t.Fatalf("size %dx%d: the help footer is missing", sz.w, sz.h)
		}
		if !strings.Contains(body, "log") {
			t.Fatalf("size %dx%d: the log panel is missing", sz.w, sz.h)
		}
		// Resize during the running task.
		m.Update(tea.WindowSizeMsg{Width: sz.w - 20, Height: sz.h - 6})
		if got := lipgloss.Height(m.renderBody()); got != sz.h-6 {
			t.Fatalf("resize %dx%d: rendered height %d", sz.w-20, sz.h-6, got)
		}
		m.Update(taskDoneMsg{name: "Preflight", summary: "done"})
	}
}

// TestLogFullscreenAndFollow covers expanding the log, scrolling and
// follow-bottom behaviour with a bounded buffer.
func TestLogFullscreenAndFollow(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	small := m.logView.Height()
	for i := 0; i < 400; i++ {
		m.pushLog(strings.Repeat("x", 10))
	}
	if len(m.taskLog) > 500 {
		t.Fatalf("log buffer is not bounded: %d", len(m.taskLog))
	}
	m.Update(tea.KeyPressMsg{Code: 'L'})
	if !m.logFullscreen {
		t.Fatal("L did not expand the log")
	}
	if m.logView.Height() <= small {
		t.Fatalf("expanded log did not grow (%d -> %d)", small, m.logView.Height())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if !m.logView.AtTop() {
		t.Fatal("Home did not scroll to the top")
	}
	// Scrolling up pauses following.
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	y := m.logView.YOffset()
	m.pushLog("new line while scrolled up")
	if m.logView.YOffset() != y {
		t.Fatal("following was not paused after scrolling up")
	}
	// Returning to the bottom resumes following.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m.pushLog("new line at the bottom")
	if !m.logView.AtBottom() {
		t.Fatal("following did not resume at the bottom")
	}
	m.Update(tea.KeyPressMsg{Code: 'L'})
	if m.logFullscreen {
		t.Fatal("L did not collapse the log")
	}
}

// TestEscLeavesFullscreenLogBeforeNavigating proves esc closes the expanded log
// first and only then returns to the overview.
func TestEscLeavesFullscreenLogBeforeNavigating(t *testing.T) {
	m := newTestModel(t)
	m.setFocusByMode(viewBuild)
	m.Update(tea.KeyPressMsg{Code: 'L'})
	if !m.logFullscreen {
		t.Fatal("log not expanded")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.logFullscreen {
		t.Fatal("esc did not close the expanded log")
	}
	if m.menu[m.focus].mode != viewBuild {
		t.Fatal("esc closed the pane as well as the log")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.menu[m.focus].mode != viewOverview {
		t.Fatal("the second esc did not return to the overview")
	}
}

// TestErrorPanelHidesForceHint proves the dashboard never shows the CLI-only
// --force instruction and instead points at the dialog.
func TestErrorPanelHidesForceHint(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	m.Update(statusLoadedMsg{st: m.svc.Status()})
	m.setFocusByMode(viewBuild)
	m.busy = true
	m.taskTitle = "Build Patch"
	m.prog = progressState{running: true, start: time.Now(), order: progress.Order, done: map[progress.Stage]bool{}}
	m.Update(taskDoneMsg{name: "Build Patch", err: errors.New("refusing to overwrite existing file /x/y.zip (pass --force to replace)")})
	body := m.renderBody()
	if strings.Contains(body, "--force") {
		t.Fatal("the dashboard must not show the CLI --force instruction")
	}
	for _, line := range m.taskLog {
		if strings.Contains(line, "--force") {
			t.Fatalf("the dashboard log shows --force: %q", line)
		}
	}
	if !strings.Contains(body, "Replace") {
		t.Fatal("the recovery hint should point at the Replace choice")
	}
}

// TestHelpHasNoForceKey proves the force shortcut is gone from the help and the
// new keys are documented.
func TestHelpHasNoForceKey(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.KeyPressMsg{Code: '?'})
	if !m.showHelp {
		t.Fatal("? did not open the help")
	}
	if !strings.Contains(m.renderBody(), "help") {
		t.Fatal("the help panel is not rendered")
	}
	ht := helpText()
	if strings.Contains(ht, "f force") || strings.Contains(ht, "force-overwrite") {
		t.Fatal("the help still documents the removed force key")
	}
	if !strings.Contains(ht, "force key") {
		t.Fatal("the help must explain there is no force key")
	}
	for _, want := range []string{"Replace", "Keep existing", "Cancel", "expand", "esc"} {
		if !strings.Contains(ht, want) {
			t.Fatalf("the help is missing %q", want)
		}
	}
}
