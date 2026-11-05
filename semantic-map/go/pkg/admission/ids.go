package admission

import (
	"fmt"
	"sync/atomic"
	"time"
)

// IDs issues decision ids, admit-<epoch>-<seq>, for accepts, declines and
// outcomes alike. The epoch is the agent's start time, so ids stay distinct
// across restarts.
type IDs struct {
	epoch int64
	n     atomic.Uint64
}

// NewIDs starts a sequence at 1 under the given start time.
func NewIDs(start time.Time) *IDs { return &IDs{epoch: start.Unix()} }

// Next returns the next id; safe for concurrent use.
func (g *IDs) Next() string {
	return fmt.Sprintf("admit-%d-%d", g.epoch, g.n.Add(1))
}
