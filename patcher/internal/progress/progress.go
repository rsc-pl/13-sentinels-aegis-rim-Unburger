// Package progress defines the structured progress events the shared service
// emits while a long operation runs. The CLI and the TUI are the only
// consumers: the service never renders anything, so both front ends report the
// same stages from the same code path.
//
// An event describes one stage of one operation. When a stage has a known
// number of items (banks, records, members, targets) Total is non-zero and the
// consumer may render a determinate bar; otherwise Total is zero and the
// consumer must render an indeterminate indicator. No percentage is ever
// fabricated.
package progress

import (
	"time"
)

// Stage names one step of an operation.
type Stage string

// The stages a build (or validation) passes through, in order.
const (
	StageInputs       Stage = "inputs"
	StageTranslations Stage = "translations"
	StageDiscovery    Stage = "discovery"
	StageExtraction   Stage = "extraction"
	StagePlan         Stage = "plan"
	StageFMSB         Stage = "fmsb-rebuild"
	StageCompress     Stage = "compress"
	StageCPK          Stage = "cpk-build"
	StageTech         Stage = "tech-mods"
	StagePackage      Stage = "package"
	StageVerify       Stage = "verify"
)

// Order lists the stages so a consumer can compute an overall position.
var Order = []Stage{
	StageInputs, StageTranslations, StageDiscovery, StageExtraction,
	StagePlan, StageFMSB, StageCompress, StageCPK, StageTech, StagePackage, StageVerify,
}

// Index returns the position of a stage in Order, or -1 when unknown.
func (s Stage) Index() int {
	for i, x := range Order {
		if x == s {
			return i
		}
	}
	return -1
}

// Label is the short human-readable name shown in progress output.
func (s Stage) Label() string {
	switch s {
	case StageInputs:
		return "reading inputs"
	case StageTranslations:
		return "translation dataset"
	case StageDiscovery:
		return "local containers"
	case StageExtraction:
		return "extraction / cache"
	case StagePlan:
		return "planning changes"
	case StageFMSB:
		return "rebuilding text banks"
	case StageCompress:
		return "compressing members"
	case StageCPK:
		return "writing archive"
	case StageTech:
		return "tech mods / resources"
	case StagePackage:
		return "packaging targets"
	case StageVerify:
		return "verifying outputs"
	}
	return string(s)
}

// Status values for an event.
const (
	StatusStart    = "start"
	StatusProgress = "progress"
	StatusDone     = "done"
	StatusWarn     = "warning"
	StatusError    = "error"
)

// Event is one structured progress report.
type Event struct {
	// Op is the operation name, e.g. "build" or "validate".
	Op string
	// Stage is the pipeline stage the event belongs to.
	Stage Stage
	// Item is the current item, when meaningful (a bank, member or target).
	Item string
	// Completed and Total count items within the stage. Total == 0 means the
	// stage is indeterminate.
	Completed int64
	Total     int64
	// Bytes counts payload bytes moved within the stage, when meaningful.
	Bytes int64
	// Elapsed is how long the operation has run.
	Elapsed time.Duration
	// Status is one of the Status* constants.
	Status string
	// Note is a short human-readable detail (current step, destination, …).
	Note string
	// Warning and Error carry a non-fatal note or the failure cause.
	Warning string
	Error   string
}

// Determinate reports whether the event carries a real item total.
func (e Event) Determinate() bool { return e.Total > 0 }

// Fraction is the within-stage fraction, or 0 when indeterminate.
func (e Event) Fraction() float64 {
	if e.Total <= 0 {
		return 0
	}
	f := float64(e.Completed) / float64(e.Total)
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// Overall returns the overall fraction across the known stage Order, or the
// within-stage fraction when the stage is unknown.
func (e Event) Overall() float64 {
	i := e.Stage.Index()
	if i < 0 || len(Order) == 0 {
		return e.Fraction()
	}
	base := float64(i) / float64(len(Order))
	span := 1.0 / float64(len(Order))
	return base + span*e.Fraction()
}

// Func receives progress events. A nil Func is safe to call through Emit.
type Func func(Event)

// Emit forwards an event when the sink is set.
func (f Func) Emit(e Event) {
	if f != nil {
		f(e)
	}
}

// Throttle wraps a Func and drops high-frequency progress updates so a busy
// consumer (a terminal UI) is never flooded. Stage changes, terminal events and
// warnings always pass through; ordinary progress updates are limited to one
// per interval.
type Throttle struct {
	fn        Func
	interval  time.Duration
	last      time.Time
	lastStage Stage
}

// NewThrottle returns a throttling sink. interval <= 0 disables throttling.
func NewThrottle(fn Func, interval time.Duration) *Throttle {
	return &Throttle{fn: fn, interval: interval}
}

// Emit applies the throttle rule and forwards the event.
func (t *Throttle) Emit(e Event) {
	if t == nil || t.fn == nil {
		return
	}
	force := e.Status != StatusProgress || e.Warning != "" || e.Error != "" || e.Stage != t.lastStage
	if !force && t.interval > 0 && time.Since(t.last) < t.interval {
		return
	}
	t.last = time.Now()
	t.lastStage = e.Stage
	t.fn(e)
}
