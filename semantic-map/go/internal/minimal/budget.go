package minimal

import "time"

// The pair budget's default follows the machine: a fixed share of its memory and of
// its CPU per collection tick, whichever allows fewer pairs. It is fixed at start
// rather than adapting while the agent runs, so the budget a run used is one number
// in its log and in /state, not a trajectory.
const (
	// PairMemoryShare is the share of the machine's memory discovery may hold in
	// tested pairs.
	PairMemoryShare = 0.01
	// PairCPUShare is the share of one collection tick, across every core, discovery
	// may spend folding readings into tested pairs.
	PairCPUShare = 0.01
	// pairCPUCost is one tested pair's work per tick, set an order of magnitude above
	// the ~0.5 µs measured on an Apple M-series core so a Raspberry Pi-class core is
	// not overcommitted.
	pairCPUCost = 5 * time.Microsecond
	// FallbackPairBudget is used when the machine's memory cannot be read.
	FallbackPairBudget = 4096
)

// PairBytes is one tested pair's memory with a window of bufSize pairs: two float64
// series plus the pair's bookkeeping, about 2.6 KB at the default 120, as measured.
func PairBytes(bufSize int) int { return 16*bufSize + 768 }

// DerivePairBudget is the default pair budget for a machine with memBytes of memory
// and cores CPUs, collecting every tick. A zero memBytes means the memory could not
// be read and gives FallbackPairBudget.
func DerivePairBudget(memBytes uint64, cores int, tick time.Duration, bufSize int) int {
	if memBytes == 0 {
		return FallbackPairBudget
	}
	byMemory := int(PairMemoryShare * float64(memBytes) / float64(PairBytes(bufSize)))
	if cores < 1 {
		cores = 1
	}
	if tick <= 0 {
		return byMemory
	}
	byCPU := int(PairCPUShare * float64(cores) * float64(tick) / float64(pairCPUCost))
	if byCPU < byMemory {
		return byCPU
	}
	return byMemory
}
