package minimal_test

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/compliance"
	"github.com/DiyazY/di-agent/internal/minimal"
	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/statemap"
	"github.com/DiyazY/di-agent/pkg/types"
)

// ── Compliance ────────────────────────────────────────────────────────────────

func TestMICorrelationProposerCompliance(t *testing.T) {
	compliance.RunProposerCompliance(t, func(t *testing.T) contracts.ProposerContract {
		// Pair the proposer with a real ontology so the backbone coverage
		// check has real propositions to consult. The compliance suite feeds
		// PS→RC observations; P10 (PS→RC −) is in the bootstrap, but the
		// suite's data has positive correlation, so the proposer emits a
		// positive PS→RC candidate (conflict-pair sibling — multigraph-legal).
		ontology := minimal.NewOntologyFromSpec(mustSpec())
		return minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 100, 0)
	})
}

// ── Strongly correlated input → emits a candidate ─────────────────────────────

func TestMICorrelationProposer_StronglyCorrelatedEmits(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	// Use a free pair (MU↛PS), threshold 0.8, minPairs 30, bufSize 200.
	p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)

	for i := 0; i < 100; i++ {
		x := float64(i) / 100.0
		y := 0.95*x + 0.01
		if err := p.Observe("MU", "PS", x, y); err != nil {
			t.Fatal(err)
		}
	}

	cs, err := p.GetCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("expected exactly 1 candidate after strong correlation; got %d", len(cs))
	}
	c := cs[0]
	if c.CandidateID != "MU->PS" {
		t.Errorf("unexpected candidate id: %s", c.CandidateID)
	}
	if c.Direction != types.Positive {
		t.Errorf("expected positive direction; got %v", c.Direction)
	}
	if c.MIScore < 0.95 {
		t.Errorf("expected MIScore close to 1.0; got %v", c.MIScore)
	}
}

// ── Uncorrelated input → no candidate ─────────────────────────────────────────

func TestMICorrelationProposer_UncorrelatedQuiet(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)
	rng := rand.New(rand.NewSource(7))

	for i := 0; i < 200; i++ {
		x := rng.Float64()
		y := rng.Float64() // independent
		if err := p.Observe("MU", "PS", x, y); err != nil {
			t.Fatal(err)
		}
	}

	cs, _ := p.GetCandidates()
	if len(cs) != 0 {
		t.Errorf("expected no candidates for uncorrelated input; got %d (first: %+v)", len(cs), cs[0])
	}
}

// ── Confirm: candidate becomes a real proposition ────────────────────────────

func TestMICorrelationProposer_ConfirmAddsProposition(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)

	for i := 0; i < 60; i++ {
		x := float64(i) / 100.0
		y := 0.9 * x
		_ = p.Observe("MU", "PS", x, y)
	}

	before, _ := ontology.Propositions()
	beforeCount := len(before)

	cs, _ := p.GetCandidates()
	if len(cs) != 1 {
		t.Fatalf("expected 1 candidate; got %d", len(cs))
	}
	// Confirm returns the proposition rather than adding it: the caller applies it
	// through the facade, which is the only path that reaches the state model too.
	prop, err := p.Confirm(cs[0].CandidateID, "")
	if err != nil {
		t.Fatalf("Confirm error: %v", err)
	}
	if prop == nil {
		t.Fatal("Confirm returned no proposition for a pending candidate")
	}
	if prop.FromConstruct != cs[0].FromID || prop.ToConstruct != cs[0].ToID {
		t.Errorf("returned proposition %s→%s does not carry the candidate's endpoints %s→%s",
			prop.FromConstruct, prop.ToConstruct, cs[0].FromID, cs[0].ToID)
	}
	if err := ontology.AddValidatedProposition(prop); err != nil {
		t.Fatalf("applying the returned proposition: %v", err)
	}

	after, _ := ontology.Propositions()
	if len(after) != beforeCount+1 {
		t.Errorf("applying the confirmation should add exactly 1 proposition; before=%d after=%d",
			beforeCount, len(after))
	}

	// Confirmed candidate must no longer appear in pending.
	cs, _ = p.GetCandidates()
	if len(cs) != 0 {
		t.Errorf("expected 0 pending candidates after Confirm; got %d", len(cs))
	}

	// History records the candidate with its final status (Confirmed).
	hist, _ := p.GetHistory()
	if len(hist) != 1 {
		t.Fatalf("expected 1 history entry; got %d", len(hist))
	}
	if hist[0].Status != types.Confirmed {
		t.Errorf("history entry should reflect Confirmed status; got %v", hist[0].Status)
	}

	// New proposition is visible via Propositions().
	var foundConfirmed bool
	for _, prop := range after {
		if prop.FromConstruct == "MU" && prop.ToConstruct == "PS" &&
			len(prop.EvidenceSources) == 1 && prop.EvidenceSources[0] == "proposer-mi" {
			foundConfirmed = true
			break
		}
	}
	if !foundConfirmed {
		t.Error("could not locate the confirmed MU→PS proposition in ontology")
	}
}

// ── Reject suppresses future re-emission ──────────────────────────────────────

func TestMICorrelationProposer_RejectSuppressesReemission(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)

	feed := func() {
		for i := 0; i < 60; i++ {
			x := float64(i) / 100.0
			y := 0.9 * x
			_ = p.Observe("MU", "PS", x, y)
		}
	}

	feed()
	cs, _ := p.GetCandidates()
	if len(cs) != 1 {
		t.Fatalf("expected 1 pending candidate; got %d", len(cs))
	}
	cid := cs[0].CandidateID
	if err := p.Reject(cid); err != nil {
		t.Fatal(err)
	}

	// Continue feeding correlated data — must not re-emit the rejected pair.
	feed()
	cs, _ = p.GetCandidates()
	for _, c := range cs {
		if c.CandidateID == cid {
			t.Errorf("rejected candidate %q re-emitted as pending after further observations", cid)
		}
	}
}

// ── Re-emission idempotency: many Observes → one CandidateID ──────────────────

func TestMICorrelationProposer_NoDuplicateCandidate(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)

	for i := 0; i < 500; i++ {
		x := float64(i%100) / 100.0
		y := 0.9 * x
		_ = p.Observe("MU", "PS", x, y)
	}
	cs, _ := p.GetCandidates()
	if len(cs) != 1 {
		t.Errorf("expected exactly 1 candidate; got %d", len(cs))
	}
	// History should accumulate but never have two distinct CandidateIDs for the same pair.
	hist, _ := p.GetHistory()
	seen := make(map[string]bool)
	for _, h := range hist {
		seen[h.CandidateID] = true
	}
	if len(seen) != 1 {
		t.Errorf("history shows %d distinct CandidateIDs for same pair; expected 1", len(seen))
	}
}

// ── Coverage check: existing same-direction proposition blocks emission ───────

func TestMICorrelationProposer_RespectsExistingDirection(t *testing.T) {
	// The spec's first proposition already occupies its (from, to) pair in one
	// direction. Same-direction proposals on that pair must be blocked;
	// opposite-direction (conflict-pair sibling) proposals are permitted
	// (multigraph behavior).
	from, to, sign := specPair()
	if from == "" {
		t.Skip("spec covers every pair in both directions; no free direction to test")
	}
	{
		ontology := minimal.NewOntologyFromSpec(mustSpec())
		p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)
		for i := 0; i < 100; i++ {
			x := float64(i) / 100.0
			y := sameDirectionSeries(sign, x)
			_ = p.Observe(from, to, x, y)
		}
		cs, _ := p.GetCandidates()
		if len(cs) != 0 {
			t.Errorf("expected no candidate (pair already in the backbone); got %d", len(cs))
		}
	}
	{
		ontology := minimal.NewOntologyFromSpec(mustSpec())
		p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.8, 30, 200, 0)
		for i := 0; i < 100; i++ {
			x := float64(i) / 100.0
			y := sameDirectionSeries(flipDir(sign), x)
			_ = p.Observe(from, to, x, y)
		}
		cs, _ := p.GetCandidates()
		if len(cs) != 1 {
			t.Fatalf("expected 1 candidate on the free direction of %s→%s; got %d", from, to, len(cs))
		}
		if wantDir := oppositeOf(sign); cs[0].Direction != wantDir {
			t.Errorf("expected %v direction; got %v", wantDir, cs[0].Direction)
		}
	}
}

// ── Pearson sanity ────────────────────────────────────────────────────────────

func TestMICorrelationProposer_PerfectCorrelation(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	p := minimal.NewMICorrelationProposer(minimal.LookupOntology(ontology), 0.5, 10, 50, 0)
	// y = x exactly → r should be ≈ 1.0.
	for i := 0; i < 20; i++ {
		x := float64(i)
		_ = p.Observe("MU", "PS", x, x)
	}
	cs, _ := p.GetCandidates()
	if len(cs) != 1 {
		t.Fatalf("expected 1 candidate; got %d", len(cs))
	}
	if math.Abs(cs[0].MIScore-1.0) > 1e-6 {
		t.Errorf("expected MIScore=1.0 for perfect correlation; got %v", cs[0].MIScore)
	}
}

// ── LookupOntology ─────────────────────────────────────────────────────────────

func TestLookupOntologyCoversDeclaredPropositions(t *testing.T) {
	ontology := minimal.NewOntologyFromSpec(mustSpec())
	first := mustSpec().Propositions[0]
	sign := 1
	if first.Direction == "negative" {
		sign = -1
	}
	l := minimal.LookupOntology(ontology)
	if !l.Covered(first.FromConstruct, first.ToConstruct, sign) {
		t.Errorf("proposition %s should cover (%s,%s,%+d)", first.PropositionID, first.FromConstruct, first.ToConstruct, sign)
	}
	if l.Covered(first.FromConstruct, first.ToConstruct, -sign) {
		t.Error("the opposite sign is not covered by the same proposition")
	}
}

// ── Property-level pairing with the scope rule ────────────────────────────────

// coveredNone is a lookup that covers nothing, for tests about pairing alone.
type coveredNone struct{}

func (coveredNone) Covered(_, _ string, _ int) bool { return false }

// subjectPairsKnown counts every pair of two subjects' properties as already a
// relationship, so a test about another mechanism sees only pairs with the node.
type subjectPairsKnown struct{}

func (subjectPairsKnown) Covered(from, to string, _ int) bool {
	return strings.Contains(from, "@") && strings.Contains(to, "@")
}

func feedScoped(p *minimal.MICorrelationProposer, n int, gap time.Duration) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%20) / 20
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_utilization@pod:a", Subject: "pod:a", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_utilization@pod:b", Subject: "pod:b", Value: 1 - x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_pressure_ratio", Subject: "", Value: 0.9*x + 0.05, At: at.Add(gap)})
	}
}

func mustCandidates(t *testing.T, p *minimal.MICorrelationProposer) []*types.CandidateEdge {
	t.Helper()
	cs, err := p.GetCandidates()
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestObserveProperty_PairsSubjectsWithTheNodeAndWithEachOther(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 40, 2*time.Second)
	byID := map[string]*types.CandidateEdge{}
	for _, c := range mustCandidates(t, p) {
		byID[c.CandidateID] = c
		if c.FromID == "cpu_pressure_ratio" {
			t.Errorf("direction must be scoped -> unscoped; got %s -> %s", c.FromID, c.ToID)
		}
	}
	for _, id := range []string{"cpu_utilization@pod:a->cpu_pressure_ratio", "cpu_utilization@pod:b->cpu_pressure_ratio"} {
		if c := byID[id]; c == nil || c.Undirected {
			t.Errorf("expected directed %s; got %+v", id, c)
		}
	}
	// The two pods move in opposite directions: an undirected, negative, same-metric pair.
	c := byID["cpu_utilization@pod:a<->cpu_utilization@pod:b"]
	if c == nil {
		t.Fatalf("expected the two pods to be paired with each other; got %v", byID)
	}
	if !c.Undirected || c.Direction != types.Negative || c.Kind != types.CandidateKindSameMetric {
		t.Errorf("pod pair: undirected=%v direction=%v kind=%q; want undirected, negative, same-metric", c.Undirected, c.Direction, c.Kind)
	}
}

func TestObserveProperty_RespectsTimeTolerance(t *testing.T) {
	p := minimal.NewMICorrelationProposer(subjectPairsKnown{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 40, 10*time.Minute) // every node reading is minutes away from every pod reading, so no pair can ever fall inside the 15s window
	if cs := mustCandidates(t, p); len(cs) != 0 {
		t.Errorf("readings outside the pair window formed %d candidates; want none", len(cs))
	}
}

func TestObserveProperty_SkipsCoveredPairs(t *testing.T) {
	m := statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	_ = m.Record(statemap.Observation{ID: "cpu_utilization@pod:a", Value: .1, At: time.Now(), Subject: "pod:a"})
	_ = m.Observe("cpu_pressure_ratio", .1, time.Now())
	_ = m.DeclareRelationship(statemap.Relationship{From: "cpu_utilization@pod:a", To: "cpu_pressure_ratio", Sign: 1, Label: "discovered"})
	p := minimal.NewMICorrelationProposer(m, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 40, 2*time.Second)
	ids := map[string]bool{}
	for _, c := range mustCandidates(t, p) {
		ids[c.CandidateID] = true
		if c.FromID == "cpu_utilization@pod:a" && !c.Undirected {
			t.Error("a pair the state map already holds must not be re-proposed")
		}
	}
	if !ids["cpu_utilization@pod:b->cpu_pressure_ratio"] {
		t.Error("expected the uncovered pod:b candidate to still be proposed")
	}
}

func TestForgetDropsEverythingAboutAProperty(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 40, 2*time.Second)
	if err := p.Forget("cpu_utilization@pod:a"); err != nil {
		t.Fatal(err)
	}
	var sawB bool
	for _, c := range mustCandidates(t, p) {
		if c.FromID == "cpu_utilization@pod:a" {
			t.Error("candidate for a forgotten property survived")
		}
		if c.FromID == "cpu_utilization@pod:b" {
			sawB = true
		}
	}
	if !sawB {
		t.Error("expected pod:b's candidate to survive the forget of pod:a")
	}
	h, _ := p.GetHistory()
	for _, c := range h {
		if c.FromID == "cpu_utilization@pod:a" {
			t.Error("history for a forgotten property survived")
		}
	}
}

// feedPendingCap feeds three pod series against a shared "node" series. pod:1
// and pod:2 are exact affine transforms of node (r = 1.0); pod:3 carries a
// periodic deviation so its |r| is clearly below the other two while staying
// above the 0.5 threshold — it is the genuinely weakest of the three.
func feedPendingCap(p *minimal.MICorrelationProposer) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		_ = p.ObserveProperty(types.PropertyReading{ID: "m@pod:1", Subject: "pod:1", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "m@pod:2", Subject: "pod:2", Value: x * 0.9, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "m@pod:3", Subject: "pod:3", Value: x*0.7 + 0.1 + 0.35*math.Sin(float64(i)), At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "node", Subject: "", Value: x, At: at})
	}
}

func TestPendingCapDefersTheWeakest(t *testing.T) {
	p := minimal.NewMICorrelationProposer(subjectPairsKnown{}, 0.5, 10, 60, 15*time.Second)
	p.SetMaxPending(2)
	feedPendingCap(p)

	cs := mustCandidates(t, p)
	if len(cs) > 2 {
		t.Errorf("pending=%d exceeds the cap of 2", len(cs))
	}
	pendingFrom := map[string]bool{}
	for _, c := range cs {
		pendingFrom[c.FromID] = true
	}
	if !pendingFrom["m@pod:1"] || !pendingFrom["m@pod:2"] {
		t.Errorf("expected the two strong candidates (m@pod:1, m@pod:2) to remain pending; got %v", pendingFrom)
	}

	h, _ := p.GetHistory()
	var deferredCount int
	var deferredFrom string
	for _, c := range h {
		if c.Status == types.Deferred {
			deferredCount++
			deferredFrom = c.FromID
		}
	}
	if deferredCount != 1 {
		t.Fatalf("expected exactly 1 deferred candidate; got %d", deferredCount)
	}
	if deferredFrom != "m@pod:3" {
		t.Errorf("expected the weaker m@pod:3 candidate to be deferred; got FromID=%q", deferredFrom)
	}
}

// buildCapScenario feeds three pod series and one node series at a shared
// cadence with maxPending=2, so the third candidate forces one deferral.
//
// With affine=true the pods are distinct affine transforms of node. Pearson is
// affine-invariant, so their |r| are equal in exact arithmetic — but not in
// float64: at the moment the cap fires the three values sit one ULP apart, and
// which one rounds lowest depends on the architecture's arithmetic (arm64 fuses
// the multiply-add in the covariance sums; amd64 at GOAMD64=v1 does not). On
// arm64 pod:3 rounds lowest, on amd64 pod:2 does. The CandidateID tie-break
// never engages for such series, so no test may assert which of them is
// deferred.
//
// With affine=false the three pods report the bit-identical series, which is
// the only way to reach the tie-break: equal bits on every platform, broken by
// the lexicographically larger CandidateID.
func buildCapScenario(affine bool) *minimal.MICorrelationProposer {
	p := minimal.NewMICorrelationProposer(subjectPairsKnown{}, 0.5, 10, 60, 15*time.Second)
	p.SetMaxPending(2)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		x2, x3 := x, x
		if affine {
			x2, x3 = x*0.9, x*0.7+0.1
		}
		_ = p.ObserveProperty(types.PropertyReading{ID: "m@pod:1", Subject: "pod:1", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "m@pod:2", Subject: "pod:2", Value: x2, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "m@pod:3", Subject: "pod:3", Value: x3, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "node", Subject: "", Value: x, At: at})
	}
	return p
}

func singleDeferredID(t *testing.T, p *minimal.MICorrelationProposer) string {
	t.Helper()
	h, _ := p.GetHistory()
	var ids []string
	for _, c := range h {
		if c.Status == types.Deferred {
			ids = append(ids, c.CandidateID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("expected exactly 1 deferred candidate; got %d (%v)", len(ids), ids)
	}
	return ids[0]
}

// TestPendingCapIsDeterministic rebuilds the affine cap scenario twice and
// checks that the same candidate is deferred both times. It deliberately does
// not say which: that is decided by one ULP of rounding and differs between
// architectures (see buildCapScenario). What it guards is the original defect
// — the weakest candidate chosen by ranging over a map, which varied run to run.
func TestPendingCapIsDeterministic(t *testing.T) {
	d1 := singleDeferredID(t, buildCapScenario(true))
	d2 := singleDeferredID(t, buildCapScenario(true))
	if d1 != d2 {
		t.Errorf("two identical runs deferred different candidates: %q vs %q", d1, d2)
	}
}

// TestPendingCapTieBreaksOnCandidateID reaches the tie-break with bit-identical
// pod series, so the |r|·n scores are equal on every platform and the
// lexicographically larger CandidateID must be the one deferred.
func TestPendingCapTieBreaksOnCandidateID(t *testing.T) {
	d := singleDeferredID(t, buildCapScenario(false))
	if d != "m@pod:3->node" {
		t.Errorf("expected the tie-break to defer %q; got %q", "m@pod:3->node", d)
	}
}

// TestConfirmedCandidateIsNeverReemitted covers finding 1: a Confirmed candidate
// must never be overwritten by a fresh Pending entry, however the correlation
// evolves afterward (including a sign flip — coverage is sign-specific, so a
// flipped correlation is exactly the case that reaches the switch again).
func TestConfirmedCandidateIsNeverReemitted(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 40, 2*time.Second)

	var cid string
	for _, c := range mustCandidates(t, p) {
		if c.FromID == "cpu_utilization@pod:a" && !c.Undirected {
			cid = c.CandidateID
		}
	}
	if cid == "" {
		t.Fatal("expected a candidate for cpu_utilization@pod:a before confirming")
	}
	if _, err := p.Confirm(cid, ""); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	// Keep feeding a strongly correlated stream for the confirmed pair, using
	// fresh timestamps so the folds are not deduped away.
	t1 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		at := t1.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%20) / 20
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_utilization@pod:a", Subject: "pod:a", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_pressure_ratio", Subject: "", Value: 0.9*x + 0.05, At: at.Add(2 * time.Second)})
	}
	// Then a sign-flipped stream for the same pair.
	t2 := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		at := t2.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%20) / 20
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_utilization@pod:a", Subject: "pod:a", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_pressure_ratio", Subject: "", Value: -(0.9*x + 0.05), At: at.Add(2 * time.Second)})
	}

	hist, _ := p.GetHistory()
	var occurrences int
	var status types.CandidateStatus
	for _, c := range hist {
		if c.CandidateID == cid {
			occurrences++
			status = c.Status
		}
	}
	if occurrences != 1 {
		t.Errorf("expected exactly 1 history entry for %q; got %d", cid, occurrences)
	}
	if status != types.Confirmed {
		t.Errorf("expected status Confirmed; got %v", status)
	}
	for _, c := range mustCandidates(t, p) {
		if c.CandidateID == cid {
			t.Errorf("confirmed candidate %q reappeared among pending candidates", cid)
		}
	}
}

// TestPendingCandidateDirectionFollowsTheEvidence: a Pending candidate is refreshed on
// every fold. Its direction is evidence like its |r| and n, so once the window has
// turned over to the opposite sign the candidate must say so — otherwise Confirm hands
// the map a sign the machine contradicts, and the map refuses to reverse it later.
func TestPendingCandidateDirectionFollowsTheEvidence(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 6, 8, 15*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feed := func(start time.Time, n int, slope float64) {
		for i := 0; i < n; i++ {
			at := start.Add(time.Duration(i) * 10 * time.Second)
			x := float64(i%10) / 10
			_ = p.ObserveProperty(types.PropertyReading{ID: "cpu@pod:a", Subject: "pod:a", Value: x, At: at})
			_ = p.ObserveProperty(types.PropertyReading{ID: "pressure", Subject: "", Value: 0.5 + slope*x, At: at})
		}
	}
	feed(t0, 12, 0.4)
	cs := mustCandidates(t, p)
	if len(cs) != 1 || cs[0].Direction != types.Positive {
		t.Fatalf("after a positive stream: %d candidates, first %+v; want one Positive", len(cs), cs)
	}
	feed(t0.Add(time.Hour), 24, -0.4)
	cs = mustCandidates(t, p)
	if len(cs) != 1 {
		t.Fatalf("after the sign flipped: %d candidates; want the same one", len(cs))
	}
	if cs[0].Direction != types.Negative {
		t.Errorf("direction %v with |r|=%.3f over n=%d after the window turned negative; the pending candidate froze its first direction",
			cs[0].Direction, cs[0].MIScore, cs[0].NObservations)
	}
}

// TestEachObservationPairsOnce: at a shared cadence each tick used to fold two pairs —
// (pod_i, node_{i-1}) when the pod arrived and (pod_i, node_i) when the node did — so
// n counted every reading twice, min-pairs was reached in half the advertised ticks
// and the Fisher p was computed over doubled, autocorrelated support. An observation
// takes part in at most one pair per counterpart.
func TestEachObservationPairsOnce(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i) / 10
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu@pod:a", Subject: "pod:a", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "pressure", Subject: "", Value: x, At: at})
	}
	cs := mustCandidates(t, p)
	if len(cs) != 1 {
		t.Fatalf("%d candidates; want 1", len(cs))
	}
	if cs[0].NObservations != 10 {
		t.Errorf("n=%d after 10 ticks of one pod and one node reading each; want 10, one pair per tick", cs[0].NObservations)
	}
}

// TestCapDeferralIsRecordedAndReentersWhenItOutranksThePending: a cap deferral is the
// proposer's choice, not an operator's, so history says so and a log line names it;
// and it is not permanent — when the deferred pair later outranks a pending one, the
// two swap, so the cap bounds what is shown rather than what can ever be seen.
func TestCapDeferralIsRecordedAndReentersWhenItOutranksThePending(t *testing.T) {
	p := minimal.NewMICorrelationProposer(subjectPairsKnown{}, 0.5, 5, 60, 15*time.Second)
	p.SetMaxPending(1)
	var logged []string
	p.SetLogger(func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func(i int, pods ...string) {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		for _, pod := range pods {
			_ = p.ObserveProperty(types.PropertyReading{ID: "m@" + pod, Subject: pod, Value: x, At: at})
		}
		_ = p.ObserveProperty(types.PropertyReading{ID: "node", Subject: "", Value: x, At: at})
	}
	byID := func() map[string]*types.CandidateEdge {
		h, _ := p.GetHistory()
		out := map[string]*types.CandidateEdge{}
		for _, c := range h {
			out[c.CandidateID] = c
		}
		return out
	}
	for i := 0; i < 6; i++ {
		tick(i, "pod:a", "pod:b")
	}
	m := byID()
	a, b := m["m@pod:a->node"], m["m@pod:b->node"]
	if a == nil || b == nil || a.Status != types.Pending || b.Status != types.Deferred {
		t.Fatalf("after the cap: a=%+v b=%+v; want a pending and b deferred by the tie-break", a, b)
	}
	if b.Reason != "cap" {
		t.Errorf("deferred candidate carries reason %q; want \"cap\" so history can tell it from an operator's Defer", b.Reason)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "m@pod:b->node") {
		t.Errorf("the cap deferral was not logged: %v", logged)
	}
	for i := 6; i < 20; i++ {
		tick(i, "pod:b") // pod:a falls silent; pod:b keeps reporting and outgrows it
	}
	m = byID()
	a, b = m["m@pod:a->node"], m["m@pod:b->node"]
	if b.Status != types.Pending {
		t.Errorf("pod:b stayed %v with |r|·n=%.1f against pod:a's %.1f; a cap deferral must re-enter when it outranks the pending",
			b.Status, b.MIScore*float64(b.NObservations), a.MIScore*float64(a.NObservations))
	}
	if a.Status != types.Deferred || a.Reason != "cap" {
		t.Errorf("pod:a is %v/%q; want cap-deferred in pod:b's place", a.Status, a.Reason)
	}
}

// TestOperatorDeferralDoesNotReenter: an operator's Defer is a decision the proposer
// does not overturn, however the evidence grows.
func TestOperatorDeferralDoesNotReenter(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 20, 2*time.Second)
	cs := mustCandidates(t, p)
	if len(cs) == 0 {
		t.Fatal("no candidate to defer")
	}
	id := cs[0].CandidateID
	if err := p.Defer(id); err != nil {
		t.Fatal(err)
	}
	feedScoped(p, 40, 2*time.Second)
	h, _ := p.GetHistory()
	for _, c := range h {
		if c.CandidateID == id && (c.Status != types.Deferred || c.Reason != "operator") {
			t.Errorf("operator-deferred candidate is %v/%q after more evidence; want Deferred/operator", c.Status, c.Reason)
		}
	}
}

// TestSettledCandidatesCannotBeRejectedOrDeferred: a Confirmed candidate has become a
// relationship in the map; letting history later say "rejected" about it would
// contradict the map.
func TestSettledCandidatesCannotBeRejectedOrDeferred(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 40, 2*time.Second)
	cs := mustCandidates(t, p)
	if len(cs) == 0 {
		t.Fatal("no candidate to confirm")
	}
	id := cs[0].CandidateID
	if _, err := p.Confirm(id, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Reject(id); err == nil {
		t.Error("a Confirmed candidate was rejected")
	}
	if err := p.Defer(id); err == nil {
		t.Error("a Confirmed candidate was deferred")
	}
	h, _ := p.GetHistory()
	for _, c := range h {
		if c.CandidateID == id && c.Status != types.Confirmed {
			t.Errorf("status %v after the refused transitions; want Confirmed", c.Status)
		}
	}
}

// TestObserveProperty_PairsNodePropertiesUndirected: two node-level properties that
// correlate become one undirected candidate, whichever arrives first, and a subject
// still pairs with each of them, directed.
func TestObserveProperty_PairsNodePropertiesUndirected(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu@pod:a", Subject: "pod:a", Value: x, At: at})
		// Alternate arrival order: the pair must still fold into one buffer.
		if i%2 == 0 {
			_ = p.ObserveProperty(types.PropertyReading{ID: "pressure", Value: x, At: at})
			_ = p.ObserveProperty(types.PropertyReading{ID: "load", Value: 0.5 * x, At: at})
		} else {
			_ = p.ObserveProperty(types.PropertyReading{ID: "load", Value: 0.5 * x, At: at})
			_ = p.ObserveProperty(types.PropertyReading{ID: "pressure", Value: x, At: at})
		}
	}
	h, _ := p.GetHistory()
	var scoped, undirected int
	for _, c := range h {
		switch {
		case c.Undirected:
			undirected++
			if c.CandidateID != "load<->pressure" || c.FromID != "load" || c.ToID != "pressure" {
				t.Errorf("undirected candidate %s (%s, %s); want load<->pressure in lexicographic order", c.CandidateID, c.FromID, c.ToID)
			}
			if c.NObservations != 20 {
				t.Errorf("n=%d; both arrival orders must fold into one buffer of 20 pairs", c.NObservations)
			}
		case strings.Contains(c.FromID, "@"):
			scoped++
		}
	}
	if undirected != 1 {
		t.Errorf("%d undirected candidates; want exactly one for the node pair", undirected)
	}
	if scoped != 2 {
		t.Errorf("%d scoped candidates; want pod:a paired with both node properties", scoped)
	}
}

// coveredOneWay covers exactly from -> to with the given sign.
type coveredOneWay struct {
	from, to string
	sign     int
}

func (c coveredOneWay) Covered(from, to string, sign int) bool {
	return from == c.from && to == c.to && sign == c.sign
}

var unit = [2]float64{0, 1}

// feedNodePair drives two node-level properties with the given series.
func feedNodePair(p *minimal.MICorrelationProposer, n int, a, b func(i int) float64, rngA, rngB *[2]float64) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		_ = p.ObserveProperty(types.PropertyReading{ID: "a", Value: a(i), At: at, Range: rngA})
		_ = p.ObserveProperty(types.PropertyReading{ID: "b", Value: b(i), At: at, Range: rngB})
	}
}

// TestVariationGate: correlation is scale-free, so a series that moves 0.05% of its
// declared range still correlates perfectly. The gate holds such a pair back, lets a
// pair that moves through it, and states that it could not check an end with no
// declared range rather than passing it silently.
func TestVariationGate(t *testing.T) {
	big := func(i int) float64 { return float64(i%10) / 10 }       // moves 90% of [0,1]
	tiny := func(i int) float64 { return 0.5 + float64(i%10)/2e4 } // moves 0.045% of [0,1]

	for _, tc := range []struct {
		name       string
		a, b       func(int) float64
		rngA, rngB *[2]float64
		want       bool
		caveats    int
	}{
		{"both move", big, big, &unit, &unit, true, 0},
		{"one barely moves", big, tiny, &unit, &unit, false, 0},
		{"range undeclared: not checked, and said so", big, tiny, &unit, nil, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
			feedNodePair(p, 20, tc.a, tc.b, tc.rngA, tc.rngB)
			cs, _ := p.GetCandidates()
			if got := len(cs) == 1; got != tc.want {
				t.Fatalf("candidates=%d; want proposed=%v", len(cs), tc.want)
			}
			if tc.want && len(cs[0].Caveats) != tc.caveats {
				t.Errorf("caveats %q; want %d", cs[0].Caveats, tc.caveats)
			}
		})
	}

	t.Run("0 disables the gate", func(t *testing.T) {
		p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
		p.SetMinSpread(0)
		feedNodePair(p, 20, big, tiny, &unit, &unit)
		if cs, _ := p.GetCandidates(); len(cs) != 1 {
			t.Errorf("candidates=%d with the gate off; want 1", len(cs))
		}
	})
}

// TestCompositionFlag: a subject's metric against the node total of the same metric
// is part against whole. It is still proposed, marked as such, and a different metric
// is not marked.
// Readings of one tick arrive one after another while the previous tick's are still
// inside the pair window. After a missed reading the first arrival of the next tick
// finds the other's previous value unpaired and folds it; the same-tick reading that
// follows must replace that pair, or the window stays one interval out of step for
// good — and on an alternating series a one-interval lag reads as r = -1.
func TestPairsReadingsOfTheSameTickAfterAGap(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i % 2)
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu@pod:a", Subject: "pod:a", Value: x, At: at})
		if i != 5 { // one failed read
			_ = p.ObserveProperty(types.PropertyReading{ID: "memory", Value: x, At: at})
		}
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu", Value: x, At: at})
	}
	h, _ := p.GetHistory()
	if len(h) == 0 {
		t.Fatal("no candidates")
	}
	for _, c := range h {
		if c.Direction != types.Positive || c.MIScore < 0.99 {
			t.Errorf("%s: direction %v |r|=%.3f; same-tick readings must pair, giving r = +1", c.CandidateID, c.Direction, c.MIScore)
		}
		if c.NObservations < 19 {
			t.Errorf("%s: n=%d; want one pair per tick with both readings", c.CandidateID, c.NObservations)
		}
	}
}

func TestCompositionFlag(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_utilization@pod:a", Subject: "pod:a", Value: x, At: at, Range: &unit})
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_utilization", Value: 0.2 + 0.5*x, At: at, Range: &unit})
		_ = p.ObserveProperty(types.PropertyReading{ID: "cpu_pressure_ratio", Value: 0.1 + 0.3*x, At: at, Range: &unit})
	}
	cs, _ := p.GetCandidates()
	kinds := map[string]string{}
	for _, c := range cs {
		kinds[c.CandidateID] = c.Kind
	}
	if k, ok := kinds["cpu_utilization@pod:a->cpu_utilization"]; !ok || k != types.CandidateKindComposition {
		t.Errorf("pod CPU -> node CPU: kind %q (proposed=%v); want %q", k, ok, types.CandidateKindComposition)
	}
	if k, ok := kinds["cpu_utilization@pod:a->cpu_pressure_ratio"]; !ok || k != "" {
		t.Errorf("pod CPU -> node pressure: kind %q (proposed=%v); want an ordinary candidate", k, ok)
	}
	prop, err := p.Confirm("cpu_utilization@pod:a->cpu_utilization", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prop.Description, "composition") {
		t.Errorf("confirmed composition's description %q does not say so", prop.Description)
	}
}

// TestUndirectedPairCoveredEitherWay: a relationship declared b -> a covers the a<->b
// candidate, since the candidate claims no direction; a directed candidate is still
// covered only by its own direction.
func TestUndirectedPairCoveredEitherWay(t *testing.T) {
	x := func(i int) float64 { return float64(i%10) / 10 }
	p := minimal.NewMICorrelationProposer(coveredOneWay{"b", "a", 1}, 0.5, 5, 60, 15*time.Second)
	feedNodePair(p, 20, x, x, &unit, &unit)
	if cs, _ := p.GetCandidates(); len(cs) != 0 {
		t.Errorf("b -> a is declared; the undirected a<->b was still proposed: %v", cs[0].CandidateID)
	}
}

// TestConfirmOrientation: a directed candidate is confirmed as proposed and refuses
// its target as the source; an undirected one runs from whichever end is named.
func TestConfirmOrientation(t *testing.T) {
	x := func(i int) float64 { return float64(i%10) / 10 }
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	feedNodePair(p, 20, x, x, &unit, &unit)
	if _, err := p.Confirm("a<->b", "b"); err != nil {
		t.Fatalf("undirected confirm from b: %v", err)
	}

	q := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	feedScoped(q, 40, 0)
	cs, _ := q.GetCandidates()
	if len(cs) == 0 {
		t.Fatal("no scoped candidate to confirm")
	}
	if _, err := q.Confirm(cs[0].CandidateID, cs[0].ToID); !errors.Is(err, contracts.ErrBadConfirmation) {
		t.Errorf("directed candidate confirmed from its target: %v; want ErrBadConfirmation", err)
	}
	if _, err := q.Confirm(cs[0].CandidateID, cs[0].FromID); err != nil {
		t.Errorf("directed candidate confirmed from its source: %v", err)
	}
}

// feedRationing drives five node-level properties: two that never move, one that
// moves without following anything, and two that move together. Their names put the
// quiet ones first, so while the budget has room they take it.
func feedRationing(p *minimal.MICorrelationProposer, from, to int) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := &[2]float64{0, 1}
	for i := from; i < to; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		for _, r := range []types.PropertyReading{
			{ID: "a_quiet", Value: 0.5},
			{ID: "b_quiet", Value: 0.5},
			{ID: "c_noise", Value: 0.5 + 0.4*math.Sin(float64(i)*1.7)},
			{ID: "x", Value: x},
			{ID: "y", Value: 0.2 + 0.7*x},
		} {
			r.At, r.Range = at, rng
			_ = p.ObserveProperty(r)
		}
	}
}

// TestBudgetRationsPairsToTheMostVariable: a budget of three is filled first by the
// quiet properties, which arrive first. Once those pairs have held matureAt pairs
// below the threshold they are displaced, and the moving pair is found.
func TestBudgetRationsPairsToTheMostVariable(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 300, 15*time.Second)
	p.SetMaxPairs(3)
	feedRationing(p, 0, 120)

	d, _ := p.Discovery()
	if d.Budget != 3 || d.TestedPairs > 3 {
		t.Errorf("budget %d, tested %d; the budget must hold", d.Budget, d.TestedPairs)
	}
	if d.Properties != 5 || d.EligiblePairs != 10 {
		t.Errorf("properties %d, eligible %d; want 5 and 10", d.Properties, d.EligiblePairs)
	}
	if d.Displaced == 0 {
		t.Error("nothing displaced: the quiet pairs kept the budget")
	}
	h, _ := p.GetHistory()
	var found *types.CandidateEdge
	for _, c := range h {
		if strings.Contains(c.CandidateID, "quiet") {
			t.Errorf("a property that never moved reached a candidate: %s", c.CandidateID)
		}
		if c.CandidateID == "x<->y" {
			found = c
		}
	}
	if found == nil {
		t.Fatalf("x<->y never proposed; history %d", len(h))
	}

	// A pair at or above the threshold keeps its slot: it goes on accumulating.
	n := found.NObservations
	feedRationing(p, 120, 200)
	for _, c := range mustCandidates(t, p) {
		if c.CandidateID == "x<->y" && c.NObservations <= n {
			t.Errorf("x<->y stayed at n=%d after 80 more ticks; a candidate's pair must not be displaced", c.NObservations)
		}
	}
}

// TestSettledPairStopsBeingTested: rejecting a candidate frees its slot at the next
// rationing pass, and the pair is not admitted again.
func TestSettledPairStopsBeingTested(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	p.SetMaxPairs(1)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func(i int) {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		_ = p.ObserveProperty(types.PropertyReading{ID: "x", Value: x, At: at})
		_ = p.ObserveProperty(types.PropertyReading{ID: "y", Value: x, At: at})
	}
	for i := 0; i < 20; i++ {
		tick(i)
	}
	if err := p.Reject("x<->y"); err != nil {
		t.Fatal(err)
	}
	for i := 20; i < 25; i++ {
		tick(i)
	}
	if d, _ := p.Discovery(); d.TestedPairs != 0 {
		t.Errorf("tested %d pairs after the only pair was rejected; want 0", d.TestedPairs)
	}
}

// TestForgetFreesTheBudget: a departed property's pairs leave the budget.
func TestForgetFreesTheBudget(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedScoped(p, 5, 2*time.Second)
	before, _ := p.Discovery()
	_ = p.Forget("cpu_utilization@pod:a")
	after, _ := p.Discovery()
	if before.TestedPairs != 3 || after.TestedPairs != 1 {
		t.Errorf("tested %d before and %d after forgetting pod:a; want 3 and 1", before.TestedPairs, after.TestedPairs)
	}
}

// TestReplayedReadingIsNotFoldedTwice: the property path keeps no identities per
// pair; a reading no newer than its property's last is what it refuses instead.
func TestReplayedReadingIsNotFoldedTwice(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.5, 5, 60, 15*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var batch []types.PropertyReading
	for i := 0; i < 10; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i % 2)
		batch = append(batch, types.PropertyReading{ID: "x", Value: x, At: at}, types.PropertyReading{ID: "y", Value: x, At: at})
	}
	for range 2 { // the whole batch, then the same batch again
		for _, r := range batch {
			_ = p.ObserveProperty(r)
		}
	}
	h, _ := p.GetHistory()
	if len(h) != 1 || h[0].NObservations != 10 {
		t.Errorf("history %d candidates, n=%v; want one with n=10", len(h), h)
	}
}

func TestDerivePairBudget(t *testing.T) {
	const gib = 1 << 30
	pi := minimal.DerivePairBudget(8*gib, 4, 10*time.Second, 120)
	vm := minimal.DerivePairBudget(2*gib, 2, 10*time.Second, 120)
	if pi <= vm {
		t.Errorf("an 8 GiB, 4-core machine got %d pairs and a 2 GiB, 2-core one %d; more machine must allow more", pi, vm)
	}
	if want := int(0.01 * 2 * gib / float64(minimal.PairBytes(120))); vm != want {
		t.Errorf("2 GiB: %d pairs; memory allows %d and should bind", vm, want)
	}
	if fast := minimal.DerivePairBudget(64*gib, 1, 100*time.Millisecond, 120); fast != 200 {
		t.Errorf("one core at a 100ms tick: %d pairs; CPU allows 200 and should bind", fast)
	}
	if got := minimal.DerivePairBudget(0, 4, 10*time.Second, 120); got != minimal.FallbackPairBudget {
		t.Errorf("unreadable memory: %d; want the fallback", got)
	}
}

// feedUncorrelated drives node-level properties that each move with their own
// amplitude and frequency, so no pair correlates: every tested pair matures weak.
func feedUncorrelated(p *minimal.MICorrelationProposer, amp map[string]float64, from, to int) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := &[2]float64{0, 1}
	ids := make([]string, 0, len(amp))
	for id := range amp {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i := from; i < to; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		for k, id := range ids {
			v := 0.5 + amp[id]*math.Sin(float64(i)*(1.3+0.71*float64(k)))
			_ = p.ObserveProperty(types.PropertyReading{ID: id, Value: v, At: at, Range: rng})
		}
	}
}

// TestDisplacementRespectsRank: a weak pair of the most variable properties is not
// displaced by a pair that varies less, so the budget settles rather than rotating.
func TestDisplacementRespectsRank(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	p.SetMaxPairs(1)
	// "a_lo"/"b_lo" sort first, so the low pair takes the only slot; the high pair
	// displaces it once it matures, and nothing displaces the high pair after.
	feedUncorrelated(p, map[string]float64{"a_lo": 0.05, "b_lo": 0.05, "c_hi": 0.45, "d_hi": 0.45}, 0, 400)
	d, _ := p.Discovery()
	if d.Displaced != 1 {
		t.Errorf("displaced %d times over 400 ticks; want once, the low pair making way for the high one", d.Displaced)
	}
}

// TestQuietPairIsDisplacedAndItsCandidateDeferred: a pair whose property stopped
// reporting holds no claim on the budget, mature or not, and a pending candidate it
// backed leaves the queue as untested rather than standing with frozen numbers.
func TestQuietPairIsDisplacedAndItsCandidateDeferred(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	p.SetMaxPairs(1)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := &[2]float64{0, 1}
	for i := 0; i < 40; i++ {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%10) / 10
		_ = p.ObserveProperty(types.PropertyReading{ID: "x", Value: x, At: at, Range: rng})
		if i < 20 { // y stops reporting
			_ = p.ObserveProperty(types.PropertyReading{ID: "y", Value: x, At: at, Range: rng})
		}
		_ = p.ObserveProperty(types.PropertyReading{ID: "z", Value: 1 - x, At: at, Range: rng})
	}
	h, _ := p.GetHistory()
	byID := map[string]*types.CandidateEdge{}
	for _, c := range h {
		byID[c.CandidateID] = c
	}
	if c := byID["x<->y"]; c == nil || c.Status != types.Deferred || c.Reason != "untested" {
		t.Errorf("x<->y after y fell silent: %+v; want deferred as untested", c)
	}
	if c := byID["x<->z"]; c == nil || c.Status != types.Pending {
		t.Errorf("x<->z: %+v; the freed slot should have found it", c)
	}
	d, _ := p.Discovery()
	if d.TestedPairs > d.EligiblePairs || d.TestedPairs+d.IdlePairs > d.Budget {
		t.Errorf("discovery %+v: tested above eligible, or above the budget", d)
	}
}

// TestDiscoveryCountsIdlePairsApart: pairs whose ends went quiet are reported as
// idle, so tested pairs never exceed the eligible ones.
func TestDiscoveryCountsIdlePairsApart(t *testing.T) {
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.8, 10, 60, 15*time.Second)
	feedUncorrelated(p, map[string]float64{"a": 0.3, "b": 0.3, "c": 0.3, "d": 0.3}, 0, 5)
	feedUncorrelated(p, map[string]float64{"a": 0.3}, 10, 12) // only a keeps reporting
	d, _ := p.Discovery()
	if d.Properties != 1 || d.EligiblePairs != 0 || d.TestedPairs != 0 || d.IdlePairs != 6 {
		t.Errorf("discovery %+v; want 1 property, 0 eligible, 0 tested, 6 idle", d)
	}
}
