package compliance

import (
	"fmt"
	"sync"
	"testing"
)

// Reporter is what the assertions need from a test runner. It is the part of
// *testing.T the suite uses, so the same checks can certify a collector under
// `go test` and, through a Recorder, from a command line with no test binary.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
	// Fatalf records a failure and ends the current check, not the whole run.
	Fatalf(format string, args ...any)
	// Run executes fn as the named check and reports whether it passed.
	Run(name string, fn func(r Reporter)) bool
}

// TReporter adapts a *testing.T: checks become subtests and Fatalf is t.Fatalf.
func TReporter(t *testing.T) Reporter { return &tReporter{t} }

type tReporter struct{ t *testing.T }

func (r *tReporter) Helper()                   { r.t.Helper() }
func (r *tReporter) Errorf(f string, a ...any) { r.t.Helper(); r.t.Errorf(f, a...) }
func (r *tReporter) Fatalf(f string, a ...any) { r.t.Helper(); r.t.Fatalf(f, a...) }
func (r *tReporter) Run(name string, fn func(Reporter)) bool {
	return r.t.Run(name, func(t *testing.T) { fn(&tReporter{t}) })
}

// testingT lets RunCollectorCompliance hand a collector factory the subtest's own
// *testing.T, which factories use for t.TempDir and t.Cleanup.
func testingT(r Reporter) *testing.T {
	if tr, ok := r.(*tReporter); ok {
		return tr.t
	}
	return nil
}

// fatalSignal unwinds a check whose Fatalf was called on a non-testing reporter;
// that reporter's Run recovers it.
type fatalSignal struct{}

// Result is the outcome of one named check.
type Result struct {
	Name     string
	Failures []string
}

// Passed reports whether the check raised no failure.
func (r Result) Passed() bool { return len(r.Failures) == 0 }

// Recorder is a Reporter that keeps each check's outcome instead of printing it,
// for callers that are not a test binary. The zero value is ready to use.
//
// A failure raised outside any check — Errorf or Fatalf on the Recorder itself —
// is recorded as a result named RootCheck, so it is not lost. A root-level Fatalf
// cannot end a check that does not exist, so it records and returns.
type Recorder struct {
	shared *recorderState
	once   sync.Once
	cur    *Result // nil on the root
}

type recorderState struct {
	mu      sync.Mutex
	results []Result
	root    int // index of the RootCheck result, or -1
}

// RootCheck names the result holding failures raised outside any check.
const RootCheck = "(root)"

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) state() *recorderState {
	r.once.Do(func() {
		if r.shared == nil {
			r.shared = &recorderState{root: -1}
		}
	})
	return r.shared
}

// Results returns the checks run so far, in the order they started.
func (r *Recorder) Results() []Result {
	st := r.state()
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]Result, len(st.results))
	for i, res := range st.results {
		res.Failures = append([]string(nil), res.Failures...)
		out[i] = res
	}
	return out
}

func (r *Recorder) Helper() {}

func (r *Recorder) Errorf(format string, args ...any) {
	st := r.state()
	st.mu.Lock()
	defer st.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if r.cur != nil {
		r.cur.Failures = append(r.cur.Failures, msg)
		return
	}
	if st.root < 0 {
		st.results = append(st.results, Result{Name: RootCheck})
		st.root = len(st.results) - 1
	}
	st.results[st.root].Failures = append(st.results[st.root].Failures, msg)
}

func (r *Recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	if r.cur != nil {
		panic(fatalSignal{})
	}
}

func (r *Recorder) Run(name string, fn func(Reporter)) (passed bool) {
	st := r.state()
	res := &Result{Name: name}
	st.mu.Lock()
	st.results = append(st.results, Result{Name: name})
	idx := len(st.results) - 1
	st.mu.Unlock()
	child := &Recorder{shared: st, cur: res}
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(fatalSignal); !ok {
				panic(p)
			}
		}
		st.mu.Lock()
		st.results[idx] = *res
		st.mu.Unlock()
		passed = res.Passed()
	}()
	fn(child)
	return
}
