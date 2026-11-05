package tui

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"13s-patcher/internal/app"
)

// Run launches the interactive dashboard. The terminal is restored by bubbletea
// on every exit path (quit, cancel, error); a recovery guard reports an internal
// panic after the screen has been restored.
func Run(svc *app.Service) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintln(os.Stderr, "\nterminal restored after internal dashboard error:", rec)
			err = fmt.Errorf("dashboard panic: %v", rec)
		}
	}()
	m := newModel(svc)
	p := tea.NewProgram(m)
	if _, err = p.Run(); err != nil {
		return err
	}
	fmt.Println("dashboard closed — terminal restored")
	return nil
}

// RunModel is used by the tests to drive the model without a terminal.
func RunModel(svc *app.Service) *model { return newModel(svc) }
