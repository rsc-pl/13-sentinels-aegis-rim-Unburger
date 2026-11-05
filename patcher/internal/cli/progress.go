package cli

import (
	"fmt"
	"io"
	"sync"

	"13s-patcher/internal/progress"
)

// stagePrinter renders a concise textual narrative of a long operation: one
// line when a stage starts and one when it finishes with a count. It is the CLI
// counterpart of the dashboard's progress bars and consumes the same events.
type stagePrinter struct {
	mu         sync.Mutex
	w          io.Writer
	last       progress.Stage
	lastStatus string
}

func newStagePrinter(w io.Writer) progress.Func {
	sp := &stagePrinter{w: w}
	return sp.emit
}

func (sp *stagePrinter) emit(e progress.Event) {
	if sp.w == nil {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	terminal := e.Status == progress.StatusDone || e.Status == progress.StatusError || e.Status == progress.StatusWarn
	changed := e.Stage != sp.last
	if !changed && !terminal {
		return
	}
	if !changed && terminal && e.Status == sp.lastStatus {
		return
	}
	sp.last = e.Stage
	sp.lastStatus = e.Status
	switch {
	case e.Error != "":
		fmt.Fprintf(sp.w, "  %-22s FAILED: %s\n", e.Stage.Label(), e.Error)
	case e.Total > 0:
		note := e.Note
		if note != "" {
			note = " · " + note
		}
		fmt.Fprintf(sp.w, "  %-22s %d/%d%s\n", e.Stage.Label(), e.Completed, e.Total, note)
	default:
		note := e.Note
		if note != "" {
			note = " · " + note
		}
		fmt.Fprintf(sp.w, "  %-22s%s\n", e.Stage.Label(), note)
	}
}
