package collectors

import "time"

// Env is what every collector is built with: the node it observes, the agent's
// collection interval, and where it logs. Logf may be nil, which discards.
type Env struct {
	NodeID   string
	Interval time.Duration
	Logf     func(format string, args ...any)
}
