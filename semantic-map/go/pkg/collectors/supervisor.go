// Package collectors assembles the agent's metric sources. A Supervisor runs the
// configured members side by side and turns their faults into status instead of a
// halted collection loop.
package collectors

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/types"
)

const (
	// backoffBase and backoffMax bound the wait after consecutive faults: the k-th
	// fault in a row waits min(backoffBase<<(k-1), backoffMax).
	backoffBase = time.Second
	backoffMax  = 5 * time.Minute

	// outOfProcessGrace is added to the supervisor's wait for exec and http members.
	// Those adapters enforce Timeout themselves and report it as their own error; the
	// grace lets that clearer error arrive before the supervisor gives up on the call.
	outOfProcessGrace = 500 * time.Millisecond

	// defaultTimeout applies to a member built without one, so a zero value cannot
	// mean "wait forever" and stall every tick behind one member.
	defaultTimeout = 5 * time.Second

	stateStarting = "starting"
	stateOK       = "ok"
	stateBackoff  = "backoff"
)

// Member is one configured source as the Supervisor sees it.
type Member struct {
	Name string
	Kind string // "type", "exec" or "http"
	Type string // the registered type name; empty unless Kind == "type"
	C    contracts.CollectorContract
	// Timeout bounds one Collect call. Exec and http adapters enforce it themselves;
	// the supervisor waits a short grace longer for them.
	Timeout time.Duration
}

// MemberStatus is one member's entry in GET /collectors.
type MemberStatus struct {
	Name             string             `json:"name"`
	Kind             string             `json:"kind"`
	Type             string             `json:"type"`
	State            string             `json:"state"`
	LastSuccessUnix  int64              `json:"last_success_unix"`
	LastError        string             `json:"last_error"`
	LastErrorUnix    int64              `json:"last_error_unix"`
	Samples          int64              `json:"samples"`
	Violations       int64              `json:"violations"`
	OwnershipDrops   int64              `json:"ownership_drops"`
	Restarts         int64              `json:"restarts"`
	BackoffUntilUnix int64              `json:"backoff_until_unix"`
	Available        []types.MetricType `json:"available"`
}

// restarter is implemented by members that restart an underlying process; the
// count is reported in status.
type restarter interface{ Restarts() int64 }

// declaration is the unit and range a member first gave a metric type; later
// samples of that type must repeat it.
type declaration struct {
	unit     string
	hasRange bool
	rng      [2]float64
}

type memberState struct {
	m    Member
	wait time.Duration

	state           string
	lastSuccessUnix int64
	lastError       string
	lastErrorUnix   int64
	faults          int
	backoffUntil    time.Time
	inflight        bool

	samples, violations, ownershipDrops int64

	// available is the member's AvailableMetrics answer, read in the member's own
	// goroutine right after a successful Collect. Asking it from elsewhere could run
	// concurrently with an abandoned call still inside the member.
	available []types.MetricType

	decl           map[types.MetricType]declaration
	loggedOwnDrop  map[types.MetricType]bool
	loggedViolKind map[string]bool
}

type callResult struct {
	samples   []*types.MetricSample
	available []types.MetricType
	err       error
}

// Supervisor collects from every member each tick and implements
// CollectorContract itself, so the collection loop sees one source.
//
// Guarantees beyond the contract:
//   - A member's error, panic or timeout costs only that member's samples for the
//     tick; Collect still returns the others with a nil error. The faulting member
//     backs off exponentially and is reported through Status and the log.
//   - At most one call per member is in flight. A Go member cannot be cancelled,
//     so a call that outlives its timeout is abandoned and the member is skipped
//     until the call returns.
//   - Every delivered sample satisfies the CollectorContract's declared-meaning
//     and node-identity guarantees; a sample that does not is dropped and counted,
//     and the rest of its batch is kept.
//   - A node-level metric type has one producer: the earliest-listed member that
//     has emitted it. Two producers alternating on one property would mix in its
//     EMA, so ownership does not move while the owner is in backoff.
type Supervisor struct {
	nodeID string
	sid    string
	logf   func(string, ...any)
	now    func() time.Time

	// tick serialises Collect; mu guards the member state, which Status reads and
	// abandoned member goroutines update when they finally return.
	tick    sync.Mutex
	mu      sync.Mutex
	members []*memberState
	owner   map[types.MetricType]int
}

// NewSupervisor wraps members, in configuration order, for the node nodeID. logf
// receives fault, violation and ownership messages; nil discards them.
func NewSupervisor(nodeID string, members []Member, logf func(string, ...any)) *Supervisor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Supervisor{
		nodeID: nodeID,
		logf:   logf,
		now:    time.Now,
		owner:  make(map[types.MetricType]int),
	}
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
		timeout := m.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		wait := timeout
		if m.Kind == "exec" || m.Kind == "http" {
			wait += outOfProcessGrace
		}
		s.members = append(s.members, &memberState{
			m:              m,
			wait:           wait,
			state:          stateStarting,
			available:      safeAvailable(m.C),
			decl:           make(map[types.MetricType]declaration),
			loggedOwnDrop:  make(map[types.MetricType]bool),
			loggedViolKind: make(map[string]bool),
		})
	}
	s.sid = "supervisor:" + strings.Join(names, ",")
	return s
}

// safeAvailable asks a member that has not been called yet for its types; a panic
// there leaves the set empty until the first successful Collect refreshes it.
func safeAvailable(c contracts.CollectorContract) (out []types.MetricType) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	return append([]types.MetricType(nil), c.AvailableMetrics()...)
}

// SourceID is "supervisor:" followed by the member names in list order.
func (s *Supervisor) SourceID() string { return s.sid }

// AvailableMetrics is the union of the members' answers in first-seen order. Each
// member's answer is the one it gave after its last successful Collect, which is
// what the contract's "declared types" guarantee is stated against.
func (s *Supervisor) AvailableMetrics() []types.MetricType {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[types.MetricType]bool)
	var out []types.MetricType
	for _, ms := range s.members {
		for _, mt := range ms.available {
			if !seen[mt] {
				seen[mt] = true
				out = append(out, mt)
			}
		}
	}
	return out
}

// Collect runs every due member concurrently and returns the samples that passed
// enforcement and ownership, concatenated in member list order. The error is
// always nil: member faults are status, not a reason to stop the loop.
func (s *Supervisor) Collect() ([]*types.MetricSample, error) {
	s.tick.Lock()
	defer s.tick.Unlock()

	now := s.now()
	chans := make([]chan callResult, len(s.members))
	s.mu.Lock()
	for i, ms := range s.members {
		if ms.inflight {
			// The previous call was abandoned and is still running; calling again
			// would stack goroutines inside a member that is already stuck.
			ms.state = stateBackoff
			continue
		}
		if now.Before(ms.backoffUntil) {
			continue
		}
		ms.inflight = true
		chans[i] = make(chan callResult, 1)
		go s.call(ms, chans[i])
	}
	s.mu.Unlock()

	// Wait for each member against its own deadline, measured from the common start,
	// so the tick lasts at most as long as the longest wait.
	start := time.Now()
	results := make([]*callResult, len(s.members))
	timedOut := make([]bool, len(s.members))
	for i, ch := range chans {
		if ch == nil {
			continue
		}
		// Look for a finished call before arming the timer. Waiting on an earlier
		// member may have used up this member's budget, which makes the timer ready
		// at once; select would then pick between it and an on-time result at random
		// and fault a member that answered in time.
		select {
		case r := <-ch:
			results[i] = &r
			continue
		default:
		}
		timer := time.NewTimer(s.members[i].wait - time.Since(start))
		select {
		case r := <-ch:
			results[i] = &r
		case <-timer.C:
			timedOut[i] = true
		}
		timer.Stop()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*types.MetricSample
	for i, ms := range s.members {
		switch {
		case timedOut[i]:
			s.fault(ms, now, fmt.Sprintf("timeout after %s", ms.wait))
		case results[i] == nil:
			// Not called this tick.
		case results[i].err != nil:
			s.fault(ms, now, results[i].err.Error())
		default:
			ms.faults = 0
			ms.backoffUntil = time.Time{}
			ms.state = stateOK
			ms.lastSuccessUnix = now.Unix()
			ms.available = results[i].available
			out = s.admit(i, ms, results[i].samples, out)
		}
	}
	return out, nil
}

// call runs one Collect in the member's goroutine. A panic becomes a fault rather
// than taking the agent down; inflight is cleared whenever the call ends, even if
// the tick that started it has already given up on it.
func (s *Supervisor) call(ms *memberState, ch chan<- callResult) {
	var r callResult
	func() {
		defer func() {
			if v := recover(); v != nil {
				r = callResult{err: fmt.Errorf("panic: %v", v)}
			}
		}()
		r.samples, r.err = ms.m.C.Collect()
		if r.err == nil {
			r.available = append([]types.MetricType(nil), ms.m.C.AvailableMetrics()...)
		}
	}()
	s.mu.Lock()
	ms.inflight = false
	s.mu.Unlock()
	ch <- r // buffered: never blocks, even when nobody is waiting any more
}

// fault records a failed call and schedules the member's next attempt.
func (s *Supervisor) fault(ms *memberState, now time.Time, msg string) {
	ms.faults++
	k := ms.faults
	if k > 20 { // keeps the shift from overflowing; 1s<<19 is already past the cap
		k = 20
	}
	d := backoffBase << (k - 1)
	if d > backoffMax {
		d = backoffMax
	}
	ms.backoffUntil = now.Add(d)
	ms.state = stateBackoff
	ms.lastError = msg
	ms.lastErrorUnix = now.Unix()
	s.logf("collector[%s]: %s (fault %d, backing off %s)", ms.m.Name, msg, ms.faults, d)
}

// admit appends the member's samples that pass enforcement and ownership to out.
// Members are admitted in list order, so within a tick an earlier member claims a
// type before a later one is checked against it.
func (s *Supervisor) admit(idx int, ms *memberState, samples []*types.MetricSample, out []*types.MetricSample) []*types.MetricSample {
	for _, smp := range samples {
		if kind := s.violation(ms, smp); kind != "" {
			ms.violations++
			if !ms.loggedViolKind[kind] {
				ms.loggedViolKind[kind] = true
				mt := types.MetricType("")
				if smp != nil {
					mt = smp.MetricType
				}
				s.logf("collector[%s]: dropped sample %q: %s (further drops of this kind are counted, not logged)", ms.m.Name, mt, kind)
			}
			continue
		}
		if smp.Subject == "" && !s.owns(idx, smp.MetricType) {
			ms.ownershipDrops++
			if !ms.loggedOwnDrop[smp.MetricType] {
				ms.loggedOwnDrop[smp.MetricType] = true
				s.logf("collector[%s]: dropped %q: owned by collector[%s]", ms.m.Name, smp.MetricType, s.members[s.owner[smp.MetricType]].m.Name)
			}
			continue
		}
		ms.samples++
		out = append(out, smp)
	}
	return out
}

// owns applies the ownership rule for an unscoped sample of mt from member idx and
// reports whether it is kept. Ownership moves only towards the front of the list.
func (s *Supervisor) owns(idx int, mt types.MetricType) bool {
	cur, ok := s.owner[mt]
	if !ok || idx <= cur {
		s.owner[mt] = idx
		return true
	}
	return false
}

// violation returns why smp breaks the collector contract, or "" if it does not.
// The first valid declaration of a type's unit and range is recorded for the
// member and later samples are held to it.
func (s *Supervisor) violation(ms *memberState, smp *types.MetricSample) string {
	switch {
	case smp == nil:
		return "nil sample"
	case !types.ValidMetricType(string(smp.MetricType)):
		return "invalid metric_type"
	case !types.ValidSubject(smp.Subject):
		return "invalid subject"
	case smp.Unit == "":
		return "missing unit"
	case smp.Range != nil && !(smp.Range[0] < smp.Range[1]):
		return "empty range"
	case smp.NodeID != s.nodeID:
		return "node_id is not this node's"
	}
	d := declaration{unit: smp.Unit, hasRange: smp.Range != nil}
	if d.hasRange {
		d.rng = *smp.Range
	}
	prev, seen := ms.decl[smp.MetricType]
	if !seen {
		ms.decl[smp.MetricType] = d
		return ""
	}
	if prev.unit != d.unit {
		return "unit differs from first declaration"
	}
	if prev.hasRange != d.hasRange || prev.rng != d.rng {
		return "range differs from first declaration"
	}
	return ""
}

// Status reports every member in list order.
func (s *Supervisor) Status() []MemberStatus {
	s.mu.Lock()
	out := make([]MemberStatus, len(s.members))
	for i, ms := range s.members {
		st := MemberStatus{
			Name:            ms.m.Name,
			Kind:            ms.m.Kind,
			Type:            ms.m.Type,
			State:           ms.state,
			LastSuccessUnix: ms.lastSuccessUnix,
			LastError:       ms.lastError,
			LastErrorUnix:   ms.lastErrorUnix,
			Samples:         ms.samples,
			Violations:      ms.violations,
			OwnershipDrops:  ms.ownershipDrops,
			Available:       append([]types.MetricType{}, ms.available...),
		}
		if !ms.backoffUntil.IsZero() {
			st.BackoffUntilUnix = ms.backoffUntil.Unix()
		}
		out[i] = st
	}
	s.mu.Unlock()
	// Restarts is asked outside mu: an exec member may hold its own lock through a
	// restart lasting seconds, and holding mu meanwhile would stall the tick.
	for i, ms := range s.members {
		if r, ok := ms.m.C.(restarter); ok {
			out[i].Restarts = r.Restarts()
		}
	}
	return out
}

// Close closes every member that holds resources (a child process, a listener)
// and returns their errors joined.
func (s *Supervisor) Close() error {
	var errs []error
	for _, ms := range s.members {
		if c, ok := ms.m.C.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, fmt.Errorf("collector[%s]: %w", ms.m.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}
