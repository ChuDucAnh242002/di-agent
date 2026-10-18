package relief

import (
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func cfg() spec.CPURelief {
	return spec.CPURelief{Enter: 0.25, Exit: 0.10, EnterFor: time.Minute, ExitFor: 3 * time.Minute}
}

// feed observes a fresh reading every 5s from start for d, inclusive of both
// ends, and returns the phase after the last one.
func feed(tr *Trigger, start time.Duration, d time.Duration, p float64) Phase {
	var ph Phase
	for at := start; at <= start+d; at += 5 * time.Second {
		ph = tr.Observe(t0.Add(at), p, true)
	}
	return ph
}

func relieving(t *testing.T) *Trigger {
	t.Helper()
	tr := NewTrigger(cfg())
	if feed(tr, 0, time.Minute, 0.3) != Relieving {
		t.Fatal("setup: not relieving")
	}
	return tr
}

func TestPressureMustLastToEnter(t *testing.T) {
	tr := NewTrigger(cfg())
	if got := feed(tr, 0, 59*time.Second, 0.3); got != Calm {
		t.Fatalf("after 59s: %s", got)
	}
	if got := tr.Observe(t0.Add(60*time.Second), 0.3, true); got != Relieving {
		t.Fatalf("at 60s: %s", got)
	}
}

func TestABriefDipDoesNotEnter(t *testing.T) {
	tr := NewTrigger(cfg())
	feed(tr, 0, 30*time.Second, 0.3)
	tr.Observe(t0.Add(35*time.Second), 0.2, true)
	if got := feed(tr, 40*time.Second, 45*time.Second, 0.3); got != Calm {
		t.Fatalf("got %s", got)
	}
}

func TestCalmStaysCalmUnderLowPressure(t *testing.T) {
	tr := NewTrigger(cfg())
	if got := feed(tr, 0, time.Hour, 0.01); got != Calm {
		t.Fatalf("got %s", got)
	}
}

func TestItLeavesOnlyBelowTheLowerBand(t *testing.T) {
	tr := relieving(t)
	at := 2 * time.Minute
	if got := feed(tr, at, 10*time.Minute, 0.15); got != Relieving {
		t.Fatalf("between bands: %s", got)
	}
	at += 11 * time.Minute
	if got := feed(tr, at, 3*time.Minute, 0.05); got != Releasing {
		t.Fatalf("below exit: %s", got)
	}
}

func releasing(t *testing.T) (*Trigger, time.Duration) {
	t.Helper()
	tr := relieving(t)
	at := 2 * time.Minute
	if feed(tr, at, 3*time.Minute, 0.05) != Releasing {
		t.Fatal("setup: not releasing")
	}
	return tr, at + 4*time.Minute
}

func TestPressureReturningStopsRelease(t *testing.T) {
	tr, at := releasing(t)
	if got := feed(tr, at, time.Minute, 0.3); got != Relieving {
		t.Fatalf("got %s", got)
	}
}

func TestReleasingDoesNotReturnToCalmByItself(t *testing.T) {
	tr, at := releasing(t)
	if got := feed(tr, at, time.Hour, 0.0); got != Releasing {
		t.Fatalf("got %s", got)
	}
}

func TestReleasedMovesReleasingToCalmOnly(t *testing.T) {
	tr := relieving(t)
	tr.Released()
	if tr.Phase() != Relieving {
		t.Fatalf("Released changed %s", tr.Phase())
	}
	tr, _ = releasing(t)
	tr.Released()
	if tr.Phase() != Calm {
		t.Fatalf("got %s", tr.Phase())
	}
}

func TestStaleReadingsHoldThePhase(t *testing.T) {
	tr := relieving(t)
	at := 2 * time.Minute
	for ; at <= 12*time.Minute; at += 5 * time.Second {
		if got := tr.Observe(t0.Add(at), 0, false); got != Relieving {
			t.Fatalf("got %s", got)
		}
	}
}

func TestStaleReadingsNeitherAdvanceNorBreakARun(t *testing.T) {
	tr := NewTrigger(cfg())
	tr.Observe(t0, 0.3, true)
	// A long stale gap does not complete the run, and does not break it.
	if got := tr.Observe(t0.Add(10*time.Minute), 0, false); got != Calm {
		t.Fatalf("stale: %s", got)
	}
	// The run began at t0, so the next fresh high reading completes it.
	if got := tr.Observe(t0.Add(11*time.Minute), 0.3, true); got != Relieving {
		t.Fatalf("resumed: %s", got)
	}
}
