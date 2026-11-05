package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// renderBar draws a progress bar of the given total width. A determinate bar
// fills proportionally to frac; an indeterminate bar shows a moving segment
// driven by tick. It never shows a fabricated percentage.
func renderBar(width int, frac float64, indeterminate bool, tick int) string {
	if width < 5 {
		width = 5
	}
	inner := width - 2
	if inner < 3 {
		inner = 3
	}
	if indeterminate {
		pos := 0
		if inner > 0 {
			pos = tick % inner
		}
		var b strings.Builder
		for i := 0; i < inner; i++ {
			if i == pos {
				b.WriteString("━")
			} else {
				b.WriteString("·")
			}
		}
		return styleDim.Render("[") + styleKey.Render(b.String()) + styleDim.Render("]")
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(inner) + 0.5)
	if filled > inner {
		filled = inner
	}
	return styleKey.Render("[") + styleOK.Render(strings.Repeat("█", filled)) +
		styleDim.Render(strings.Repeat("░", inner-filled)) + styleKey.Render("]")
}

// percentLabel renders " 42%" for a determinate fraction.
func percentLabel(frac float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return styleDim.Render(itoa(int(frac*100+0.5)) + "%")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var _ = lipgloss.NewStyle
