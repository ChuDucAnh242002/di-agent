package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
)

func defaults() spec.Settings {
	return spec.Settings{
		Allow: []string{"cpu.limit"}, Deny: []string{"evict"},
		Floor:             spec.NewFloor(0.1, 0.5),
		MaxActionsPerHour: 6, Cooldown: 10 * time.Minute,
	}
}

func mustNew(t *testing.T, p spec.PolicySpec) *Policy {
	t.Helper()
	pol, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func TestTheFirstMatchSuppliesSettings(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Defaults: defaults(), Rules: []spec.Rule{
		{Match: spec.Match{Name: "batch-*"}, Settings: spec.Settings{Floor: &spec.Floor{CPUCores: f64(0.5)}}},
		{Match: spec.Match{Name: "batch-x"}, Settings: spec.Settings{Floor: &spec.Floor{CPUCores: f64(0.9)}}},
	}})
	e := pol.For(Target{Kind: "pod", Name: "batch-x"})
	if e.Rule != 0 || *e.Settings.Floor.CPUCores != 0.5 {
		t.Fatalf("rule %d floor %v, want rule 0 floor 0.5", e.Rule, e.Settings.Floor)
	}
}

func TestUnsetFieldsComeFromDefaults(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Defaults: defaults(), Rules: []spec.Rule{
		{Match: spec.Match{Name: "a"}, Settings: spec.Settings{Floor: &spec.Floor{CPUCores: f64(0.5)}}},
	}})
	e := pol.For(Target{Kind: "pod", Name: "a"})
	if e.Settings.Cooldown != 10*time.Minute || e.Settings.MaxActionsPerHour != 6 || !e.Allows("cpu.limit") {
		t.Fatalf("defaults not inherited: %+v", e.Settings)
	}
	none := pol.For(Target{Kind: "pod", Name: "zzz"})
	if none.Rule != -1 || *none.Settings.Floor.CPUCores != 0.1 {
		t.Fatalf("unmatched should be defaults: %+v", none)
	}
}

func TestFloorIsNeverNil(t *testing.T) {
	e := mustNew(t, spec.PolicySpec{}).For(Target{Kind: "pod", Name: "a"})
	if e.Settings.Floor == nil {
		t.Fatal("floor nil")
	}
}

func TestFreezeWinsRegardlessOfOrder(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Defaults: defaults(), Rules: []spec.Rule{
		{Match: spec.Match{Name: "a"}},
		{Match: spec.Match{Name: "b"}},
		{Match: spec.Match{Name: "c"}},
		{Match: spec.Match{Name: "a"}, Freeze: true},
	}})
	e := pol.For(Target{Kind: "pod", Name: "a"})
	if !e.Frozen || e.FrozenBy != "rule 3" || e.Rule != 0 || e.Allows("cpu.limit") {
		t.Fatalf("got %+v", e)
	}
}

func TestTheAgentItselfIsAlwaysFrozen(t *testing.T) {
	e := mustNew(t, spec.PolicySpec{}).For(Target{Self: true})
	if !e.Frozen || e.FrozenBy != "built-in: the agent itself" {
		t.Fatalf("got %+v", e)
	}
}

func TestLabelsMatchOneOfAList(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Defaults: defaults(), Rules: []spec.Rule{{
		Match: spec.Match{Labels: map[string]spec.StringList{
			"k8s/priority-class": {"system-node-critical", "system-cluster-critical"}}},
		Freeze: true,
	}}})
	hit := pol.For(Target{Kind: "pod", Name: "a", Labels: map[string]string{"k8s/priority-class": "system-cluster-critical"}})
	miss := pol.For(Target{Kind: "pod", Name: "a", Labels: map[string]string{"k8s/priority-class": "high"}})
	none := pol.For(Target{Kind: "pod", Name: "a"})
	if !hit.Frozen || miss.Frozen || none.Frozen {
		t.Fatalf("hit %v miss %v none %v", hit.Frozen, miss.Frozen, none.Frozen)
	}
}

func TestAnEmptyMatchMatchesEverything(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Rules: []spec.Rule{{Freeze: true}}})
	if !pol.For(Target{Kind: "unit", Name: "x"}).Frozen {
		t.Fatal("empty match should match")
	}
}

func TestKindAndNameMustBothHold(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Rules: []spec.Rule{{Match: spec.Match{Kind: "pod", Name: "a*"}, Freeze: true}}})
	if pol.For(Target{Kind: "unit", Name: "ab"}).Frozen || pol.For(Target{Kind: "pod", Name: "b"}).Frozen {
		t.Fatal("matched when it should not")
	}
	if !pol.For(Target{Kind: "pod", Name: "ab"}).Frozen {
		t.Fatal("did not match")
	}
}

func TestDenyBeatsAllow(t *testing.T) {
	pol := mustNew(t, spec.PolicySpec{Defaults: spec.Settings{
		Allow: []string{"cpu.limit", "evict"}, Deny: []string{"evict"}}})
	e := pol.For(Target{Kind: "pod", Name: "a"})
	if e.Allows("evict") || !e.Allows("cpu.limit") || e.Allows("other") {
		t.Fatalf("got %+v", e.Settings)
	}
}

func TestABadGlobIsRefused(t *testing.T) {
	if _, err := New(spec.PolicySpec{Rules: []spec.Rule{{Match: spec.Match{Name: "[a"}}}}); err == nil {
		t.Fatal("want error")
	}
}

// floorOf is the effective floor's two bounds.
func floorOf(e Effective) (cores, ofOriginal float64) {
	return e.Settings.Floor.Values()
}

func f64(v float64) *float64 { return &v }

func parsedPolicy(t *testing.T, doc string) *Policy {
	t.Helper()
	s, err := spec.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("the spec does not load: %v", err)
	}
	return mustNew(t, s.Policy)
}

// TestARuleFloorMergesFieldByField: the spec's own example names one bound and
// inherits the other, rather than replacing the default floor wholesale.
func TestARuleFloorMergesFieldByField(t *testing.T) {
	pol := parsedPolicy(t, `
policy:
  rules:
    - match: {name: "batch-*"}
      floor: {cpu_cores: 0.5}
    - match: {name: "web-*"}
      floor: {of_original: 0.8}
`)
	if c, o := floorOf(pol.For(Target{Kind: "pod", Name: "batch-x"})); c != 0.5 || o != 0.5 {
		t.Errorf("batch-x floor = {cpu_cores %v, of_original %v}, want {0.5, 0.5}", c, o)
	}
	if c, o := floorOf(pol.For(Target{Kind: "pod", Name: "web-x"})); c != 0.1 || o != 0.8 {
		t.Errorf("web-x floor = {cpu_cores %v, of_original %v}, want {0.1, 0.8}", c, o)
	}
}

// TestMissingDefaultFreezes: policy.rules replaces the defaults, so a file that
// leaves out a default freeze rule is named, one rule at a time.
func TestMissingDefaultFreezes(t *testing.T) {
	if got := MissingDefaultFreezes(spec.Default().Policy); len(got) != 0 {
		t.Fatalf("defaults: missing %v", got)
	}
	got := MissingDefaultFreezes(parsedSpec(t, `
policy:
  rules:
    - match: {name: "batch-*"}
      floor: {cpu_cores: 0.5}
`))
	if len(got) != 2 || !strings.Contains(got[0], "di-agent/freeze") || !strings.Contains(got[1], "system-node-critical") {
		t.Fatalf("missing %v; want both default freeze rules named", got)
	}
	got = MissingDefaultFreezes(parsedSpec(t, `
policy:
  rules:
    - match: {labels: {di-agent/freeze: "true"}}
      freeze: true
`))
	if len(got) != 1 || !strings.Contains(got[0], "k8s/priority-class") {
		t.Fatalf("missing %v; want the priority-class rule", got)
	}
}

func parsedSpec(t *testing.T, doc string) spec.PolicySpec {
	t.Helper()
	s, err := spec.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return s.Policy
}
