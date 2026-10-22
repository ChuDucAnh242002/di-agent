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
// for callers that are not a test binary.
type Recorder struct {
	mu      *sync.Mutex
	results *[]Result
	cur     *Result
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder {
	return &Recorder{mu: &sync.Mutex{}, results: &[]Result{}}
}

// Results returns the checks run so far, in the order they finished starting.
func (r *Recorder) Results() []Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Result(nil), *r.results...)
}

func (r *Recorder) Helper() {}

func (r *Recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil {
		r.cur.Failures = append(r.cur.Failures, fmt.Sprintf(format, args...))
	}
}

func (r *Recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(fatalSignal{})
}

func (r *Recorder) Run(name string, fn func(Reporter)) (passed bool) {
	res := &Result{Name: name}
	r.mu.Lock()
	*r.results = append(*r.results, Result{})
	idx := len(*r.results) - 1
	r.mu.Unlock()
	child := &Recorder{mu: r.mu, results: r.results, cur: res}
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(fatalSignal); !ok {
				panic(p)
			}
		}
		r.mu.Lock()
		(*r.results)[idx] = *res
		r.mu.Unlock()
		passed = res.Passed()
	}()
	fn(child)
	return
}
