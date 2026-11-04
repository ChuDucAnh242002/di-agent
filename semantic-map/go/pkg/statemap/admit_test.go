package statemap

import (
	"strings"
	"testing"
	"time"
)

// admitFixture: a 4-core, 8 GiB node at 40% CPU and 50% memory, with neither kind
// of contention, and the default relief levers' enter levels.
func admitFixture(t *testing.T) (*Map, *clock) {
	t.Helper()
	m, c := newTestMap(t, Config{AdmitUnknown: true, StaleAfter: time.Minute})
	if err := m.RecordFacts("test", []Fact{NumberFact("cpu_cores", 4, "cores"), NumberFact("memory_total", 8<<30, "bytes")}); err != nil {
		t.Fatal(err)
	}
	for id, v := range map[string]float64{
		"cpu_utilization": 0.4, "memory_utilization": 0.5,
		"cpu_contention_ratio": 0.05, "memory_contention_ratio": 0.01,
	} {
		if err := m.Record(Observation{ID: id, Value: v, At: c.now()}); err != nil {
			t.Fatal(err)
		}
	}
	return m, c
}

func admitReq(cpu, mem float64) AdmitRequest {
	return AdmitRequest{CPUCores: cpu, MemoryBytes: mem, CPUEnter: 0.25, MemoryEnter: 0.08}
}

func hasCaveat(cs []string, sub string) bool {
	for _, c := range cs {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestAdmitAcceptsWithHeadroom(t *testing.T) {
	m, _ := admitFixture(t)
	res := m.Admit(admitReq(1, 1<<30))
	if res.Err != "" || !res.Accepted {
		t.Fatalf("%+v; want accepted", res)
	}
	// 0.4 + 1/4 = 0.65 ; 0.5 + 1/8 = 0.625
	if !near(res.CPUAfter, 0.65) || !near(res.MemoryAfter, 0.625) || res.Basis != BasisDeclared {
		t.Errorf("cpu_after=%.3f memory_after=%.3f basis=%s; want 0.65, 0.625, declared", res.CPUAfter, res.MemoryAfter, res.Basis)
	}
	if !hasCaveat(res.Caveats, "declared by the requester") {
		t.Errorf("caveats %v; a declared demand must say it was not observed", res.Caveats)
	}
	if !strings.HasPrefix(res.DecisionID, "adm-") {
		t.Errorf("decision id %q; want adm-<revision>-<digest>", res.DecisionID)
	}
}

func TestAdmitDeclinesOnCPUHeadroom(t *testing.T) {
	m, _ := admitFixture(t)
	res := m.Admit(admitReq(2.5, 0)) // 0.4 + 0.625 > 0.9
	if res.Accepted || !strings.Contains(res.Reason, "cpu") {
		t.Errorf("%+v; want declined on cpu", res)
	}
}

func TestAdmitDeclinesOnMemoryHeadroom(t *testing.T) {
	m, _ := admitFixture(t)
	res := m.Admit(admitReq(0, 4<<30)) // 0.5 + 0.5 > 0.9
	if res.Accepted || !strings.Contains(res.Reason, "memory") {
		t.Errorf("%+v; want declined on memory", res)
	}
}

func TestAdmitDeclinesWhenContended(t *testing.T) {
	m, c := admitFixture(t)
	for range 5 {
		c.advance(time.Second)
		if err := m.Record(Observation{ID: "cpu_contention_ratio", Value: 0.9, At: c.now()}); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := m.Property("cpu_contention_ratio"); p.Value < 0.25 {
		t.Fatalf("fixture: contention %.3f did not reach enter", p.Value)
	}
	res := m.Admit(admitReq(0.1, 0))
	if res.Accepted || !strings.Contains(res.Reason, "contended") {
		t.Errorf("%+v; want declined as contended", res)
	}
}

func TestAdmitSkipsContentionWithoutALever(t *testing.T) {
	m, c := admitFixture(t)
	_ = m.Record(Observation{ID: "cpu_contention_ratio", Value: 0.9, At: c.now()})
	req := admitReq(0.1, 0)
	req.CPUEnter = 0
	res := m.Admit(req)
	if !res.Accepted || !hasCaveat(res.Caveats, "no relief lever") {
		t.Errorf("%+v; want accepted with the missing lever caveated", res)
	}
}

func TestAdmitMeasuresAStaleSubject(t *testing.T) {
	m, c := admitFixture(t)
	unit := [2]float64{0, 1}
	for id, v := range map[string]float64{"cpu_utilization@pod:w": 0.3, "memory_utilization@pod:w": 0.1} {
		if err := m.Record(Observation{ID: id, Value: v, At: c.now(), Subject: "pod:w", Range: &unit}); err != nil {
			t.Fatal(err)
		}
	}
	c.advance(5 * time.Minute)
	// The node's own readings keep arriving; the workload's have stopped.
	for _, id := range []string{"cpu_utilization", "memory_utilization", "cpu_contention_ratio", "memory_contention_ratio"} {
		p, _ := m.Property(id)
		_ = m.Record(Observation{ID: id, Value: p.Value, At: c.now()})
	}
	m.Sweep()
	if p, _ := m.Property("cpu_utilization@pod:w"); p.Status != Stale {
		t.Fatalf("fixture: pod:w is %s, want stale", p.Status)
	}
	req := admitReq(3, 0) // declared would decline; measured must be used
	req.Workload = "pod:w"
	res := m.Admit(req)
	if !res.Accepted || res.Basis != BasisMeasured || !near(res.CPUAfter, 0.7) || !near(res.MemoryAfter, 0.6) {
		t.Fatalf("%+v; want accepted on the measured 0.3/0.1", res)
	}
	if !hasCaveat(res.Caveats, "measured when it last ran here, 5m0s ago") {
		t.Errorf("caveats %v; want the age of the measurement", res.Caveats)
	}
}

func TestAdmitAnActiveSubjectIsAlreadyRunning(t *testing.T) {
	m, c := admitFixture(t)
	unit := [2]float64{0, 1}
	_ = m.Record(Observation{ID: "cpu_utilization@pod:w", Value: 0.3, At: c.now(), Subject: "pod:w", Range: &unit})
	_ = m.Record(Observation{ID: "memory_utilization@pod:w", Value: 0.1, At: c.now(), Subject: "pod:w", Range: &unit})
	res := m.Admit(AdmitRequest{Workload: "pod:w", CPUEnter: 0.25, MemoryEnter: 0.08})
	if !res.Accepted || !strings.Contains(res.Reason, "already running") || !near(res.CPUAfter, 0.4) {
		t.Errorf("%+v; want accepted as already running, with no projection", res)
	}
}

// TestAdmitAlreadyRunningIsNotDeclinedForContention: a workload already here has
// nothing to admit, so the node's contention cannot decline it.
func TestAdmitAlreadyRunningIsNotDeclinedForContention(t *testing.T) {
	m, c := admitFixture(t)
	unit := [2]float64{0, 1}
	_ = m.Record(Observation{ID: "cpu_utilization@pod:w", Value: 0.3, At: c.now(), Subject: "pod:w", Range: &unit})
	_ = m.Record(Observation{ID: "memory_utilization@pod:w", Value: 0.1, At: c.now(), Subject: "pod:w", Range: &unit})
	for range 5 {
		c.advance(time.Second)
		_ = m.Record(Observation{ID: "cpu_contention_ratio", Value: 0.9, At: c.now()})
	}
	if p, _ := m.Property("cpu_contention_ratio"); p.Value < 0.25 {
		t.Fatalf("fixture: contention %.3f did not reach enter", p.Value)
	}
	res := m.Admit(AdmitRequest{Workload: "pod:w", CPUEnter: 0.25, MemoryEnter: 0.08})
	if !res.Accepted || !strings.Contains(res.Reason, "already running") {
		t.Errorf("%+v; want accepted as already running on a contended node", res)
	}
}

func TestAdmitDeclinesOnWhatTheMapLacks(t *testing.T) {
	m, c := newTestMap(t, Config{AdmitUnknown: true})
	res := m.Admit(admitReq(1, 0))
	if res.Accepted || !strings.Contains(res.Reason, "cpu_cores") || res.DecisionID == "" {
		t.Errorf("no facts: %+v; want a recorded decline naming cpu_cores", res)
	}
	_ = m.RecordFacts("test", []Fact{NumberFact("cpu_cores", 4, "cores"), NumberFact("memory_total", 8<<30, "bytes")})
	_ = m.Record(Observation{ID: "cpu_utilization", Value: 0.1, At: c.now()})
	res = m.Admit(admitReq(1, 0))
	if res.Accepted || !strings.Contains(res.Reason, "memory_utilization") {
		t.Errorf("no node memory reading: %+v; want a decline naming memory_utilization", res)
	}
	_ = m.Record(Observation{ID: "memory_utilization", Value: 0.1, At: c.now()})
	res = m.Admit(admitReq(0, 0))
	if res.Accepted || !strings.Contains(res.Reason, "nothing to price") {
		t.Errorf("no demand: %+v; want nothing to price", res)
	}
}

func TestAdmitIsReproducibleFromTheJournal(t *testing.T) {
	m, _ := admitFixture(t)
	res := m.Admit(admitReq(1, 1<<30))
	d, ok := m.Journal().Decision(res.DecisionID)
	if !ok {
		t.Fatalf("decision %s not in the journal", res.DecisionID)
	}
	read := map[string]float64{}
	for _, p := range d.PropertiesRead {
		read[p.ID] = p.Value
	}
	if read["cpu_utilization"] != 0.4 || read["memory_utilization"] != 0.5 {
		t.Errorf("properties read %v; want the node shares the projection used", read)
	}
	facts := map[string]bool{}
	for _, f := range d.FactsRead {
		facts[f.Key] = true
	}
	if !facts["cpu_cores"] || !facts["memory_total"] {
		t.Errorf("facts read %v; want cpu_cores and memory_total", d.FactsRead)
	}
	if d.Answer["accepted"] != true || d.Answer["basis"] != string(BasisDeclared) || !near(d.Answer["cpu_after"].(float64), 0.65) {
		t.Errorf("answer %v; want the result recorded", d.Answer)
	}
}

func TestAdmitRefusesAReusedID(t *testing.T) {
	m, _ := admitFixture(t)
	req := admitReq(1, 0)
	req.ID = "mine"
	if res := m.Admit(req); res.Err != "" {
		t.Fatal(res.Err)
	}
	if res := m.Admit(req); !strings.Contains(res.Err, "already used") {
		t.Errorf("%+v; want the reused id refused", res)
	}
	// Two different questions at one revision get two ids.
	a, b := m.Admit(admitReq(1, 0)), m.Admit(admitReq(2, 0))
	if a.DecisionID == b.DecisionID {
		t.Errorf("two requests share decision id %s", a.DecisionID)
	}
}

func TestAdmitRefusesABadDemand(t *testing.T) {
	m, _ := admitFixture(t)
	if res := m.Admit(admitReq(-1, 0)); res.Err == "" {
		t.Errorf("%+v; want a negative demand refused", res)
	}
}
