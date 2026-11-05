//go:build linux

package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a race-free buffer for a concurrently written PTY capture.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("module root not found from %s: %v", wd, err)
	}
	return root
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func buildCLI(t *testing.T, root string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "13s-patcher")
	cmd := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-o", out, "./cmd/13s-patcher")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=vendor", "GOPROXY=off", "CGO_ENABLED=0")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, raw)
	}
	return out
}

// ptyRun launches a command under a real pseudo-terminal via script(1), waits
// for the dashboard to appear, types the given keys and returns the captured
// terminal output and the exit code. It is skipped when script(1) is absent.
func ptyRun(t *testing.T, tmp, command string, keys []string) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script(1) is not available for the PTY test")
	}
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("script", "-qec", "stty rows 30 cols 100 2>/dev/null; "+command, "/dev/null")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color", "PATCHER_TUI=1", "CI=",
		"HOME="+tmp, "XDG_CONFIG_HOME="+filepath.Join(tmp, "xdg"),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until the dashboard has actually drawn its action list.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "Overview") {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	for _, k := range keys {
		time.Sleep(500 * time.Millisecond)
		_, _ = stdin.Write([]byte(k))
	}
	time.Sleep(500 * time.Millisecond)
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	code := 0
	select {
	case err := <-done:
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		code = -1
	}
	return out.String(), code
}

// TestDashboardInRealPTY proves the dashboard launches, draws, expands the log
// and exits cleanly in a real terminal.
func TestDashboardInRealPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PTY test in short mode")
	}
	root := moduleRoot(t)
	bin := buildCLI(t, root)
	tmp := t.TempDir()
	text, code := ptyRun(t, tmp, shq(bin)+" tui", []string{"?", "L", "q"})
	if code != 0 {
		t.Fatalf("dashboard exit code %d\n%s", code, tail(text))
	}
	if strings.Contains(text, "panic") {
		t.Fatalf("dashboard panicked:\n%s", tail(text))
	}
	for _, want := range []string{"13s-patcher", "Overview", "help", "log", "target"} {
		if !strings.Contains(text, want) {
			t.Fatalf("PTY output is missing %q:\n%s", want, tail(text))
		}
	}
}

// TestLauncherInRealPTY proves the root launcher builds (or reuses) the tool and
// opens the dashboard in a real terminal.
func TestLauncherInRealPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PTY test in short mode")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script(1) is not available for the PTY test")
	}
	root := moduleRoot(t)
	launcher := filepath.Clean(filepath.Join(root, "..", "run-patcher.sh"))
	if _, err := os.Stat(launcher); err != nil {
		t.Skipf("root launcher not present (isolation copy): %v", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	tmp := t.TempDir()
	command := shq(launcher) + " tui -config " + shq(filepath.Join(tmp, "config.json")) +
		" -workdir " + shq(filepath.Join(tmp, "work"))
	text, code := ptyRun(t, tmp, command, []string{"q"})
	if code != 0 {
		t.Fatalf("launcher dashboard exit code %d\n%s", code, tail(text))
	}
	if strings.Contains(text, "panic") {
		t.Fatalf("launcher dashboard panicked:\n%s", tail(text))
	}
	for _, want := range []string{"13s-patcher", "Overview"} {
		if !strings.Contains(text, want) {
			t.Fatalf("launcher PTY output is missing %q:\n%s", want, tail(text))
		}
	}
}

func tail(s string) string {
	const n = 3000
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// TestOverwriteDialogInRealPTY drives the dashboard in a real terminal all the
// way to the overwrite dialog: it navigates to Build with an existing output,
// opens the dialog and chooses Replace.
func TestOverwriteDialogInRealPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PTY test in short mode")
	}
	root := moduleRoot(t)
	bin := buildCLI(t, root)
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-existing (deliberately fake) output archive for the atmosphere target.
	if err := os.WriteFile(filepath.Join(out, "13-Sentinels-Patch-Atmosphere.zip"), []byte("PK\x03\x04 dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"target":"atmosphere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Down Down => Build; Enter opens the dialog; Right => Replace; Enter runs.
	command := shq(bin) + " tui -config " + shq(cfg) + " -workdir " + shq(filepath.Join(tmp, "work")) +
		" -output " + shq(out) + " -target atmosphere"
	text, code := ptyRun(t, tmp, command, []string{"\x1b[B", "\x1b[B", "\r", "\x1b[C", "\r", "q"})
	if strings.Contains(text, "panic") {
		t.Fatalf("dashboard panicked:\n%s", tail(text))
	}
	if !strings.Contains(text, "existing output found") {
		t.Fatalf("the overwrite dialog never appeared:\n%s", tail(text))
	}
	for _, want := range []string{"Replace", "Keep existing", "Cancel"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the dialog is missing the %q choice:\n%s", want, tail(text))
		}
	}
	if code != 0 {
		t.Fatalf("dashboard exit code %d\n%s", code, tail(text))
	}
}
