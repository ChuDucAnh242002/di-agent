package relief

import (
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

var unit = [2]float64{0, 1}

// reliefCfg is the spec's defaults, so a test reads against the numbers an
// operator gets.
func reliefCfg() spec.CPURelief { return spec.Default().Loops.Relief.CPU }

// defaultPolicy is the spec's default policy with the hourly limit and
// cooldown a test may want to change.
func defaultPolicy(t *testing.T, edit func(*spec.PolicySpec)) *policy.Policy {
	t.Helper()
	ps := spec.Default().Policy
	if edit != nil {
		edit(&ps)
	}
	p, err := policy.New(ps)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newMap is a map whose properties take the latest reading as their value
// (Alpha 1), so a test that says "pressure falls to 0.44" means the map says so.
func newMap(t *testing.T, now func() time.Time) *statemap.Map {
	t.Helper()
	m := statemap.New(statemap.Config{AdmitUnknown: true, Alpha: 1}, statemap.NewJournal(0))
	m.SetClock(now)
	return m
}

func record(t *testing.T, m *statemap.Map, id, subject string, v float64, at time.Time) {
	t.Helper()
	if err := m.Record(statemap.Observation{ID: id, Value: v, At: at, Subject: subject, Range: &unit}); err != nil {
		t.Fatal(err)
	}
}

func usage(subject string) string { return NodeUsage + "@" + subject }

// relate declares subject's usage as driving node pressure with strength s.
func relate(t *testing.T, m *statemap.Map, subject string, s float64) {
	t.Helper()
	r := statemap.Relationship{From: usage(subject), To: NodePressure, Sign: 1, Label: "drives"}
	if err := m.DeclareRelationship(r); err != nil {
		t.Fatal(err)
	}
	if err := m.AssertRelationshipStrength(statemap.RelationshipID(r.From, r.To, r.Label), s, "test", "fixture"); err != nil {
		t.Fatal(err)
	}
}

func cores(t *testing.T, m *statemap.Map, n float64) {
	t.Helper()
	if err := m.RecordFacts("test", []statemap.Fact{statemap.NumberFact("cpu_cores", n, "cores")}); err != nil {
		t.Fatal(err)
	}
}

func pod(subject string) runtime.Workload {
	return runtime.Workload{Subject: subject, Name: strings.TrimPrefix(subject, "pod:"),
		Can: map[string]string{runtime.ActionCPULimit: ""}}
}

func limited(w runtime.Workload, limit float64) runtime.Workload {
	w.CPU.Limit = &limit
	return w
}

func one(string, string) float64 { return 1 }

// planFixture: node pressure 0.5 and usage 0.8 on 4 cores; pod:a uses 0.4 of
// the node and drives pressure with strength s.
func planFixture(t *testing.T, s float64) *statemap.Map {
	t.Helper()
	m := newMap(t, func() time.Time { return t0 })
	record(t, m, NodePressure, "", 0.5, t0)
	record(t, m, NodeUsage, "", 0.8, t0)
	record(t, m, usage("pod:a"), "pod:a", 0.4, t0)
	relate(t, m, "pod:a", s)
	cores(t, m, 4)
	return m
}

func subjects(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Subject
	}
	return out
}

func TestAVictimIsNotACandidate(t *testing.T) {
	m := planFixture(t, 1)
	record(t, m, usage("pod:v"), "pod:v", 0.02, t0)
	record(t, m, NodePressure+"@pod:v", "pod:v", 0.9, t0)
	cs := Attribute(m, []runtime.Workload{pod("pod:a"), pod("pod:v")}, defaultPolicy(t, nil), reliefCfg())
	for _, c := range cs {
		if c.Subject == "pod:v" {
			t.Fatalf("candidates %v: a workload using 2%% of the node is a victim, not a cause", subjects(cs))
		}
	}
	if len(cs) != 1 {
		t.Fatalf("candidates %v; want pod:a alone", subjects(cs))
	}
}

func TestTheMapRanksBeforeUsage(t *testing.T) {
	m := planFixture(t, 1)
	record(t, m, usage("pod:b"), "pod:b", 0.6, t0)
	cs := Attribute(m, []runtime.Workload{pod("pod:b"), pod("pod:a")}, defaultPolicy(t, nil), reliefCfg())
	if len(cs) != 2 || cs[0].Subject != "pod:a" || cs[1].Subject != "pod:b" {
		t.Fatalf("candidates %v; want pod:a (the map) before pod:b (usage)", subjects(cs))
	}
	if cs[0].Basis != "map" || cs[1].Basis != "usage" {
		t.Errorf("bases %q, %q; want map and usage", cs[0].Basis, cs[1].Basis)
	}
	if !near(cs[0].Drop, 0.4) || !near(cs[0].Share, 0.4) || !near(cs[1].Share, 0.6) {
		t.Errorf("candidates %+v; want pod:a drop 0.4 share 0.4, pod:b share 0.6", cs)
	}
}

func TestAFrozenWorkloadIsSkipped(t *testing.T) {
	m := planFixture(t, 1)
	w := pod("pod:a")
	w.Labels = map[string]string{"di-agent/freeze": "true"}
	if cs := Attribute(m, []runtime.Workload{w}, defaultPolicy(t, nil), reliefCfg()); len(cs) != 0 {
		t.Fatalf("candidates %v; a frozen workload is not one", subjects(cs))
	}
	self := pod("pod:a")
	self.Self = true
	if cs := Attribute(m, []runtime.Workload{self}, defaultPolicy(t, nil), reliefCfg()); len(cs) != 0 {
		t.Fatalf("candidates %v; the agent itself is not one", subjects(cs))
	}
}

func TestAWorkloadThatCannotBeResizedIsSkipped(t *testing.T) {
	m := planFixture(t, 1)
	w := pod("pod:a")
	w.Can[runtime.ActionCPULimit] = "QoS would change"
	if cs := Attribute(m, []runtime.Workload{w}, defaultPolicy(t, nil), reliefCfg()); len(cs) != 0 {
		t.Fatalf("candidates %v; the runtime said it cannot resize this one", subjects(cs))
	}
	w.Can = nil
	if cs := Attribute(m, []runtime.Workload{w}, defaultPolicy(t, nil), reliefCfg()); len(cs) != 0 {
		t.Fatalf("candidates %v; a workload that lists no actions cannot take this one", subjects(cs))
	}
}

func TestTheStepRespectsTheFloors(t *testing.T) {
	m := newMap(t, func() time.Time { return t0 })
	record(t, m, NodePressure, "", 0.5, t0)
	record(t, m, NodeUsage, "", 0.8, t0)
	record(t, m, usage("pod:a"), "pod:a", 0.1, t0)
	relate(t, m, "pod:a", 1)
	cores(t, m, 4)
	ws := []runtime.Workload{limited(pod("pod:a"), 0.5)}
	pol := defaultPolicy(t, nil)
	c := reliefCfg()
	c.MinEffect = 0.01 // a 0.1 share stepped by a quarter predicts 0.025
	p, ok, why := MakePlan(m, Attribute(m, ws, pol, c), ws, pol, c, one)
	if !ok {
		t.Fatalf("no plan: %s", why)
	}
	// 0.1 of 4 cores = 0.4; max(0.4·0.75, 0.1, 0.5·0.5) = 0.3
	if !near(p.FromCores, 0.4) || !near(p.ToCores, 0.3) {
		t.Fatalf("plan %+v; want 0.4 → 0.3 cores", p)
	}
	if p.EstimateID == "" || p.Subject != "pod:a" || p.Rule != -1 {
		t.Errorf("plan %+v; want pod:a, an estimate id and the defaults' rule -1", p)
	}

	// A floor at or above usage leaves no step to take.
	high := defaultPolicy(t, func(ps *spec.PolicySpec) { ps.Defaults.Floor = &spec.Floor{CPUCores: 0.4} })
	if _, ok, why := MakePlan(m, Attribute(m, ws, high, c), ws, high, c, one); ok || !strings.Contains(why, "floor") {
		t.Fatalf("ok=%v why=%q; want refused at the floor", ok, why)
	}
}

func TestAStepThatDoesNotHelpIsNotTaken(t *testing.T) {
	// share 0.4, step to 0.3: delta 0.1·0.1 = 0.01 < min_effect 0.03
	m := planFixture(t, 0.1)
	ws := []runtime.Workload{pod("pod:a")}
	pol := defaultPolicy(t, nil)
	c := reliefCfg()
	_, ok, why := MakePlan(m, Attribute(m, ws, pol, c), ws, pol, c, one)
	if ok || !strings.Contains(why, "min_effect") {
		t.Fatalf("ok=%v why=%q; want refused below min_effect", ok, why)
	}
}

func TestAUsageCandidateIsPredictedFromItsShare(t *testing.T) {
	m := planFixture(t, 1)
	record(t, m, usage("pod:b"), "pod:b", 0.6, t0)
	ws := []runtime.Workload{pod("pod:b")}
	pol := defaultPolicy(t, nil)
	c := reliefCfg()
	p, ok, why := MakePlan(m, Attribute(m, ws, pol, c), ws, pol, c, one)
	if !ok {
		t.Fatalf("no plan: %s", why)
	}
	// 0.6 · 0.25 · (0.5 / 0.8)
	if !near(p.Predicted, 0.6*0.25*0.5/0.8) || p.Basis != "usage" || p.EstimateID != "" {
		t.Fatalf("plan %+v; want a usage prediction with no estimate", p)
	}
}

func TestNoCoresNoPlan(t *testing.T) {
	m := newMap(t, func() time.Time { return t0 })
	record(t, m, NodePressure, "", 0.5, t0)
	record(t, m, NodeUsage, "", 0.8, t0)
	record(t, m, usage("pod:a"), "pod:a", 0.4, t0)
	relate(t, m, "pod:a", 1)
	ws := []runtime.Workload{pod("pod:a")}
	pol := defaultPolicy(t, nil)
	c := reliefCfg()
	_, ok, why := MakePlan(m, Attribute(m, ws, pol, c), ws, pol, c, one)
	if ok || why != "no cpu_cores fact: shares cannot become cores" {
		t.Fatalf("ok=%v why=%q", ok, why)
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

// TestOnlyItsOwnUsageRanksAWorkloadByTheMap: a relationship from another of the
// workload's properties says nothing about what capping its CPU would do, which
// is what the plan predicts by assuming a new cpu_utilization@<subject>.
func TestOnlyItsOwnUsageRanksAWorkloadByTheMap(t *testing.T) {
	m := newMap(t, func() time.Time { return t0 })
	record(t, m, NodePressure, "", 0.5, t0)
	record(t, m, NodeUsage, "", 0.8, t0)
	record(t, m, usage("pod:q"), "pod:q", 0.4, t0)
	record(t, m, "queue@pod:q", "pod:q", 0.9, t0)
	r := statemap.Relationship{From: "queue@pod:q", To: NodePressure, Sign: 1, Label: "drives"}
	if err := m.DeclareRelationship(r); err != nil {
		t.Fatal(err)
	}
	if err := m.AssertRelationshipStrength(statemap.RelationshipID(r.From, r.To, r.Label), 1, "test", "fixture"); err != nil {
		t.Fatal(err)
	}
	cs := Attribute(m, []runtime.Workload{pod("pod:q")}, defaultPolicy(t, nil), reliefCfg())
	if len(cs) != 1 || cs[0].Basis != BasisUsage {
		t.Fatalf("candidates %+v; want pod:q ranked on usage", cs)
	}
}

func TestTheStepNeverGoesBelowTheRequest(t *testing.T) {
	m := planFixture(t, 1)
	w := limited(pod("pod:a"), 2)
	req := 1.4
	w.CPU.Request = &req
	ws := []runtime.Workload{w}
	pol := defaultPolicy(t, nil)
	c := reliefCfg()
	c.MinEffect = 0.001
	p, ok, why := MakePlan(m, Attribute(m, ws, pol, c), ws, pol, c, one)
	if !ok {
		t.Fatalf("no plan: %s", why)
	}
	// 0.4 of 4 cores = 1.6; the step alone would reach 1.2, but the request is 1.4.
	if !near(p.FromCores, 1.6) || !near(p.ToCores, 1.4) {
		t.Fatalf("plan %+v; want 1.6 → 1.4 cores", p)
	}
}
