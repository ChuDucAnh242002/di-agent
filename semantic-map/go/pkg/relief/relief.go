package relief

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
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
	// failedCooldown multiplies the cooldown of a workload whose throttle did
	// not help: the map was wrong about it, and acting on it again soon would
	// repeat the mistake.
	failedCooldown = 4
	// backoffWindow is how soon after its release a workload must be throttled
	// again for the throttling to count as a repeated cycle.
	backoffWindow = time.Hour
	// backoffCap bounds a repeatedly throttled workload's cooldown.
	backoffCap = 4 * time.Hour
	// historySize is how many actions Actions keeps.
	historySize = 200
)

// Status is the loop at a glance: where the node stands, what is in flight, and
// how often the loop's predictions have held. Cooling lists workloads the loop
// will not act on before the time given.
type Status struct {
	Phase           Phase                `json:"phase"`
	Paused          bool                 `json:"paused"`
	InFlight        *Action              `json:"in_flight,omitempty"`
	Actions         int                  `json:"actions"`
	Kept            int                  `json:"kept"`
	Reverted        int                  `json:"reverted"`
	Refused         int                  `json:"refused"`
	PredictionsHeld float64              `json:"predictions_held"` // kept / (kept+reverted)
	Cooling         map[string]time.Time `json:"cooling,omitempty"`
}

// Action is one change the loop made, with what it predicted and what it then
// observed. ID is its act decision's id, so an action and its justification
// are named alike. Resumed marks one found on the runtime at start rather than
// made by this loop: its prediction is only known if the journal still has the
// decision that made it.
type Action struct {
	ID            string       `json:"id"`
	Subject       string       `json:"subject"`
	DecisionID    string       `json:"decision_id"`
	Plan          Plan         `json:"plan"`
	Undo          runtime.Undo `json:"undo"`
	PressureAtAct float64      `json:"pressure_at_act"`
	ActedAt       time.Time    `json:"acted_at"`
	Observed      *float64     `json:"observed,omitempty"` // observed drop, after settle
	Outcome       string       `json:"outcome"`
	Resumed       bool         `json:"resumed,omitempty"`

	cooldown      time.Duration // the workload's policy cooldown, the base of every cooldown after this
	revertPending bool          // judged not to have helped; the revert has not yet succeeded
}

// Config is what a Loop is built from.
type Config struct {
	Relief  spec.Relief
	NodeID  string
	Runtime runtime.Runtime
	Policy  *policy.Policy
	Map     *statemap.Map
	Now     func() time.Time
	Logf    func(string, ...any)
}

// Loop relieves CPU pressure on one node: it notices pressure, throttles the
// workload the map says is causing it, checks after Settle that the throttle
// helped, undoes it if not, and gives every cap back once pressure is gone.
// Every step is a decision in the map's journal.
//
// Tick, Reconcile and Revert each hold the pass lock for their whole run,
// Runtime calls included, so only one of them changes the system at a time and
// an outcome can be attributed to one action. The state lock is never held
// across a Runtime call: Status, Actions, Pause and Resume take only it, so an
// HTTP handler is never blocked behind a slow orchestrator, and they always see
// state between two steps, never in the middle of one.
type Loop struct {
	cfg   Config
	c     spec.CPURelief
	epoch int64

	pass sync.Mutex // serialises the passes that change the system
	seq  int        // decision counter; touched only under pass

	mu          sync.Mutex
	trigger     *Trigger
	phase       Phase
	since       time.Time // when the current phase began
	paused      bool
	inFlight    *Action
	kept        []*Action // oldest first; release reverts from the end
	history     []*Action // oldest first, at most historySize
	acted       []time.Time
	cooling     map[string]time.Time
	released    map[string]time.Time // when each workload was last released
	cycles      map[string]int       // throttle/release cycles within backoffWindow
	lastRelease time.Time            // the last release revert, for one per Settle
	noted       string               // the last refusal journalled this episode
	nActs       int
	nKept       int
	nReverted   int
	nRefused    int
}

// New builds a loop in Calm. Its decision ids carry the time it was built, so a
// restarted loop cannot reuse an id the journal already holds.
func New(c Config) (*Loop, error) {
	switch {
	case c.Runtime == nil:
		return nil, errors.New("relief: no runtime")
	case c.Policy == nil:
		return nil, errors.New("relief: no policy")
	case c.Map == nil:
		return nil, errors.New("relief: no state map")
	case c.Relief.Interval <= 0:
		return nil, fmt.Errorf("relief: interval %s must be positive", c.Relief.Interval)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	now := c.Now()
	return &Loop{
		cfg: c, c: c.Relief.CPU, epoch: now.Unix(),
		trigger: NewTrigger(c.Relief.CPU), phase: Calm, since: now,
		cooling: map[string]time.Time{}, released: map[string]time.Time{}, cycles: map[string]int{},
	}, nil
}

// Start reconciles with what the runtime says this node changed, then ticks
// every Relief.Interval until ctx ends.
func (l *Loop) Start(ctx context.Context) {
	if err := l.Reconcile(ctx); err != nil {
		l.cfg.Logf("relief: reconcile: %v", err)
	}
	t := time.NewTicker(l.cfg.Relief.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Tick(ctx)
		}
	}
}

// Pause stops the loop making changes and releasing them. It still observes,
// runs the trigger, verifies what is in flight, and gives back what expires.
func (l *Loop) Pause() { l.mu.Lock(); l.paused = true; l.mu.Unlock() }

// Resume undoes Pause.
func (l *Loop) Resume() { l.mu.Lock(); l.paused = false; l.mu.Unlock() }

// Status is the loop now.
func (l *Loop) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()
	st := Status{Phase: l.trigger.Phase(), Paused: l.paused,
		Actions: l.nActs, Kept: l.nKept, Reverted: l.nReverted, Refused: l.nRefused}
	if l.inFlight != nil {
		a := copyAction(l.inFlight)
		st.InFlight = &a
	}
	if judged := l.nKept + l.nReverted; judged > 0 {
		st.PredictionsHeld = float64(l.nKept) / float64(judged)
	}
	for s, until := range l.cooling {
		if now.Before(until) {
			if st.Cooling == nil {
				st.Cooling = map[string]time.Time{}
			}
			st.Cooling[s] = until
		}
	}
	return st
}

// Actions lists the actions, newest first, at most 200.
func (l *Loop) Actions() []Action {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Action, 0, len(l.history))
	for i := len(l.history) - 1; i >= 0; i-- {
		out = append(out, copyAction(l.history[i]))
	}
	return out
}

// Tick is one pass: observe pressure, give back what expired, judge what has
// settled, then act or release as the phase asks. Stale pressure, or none,
// moves nothing: the trigger holds, no change is made and none is released,
// because a decision on a reading that is not current is a decision about a
// system that may no longer exist. Only expiry, which needs no reading, still
// happens.
func (l *Loop) Tick(ctx context.Context) {
	l.pass.Lock()
	defer l.pass.Unlock()
	now := l.cfg.Now()
	pressure, fresh := l.pressure()

	l.mu.Lock()
	prev := l.phase
	l.phase = l.trigger.Observe(now, pressure, fresh)
	if l.phase != prev {
		l.since = now
		switch l.phase {
		case Relieving:
			l.noted = ""
		case Releasing:
			l.lastRelease = time.Time{}
		}
	}
	phase, paused := l.phase, l.paused
	l.mu.Unlock()

	l.expire(ctx, now)

	var (
		ws     []runtime.Workload
		listed bool
		failed bool
	)
	workloads := func() ([]runtime.Workload, bool) {
		if !listed && !failed {
			var err error
			if ws, err = l.cfg.Runtime.Workloads(ctx); err != nil {
				l.cfg.Logf("relief: listing workloads: %v", err)
				failed = true
			} else {
				listed = true
			}
		}
		return ws, listed
	}

	l.verify(ctx, now, pressure, fresh, workloads)
	if paused || !fresh {
		return
	}
	switch phase {
	case Relieving:
		l.relieve(ctx, now, pressure, workloads)
	case Releasing:
		l.release(ctx, now, pressure)
	}
}

// Reconcile takes up the changes the runtime says this node made and has not
// given back, as after a restart. The most recent unexpired change becomes the
// action in flight, judged afresh from the pressure now, with its prediction
// taken from the decision that made it if the journal still holds it; with
// none, it is kept, because there is nothing to judge it against. Older
// unexpired changes are kept, for release. Expired ones are given back. A
// change stamped by another node is never touched.
func (l *Loop) Reconcile(ctx context.Context) error {
	l.pass.Lock()
	defer l.pass.Unlock()
	undos, err := l.cfg.Runtime.Changed(ctx, l.cfg.NodeID)
	if err != nil {
		return fmt.Errorf("relief: listing this node's changes: %w", err)
	}
	now := l.cfg.Now()
	pressure, _ := l.pressure()
	cooldowns := l.cooldowns(ctx)

	l.mu.Lock()
	live := l.liveSubjects()
	busy := l.inFlight != nil
	l.mu.Unlock()

	var resumed []*Action
	var errs []error
	for _, u := range undos {
		if u.NodeID != l.cfg.NodeID || live[u.Subject] {
			continue
		}
		a := &Action{ID: u.DecisionID, Subject: u.Subject, DecisionID: u.DecisionID, Undo: u,
			PressureAtAct: pressure, ActedAt: now, Resumed: true, cooldown: cooldowns(u.Subject)}
		if a.ID == "" {
			a.ID = l.id("act")
		}
		a.Plan = l.journalledPlan(u)
		if !now.Before(u.Expires) {
			l.mu.Lock()
			l.history = appendHistory(l.history, a)
			l.mu.Unlock()
			if err := l.giveBack(ctx, now, a, OutcomeExpired, "expired", a.cooldown); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		resumed = append(resumed, a)
	}
	sort.SliceStable(resumed, func(i, j int) bool { return resumed[i].Undo.Expires.Before(resumed[j].Undo.Expires) })

	l.mu.Lock()
	defer l.mu.Unlock()
	for i, a := range resumed {
		l.history = appendHistory(l.history, a)
		if i == len(resumed)-1 && !busy {
			l.inFlight = a
			continue
		}
		a.Outcome = OutcomeKept
		l.kept = append(l.kept, a)
	}
	for _, a := range resumed {
		l.cfg.Logf("relief: resumed %s on %s (expires %s)", a.ID, a.Subject, a.Undo.Expires.Format(time.RFC3339))
	}
	return errors.Join(errs...)
}

// Revert undoes an action by hand and cools its workload down, as an operator
// overriding the loop. Only an action still in force can be reverted.
func (l *Loop) Revert(ctx context.Context, id string) error {
	l.pass.Lock()
	defer l.pass.Unlock()
	l.mu.Lock()
	var a *Action
	for _, c := range l.liveLocked() {
		if c.ID == id {
			a = c
		}
	}
	l.mu.Unlock()
	if a == nil {
		return fmt.Errorf("no action %s is in force", id)
	}
	return l.giveBack(ctx, l.cfg.Now(), a, OutcomeManual, "manual", a.cooldown)
}

// pressure is node pressure and whether it is current. A property that is
// absent, stale or retired is not.
func (l *Loop) pressure() (float64, bool) {
	p, ok := l.cfg.Map.Property(NodePressure)
	return p.Value, ok && p.Status == statemap.Active
}

// expire gives back every action past its expiry, whatever the phase and
// whether or not the loop is paused: a change the agent can no longer vouch for
// must not outlive the time it was granted.
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
			l.cfg.Logf("relief: %v", err)
		}
	}
}

// verify judges the action in flight once Settle has passed: kept if the drop
// observed is at least half the drop predicted, reverted otherwise. A workload
// the runtime no longer lists is gone, and its slot is freed. Judging waits for
// a fresh reading.
func (l *Loop) verify(ctx context.Context, now time.Time, pressure float64, fresh bool, workloads func() ([]runtime.Workload, bool)) {
	l.mu.Lock()
	a := l.inFlight
	l.mu.Unlock()
	if a == nil || now.Sub(a.ActedAt) < l.c.Settle {
		return
	}
	ws, ok := workloads()
	if !ok {
		return
	}
	if !hasSubject(ws, a.Subject) {
		// Revert anyway, so a runtime that still holds the change forgets it.
		if err := l.cfg.Runtime.Revert(ctx, a.Undo); err != nil && !errors.Is(err, runtime.ErrGone) {
			l.cfg.Logf("relief: forgetting %s's change: %v", a.Subject, err)
		}
		l.gone(now, a, "the runtime no longer lists the workload")
		return
	}
	if a.revertPending {
		l.revertFailed(ctx, now, a, "")
		return
	}
	if !fresh {
		return
	}

	observed := a.PressureAtAct - pressure
	keep := observed >= keepRatio*a.Plan.Predicted || (a.Resumed && a.Plan.Predicted == 0)
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
	if a.Resumed && a.Plan.Predicted == 0 {
		b.Caveat("resumed after a restart with no journalled prediction: kept, as there is nothing to judge it against")
	}
	b.Commit(map[string]any{"verdict": verdict, "observed": observed, "predicted": a.Plan.Predicted})

	l.mu.Lock()
	a.Observed = &observed
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

// revertFailed undoes an action that did not help and puts its workload on a
// long cooldown. A revert the runtime refuses is retried on the next tick
// without judging the action again.
func (l *Loop) revertFailed(ctx context.Context, now time.Time, a *Action, verifyID string) {
	err := l.cfg.Runtime.Revert(ctx, a.Undo)
	switch {
	case errors.Is(err, runtime.ErrGone):
		l.gone(now, a, "the runtime no longer has the workload")
		return
	case err != nil:
		l.cfg.Logf("relief: reverting %s on %s: %v (retrying next tick)", a.ID, a.Subject, err)
		return
	}
	b := l.cfg.Map.Decide(l.id("revert"), fmt.Sprintf("give %s back its CPU: capping it did not help", a.Subject))
	b.Input("act", a.DecisionID)
	if verifyID != "" {
		b.Input("verify", verifyID)
	}
	b.Input("undo", a.Undo)
	b.Note("attribution failed; cooling %s down for %s", a.Subject, failedCooldown*a.cooldown)
	b.Commit(map[string]any{"reverted": a.Subject})
	l.end(now, a, OutcomeReverted, failedCooldown*a.cooldown)
	l.mu.Lock()
	l.nReverted++
	l.mu.Unlock()
}

// relieve takes one step when nothing is in flight: the eligible workloads are
// the actionable ones with no change in force and no cooldown; the hourly limit
// is checked before anything is estimated, because an estimate is a journalled
// decision and attributing on every tick when no action could follow would
// bury the journal.
func (l *Loop) relieve(ctx context.Context, now time.Time, pressure float64, workloads func() ([]runtime.Workload, bool)) {
	l.mu.Lock()
	busy := l.inFlight != nil
	live := l.liveSubjects()
	cutoff := now.Add(-time.Hour)
	for len(l.acted) > 0 && !l.acted[0].After(cutoff) {
		l.acted = l.acted[1:]
	}
	inHour := len(l.acted)
	cooling := make(map[string]bool)
	for s, until := range l.cooling {
		if now.Before(until) {
			cooling[s] = true
		}
	}
	since := l.since
	l.mu.Unlock()
	if busy {
		return
	}
	ws, ok := workloads()
	if !ok {
		return
	}
	pol := l.cfg.Policy
	var open []runtime.Workload
	var limitedBy *policy.Effective
	var limitedSubject string
	for _, w := range ws {
		if live[w.Subject] || cooling[w.Subject] || !actionable(w, pol) {
			continue
		}
		eff := pol.For(targetOf(w))
		if inHour >= eff.Settings.MaxActionsPerHour {
			if limitedBy == nil {
				limitedBy, limitedSubject = &eff, w.Subject
			}
			continue
		}
		open = append(open, w)
	}
	if len(open) == 0 {
		if limitedBy != nil {
			l.refuse(fmt.Sprintf("max_actions_per_hour %d reached", limitedBy.Settings.MaxActionsPerHour),
				map[string]any{"rule": limitedBy.Rule, "policy": *limitedBy, "subject": limitedSubject,
					"actions_last_hour": inHour})
		}
		return
	}
	if _, ok := coreCount(l.cfg.Map); !ok {
		l.refuse(noCores, nil)
		return
	}

	cands := Attribute(l.cfg.Map, open, pol, l.c)
	plan, ok, why := MakePlan(l.cfg.Map, cands, open, pol, l.c, func(string) float64 { return 1 })
	if !ok {
		l.refuse(why, map[string]any{"attribution": cands})
		return
	}
	var w runtime.Workload
	for _, c := range open {
		if c.Subject == plan.Subject {
			w = c
		}
	}
	eff := pol.For(targetOf(w))

	id := l.id("act")
	b := l.cfg.Map.Decide(id, fmt.Sprintf("cap %s's CPU to relieve node pressure?", plan.Subject))
	b.Property(NodePressure)
	b.Property(NodeUsage + "@" + plan.Subject)
	b.Facts()
	undo, err := l.cfg.Runtime.Apply(ctx, plan.Subject, runtime.Change{
		Action: runtime.ActionCPULimit, CPULimit: plan.ToCores, Expires: now.Add(l.c.Expiry), DecisionID: id,
	})
	if err != nil {
		r := l.cfg.Map.Decide(l.id("refused"), fmt.Sprintf("cap %s's CPU to relieve node pressure?", plan.Subject))
		r.Input("plan", plan)
		r.Input("policy", eff)
		r.Input("rule", eff.Rule)
		r.Input("error", err.Error())
		r.Note("the runtime refused the change; cooling %s down for %s", plan.Subject, eff.Settings.Cooldown)
		r.Commit(map[string]any{"refused": "runtime: " + err.Error()})
		l.mu.Lock()
		l.cooling[plan.Subject] = now.Add(eff.Settings.Cooldown)
		l.nRefused++
		l.mu.Unlock()
		return
	}

	b.Input("notice", map[string]any{"pressure": pressure, "phase": string(Relieving), "since": since})
	b.Input("attribution", cands)
	b.Input("plan", plan)
	b.Input("policy", eff)
	b.Note("capping %s from %.3f to %.3f cores; predicted drop in node pressure %.4f", plan.Subject,
		plan.FromCores, plan.ToCores, plan.Predicted)
	if plan.Basis == BasisUsage {
		b.Caveat("%s", usageCaveat)
	}
	b.Commit(map[string]any{"action": runtime.ActionCPULimit, "subject": plan.Subject,
		"to_cores": plan.ToCores, "predicted": plan.Predicted, "expires": undo.Expires})

	a := &Action{ID: id, Subject: plan.Subject, DecisionID: id, Plan: plan, Undo: undo,
		PressureAtAct: pressure, ActedAt: now, cooldown: eff.Settings.Cooldown}
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.released[plan.Subject]; ok && now.Sub(last) < backoffWindow {
		l.cycles[plan.Subject]++
	} else {
		l.cycles[plan.Subject] = 0
	}
	l.inFlight = a
	l.history = appendHistory(l.history, a)
	l.acted = append(l.acted, now)
	l.nActs++
	l.noted = ""
}

// release gives back the most recent kept change, one per Settle, once the
// node has cooled. With nothing left it ends Releasing. An action in flight is
// judged first, so it is either kept and released in turn or reverted.
func (l *Loop) release(ctx context.Context, now time.Time, pressure float64) {
	l.mu.Lock()
	if l.inFlight != nil {
		l.mu.Unlock()
		return
	}
	if len(l.kept) == 0 {
		l.trigger.Released()
		l.phase, l.since = l.trigger.Phase(), now
		l.mu.Unlock()
		return
	}
	if !l.lastRelease.IsZero() && now.Sub(l.lastRelease) < l.c.Settle {
		l.mu.Unlock()
		return
	}
	a := l.kept[len(l.kept)-1]
	cooldown := backoff(a.cooldown, l.cycles[a.Subject])
	l.mu.Unlock()

	err := l.cfg.Runtime.Revert(ctx, a.Undo)
	switch {
	case errors.Is(err, runtime.ErrGone):
		l.gone(now, a, "the runtime no longer has the workload")
		return
	case err != nil:
		l.cfg.Logf("relief: releasing %s on %s: %v (retrying next tick)", a.ID, a.Subject, err)
		return
	}
	b := l.cfg.Map.Decide(l.id("release"), fmt.Sprintf("give %s back its CPU: node pressure has eased", a.Subject))
	b.Property(NodePressure)
	b.Input("act", a.DecisionID)
	b.Input("undo", a.Undo)
	b.Input("pressure", pressure)
	b.Note("released most recent first; cooling %s down for %s", a.Subject, cooldown)
	b.Commit(map[string]any{"released": a.Subject, "cooldown": cooldown.String()})
	l.end(now, a, OutcomeReleased, cooldown)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastRelease = now
	l.released[a.Subject] = now
	if len(l.kept) == 0 && l.inFlight == nil {
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
	for i, k := range l.kept {
		if k == a {
			l.kept = append(l.kept[:i:i], l.kept[i+1:]...)
			break
		}
	}
	if cooldown > 0 {
		l.cooling[a.Subject] = now.Add(cooldown)
	}
}

// refuse journals why no action was taken, once per reason per relieving
// episode, so a reason that holds for an hour is one record rather than 240.
func (l *Loop) refuse(why string, inputs map[string]any) {
	l.mu.Lock()
	if why == l.noted {
		l.mu.Unlock()
		return
	}
	l.noted = why
	l.nRefused++
	l.mu.Unlock()
	b := l.cfg.Map.Decide(l.id("refused"), "relieve node CPU pressure?")
	b.Property(NodePressure)
	for k, v := range inputs {
		b.Input(k, v)
	}
	b.Caveat("%s", why)
	b.Commit(map[string]any{"refused": why})
}

// id names the next decision: relief-<kind>-<loop start>-<n>.
func (l *Loop) id(kind string) string {
	l.seq++
	return fmt.Sprintf("relief-%s-%d-%d", kind, l.epoch, l.seq)
}

// liveLocked is every action still in force. Caller holds mu.
func (l *Loop) liveLocked() []*Action {
	out := append([]*Action(nil), l.kept...)
	if l.inFlight != nil {
		out = append(out, l.inFlight)
	}
	return out
}

// liveSubjects is the workloads with a change in force. A second change on top
// of one would record the first cap as the original, and reverting it would
// leave the workload capped. Caller holds mu.
func (l *Loop) liveSubjects() map[string]bool {
	out := map[string]bool{}
	for _, a := range l.liveLocked() {
		out[a.Subject] = true
	}
	return out
}

// cooldowns returns each workload's policy cooldown, for actions found on the
// runtime rather than made here. A workload the runtime no longer lists gets
// the policy for its kind alone.
func (l *Loop) cooldowns(ctx context.Context) func(subject string) time.Duration {
	ws, err := l.cfg.Runtime.Workloads(ctx)
	if err != nil {
		l.cfg.Logf("relief: listing workloads: %v", err)
	}
	return func(subject string) time.Duration {
		for _, w := range ws {
			if w.Subject == subject {
				return l.cfg.Policy.For(targetOf(w)).Settings.Cooldown
			}
		}
		return l.cfg.Policy.For(targetOf(runtime.Workload{Subject: subject})).Settings.Cooldown
	}
}

// journalledPlan is the plan the decision that made a change recorded, so a
// resumed action is judged against what was predicted. A snapshot restores
// inputs as decoded JSON rather than as a Plan, so both are read.
func (l *Loop) journalledPlan(u runtime.Undo) Plan {
	p := Plan{Subject: u.Subject, ToCores: u.Applied}
	if u.DecisionID == "" {
		return p
	}
	d, ok := l.cfg.Map.Journal().Decision(u.DecisionID)
	if !ok {
		return p
	}
	switch v := d.Inputs["plan"].(type) {
	case Plan:
		return v
	case map[string]any:
		if f, ok := v["predicted"].(float64); ok {
			p.Predicted = f
		}
		if s, ok := v["estimate_id"].(string); ok {
			p.EstimateID = s
		}
	}
	return p
}

// backoff is a workload's cooldown after its k-th throttle/release cycle within
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
	if a.Observed != nil {
		v := *a.Observed
		c.Observed = &v
	}
	c.Undo.Original = runtime.Resource{Request: cloneFloat(a.Undo.Original.Request), Limit: cloneFloat(a.Undo.Original.Limit)}
	return c
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
