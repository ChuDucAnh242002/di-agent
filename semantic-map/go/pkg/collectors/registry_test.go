package collectors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/spec"
	"go.yaml.in/yaml/v3"
)

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	f := func(Env, *yaml.Node) (contracts.CollectorContract, error) { return nil, nil }
	for name, typ := range map[string]string{"empty": "", "duplicate": "cgroup"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: Register(%q) did not panic", name, typ)
				}
			}()
			Register(typ, f)
		}()
	}
}

func TestBuildUnknownType(t *testing.T) {
	_, err := Build(Env{NodeID: "n"}, []spec.CollectorEntry{{Name: "x", Type: "nope"}})
	if err == nil || !strings.Contains(err.Error(), "sources.collectors[0] x") || !strings.Contains(err.Error(), `unknown collector type "nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildEmptyIsNil(t *testing.T) {
	s, err := Build(Env{NodeID: "n"}, nil)
	if s != nil || err != nil {
		t.Fatalf("Build(nil) = %v, %v", s, err)
	}
}

func TestBuildRefusesMissingExecutable(t *testing.T) {
	_, err := Build(Env{NodeID: "n", Interval: time.Second}, []spec.CollectorEntry{
		{Name: "gone", Exec: &spec.ExecSpec{Command: "/nonexistent"}},
	})
	if err == nil || !strings.Contains(err.Error(), "sources.collectors[0] gone") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildClosesBuiltMembersOnError(t *testing.T) {
	var built *ExecCollector
	dir := t.TempDir()
	buildHook = func(e *ExecCollector) {
		e.extraEnv = []string{"DI_FIXTURE=good", "DI_FIXTURE_DIR=" + dir}
		built = e
	}
	t.Cleanup(func() { buildHook = nil })
	_, err := Build(Env{NodeID: "n", Interval: 10 * time.Second}, []spec.CollectorEntry{
		{Name: "fx", Exec: &spec.ExecSpec{Command: os.Args[0], Args: []string{"-test.run=^$"}}},
		{Name: "x", Type: "nope"},
	})
	if err == nil || !strings.Contains(err.Error(), "sources.collectors[1] x") {
		t.Fatalf("err = %v", err)
	}
	if built == nil {
		t.Fatal("the fixture member was never built")
	}
	if _, err := os.Stat(filepath.Join(dir, "hellos")); err != nil {
		t.Fatalf("the fixture was never started: %v", err)
	}
	// Close waits for the process to be reaped, so a closed member has no process.
	built.mu.Lock()
	defer built.mu.Unlock()
	if !built.closed || built.proc != nil {
		t.Fatalf("the member built before the failure was left running (closed=%v)", built.closed)
	}
}

func TestBuildSetsTimeout(t *testing.T) {
	s, err := Build(Env{NodeID: "n", Interval: 4 * time.Second}, []spec.CollectorEntry{
		{Name: "a", Type: "cgroup"}, {Name: "b", Type: "cgroup", Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.members[0].m.Timeout; got != 2*time.Second {
		t.Errorf("default timeout = %s", got)
	}
	if got := s.members[1].m.Timeout; got != time.Second {
		t.Errorf("entry timeout = %s", got)
	}
}

func TestDefaultTimeout(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		10 * time.Second: 5 * time.Second, 4 * time.Second: 2 * time.Second, 0: 5 * time.Second,
	} {
		if got := DefaultTimeout(in); got != want {
			t.Errorf("DefaultTimeout(%s) = %s, want %s", in, got, want)
		}
	}
}
