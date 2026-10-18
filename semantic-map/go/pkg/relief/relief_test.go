package relief

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

const tick = 15 * time.Second

// harness is a loop on a dry-run runtime, a map and a clock the test moves.
// Node usage is 0.8 on 4 cores; each pod named is given a share and a
// relationship of strength 1 to node pressure, so pod:a at 0.4 is predicted to
// cut pressure by 0.1 when stepped to 0.3.
type harness struct {
	t   *testing.T
	now time.Time
	m   *statemap.Map
	rt  *runtime.DryRun
	l   *Loop
	ctx context.Context

	mu sync.Mutex // guards now against readers on other goroutines; written only by the test
}

type fixture struct {
	shares map[string]float64
	noFact bool
	policy func(*spec.PolicySpec)
}

func newHarness(t *testing.T, f fixture) *harness {
	t.Helper()
	h := &harness{t: t, now: t0, ctx: context.Background()}
	h.m = newMap(t, h.clock)
	record(t, h.m, NodeUsage, "", 0.8, h.now)
	record(t, h.m, NodePressure, "", 0, h.now)
	if !f.noFact {
		cores(t, h.m, 4)
	}
	rt, err := runtime.Build("dry-run", runtime.Env{NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	h.rt = rt.(*runtime.DryRun)
	for s, share := range f.shares {
		record(t, h.m, usage(s), s, share, h.now)
		relate(t, h.m, s, 1)
		h.rt.Put(pod(s))
	}
	relief := spec.Default().Loops.Relief
	relief.Enabled = true
	l, err := New(Config{
		Relief: relief, NodeID: "n1", Runtime: h.rt, Policy: defaultPolicy(t, f.policy), Map: h.m,
		Now: h.clock, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.l = l
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

// step moves the clock one interval, reads pressure p, and ticks.
func (h *harness) step(p float64) {
	h.t.Helper()
	h.advance(tick)
	record(h.t, h.m, NodePressure, "", p, h.now)
	h.l.Tick(h.ctx)
}

// run steps at pressure p for d.
func (h *harness) run(d time.Duration, p float64) {
	h.t.Helper()
	for i := time.Duration(0); i < d; i += tick {
		h.step(p)
	}
}

// until steps at pressure p until cond holds, failing after limit.
func (h *harness) until(limit time.Duration, p float64, what string, cond func() bool) {
	h.t.Helper()
	for i := time.Duration(0); i < limit; i += tick {
		h.step(p)
		if cond() {
			return
		}
	}
	h.t.Fatalf("after %s at pressure %v: %s did not happen", limit, p, what)
}

func (h *harness) applied() map[string]runtime.Undo {
	out := map[string]runtime.Undo{}
	for _, u := range h.rt.Applied() {
		out[u.Subject] = u
	}
	return out
}

func (h *harness) limit(subject string) *float64 {
	ws, _ := h.rt.Workloads(h.ctx)
	for _, w := range ws {
		if w.Subject == subject {
			return w.CPU.Limit
		}
	}
	h.t.Fatalf("no workload %s", subject)
	return nil
}

// act steps at 0.5 until a change is applied to subject.
func (h *harness) act(subject string) {
	h.t.Helper()
	h.until(2*time.Hour, 0.5, "an action on "+subject, func() bool { _, ok := h.applied()[subject]; return ok })
}

func (h *harness) action(subject string) Action {
	h.t.Helper()
	for _, a := range h.l.Actions() {
		if a.Subject == subject {
			return a
		}
	}
	h.t.Fatalf("no action on %s in %+v", subject, h.l.Actions())
	return Action{}
}

func (h *harness) decisions(kind string) []*statemap.Decision {
	var out []*statemap.Decision
	for _, d := range h.m.Journal().Decisions(0) {
		if strings.HasPrefix(d.ID, "relief-"+kind+"-") {
			out = append(out, d)
		}
	}
	return out
}

func TestItActsOnceUnderPressure(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3}})
	h.run(time.Minute, 0.5)
	if n := len(h.rt.Applied()); n != 0 {
		t.Fatalf("%d changes before pressure lasted enter_for", n)
	}
	h.step(0.5)
	if n := len(h.rt.Applied()); n != 1 {
		t.Fatalf("%d changes after a minute of pressure; want 1", n)
	}
	u := h.applied()["pod:a"]
	if !near(u.Applied, 1.2) || !u.Expires.Equal(h.now.Add(time.Hour)) {
		t.Errorf("undo %+v; want pod:a capped at 1.2 cores, expiring in an hour", u)
	}
	h.step(0.5)
	st := h.l.Status()
	if n := len(h.rt.Applied()); n != 1 || st.Actions != 1 || st.InFlight == nil {
		t.Fatalf("applied %d, status %+v; want one action, in flight", n, st)
	}
	if st.Phase != Relieving {
		t.Errorf("phase %s", st.Phase)
	}
}

func TestAnActionThatHelpedIsKept(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	h.act("pod:a")
	if p := h.l.Status().InFlight.Plan.Predicted; !near(p, 0.1) {
		t.Fatalf("predicted %v; want 0.1", p)
	}
	h.run(2*time.Minute, 0.44)
	a := h.action("pod:a")
	if a.Outcome != "kept" || a.Observed == nil || !near(*a.Observed, 0.06) {
		t.Fatalf("action %+v; want kept with 0.06 observed", a)
	}
	if l := h.limit("pod:a"); l == nil || !near(*l, 1.2) {
		t.Fatalf("limit %v; want the cap kept at 1.2", l)
	}
	st := h.l.Status()
	if st.InFlight != nil || st.Kept != 1 || st.PredictionsHeld != 1 {
		t.Errorf("status %+v; want nothing in flight, one kept, predictions held 1", st)
	}
}

func TestAnActionThatDidNotHelpIsReverted(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	h.act("pod:a")
	h.run(2*time.Minute, 0.5)
	a := h.action("pod:a")
	if a.Outcome != "reverted" {
		t.Fatalf("action %+v; want reverted", a)
	}
	if l := h.limit("pod:a"); l != nil {
		t.Fatalf("limit %v; want no limit, as before", *l)
	}
	revertedAt := h.now
	if until := h.l.Status().Cooling["pod:a"]; !until.Equal(revertedAt.Add(40 * time.Minute)) {
		t.Fatalf("cooling until %v; want 40m after the revert", until)
	}
	h.run(40*time.Minute-tick, 0.5)
	if n := len(h.rt.Applied()); n != 0 {
		t.Fatalf("%d changes while cooling down", n)
	}
	h.step(0.5)
	if n := len(h.rt.Applied()); n != 1 {
		t.Fatalf("%d changes once the cooldown ended; want 1", n)
	}
	if st := h.l.Status(); st.Reverted != 1 || st.PredictionsHeld != 0 {
		t.Errorf("status %+v", st)
	}
}

func TestReleaseRevertsLastFirstAndStopsOnReturn(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3}})
	h.act("pod:a")
	h.run(2*time.Minute, 0.44)
	h.until(time.Minute, 0.44, "an action on pod:b", func() bool { _, ok := h.applied()["pod:b"]; return ok })
	h.run(2*time.Minute, 0.38)
	if a, b := h.action("pod:a"), h.action("pod:b"); a.Outcome != "kept" || b.Outcome != "kept" {
		t.Fatalf("outcomes %q, %q; want both kept", a.Outcome, b.Outcome)
	}
	h.until(4*time.Minute, 0.05, "a release", func() bool { return len(h.rt.Applied()) < 2 })
	if h.l.Status().Phase != Releasing {
		t.Fatalf("phase %s", h.l.Status().Phase)
	}
	if _, ok := h.applied()["pod:a"]; !ok || h.action("pod:b").Outcome != "released" {
		t.Fatalf("applied %v, pod:b %+v; want the newest (pod:b) released first", h.applied(), h.action("pod:b"))
	}
	if n := len(h.decisions("release")); n != 1 {
		t.Fatalf("%d release decisions", n)
	}
	h.run(time.Minute+tick, 0.4)
	if h.l.Status().Phase != Relieving {
		t.Fatalf("phase %s; want relieving again", h.l.Status().Phase)
	}
	h.run(5*time.Minute, 0.4)
	if _, ok := h.applied()["pod:a"]; !ok {
		t.Fatalf("pod:a was released after pressure returned")
	}
}

func TestRepeatedThrottlingBacksOff(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	for i, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute} {
		h.act("pod:a")
		h.run(2*time.Minute, 0.44)
		h.until(5*time.Minute, 0.05, "a release", func() bool { return len(h.rt.Applied()) == 0 })
		if got := h.l.Status().Cooling["pod:a"].Sub(h.now); got != want {
			t.Fatalf("cycle %d: cooldown %s; want %s", i+1, got, want)
		}
	}
	if got := backoff(10*time.Minute, 5); got != 4*time.Hour {
		t.Errorf("backoff after five cycles %s; want capped at 4h", got)
	}
}

func TestAnExpiredChangeIsRevertedInAnyPhase(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	h.act("pod:a")
	acted := h.now
	h.run(2*time.Minute, 0.44)
	h.until(time.Hour, 0.44, "expiry", func() bool { return len(h.rt.Applied()) == 0 })
	if !h.now.Equal(acted.Add(time.Hour)) {
		t.Fatalf("reverted at %v; want an hour after %v", h.now, acted)
	}
	if a := h.action("pod:a"); a.Outcome != "expired" || h.limit("pod:a") != nil {
		t.Fatalf("action %+v; want expired with the limit given back", a)
	}
	if n := len(h.decisions("expired")); n != 1 {
		t.Fatalf("%d expiry decisions", n)
	}
}

func TestReconcileResumesAndRevertsExpired(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3, "pod:c": 0.2}})
	record(t, h.m, NodePressure, "", 0.5, h.now)
	b := h.m.Decide("relief-act-1-1", "earlier act")
	b.Input("plan", Plan{Subject: "pod:a", Predicted: 0.1})
	b.Commit(nil)
	h.rt.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "n1", Subject: "pod:a", Action: runtime.ActionCPULimit,
		Applied: 1.2, Expires: h.now.Add(30 * time.Minute), DecisionID: "relief-act-1-1"})
	h.rt.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "n1", Subject: "pod:b", Action: runtime.ActionCPULimit,
		Applied: 0.9, Expires: h.now.Add(-time.Minute), DecisionID: "relief-act-1-2"})
	h.rt.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "other", Subject: "pod:c", Action: runtime.ActionCPULimit,
		Applied: 0.6, Expires: h.now.Add(-time.Minute), DecisionID: "theirs"})

	if err := h.l.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	st := h.l.Status()
	if st.InFlight == nil || st.InFlight.Subject != "pod:a" || !near(st.InFlight.Plan.Predicted, 0.1) ||
		!st.InFlight.ActedAt.Equal(h.now) || !near(st.InFlight.PressureAtAct, 0.5) {
		t.Fatalf("in flight %+v; want pod:a resumed with its journalled prediction", st.InFlight)
	}
	ap := h.applied()
	if _, ok := ap["pod:b"]; ok {
		t.Error("the expired change on pod:b was not reverted")
	}
	if _, ok := ap["pod:c"]; !ok {
		t.Error("another node's change was reverted")
	}
	if a := h.action("pod:b"); a.Outcome != "expired" {
		t.Errorf("pod:b %+v; want expired", a)
	}
}

func TestReconcileKeepsFurtherChangesForRelease(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3}})
	record(t, h.m, NodePressure, "", 0.5, h.now)
	h.rt.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "n1", Subject: "pod:a", Action: runtime.ActionCPULimit,
		Applied: 1.2, Expires: h.now.Add(20 * time.Minute), DecisionID: "relief-act-1-1"})
	h.rt.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "n1", Subject: "pod:b", Action: runtime.ActionCPULimit,
		Applied: 0.9, Expires: h.now.Add(30 * time.Minute), DecisionID: "relief-act-1-2"})
	if err := h.l.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	st := h.l.Status()
	if st.InFlight == nil || st.InFlight.Subject != "pod:b" || st.InFlight.Plan.Predicted != 0 {
		t.Fatalf("in flight %+v; want the most recent change, pod:b, with no prediction", st.InFlight)
	}
	if a := h.action("pod:a"); a.Outcome != "kept" {
		t.Fatalf("pod:a %+v; want kept, for release", a)
	}
	// With no prediction to judge against, verify keeps it.
	h.run(2*time.Minute, 0.6)
	if a := h.action("pod:b"); a.Outcome != "kept" {
		t.Fatalf("pod:b %+v; want kept", a)
	}
}

func TestPausedItDoesNotAct(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3}})
	record(t, h.m, NodePressure, "", 0.5, h.now)
	h.rt.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "n1", Subject: "pod:b", Action: runtime.ActionCPULimit,
		Applied: 0.9, Expires: h.now.Add(3 * time.Minute), DecisionID: "relief-act-1-1"})
	if err := h.l.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.l.Pause()
	if !h.l.Status().Paused {
		t.Fatal("not paused")
	}
	h.run(5*time.Minute, 0.5)
	if n := len(h.rt.Applied()); n != 0 {
		t.Fatalf("%d changes while paused; want none, and the resumed one expired", n)
	}
	if h.l.Status().Phase != Relieving {
		t.Errorf("phase %s; the trigger still runs while paused", h.l.Status().Phase)
	}
	if a := h.action("pod:b"); a.Outcome != "expired" {
		t.Errorf("pod:b %+v; want expired while paused", a)
	}
	h.l.Resume()
	h.step(0.5)
	if _, ok := h.applied()["pod:a"]; !ok {
		t.Fatalf("applied %v after resume; want pod:a", h.applied())
	}
}

func TestAGoneWorkloadFreesTheSlot(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3}})
	h.act("pod:a")
	h.rt.Remove("pod:a")
	h.run(2*time.Minute, 0.5)
	if a := h.action("pod:a"); a.Outcome != "gone" {
		t.Fatalf("pod:a %+v; want gone", a)
	}
	if f := h.l.Status().InFlight; f != nil && f.Subject == "pod:a" {
		t.Fatal("the slot is still held by a gone workload")
	}
	if n := len(h.decisions("gone")); n != 1 {
		t.Fatalf("%d gone decisions", n)
	}
	h.step(0.5)
	if _, ok := h.applied()["pod:b"]; !ok {
		t.Fatalf("applied %v; want the next action on pod:b", h.applied())
	}

	// A kept change whose workload is removed is dropped at expiry, once.
	h.run(2*time.Minute, 0.42)
	h.rt.Remove("pod:b")
	h.run(time.Hour, 0.42)
	if a := h.action("pod:b"); a.Outcome != "gone" {
		t.Fatalf("pod:b %+v; want gone at expiry", a)
	}
	n := len(h.m.Journal().Decisions(0))
	h.run(10*time.Minute, 0.42)
	if got := len(h.m.Journal().Decisions(0)); got != n || len(h.decisions("gone")) != 2 {
		t.Fatalf("decisions %d → %d; a gone workload must not be retried", n, got)
	}
}

func TestTheRateLimitHolds(t *testing.T) {
	h := newHarness(t, fixture{
		shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3},
		policy: func(ps *spec.PolicySpec) { ps.Defaults.MaxActionsPerHour = 1 },
	})
	h.act("pod:a")
	h.run(2*time.Minute, 0.44)
	h.run(5*time.Minute, 0.44)
	if n := len(h.rt.Applied()); n != 1 {
		t.Fatalf("%d changes; want the hourly limit of 1 to hold", n)
	}
	refused := h.decisions("refused")
	if len(refused) != 1 {
		t.Fatalf("%d refusals; want one, not one per tick", len(refused))
	}
	if r, ok := refused[0].Inputs["rule"]; !ok || r != -1 {
		t.Fatalf("refusal inputs %v; want the rule that refused", refused[0].Inputs)
	}
	if !strings.Contains(refused[0].Answer["refused"].(string), "max_actions_per_hour") {
		t.Errorf("refusal %v", refused[0].Answer)
	}
}

func TestEveryStepIsADecision(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	h.act("pod:a")
	h.run(2*time.Minute, 0.44)
	a := h.action("pod:a")
	if !strings.HasPrefix(a.DecisionID, "relief-act-") || a.ID != a.DecisionID {
		t.Fatalf("action %+v; want its id to be its act decision", a)
	}
	act, ok := h.m.Journal().Decision(a.DecisionID)
	if !ok {
		t.Fatalf("no decision %s", a.DecisionID)
	}
	for _, in := range []string{"notice", "attribution", "plan", "policy"} {
		if _, ok := act.Inputs[in]; !ok {
			t.Errorf("act decision inputs %v; missing %s", act.Inputs, in)
		}
	}
	if plan, _ := act.Inputs["plan"].(Plan); plan.EstimateID == "" {
		t.Errorf("plan %+v; want the estimate's decision id", act.Inputs["plan"])
	}
	if a.Undo.DecisionID != a.DecisionID {
		t.Errorf("undo %+v; want it stamped with the act decision", a.Undo)
	}
	verify := h.decisions("verify")
	if len(verify) != 1 || verify[0].Inputs["act"] != a.DecisionID {
		t.Fatalf("verify decisions %+v; want one citing %s", verify, a.DecisionID)
	}
}

func TestNoCoresTheLoopRefusesOnce(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}, noFact: true})
	h.run(5*time.Minute, 0.5)
	if n := len(h.rt.Applied()); n != 0 {
		t.Fatalf("%d changes without a cpu_cores fact", n)
	}
	refused := h.decisions("refused")
	if len(refused) != 1 || refused[0].Answer["refused"] != "no cpu_cores fact: shares cannot become cores" {
		t.Fatalf("refusals %+v; want one naming the missing fact", refused)
	}
}

func TestStalePressureTakesNoAction(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	h.run(50*time.Second, 0.5)
	// The source goes quiet: no readings, and the map marks pressure stale.
	h.advance(3 * time.Minute)
	h.m.Sweep()
	if p, _ := h.m.Property(NodePressure); p.Status == statemap.Active {
		t.Fatal("setup: pressure still active")
	}
	for i := 0; i < 20; i++ {
		h.advance(tick)
		h.l.Tick(h.ctx)
	}
	if n := len(h.rt.Applied()); n != 0 || h.l.Status().Phase != Calm {
		t.Fatalf("applied %d, phase %s; want no action on stale pressure", n, h.l.Status().Phase)
	}
}

func TestAManualRevertCoolsDown(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4}})
	h.act("pod:a")
	id := h.l.Status().InFlight.ID
	if err := h.l.Revert(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	if a := h.action("pod:a"); a.Outcome != "manual" || h.limit("pod:a") != nil {
		t.Fatalf("pod:a %+v; want reverted by hand", a)
	}
	if until := h.l.Status().Cooling["pod:a"]; !until.Equal(h.now.Add(10 * time.Minute)) {
		t.Fatalf("cooling until %v", until)
	}
	if err := h.l.Revert(h.ctx, id); err == nil {
		t.Fatal("a second revert of the same action succeeded")
	}
	if n := len(h.decisions("manual")); n != 1 {
		t.Fatalf("%d manual decisions", n)
	}
}

// TestReadersDoNotRaceTheLoop: Status, Actions, Pause and Resume are called from
// HTTP handlers while the ticker runs passes.
func TestReadersDoNotRaceTheLoop(t *testing.T) {
	h := newHarness(t, fixture{shares: map[string]float64{"pod:a": 0.4, "pod:b": 0.3}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = h.l.Status()
			_ = h.l.Actions()
			if i%50 == 0 {
				h.l.Pause()
				h.l.Resume()
			}
		}
	}()
	for i := 0; i < 40; i++ {
		h.step(0.5)
	}
	<-done
}
