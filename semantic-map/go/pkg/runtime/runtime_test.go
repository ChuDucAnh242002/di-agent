package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
)

func f64(v float64) *float64 { return &v }

func newDry(t *testing.T, node string, subjects ...string) *DryRun {
	t.Helper()
	r, err := Build("dry-run", Env{NodeID: node, Subjects: func() []string { return subjects }})
	if err != nil {
		t.Fatal(err)
	}
	return r.(*DryRun)
}

func limitOf(t *testing.T, r Runtime, subject string) *float64 {
	t.Helper()
	ws, err := r.Workloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Subject == subject {
			return w.CPU.Limit
		}
	}
	t.Fatalf("no workload %q", subject)
	return nil
}

func TestDryRunAppliesAndReverts(t *testing.T) {
	ctx := context.Background()
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a", CPU: Resource{Limit: f64(1.0)}, Can: map[string]string{ActionCPULimit: ""}})
	exp := time.Unix(1000, 0)
	u, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 0.5, Expires: exp, DecisionID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Runtime != "dry-run" || u.NodeID != "n1" || u.Subject != "pod:a" || u.Applied != 0.5 || !u.Expires.Equal(exp) || u.DecisionID != "d1" {
		t.Fatalf("undo %+v", u)
	}
	if u.Original.Limit == nil || *u.Original.Limit != 1.0 {
		t.Fatalf("original %+v", u.Original)
	}
	if l := limitOf(t, d, "pod:a"); l == nil || *l != 0.5 {
		t.Fatalf("limit %v", l)
	}
	if err := d.Revert(ctx, u); err != nil {
		t.Fatal(err)
	}
	if l := limitOf(t, d, "pod:a"); l == nil || *l != 1.0 {
		t.Fatalf("limit %v", l)
	}
}

func TestRevertRestoresNoLimit(t *testing.T) {
	ctx := context.Background()
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a"})
	u, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if u.Original.Limit != nil {
		t.Fatalf("original %+v", u.Original)
	}
	if err := d.Revert(ctx, u); err != nil {
		t.Fatal(err)
	}
	if l := limitOf(t, d, "pod:a"); l != nil {
		t.Fatalf("limit %v, want nil", *l)
	}
}

func TestChangedIsThisNodesOnly(t *testing.T) {
	ctx := context.Background()
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a"})
	d.PutUndo(Undo{Runtime: "dry-run", NodeID: "other", Subject: "pod:b", Action: ActionCPULimit})
	if _, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 0.5}); err != nil {
		t.Fatal(err)
	}
	mine, _ := d.Changed(ctx, "n1")
	if len(mine) != 1 || mine[0].Subject != "pod:a" {
		t.Fatalf("n1: %+v", mine)
	}
	theirs, _ := d.Changed(ctx, "other")
	if len(theirs) != 1 || theirs[0].Subject != "pod:b" {
		t.Fatalf("other: %+v", theirs)
	}
}

func TestChangedDropsRevertedAndSorts(t *testing.T) {
	ctx := context.Background()
	d := newDry(t, "n1")
	t0 := time.Unix(100, 0)
	for _, u := range []Undo{
		{NodeID: "n1", Subject: "pod:b", Expires: t0},
		{NodeID: "n1", Subject: "pod:a", Expires: t0},
		{NodeID: "n1", Subject: "pod:c", Expires: t0.Add(-time.Second)},
	} {
		d.Put(Workload{Subject: u.Subject, Name: u.Subject})
		d.PutUndo(u)
	}
	got, _ := d.Changed(ctx, "n1")
	if len(got) != 3 || got[0].Subject != "pod:c" || got[1].Subject != "pod:a" || got[2].Subject != "pod:b" {
		t.Fatalf("order %+v", got)
	}
	if err := d.Revert(ctx, got[0]); err != nil {
		t.Fatal(err)
	}
	got, _ = d.Changed(ctx, "n1")
	if len(got) != 2 {
		t.Fatalf("after revert %+v", got)
	}
}

func TestAGoneWorkloadSaysSo(t *testing.T) {
	ctx := context.Background()
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a"})
	u, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	d.Remove("pod:a")
	if err := d.Revert(ctx, u); !errors.Is(err, ErrGone) {
		t.Fatalf("revert: %v", err)
	}
	if _, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 0.5}); !errors.Is(err, ErrGone) {
		t.Fatalf("apply: %v", err)
	}
}

func TestApplyRefusesOtherActions(t *testing.T) {
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a"})
	_, err := d.Apply(context.Background(), "pod:a", Change{Action: "evict"})
	if err == nil || !strings.Contains(err.Error(), "evict") {
		t.Fatalf("err %v", err)
	}
}

func TestWorkloadsAreCopies(t *testing.T) {
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a", Labels: map[string]string{"k": "v"}, Can: map[string]string{ActionCPULimit: ""}, CPU: Resource{Limit: f64(1)}})
	ws, _ := d.Workloads(context.Background())
	*ws[0].CPU.Limit = 9
	ws[0].Labels["k"] = "x"
	ws[0].Can["zzz"] = ""
	again, _ := d.Workloads(context.Background())
	if *again[0].CPU.Limit != 1 || again[0].Labels["k"] != "v" {
		t.Fatalf("state leaked: %+v", again[0])
	}
	if _, ok := again[0].Can["zzz"]; ok {
		t.Fatal("Can leaked")
	}
}

func TestAnUnknownRuntimeNamesTheKnown(t *testing.T) {
	_, err := Build("nope", Env{})
	if err == nil || !strings.Contains(err.Error(), "dry-run") {
		t.Fatalf("err %v", err)
	}
}

func TestTheMapsSubjectsBecomeWorkloads(t *testing.T) {
	d := newDry(t, "n1", "pod:a")
	ws, err := d.Workloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Subject != "pod:a" || ws[0].Name != "pod:a" || ws[0].CPU.Limit != nil {
		t.Fatalf("ws %+v", ws)
	}
	if _, ok := ws[0].Can[ActionCPULimit]; !ok {
		t.Fatalf("can %+v", ws[0].Can)
	}
}

// TestASecondChangeKeepsTheFirstOriginal: a workload stepped twice by this node is
// given back, in one revert, what it had before the first step.
func TestASecondChangeKeepsTheFirstOriginal(t *testing.T) {
	ctx := context.Background()
	d := newDry(t, "n1")
	d.Put(Workload{Subject: "pod:a", Name: "a", CPU: Resource{Limit: f64(2.0)}})
	if _, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 1.5, DecisionID: "d1"}); err != nil {
		t.Fatal(err)
	}
	exp := time.Unix(2000, 0)
	u, err := d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 1.0, Expires: exp, DecisionID: "d2"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Original.Limit == nil || *u.Original.Limit != 2.0 || u.Applied != 1.0 || u.DecisionID != "d2" || !u.Expires.Equal(exp) {
		t.Fatalf("undo %+v; want the first original (2.0) with the second change", u)
	}
	ch, _ := d.Changed(ctx, "n1")
	if len(ch) != 1 || *ch[0].Original.Limit != 2.0 || ch[0].Applied != 1.0 || ch[0].DecisionID != "d2" {
		t.Fatalf("outstanding %+v; want one record, updated", ch)
	}
	if err := d.Revert(ctx, u); err != nil {
		t.Fatal(err)
	}
	if l := limitOf(t, d, "pod:a"); l == nil || *l != 2.0 {
		t.Fatalf("limit %v; want 2.0", l)
	}

	// Another node's outstanding change is not this node's first original.
	d.PutUndo(Undo{NodeID: "other", Subject: "pod:a", Original: Resource{Limit: f64(9)}})
	u, _ = d.Apply(ctx, "pod:a", Change{Action: ActionCPULimit, CPULimit: 1.0})
	if *u.Original.Limit != 2.0 {
		t.Fatalf("original %v; want the current limit, not another node's", *u.Original.Limit)
	}
}

// TestThePolicyKnowsEveryAction: the spec refuses an allow or deny entry that is
// not in its own list, which it keeps because this package imports it; every
// action a runtime performs must be there, or no policy could allow it.
func TestThePolicyKnowsEveryAction(t *testing.T) {
	for _, a := range []string{ActionCPULimit} {
		if !slices.Contains(spec.PolicyActions, a) {
			t.Errorf("spec.PolicyActions %v lacks %q", spec.PolicyActions, a)
		}
	}
}
