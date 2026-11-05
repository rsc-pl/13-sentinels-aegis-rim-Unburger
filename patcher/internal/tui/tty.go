package tui

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// StdinIsTTY reports whether stdin is an interactive terminal.
func StdinIsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// StdoutIsTTY reports whether stdout is an interactive terminal.
func StdoutIsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// TerminalCapable reports whether the interactive dashboard can be launched. It
// refuses to start full-screen on a pipe, in CI, or with a dumb terminal, so the
// caller falls back to plain-text output. PATCHER_TUI=1/0 forces the decision.
func TerminalCapable() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PATCHER_TUI"))) {
	case "1", "true", "yes", "on":
		return StdinIsTTY() && StdoutIsTTY()
	case "0", "false", "no", "off":
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	if strings.TrimSpace(os.Getenv("CI")) != "" {
		return false
	}
	return StdinIsTTY() && StdoutIsTTY()
}
