package app

import (
	"13s-patcher/internal/hashutil"
)

// shortSHA truncates a digest for display.
func shortSHA(h string) string { return hashutil.Short(h) }
