package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	colAccent = lipgloss.Color("#7DA6FF")
	colOK     = lipgloss.Color("#6DDF8F")
	colBad    = lipgloss.Color("#FF6B6B")
	colWarn   = lipgloss.Color("#FFC857")
	colDim    = lipgloss.Color("#8A8F98")
	colText   = lipgloss.Color("#D7DAE0")
	colBg     = lipgloss.Color("#1B1F2A")

	styleTitle       = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleDim         = lipgloss.NewStyle().Foreground(colDim)
	styleText        = lipgloss.NewStyle().Foreground(colText)
	styleOK          = lipgloss.NewStyle().Foreground(colOK).Bold(true)
	styleBad         = lipgloss.NewStyle().Foreground(colBad).Bold(true)
	styleWarn        = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	styleKey         = lipgloss.NewStyle().Foreground(colAccent)
	styleSel         = lipgloss.NewStyle().Background(lipgloss.Color("#2A3350")).Foreground(colText).Bold(true)
	stylePanel       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 1)
	stylePanelActive = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1)
	styleHeader      = lipgloss.NewStyle().Background(colBg).Foreground(colText).Padding(0, 1)
	styleHelp        = lipgloss.NewStyle().Foreground(colDim)
)

// tag renders a status chip.
func tag(status string) string {
	switch status {
	case "PASS":
		return styleOK.Render("PASS")
	case "FAIL":
		return styleBad.Render("FAIL")
	case "WARN":
		return styleWarn.Render("WARN")
	}
	return styleDim.Render(status)
}

// panel wraps lines in a rounded border with a title.
func panel(title string, lines []string, width int, active bool) string {
	st := stylePanel
	if active {
		st = stylePanelActive
	}
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render(title))
	b.WriteString("\n")
	for _, l := range lines {
		b.WriteString(lipgloss.NewStyle().MaxWidth(inner).Render(l))
		b.WriteString("\n")
	}
	return st.Width(width).Render(strings.TrimRight(b.String(), "\n"))
}

func kvLine(k, v string) string {
	if v == "" {
		return styleDim.Render(k)
	}
	return styleDim.Render(padRight(k, 18)) + " " + styleText.Render(v)
}

// wrapText word-wraps s to at most width columns, so a long warning is never
// truncated by the panel's MaxWidth. It returns at least one line.
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func stack(panels []string) string {
	return lipgloss.JoinVertical(lipgloss.Left, panels...)
}

func boolWord(b bool) string {
	if b {
		return styleOK.Render("yes")
	}
	return styleBad.Render("no")
}
