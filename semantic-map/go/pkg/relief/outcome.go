package relief

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DiyazY/di-agent/pkg/runtime"
)

// What became of an action. An action with no outcome is in flight: applied
// and waiting for Settle to pass so its effect can be judged.
const (
	OutcomeKept     = "kept"     // it helped; the cap stays until release or expiry
	OutcomeReverted = "reverted" // it did not help and was undone
	OutcomeReleased = "released" // pressure ended and the cap was given back
	OutcomeExpired  = "expired"  // it outlived its expiry and was given back
	OutcomeGone     = "gone"     // the workload left; there was nothing to give back
	OutcomeManual   = "manual"   // an operator undid it
)

const (
	// keepRatio is how much of the predicted drop must be observed for an
	// action to be judged to have helped.
	keepRatio = 0.5
	// failedCooldown multiplies the cooldown of a workload whose cap did not
	// help: the map was wrong about it, and acting on it again soon would
	// repeat the mistake.
	failedCooldown = 4
	// backoffWindow is how soon after its release a workload must be capped
	// again for the capping to count as a repeated cycle.
	backoffWindow = time.Hour
	// backoffCap bounds a repeatedly capped workload's cooldown.
	backoffCap = 4 * time.Hour
	// historySize is how many actions Actions keeps.
	historySize = 200
)

// expire gives back every action past its expiry, whatever the phase and
// whether or not the loop is paused: a change the agent can no longer vouch for
// must not outlive the time it was granted. One the runtime will not give back
// stays in force and is tried again next tick.
func (l *Loop) expire(ctx context.Context, now time.Time) {
	l.mu.Lock()
	var due []*Action
	for _, a := range l.liveLocked() {
		if !now.Before(a.Undo.Expires) {
			due = append(due, a)
		}
	}
	l.mu.Unlock()
	for _, a := range due {
		if err := l.giveBack(ctx, now, a, OutcomeExpired, "expired", a.cooldown); err != nil {
			l.undoFailed(a, "expiring", err)
		}
	}
}

// undoFailed records that the runtime refused to undo a's change: counted and
// kept on the action, so Actions and Status show a change stuck in force, logged
// every time, and journalled only on the first refusal, so a runtime that refuses
// for an hour is one record rather than 240. The change stays in force and the
// undo is retried on the next tick. Called under the pass lock, with mu not held.
func (l *Loop) undoFailed(a *Action, what string, err error) {
	l.mu.Lock()
	a.RevertAttempts++
	a.LastError = err.Error()
	n := a.RevertAttempts
	l.mu.Unlock()
	l.cfg.Logf("relief: %s %s on %s: %v (attempt %d, retrying next tick)", what, a.ID, a.Subject, err, n)
	if n != 1 {
		return
	}
	b := l.cfg.Map.Decide(l.id("stuck"), fmt.Sprintf("give back the change to %s?", a.Subject))
	b.Input("act", a.DecisionID)
	b.Input("undo", a.Undo)
	b.Input("while", what)
	b.Input("error", err.Error())
	b.Caveat("the runtime refused to undo the change: it stays in force and is retried every tick; later refusals are counted on the action, not journalled")
	b.Commit(map[string]any{"stuck": a.Subject})
}

// verify judges the action in flight once Settle has passed: kept if the drop
// observed is at least half the drop predicted, reverted otherwise. A resumed
// action is always kept, as there is no pre-cap baseline to judge it by. The
// judgement needs a fresh reading taken after Settle; one taken before it says
// nothing about the cap's effect. A workload the runtime no longer lists is
// gone, and its slot is freed once the runtime has let go of the change.
func (l *Loop) verify(ctx context.Context, now time.Time, pressure float64, at time.Time, fresh bool, workloads func() ([]runtime.Workload, bool)) {
	l.mu.Lock()
	a := l.inFlight
	l.mu.Unlock()
	if a == nil || a.needBaseline || now.Sub(a.ActedAt) < l.c.Settle {
		return
	}
	ws, ok := workloads()
	if !ok {
		return
	}
	if !hasSubject(ws, a.Subject) {
		// Revert anyway, so a runtime that still holds the change gives it up;
		// the undo is dropped only once it has.
		if err := l.cfg.Runtime.Revert(ctx, a.Undo); err != nil && !errors.Is(err, runtime.ErrGone) {
			l.undoFailed(a, "letting go of", err)
			return
		}
		l.gone(now, a, "the runtime no longer lists the workload")
		return
	}
	if a.revertPending {
		l.revertFailed(ctx, now, a, a.VerifyID)
		return
	}
	if !fresh || at.Before(a.ActedAt.Add(l.c.Settle)) {
		return
	}

	observed := a.PressureAtAct - pressure
	// A resumed action's baseline was read with its cap already in force, so the
	// drop it caused is in neither reading: judging it would revert a cap that
	// works. It is kept, and given back by release or expiry like any other.
	keep := a.Resumed || observed >= keepRatio*a.Plan.Predicted
	verdict := "revert"
	if keep {
		verdict = "keep"
	}
	id := l.id("verify")
	b := l.cfg.Map.Decide(id, fmt.Sprintf("did capping %s relieve node CPU pressure?", a.Subject))
	b.Property(NodePressure)
	b.Input("act", a.DecisionID)
	b.Input("predicted", a.Plan.Predicted)
	b.Input("observed", observed)
	b.Input("pressure_at_act", a.PressureAtAct)
	b.Note("pressure %.4f at act, %.4f now: observed drop %.4f against %.4f predicted; keep needs %.0f%% of it",
		a.PressureAtAct, pressure, observed, a.Plan.Predicted, keepRatio*100)
	if a.Resumed {
		b.Caveat("resumed after a restart: its baseline was taken with the change already in force, so there is no pre-cap pressure to judge it against; kept until release or expiry")
	}
	l.learn(b, id, a, keep, observed, now)
	b.Commit(map[string]any{"verdict": verdict, "observed": observed, "predicted": a.Plan.Predicted})

	l.mu.Lock()
	a.Observed, a.VerifyID = &observed, id
	if keep {
		a.Outcome = OutcomeKept
		l.inFlight = nil
		l.kept = append(l.kept, a)
		l.nKept++
		l.mu.Unlock()
		return
	}
	a.revertPending = true
	l.mu.Unlock()
	l.revertFailed(ctx, now, a, id)
}

// revertFailed undoes a step that did not help and puts its workload on a long
// cooldown. A first step is reverted; a further step is undone by putting back
// the cap before it, which stays kept. A revert the runtime refuses is retried
// on the next tick without judging the step again.
func (l *Loop) revertFailed(ctx context.Context, now time.Time, a *Action, verifyID string) {
	var (
		undo runtime.Undo
		err  error
	)
	if a.prev != nil {
		p := a.prev
		undo, err = l.cfg.Runtime.Apply(ctx, a.Subject, runtime.Change{Action: runtime.ActionCPULimit,
			CPULimit: p.Undo.Applied, Expires: p.Undo.Expires, DecisionID: p.DecisionID})
		if err != nil {
			l.reconciled = false // what the runtime holds now is asked again, as after any failed Apply
		}
	} else {
		err = l.cfg.Runtime.Revert(ctx, a.Undo)
	}
	switch {
	case errors.Is(err, runtime.ErrGone):
		l.gone(now, a, "the runtime no longer has the workload")
		return
	case err != nil:
		l.undoFailed(a, "reverting", err)
		return
	}
	cooldown := failedCooldown * a.cooldown
	question := fmt.Sprintf("give %s back its CPU: capping it did not help", a.Subject)
	if a.prev != nil {
		question = fmt.Sprintf("put %s back at its earlier cap: the further step did not help", a.Subject)
	}
	b := l.cfg.Map.Decide(l.id("revert"), question)
	b.Input("act", a.DecisionID)
	if verifyID != "" {
		b.Input("verify", verifyID)
	}
	b.Input("undo", a.Undo)
	if a.prev != nil {
		b.Input("restored", map[string]any{"act": a.prev.DecisionID, "to_cores": a.prev.Undo.Applied})
	}
	b.Note("attribution failed; cooling %s down for %s", a.Subject, cooldown)
	b.Commit(map[string]any{"reverted": a.Subject})

	l.mu.Lock()
	l.nReverted++
	if p := a.prev; p != nil {
		id, failed := a.ID, a.VerifyID
		*a = *p
		// The verify that caused the revert is the one an operator wants, not the earlier step's.
		a.ID, a.Undo, a.Outcome, a.revertPending, a.VerifyID = id, undo, OutcomeKept, false, failed
		a.RevertAttempts, a.LastError = 0, "" // the undo went through; the earlier cap is in force anew
		if l.inFlight == a {
			l.inFlight = nil
		}
		l.kept = append(l.kept, a)
		l.cooling[a.Subject] = now.Add(cooldown)
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	l.end(now, a, OutcomeReverted, cooldown)
}

// release gives back the most recent kept change, one per Settle, whenever the
// node is not relieving: after pressure has eased, and after a restart that
// found caps on a calm node. One the runtime will not give back does not hold up
// the rest: the next most recent is tried in the same tick, and the refused one
// is tried again on the next. In Releasing, with nothing left, it ends the
// phase. An action in flight is judged first, so it is either kept and
// released in turn or reverted.
func (l *Loop) release(ctx context.Context, now time.Time, pressure float64, phase Phase) {
	l.mu.Lock()
	if l.inFlight != nil {
		l.mu.Unlock()
		return
	}
	if len(l.kept) == 0 {
		if phase == Releasing {
			l.trigger.Released()
			l.phase, l.since = l.trigger.Phase(), now
		}
		l.mu.Unlock()
		return
	}
	if !l.lastRelease.IsZero() && now.Sub(l.lastRelease) < l.c.Settle {
		l.mu.Unlock()
		return
	}
	kept := make([]*Action, len(l.kept))
	cooldowns := make([]time.Duration, len(l.kept))
	for i, a := range l.kept {
		kept[len(kept)-1-i] = a // newest first
		cooldowns[len(kept)-1-i] = backoff(a.cooldown, l.cycles[a.Subject])
	}
	l.mu.Unlock()

	var skipped []string
	for i, a := range kept {
		err := l.cfg.Runtime.Revert(ctx, a.Undo)
		switch {
		case errors.Is(err, runtime.ErrGone):
			l.gone(now, a, "the runtime no longer has the workload")
			return
		case err != nil:
			l.undoFailed(a, "releasing", err)
			skipped = append(skipped, a.ID)
			continue
		}
		l.recordRelease(now, a, pressure, phase, cooldowns[i], skipped)
		return
	}
}

// recordRelease journals and records the release of a, whose revert went
// through. skipped lists the newer actions the runtime would not give back first.
func (l *Loop) recordRelease(now time.Time, a *Action, pressure float64, phase Phase, cooldown time.Duration, skipped []string) {
	b := l.cfg.Map.Decide(l.id("release"), fmt.Sprintf("give %s back its CPU: node pressure has eased", a.Subject))
	b.Property(NodePressure)
	b.Input("act", a.DecisionID)
	b.Input("undo", a.Undo)
	b.Input("pressure", pressure)
	b.Input("phase", string(phase))
	b.Note("released most recent first; cooling %s down for %s", a.Subject, cooldown)
	if len(skipped) > 0 {
		b.Input("skipped", skipped)
		b.Note("newer changes the runtime would not give back were passed over, to be retried: %s", strings.Join(skipped, ", "))
	}
	b.Commit(map[string]any{"released": a.Subject, "cooldown": cooldown.String()})
	l.end(now, a, OutcomeReleased, cooldown)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastRelease = now
	l.released[a.Subject] = now
	if len(l.kept) == 0 && l.inFlight == nil && l.trigger.Phase() == Releasing {
		l.trigger.Released()
		l.phase, l.since = l.trigger.Phase(), now
	}
}

// giveBack reverts an action for a reason other than its verdict or a release:
// expiry or an operator. A workload that is gone is recorded as such.
func (l *Loop) giveBack(ctx context.Context, now time.Time, a *Action, outcome, kind string, cooldown time.Duration) error {
	err := l.cfg.Runtime.Revert(ctx, a.Undo)
	switch {
	case errors.Is(err, runtime.ErrGone):
		l.gone(now, a, "the runtime no longer has the workload")
		return nil
	case err != nil:
		return fmt.Errorf("reverting %s on %s (%s): %w", a.ID, a.Subject, outcome, err)
	}
	question := fmt.Sprintf("give %s back its CPU: the change expired", a.Subject)
	if outcome == OutcomeManual {
		question = fmt.Sprintf("give %s back its CPU: an operator asked", a.Subject)
	}
	b := l.cfg.Map.Decide(l.id(kind), question)
	b.Input("act", a.DecisionID)
	b.Input("undo", a.Undo)
	b.Note("cooling %s down for %s", a.Subject, cooldown)
	b.Commit(map[string]any{outcome: a.Subject})
	l.end(now, a, outcome, cooldown)
	return nil
}

// gone records that an action's workload left and frees its slot. There is
// nothing to give back and nothing to retry: the workload, if it returns, is a
// new one.
func (l *Loop) gone(now time.Time, a *Action, why string) {
	b := l.cfg.Map.Decide(l.id("gone"), fmt.Sprintf("what became of the change to %s?", a.Subject))
	b.Input("act", a.DecisionID)
	b.Input("undo", a.Undo)
	b.Note("%s; the change is dropped", why)
	b.Commit(map[string]any{"gone": a.Subject})
	l.end(now, a, OutcomeGone, 0)
}

// end closes an action: it leaves the slot it held and its workload cools down
// from now.
func (l *Loop) end(now time.Time, a *Action, outcome string, cooldown time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a.Outcome, a.revertPending = outcome, false
	if l.inFlight == a {
		l.inFlight = nil
	}
	l.kept = removeAction(l.kept, a)
	if cooldown > 0 {
		l.cooling[a.Subject] = now.Add(cooldown)
	}
}

// backoff is a workload's cooldown after its k-th cap/release cycle within
// backoffWindow: doubled each cycle, capped at backoffCap.
func backoff(base time.Duration, k int) time.Duration {
	d := base
	for i := 0; i < k && d < backoffCap; i++ {
		d *= 2
	}
	return min(d, backoffCap)
}

func hasSubject(ws []runtime.Workload, subject string) bool {
	for _, w := range ws {
		if w.Subject == subject {
			return true
		}
	}
	return false
}

// removeAction drops a from as without disturbing the order of the rest.
func removeAction(as []*Action, a *Action) []*Action {
	for i, x := range as {
		if x == a {
			return append(as[:i:i], as[i+1:]...)
		}
	}
	return as
}

func appendHistory(h []*Action, a *Action) []*Action {
	h = append(h, a)
	if len(h) > historySize {
		h = h[len(h)-historySize:]
	}
	return h
}

// copyAction is an action the caller cannot change the loop's record through.
func copyAction(a *Action) Action {
	c := *a
	c.Observed = cloneFloat(a.Observed)
	c.Undo.Original = runtime.Resource{Request: cloneFloat(a.Undo.Original.Request), Limit: cloneFloat(a.Undo.Original.Limit)}
	if a.SharesAtAct != nil {
		c.SharesAtAct = make(map[string]float64, len(a.SharesAtAct))
		for s, v := range a.SharesAtAct {
			c.SharesAtAct[s] = v
		}
	}
	c.prev, c.origin = nil, nil
	return c
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
