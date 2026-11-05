package progress

import (
	"testing"
	"time"
)

func TestEventFractions(t *testing.T) {
	ind := Event{Stage: StageFMSB, Completed: 3, Total: 0}
	if ind.Determinate() {
		t.Fatal("zero total must be indeterminate")
	}
	if ind.Fraction() != 0 {
		t.Fatalf("indeterminate fraction must be 0, got %v", ind.Fraction())
	}
	det := Event{Stage: StageFMSB, Completed: 3, Total: 12}
	if !det.Determinate() {
		t.Fatal("non-zero total must be determinate")
	}
	if f := det.Fraction(); f < 0.24 || f > 0.26 {
		t.Fatalf("fraction wrong: %v", f)
	}
	over := Event{Stage: StageFMSB, Completed: 20, Total: 12}
	if over.Fraction() != 1 {
		t.Fatalf("fraction must clamp to 1, got %v", over.Fraction())
	}
	under := Event{Stage: StageFMSB, Completed: -5, Total: 12}
	if under.Fraction() != 0 {
		t.Fatalf("fraction must clamp to 0, got %v", under.Fraction())
	}
}

func TestOverallIsMonotonicAcrossStages(t *testing.T) {
	first := Event{Stage: StageInputs, Completed: 1, Total: 1}.Overall()
	last := Event{Stage: StageVerify, Completed: 1, Total: 1}.Overall()
	if !(first < last) {
		t.Fatalf("overall must increase across stages: %v -> %v", first, last)
	}
	if last > 1 || last < 0.99 {
		t.Fatalf("final stage must be near 1, got %v", last)
	}
	// An unknown stage falls back to the within-stage fraction.
	unknown := Event{Stage: Stage("nope"), Completed: 1, Total: 2}.Overall()
	if unknown != 0.5 {
		t.Fatalf("unknown stage must use the within-stage fraction, got %v", unknown)
	}
}

func TestThrottleOnlyLimitsProgress(t *testing.T) {
	var got []Event
	th := NewThrottle(func(e Event) { got = append(got, e) }, time.Hour)
	th.Emit(Event{Stage: StageFMSB, Status: StatusStart})
	th.Emit(Event{Stage: StageFMSB, Status: StatusProgress}) // dropped: same stage, within interval
	th.Emit(Event{Stage: StageFMSB, Status: StatusProgress}) // dropped
	th.Emit(Event{Stage: StageFMSB, Status: StatusDone})     // terminal: passes
	th.Emit(Event{Stage: StageCPK, Status: StatusProgress})  // stage change: passes
	if len(got) != 3 {
		t.Fatalf("throttle forwarded %d events, want 3: %+v", len(got), got)
	}
}

func TestThrottleNilSafe(t *testing.T) {
	var th *Throttle
	th.Emit(Event{Stage: StageInputs}) // must not panic
	Func(nil).Emit(Event{})            // must not panic
}
