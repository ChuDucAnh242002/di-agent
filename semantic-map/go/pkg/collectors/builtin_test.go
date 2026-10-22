package collectors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
	"go.yaml.in/yaml/v3"
)

func node(t *testing.T, src string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	if n.Kind == yaml.DocumentNode {
		return *n.Content[0]
	}
	return n
}

func TestBuiltinConfigStrict(t *testing.T) {
	_, err := Build(Env{NodeID: "n"}, []spec.CollectorEntry{{Name: "cg", Type: "cgroup", Config: node(t, "roots: /x")}})
	if err == nil || !strings.Contains(err.Error(), "roots") || !strings.Contains(err.Error(), "sources.collectors[0] cg") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuiltinCgroupDefaults(t *testing.T) {
	root := newFakeCgroupRoot(t)
	s, err := Build(Env{NodeID: "n", Interval: time.Second}, []spec.CollectorEntry{
		{Name: "cg", Type: "cgroup", Config: node(t, "root: "+root)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.members[0].m.C.Collect() //nolint:errcheck // the first call only stores the baseline
	time.Sleep(2 * time.Millisecond)
	got, err := s.members[0].m.C.Collect()
	if err != nil || len(got) == 0 {
		t.Fatalf("Collect = %d samples, %v", len(got), err)
	}
}

func TestBuiltinCgroupBadUnits(t *testing.T) {
	_, err := Build(Env{NodeID: "n"}, []spec.CollectorEntry{{Name: "cg", Type: "cgroup", Config: node(t, `units: ["["]`)}})
	if err == nil || !strings.Contains(err.Error(), "sources.collectors[0] cg") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuiltinNetdataRequiresURL(t *testing.T) {
	_, err := Build(Env{NodeID: "n"}, []spec.CollectorEntry{{Name: "nd", Type: "netdata"}})
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Build(Env{NodeID: "n"}, []spec.CollectorEntry{{Name: "nd", Type: "netdata", Config: node(t, "url: http://localhost:19999")}}); err != nil {
		t.Fatalf("with url: %v", err)
	}
}

func TestBuiltinScriptIntervalMismatchRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	sc := `{"name":"t","seed":1,"tick_seconds":10,"duration_seconds":600,"noise":0.01,
"node":{"node_cpu":{"coupling":"sum","base":0.1,"of":"cpu_utilization"}},
"subjects":[{"id":"pod:a","arrive":0,"properties":{"cpu_utilization":{"pattern":"sine","min":0.1,"max":0.6,"period":120}}}],
"expect":{"admitted_within_ticks":1}}`
	if err := os.WriteFile(p, []byte(sc), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := spec.CollectorEntry{Name: "sc", Type: "script", Config: node(t, "path: "+p)}
	_, err := Build(Env{NodeID: "n", Interval: 5 * time.Second}, []spec.CollectorEntry{entry})
	if err == nil || !strings.Contains(err.Error(), "sources.interval") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Build(Env{NodeID: "n", Interval: 10 * time.Second}, []spec.CollectorEntry{entry}); err != nil {
		t.Fatalf("matching interval: %v", err)
	}
}
