package minimal

import (
	"math"
	"testing"
)

// TestAPodThatGetsItsShareIsNotContended: a hog of six threads beside a victim of three,
// on six CPUs at equal weight, each gets three cores. The hog waits almost all the time —
// on its own threads, past its share — and the victim hardly at all. Neither is owed CPU,
// so the node is not contended; counting the hog's waiting made relief cap it, which made
// it wait more, and every action was reverted (case-study trials, 2026-10-20).
func TestAPodThatGetsItsShareIsNotContended(t *testing.T) {
	got, ok := owedContention([]podCPU{
		{subject: "pod:hog", contention: 0.99, cores: 3, weight: 100, limit: 4.5},
		{subject: "pod:victim", contention: 0.015, cores: 3, weight: 100, limit: 6},
	}, 6)
	if !ok || got != 0 {
		t.Fatalf("node contention %v (ok=%v); want 0: each pod gets what it is owed", got, ok)
	}
}

// TestAPodWaitingBelowItsShareIsContended: a pod that waits while getting less than its
// weighted share is owed CPU another pod holds; the node's contention is its waiting.
func TestAPodWaitingBelowItsShareIsContended(t *testing.T) {
	got, _ := owedContention([]podCPU{
		{subject: "pod:hog", contention: 0.3, cores: 4.6, weight: 100, limit: 6},
		{subject: "pod:victim", contention: 0.6, cores: 1.2, weight: 100, limit: 6},
		{subject: "pod:dns", contention: 0, cores: 0.01, weight: 100, limit: math.Inf(1)},
	}, 6)
	if got != 0.6 {
		t.Fatalf("node contention %v; want the victim's 0.6 — it waits on 1.2 cores of a share near 3", got)
	}
}

// TestAShareIsWeighted: a pod with a larger CPU request has a larger weight, and is owed
// more: at 1.5 cores it is under its share of 6*300/400 = 4.5 and contended; at equal
// weight 1.5 of 3 would be too, and at 3.5 of an equal 3 it would not.
func TestAShareIsWeighted(t *testing.T) {
	heavy, _ := owedContention([]podCPU{
		{subject: "pod:a", contention: 0.5, cores: 3.5, weight: 100, limit: 6},
		{subject: "pod:b", contention: 0.4, cores: 2.5, weight: 300, limit: 6},
	}, 6)
	if heavy != 0.4 {
		t.Fatalf("node contention %v; want b's 0.4 — owed 4.5 at weight 300, it has 2.5; a has more than its 1.5", heavy)
	}
}

// TestALimitCapsWhatAPodIsOwed: a pod at its own CPU limit is owed no more than the limit,
// whatever its weight; its waiting is its throttling, which contention already excludes.
func TestALimitCapsWhatAPodIsOwed(t *testing.T) {
	got, _ := owedContention([]podCPU{
		{subject: "pod:capped", contention: 0.5, cores: 1, weight: 100, limit: 1},
		{subject: "pod:other", contention: 0, cores: 2, weight: 100, limit: 6},
	}, 6)
	if got != 0 {
		t.Fatalf("node contention %v; want 0 — the capped pod has all its limit allows", got)
	}
}
