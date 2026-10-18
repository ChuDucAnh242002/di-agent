package relief

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
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

// Action is the agent's change to one workload, with what it predicted and what
// it then observed. A workload stepped again while its change is in force keeps
// one Action: Plan, Undo and DecisionID describe the latest step, and ID stays
// the first step's act decision, so an operator's handle on the change does not
// move. Resumed marks one found on the runtime at start rather than made by this
// loop: its prediction is only known if the journal still has the decision that
// made it. SharesAtAct is every workload's share of node CPU when the action's
// baseline was taken, so its outcome can be told apart from another workload
// moving at the same time.
type Action struct {
	ID            string             `json:"id"`
	Subject       string             `json:"subject"`
	DecisionID    string             `json:"decision_id"`
	Plan          Plan               `json:"plan"`
	Undo          runtime.Undo       `json:"undo"`
	PressureAtAct float64            `json:"pressure_at_act"`
	ActedAt       time.Time          `json:"acted_at"`
	Observed      *float64           `json:"observed,omitempty"` // observed drop, after settle
	Outcome       string             `json:"outcome"`
	Resumed       bool               `json:"resumed,omitempty"`
	SharesAtAct   map[string]float64 `json:"shares_at_act,omitempty"`

	cooldown      time.Duration // the workload's policy cooldown, the base of every cooldown after this
	revertPending bool          // judged not to have helped; the revert has not yet succeeded
	needBaseline  bool          // resumed without a fresh reading; PressureAtAct and ActedAt await one
	origin        *float64      // the limit before the agent's first step, for the of_original floor
	prev          *Action       // the action as it stood before its latest step
	originRefused bool          // a further step was refused for want of origin, and journalled
}

// Config is what a Loop is built from. Evidence says the runtime really changes
// the system, so an action's outcome is evidence about it: only then do outcomes
// feed the map, the learned effects and the discovery queue. A dry run changes
// nothing, and what pressure did after it is the workload's own doing. Candidates
// is the discovery queue interventions settle; nil settles nothing.
type Config struct {
	Relief     spec.Relief
	NodeID     string
	Runtime    runtime.Runtime
	Policy     *policy.Policy
	Map        *statemap.Map
	Now        func() time.Time
	Logf       func(string, ...any)
	Evidence   bool
	Candidates Candidates
}

// Loop relieves CPU pressure on one node: it notices pressure, caps the
// workload the map says is causing it, checks after Settle that the cap helped,
// undoes it if not, and gives every cap back once pressure is gone. Every step
// is a decision in the map's journal.
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

	effects effects // what acting on each workload has done; safe on its own

	pass       sync.Mutex // serialises the passes that change the system
	seq        int        // decision counter; touched only under pass
	reconciled bool       // Changed has answered; until then nothing is applied. Touched only under pass

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
	noPlanAt    time.Time            // when attribution last found no plan
	noPlanKey   string               // the eligible workloads it found none among
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
// every Relief.Interval until ctx ends. A failed reconcile is retried by every
// tick until it succeeds, and nothing is applied before then.
func (l *Loop) Start(ctx context.Context) {
	if err := l.Reconcile(ctx); err != nil {
		l.cfg.Logf("relief: %v (no change is made until it succeeds)", err)
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
// settled, then act or release as the phase asks. A reading counts only while
// it is fresh: present, active, and younger than the map's StaleAfter, judged
// here rather than left to the next sweep. The trigger is fed the reading's own
// time, so a reading that has stopped changing cannot complete a run by being
// read again. Without a fresh reading the trigger holds and no change is made
// or released, because a decision on a reading that is not current is a
// decision about a system that may no longer exist; only expiry, which needs no
// reading, still happens.
func (l *Loop) Tick(ctx context.Context) {
	l.pass.Lock()
	defer l.pass.Unlock()
	now := l.cfg.Now()
	if !l.reconciled {
		if err := l.reconcile(ctx, now); err != nil {
			l.cfg.Logf("relief: %v (no change is made until it succeeds)", err)
		}
	}
	pressure, at, fresh := l.pressure(now)
	var shares map[string]float64
	if fresh {
		// For a resumed action's baseline, taken below; read outside the state lock,
		// as the loop's other reads of the map are.
		shares = workloadShares(l.cfg.Map)
	}

	l.mu.Lock()
	seen := now
	if fresh {
		seen = at
	}
	prev := l.phase
	l.phase = l.trigger.Observe(seen, pressure, fresh)
	if l.phase != prev {
		l.since = now
		switch l.phase {
		case Relieving:
			l.noted, l.noPlanAt = "", time.Time{}
		case Releasing:
			l.lastRelease = time.Time{}
		}
	}
	if a := l.inFlight; a != nil && a.needBaseline && fresh {
		a.PressureAtAct, a.ActedAt, a.SharesAtAct, a.needBaseline = pressure, now, shares, false
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

	l.verify(ctx, now, pressure, at, fresh, workloads)
	if paused || !fresh {
		return
	}
	if phase == Relieving {
		if l.reconciled {
			l.relieve(ctx, now, pressure, workloads)
		}
		return
	}
	l.release(ctx, now, pressure, phase)
}

// Reconcile takes up the changes the runtime says this node made and has not
// given back, as after a restart. Until it has succeeded once the loop applies
// nothing, because a change made on top of caps it does not know of would
// record one of them as the workload's original.
func (l *Loop) Reconcile(ctx context.Context) error {
	l.pass.Lock()
	defer l.pass.Unlock()
	now := l.cfg.Now()
	if err := l.reconcile(ctx, now); err != nil {
		return err
	}
	l.expire(ctx, now)
	return nil
}

// reconcile is Reconcile under the pass lock. The most recent unexpired change
// becomes the action in flight, judged afresh, with its prediction taken from
// the decision that made it if the journal still holds it; with none, it is
// kept, because there is nothing to judge it against. Its baseline is the
// pressure now if that is fresh, else the first fresh reading after. Older
// unexpired changes are kept, for release. Expired ones are kept in force for
// the caller's expire to give back, so one the runtime will not yet give back is
// retried every tick.
// A change stamped by another node is never touched.
func (l *Loop) reconcile(ctx context.Context, now time.Time) error {
	undos, err := l.cfg.Runtime.Changed(ctx, l.cfg.NodeID)
	if err != nil {
		return fmt.Errorf("relief: listing this node's changes: %w", err)
	}
	pressure, _, fresh := l.pressure(now)
	cooldowns := l.cooldowns(ctx)
	shares := workloadShares(l.cfg.Map)

	l.mu.Lock()
	live := l.liveSubjects()
	busy := l.inFlight != nil
	l.mu.Unlock()

	var expired, resumed []*Action
	for _, u := range undos {
		if u.NodeID != l.cfg.NodeID || live[u.Subject] {
			continue
		}
		a := &Action{ID: u.DecisionID, Subject: u.Subject, DecisionID: u.DecisionID, Undo: u,
			Resumed: true, cooldown: cooldowns(u.Subject), Plan: Plan{Subject: u.Subject, ToCores: u.Applied}}
		if p, first, ok := l.journalledAct(u.DecisionID); ok {
			a.Plan = p
			if first != "" {
				a.ID = first
			}
		}
		a.origin = l.originOf(a)
		if a.ID == "" {
			a.ID = l.id("act")
		}
		if fresh {
			a.PressureAtAct, a.ActedAt, a.SharesAtAct = pressure, now, shares
		} else {
			a.needBaseline = true
		}
		if !now.Before(u.Expires) {
			expired = append(expired, a)
		} else {
			resumed = append(resumed, a)
		}
	}
	sort.SliceStable(resumed, func(i, j int) bool { return resumed[i].Undo.Expires.Before(resumed[j].Undo.Expires) })

	l.mu.Lock()
	for _, a := range expired {
		a.Outcome = OutcomeKept
		l.history = appendHistory(l.history, a)
		l.kept = append(l.kept, a)
	}
	for i, a := range resumed {
		l.history = appendHistory(l.history, a)
		if i == len(resumed)-1 && !busy {
			l.inFlight = a
			continue
		}
		a.Outcome = OutcomeKept
		l.kept = append(l.kept, a)
	}
	l.mu.Unlock()
	l.reconciled = true
	for _, a := range resumed {
		l.cfg.Logf("relief: resumed %s on %s (expires %s)", a.ID, a.Subject, a.Undo.Expires.Format(time.RFC3339))
	}
	return nil
}

// ErrNoAction is Revert's answer for an id that is not an action in force.
var ErrNoAction = errors.New("no such action in force")

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
		return fmt.Errorf("%w: %s", ErrNoAction, id)
	}
	return l.giveBack(ctx, l.cfg.Now(), a, OutcomeManual, "manual", a.cooldown)
}

// pressure is node pressure, when it was read, and whether it is fresh. The
// sweep marks a silent property stale only when it next runs, so the reading's
// age is checked here as well: a source that has just gone quiet leaves a value
// that is active for up to a sweep interval longer and describes nothing now.
func (l *Loop) pressure(now time.Time) (float64, time.Time, bool) {
	p, ok := l.cfg.Map.Property(NodePressure)
	fresh := ok && p.Status == statemap.Active && !p.LastObserved.IsZero() &&
		now.Sub(p.LastObserved) <= l.cfg.Map.StaleAfter()
	return p.Value, p.LastObserved, fresh
}

// relieve takes one step when nothing is in flight. The eligible workloads are
// the actionable ones not cooling down, including one already capped, which is
// stepped again from its current cap. The hourly limit is checked before
// anything is estimated, and attribution that found no plan is not repeated
// until Settle has passed or the eligible workloads change, because each
// estimate is a journalled decision and asking the same question every tick
// would bury the journal in identical answers.
func (l *Loop) relieve(ctx context.Context, now time.Time, pressure float64, workloads func() ([]runtime.Workload, bool)) {
	l.mu.Lock()
	busy := l.inFlight != nil
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
	kept := make(map[string]*Action, len(l.kept))
	for _, a := range l.kept {
		kept[a.Subject] = a
	}
	since, noPlanAt, noPlanKey := l.since, l.noPlanAt, l.noPlanKey
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
	var unanchored []*Action
	for _, w := range ws {
		if cooling[w.Subject] || !actionable(w, pol) {
			continue
		}
		if k := kept[w.Subject]; k != nil && k.origin == nil {
			unanchored = append(unanchored, k)
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
	for _, a := range unanchored {
		l.refuseUnanchored(a)
	}
	if len(open) == 0 {
		if limitedBy != nil {
			l.refuse(fmt.Sprintf("max_actions_per_hour %d reached", limitedBy.Settings.MaxActionsPerHour),
				map[string]any{"rule": limitedBy.Rule, "policy": *limitedBy, "subject": limitedSubject,
					"actions_last_hour": inHour})
		}
		return
	}
	key := eligibleKey(open)
	if !noPlanAt.IsZero() && key == noPlanKey && now.Sub(noPlanAt) < l.c.Settle {
		return
	}
	if _, ok := coreCount(l.cfg.Map); !ok {
		l.refuse(noCores, nil)
		return
	}

	originals := map[string]float64{}
	for s, a := range kept {
		if a.origin != nil {
			originals[s] = *a.origin
		}
	}
	cands := Attribute(l.cfg.Map, open, pol, l.c)
	plan, ok, why := makePlan(l.cfg.Map, cands, open, pol, l.c, l.effects.Of, originals)
	if !ok {
		l.mu.Lock()
		l.noPlanAt, l.noPlanKey = now, key
		l.mu.Unlock()
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
	k := kept[plan.Subject]

	id := l.id("act")
	b := l.cfg.Map.Decide(id, fmt.Sprintf("cap %s's CPU to relieve node pressure?", plan.Subject))
	b.Property(NodePressure)
	b.Property(NodeUsage + "@" + plan.Subject)
	b.Facts()
	shares := workloadShares(l.cfg.Map)
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
	if k != nil {
		b.Input("previous", map[string]any{"act": k.DecisionID, "plan": k.Plan, "action": k.ID})
		b.Note("a further step on the change %s already in force", k.ID)
	}
	b.Note("capping %s from %.3f to %.3f cores; predicted drop in node pressure %.4f", plan.Subject,
		plan.FromCores, plan.ToCores, plan.Predicted)
	if plan.Basis == BasisUsage {
		b.Caveat("%s", usageCaveat)
	}
	b.Commit(map[string]any{"action": runtime.ActionCPULimit, "subject": plan.Subject,
		"to_cores": plan.ToCores, "predicted": plan.Predicted, "expires": undo.Expires})

	l.mu.Lock()
	defer l.mu.Unlock()
	a := k
	if a != nil {
		before := *a
		a.prev = &before
		l.kept = removeAction(l.kept, a)
		l.history = appendHistory(removeAction(l.history, a), a)
		a.DecisionID, a.Plan, a.Undo, a.Observed, a.Outcome = id, plan, undo, nil, ""
		a.PressureAtAct, a.ActedAt, a.cooldown, a.Resumed, a.needBaseline = pressure, now, eff.Settings.Cooldown, false, false
		a.SharesAtAct = shares
	} else {
		origin := plan.FromCores
		if w.CPU.Limit != nil {
			origin = *w.CPU.Limit
		}
		a = &Action{ID: id, Subject: plan.Subject, DecisionID: id, Plan: plan, Undo: undo,
			PressureAtAct: pressure, ActedAt: now, cooldown: eff.Settings.Cooldown, origin: &origin, SharesAtAct: shares}
		if last, ok := l.released[plan.Subject]; ok && now.Sub(last) < backoffWindow {
			l.cycles[plan.Subject]++
		} else {
			l.cycles[plan.Subject] = 0
		}
		l.history = appendHistory(l.history, a)
	}
	l.inFlight = a
	l.acted = append(l.acted, now)
	l.nActs++
	l.noted, l.noPlanAt = "", time.Time{}
}

// eligibleKey names a set of workloads, so attribution can tell whether the
// set it last found no plan among has changed.
func eligibleKey(ws []runtime.Workload) string {
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.Subject
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
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

// liveSubjects is the workloads with a change in force. Caller holds mu.
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

// refuseUnanchored journals, once per action, that a workload whose
// pre-agent limit is not known will not be stepped again: the of_original floor
// would be a share of the agent's own cap, and would slide down with every step
// and every restart. The change itself stays, for verify, release and expiry.
func (l *Loop) refuseUnanchored(a *Action) {
	l.mu.Lock()
	done := a.originRefused
	a.originRefused = true
	if !done {
		l.nRefused++
	}
	l.mu.Unlock()
	if done {
		return
	}
	why := fmt.Sprintf("origin of %s's change is unknown: no original limit was recorded and the journal does not hold its first act %s", a.Subject, a.ID)
	b := l.cfg.Map.Decide(l.id("refused"), fmt.Sprintf("cap %s's CPU further to relieve node pressure?", a.Subject))
	b.Input("action", a.ID)
	b.Input("undo", a.Undo)
	b.Caveat("%s", why)
	b.Commit(map[string]any{"refused": why})
}

// originOf is the limit a resumed action's workload had before the agent first
// touched it, or nil when that is not known for certain. It is the undo's
// original limit when one was set; otherwise the usage the first act recorded,
// read from that act and never from a further step, whose usage is already the
// agent's doing.
func (l *Loop) originOf(a *Action) *float64 {
	if a.Undo.Original.Limit != nil {
		return cloneFloat(a.Undo.Original.Limit)
	}
	if a.ID == "" {
		return nil
	}
	p, first, ok := l.journalledAct(a.ID)
	if !ok || first != "" || p.FromCores <= 0 {
		return nil
	}
	return &p.FromCores
}

// journalledAct reads an act decision: the plan it recorded, so a resumed action
// is judged against what was predicted, and, for a further step, the id of the
// action's first act, which it cites. A snapshot restores inputs as decoded JSON
// rather than as Go values, so both are read.
func (l *Loop) journalledAct(id string) (p Plan, first string, ok bool) {
	if id == "" {
		return Plan{}, "", false
	}
	d, ok := l.cfg.Map.Journal().Decision(id)
	if !ok {
		return Plan{}, "", false
	}
	switch v := d.Inputs["plan"].(type) {
	case Plan:
		p = v
	case map[string]any:
		p.Subject, _ = v["subject"].(string)
		p.Predicted, _ = v["predicted"].(float64)
		p.FromCores, _ = v["from_cores"].(float64)
		p.ToCores, _ = v["to_cores"].(float64)
		p.EstimateID, _ = v["estimate_id"].(string)
		p.Basis, _ = v["basis"].(string)
		p.Effect, _ = v["effect"].(float64)
	}
	if prev, isMap := d.Inputs["previous"].(map[string]any); isMap {
		first, _ = prev["action"].(string)
	}
	return p, first, true
}
