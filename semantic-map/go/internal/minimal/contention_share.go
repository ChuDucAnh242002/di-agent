package minimal

import "math"

// podCPU is what owedContention needs of one pod promised CPU: its contention (stall
// share less its own throttling), the cores it used, its cpu.weight, and its CPU limit in
// cores (+Inf for none).
type podCPU struct {
	subject                          string
	contention, cores, weight, limit float64
}

// waitingFloor is the contention below which a pod is not waiting for anything.
const waitingFloor = 0.05

// shareSlack is how close to what it is owed a pod's use must come to count as having it.
const shareSlack = 0.9

// owedContention is the node's contention: the worst waiting of a pod that is owed CPU
// another pod holds. What a pod is owed is its weighted share of the pods' CPUs, capped by
// its own limit, found by water-filling: a pod that is not waiting keeps what it uses, a pod
// that has its share (or more) keeps its share, and what is left is shared again among the
// rest, until no pod is settled. The pods left are waiting on less than they are owed.
//
// A pod's own contention cannot tell waiting for CPU another pod holds from waiting on its
// own threads past its share: a six-thread hog on three of six cores waits almost all the
// time. Only the first is relief's to relieve, and capping the hog makes the second worse.
// ok is false when no pod had a reading.
func owedContention(pods []podCPU, cpus float64) (float64, bool) {
	if len(pods) == 0 {
		return 0, false
	}
	active := make([]int, len(pods))
	for i := range pods {
		active[i] = i
	}
	remaining := cpus
	for {
		var w float64
		for _, i := range active {
			w += weightOf(pods[i])
		}
		if w == 0 || len(active) == 0 {
			break
		}
		var keep []int
		settled := 0.0
		for _, i := range active {
			p := pods[i]
			owed := math.Min(math.Max(remaining, 0)*weightOf(p)/w, p.limit)
			switch {
			case p.cores >= shareSlack*owed:
				settled += owed
			case p.contention < waitingFloor:
				settled += p.cores
			default:
				keep = append(keep, i)
			}
		}
		if len(keep) == len(active) {
			break
		}
		remaining -= settled
		active = keep
	}
	worst := 0.0
	for _, i := range active {
		worst = math.Max(worst, pods[i].contention)
	}
	return worst, true
}

// weightOf is a pod's cpu.weight, the cgroup v2 default of 100 when it was not read.
func weightOf(p podCPU) float64 {
	if p.weight > 0 {
		return p.weight
	}
	return 100
}
