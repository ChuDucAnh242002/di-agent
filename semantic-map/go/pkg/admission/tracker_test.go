package admission

import (
	"context"
	"fmt"
	"maps"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

// rig is a tracker over a node map with a fake clock, a fake workload list and fakes
// of relief's Shares and Moved that read the map directly.
type rig struct {
	t     *testing.T
	m     *statemap.Map
	j     *statemap.Journal
	tr    *Tracker
	now   time.Time
	wls   map[string]string
	acted bool
	logs  []string
	mu    sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	m, j := node(t, 0.4, 0.02)
	r := &rig{t: t, m: m, j: j, now: t0, wls: map[string]string{}}
	shares := func(usage string) map[string]float64 {
		out := map[string]float64{}
		for _, s := range m.Subjects() {
			if p, ok := m.Property(usage + "@" + s); ok && p.Status != statemap.Retired {
				out[s] = p.Value
			}
		}
		return out
	}
	r.tr = NewTracker(TrackerConfig{
		Map: m,
		Workloads: func(context.Context) (map[string]string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return maps.Clone(r.wls), nil
		},
		Shares: shares,
		Moved: func(usage string, at map[string]float64, except string) []string {
			var out []string
			for s, v := range shares(usage) {
				if s != except && math.Abs(v-at[s]) > 0.02 {
					out = append(out, s)
				}
			}
			return out
		},
		Acted: func(string, time.Time) bool { return r.acted },
		IDs:   NewIDs(t0),
		Now:   func() time.Time { return r.now },
		Logf: func(f string, a ...any) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.logs = append(r.logs, fmt.Sprintf(f, a...))
		},
	})
	return r
}

// accept registers an accept for name: cpu at pressure 0.02 with map prediction
// 0.095 and usage prediction 0.05, memory at 0 predicted 0.01 on the usage basis.
func (r *rig) accept(name string) Decision {
	r.t.Helper()
	mp, up, mu := 0.095, 0.05, 0.01
	d := Decision{ID: "admit-1-" + name, Verdict: Accept, Lines: []Line{
		{Domain: cpuDom.Relieves, Basis: "map", Pressure: 0.02, MapPred: &mp, UsagePred: &up, Pass: true},
		{Domain: memDom.Relieves, Basis: "usage", Pressure: 0, UsagePred: &mu, Pass: true},
	}}
	if err := r.tr.Accepted(Offer{Name: name}, d, both); err != nil {
		r.t.Fatal(err)
	}
	return d
}

// arrive lists name as pod:<name> and records its usage on both domains.
func (r *rig) arrive(name string) {
	r.mu.Lock()
	r.wls[name] = "pod:" + name
	r.mu.Unlock()
	record(r.t, r.m, "cpu_utilization@pod:"+name, "pod:"+name, 0.1)
	record(r.t, r.m, "memory_utilization@pod:"+name, "pod:"+name, 0.05)
}

func (r *rig) tick(after time.Duration) {
	r.now = r.now.Add(after)
	r.tr.Tick(context.Background())
}

func (r *rig) only() Outcome {
	r.t.Helper()
	out := r.tr.Outcomes()
	if len(out) != 1 {
		r.t.Fatalf("outcomes %+v; want one", out)
	}
	return out[0]
}

func (r *rig) result(want string) Outcome {
	r.t.Helper()
	o := r.only()
	if o.Result != want {
		r.t.Fatalf("result %q; want %q (%+v)", o.Result, want, o)
	}
	return o
}

// arrived runs an accept through arrival and the snapshot.
func arrived(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.accept("web")
	r.arrive("web")
	r.tick(10 * time.Second)
	if len(r.tr.Outcomes()) != 0 || !r.tr.Pending("web") {
		t.Fatal("judged before settle")
	}
	return r
}

func TestHeld(t *testing.T) {
	r := arrived(t)
	record(t, r.m, "cpu_contention_ratio", "", 0.07)
	r.tick(30 * time.Second)
	if len(r.tr.Outcomes()) != 0 {
		t.Fatal("judged before settle elapsed")
	}
	r.tick(30 * time.Second)
	o := r.result("held")
	if o.Subject != "pod:web" || o.Name != "web" || r.tr.Pending("web") {
		t.Fatalf("outcome %+v pending %v", o, r.tr.Pending("web"))
	}
}

func TestBreached(t *testing.T) {
	r := arrived(t)
	record(t, r.m, "cpu_contention_ratio", "", 0.26)
	r.tick(time.Minute)
	o := r.result("breached")
	if !o.Lines[0].Breached || o.Lines[1].Breached {
		t.Fatalf("lines %+v; want cpu breached only", o.Lines)
	}
}

func TestConfoundedByOtherWorkload(t *testing.T) {
	r := arrived(t)
	record(t, r.m, "cpu_utilization@pod:a", "pod:a", 0.4)
	record(t, r.m, "cpu_contention_ratio", "", 0.26)
	r.tick(time.Minute)
	o := r.result("confounded")
	for _, l := range o.Lines {
		if l.MapRise != nil || l.MapErr != nil || l.UsageErr != nil {
			t.Fatalf("confounded line scored: %+v", l)
		}
	}
}

func TestConfoundedByRelief(t *testing.T) {
	r := arrived(t)
	r.acted = true
	r.tick(time.Minute)
	r.result("confounded")
}

func TestNotArrived(t *testing.T) {
	r := newRig(t)
	r.accept("web")
	r.tick(ArrivalWindow)
	if len(r.tr.Outcomes()) != 0 {
		t.Fatal("expired at the window's edge")
	}
	r.tick(time.Second)
	r.result("not_arrived")
	if r.tr.Pending("web") {
		t.Fatal("still pending")
	}
}

func TestWaitsForUsageOnEveryDomain(t *testing.T) {
	r := newRig(t)
	r.accept("web")
	r.mu.Lock()
	r.wls["web"] = "pod:web"
	r.mu.Unlock()
	record(t, r.m, "cpu_utilization@pod:web", "pod:web", 0.1)
	r.tick(2 * time.Minute)
	r.tick(2 * time.Minute)
	if len(r.tr.Outcomes()) != 0 {
		t.Fatal("judged without memory usage")
	}
	r.tick(time.Minute + time.Second)
	r.result("not_arrived")
}

func TestPendingCapDropsOldest(t *testing.T) {
	r := newRig(t)
	for i := 0; i <= PendingCap; i++ {
		r.accept(fmt.Sprintf("w%d", i))
	}
	if r.tr.Pending("w0") || !r.tr.Pending("w1") || !r.tr.Pending(fmt.Sprintf("w%d", PendingCap)) {
		t.Fatal("cap did not drop the oldest")
	}
	if len(r.logs) != 1 || !strings.Contains(r.logs[0], "pending cap 32 reached, dropped w0") {
		t.Fatalf("logs %q", r.logs)
	}
}

func TestAlreadyPending(t *testing.T) {
	r := newRig(t)
	r.accept("web")
	err := r.tr.Accepted(Offer{Name: "web"}, Decision{ID: "x", Verdict: Accept}, both)
	if err == nil || err.Error() != "already pending" {
		t.Fatalf("err %v; want already pending", err)
	}
}

// TestAcceptedConcurrent: many goroutines racing the same name admit exactly one.
func TestAcceptedConcurrent(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.tr.Accepted(Offer{Name: "web"}, Decision{ID: "x", Verdict: Accept}, both) == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d accepts registered; want 1", ok)
	}
}

func TestOutcomeLinksAccept(t *testing.T) {
	r := arrived(t)
	r.tick(time.Minute)
	o := r.result("held")
	if o.AcceptID != "admit-1-web" || !strings.HasPrefix(o.OutcomeID, fmt.Sprintf("admit-%d-", t0.Unix())) {
		t.Fatalf("ids %q %q", o.AcceptID, o.OutcomeID)
	}
	rec, ok := r.j.Decision(o.OutcomeID)
	if !ok {
		t.Fatal("outcome decision not recorded")
	}
	if rec.Inputs["accept"] != o.AcceptID || rec.Answer["outcome"] != "held" {
		t.Fatalf("record inputs %v answer %v", rec.Inputs, rec.Answer)
	}
}

func TestRisesAndErrors(t *testing.T) {
	r := arrived(t)
	record(t, r.m, "cpu_contention_ratio", "", 0.07)
	r.tick(time.Minute)
	o := r.result("held")
	c := o.Lines[0]
	if c.Domain != cpuDom.Relieves || !near(c.ObservedRise, 0.05) ||
		!ptrNear(c.MapRise, 0.075) || !ptrNear(c.MapErr, 0.025) ||
		!ptrNear(c.UsageRise, 0.03) || !ptrNear(c.UsageErr, -0.02) {
		t.Fatalf("cpu line %+v", c)
	}
	mem := o.Lines[1]
	if mem.MapRise != nil || mem.MapErr != nil || !ptrNear(mem.UsageErr, 0.01) {
		t.Fatalf("memory line %+v", mem)
	}
}

func TestOutcomesNewestFirst(t *testing.T) {
	r := newRig(t)
	r.accept("a1")
	r.tick(time.Minute)
	r.accept("a2")
	r.tick(5 * time.Minute)
	r.tick(time.Second)
	out := r.tr.Outcomes()
	if len(out) != 2 || out[0].Name != "a2" || out[1].Name != "a1" {
		t.Fatalf("outcomes %+v", out)
	}
}
