package collectors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/ingest/client"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/types"
	"go.yaml.in/yaml/v3"
)

// logSink captures Logf lines for assertions.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// fixture builds an ExecCollector named "fx" for node "n1" that runs this test
// binary in the given fixture mode, with its state directory under t.TempDir.
func fixture(t *testing.T, mode string, timeout time.Duration, sink *logSink) (*ExecCollector, string) {
	t.Helper()
	dir := t.TempDir()
	logf := func(string, ...any) {}
	if sink != nil {
		logf = sink.logf
	}
	e := NewExecCollector(Env{NodeID: "n1", Interval: 10 * time.Second, Logf: logf}, "fx",
		spec.ExecSpec{Command: os.Args[0], Args: []string{"-test.run=^$"}}, timeout, map[string]any{"device": 0})
	e.extraEnv = []string{"DI_FIXTURE=" + mode, "DI_FIXTURE_DIR=" + dir}
	t.Cleanup(func() { e.Close() })
	return e, dir
}

func exited(p *execProc) bool {
	select {
	case <-p.done:
		return p.cmd.ProcessState != nil
	default:
		return false
	}
}

func TestExecHelloAndCollect(t *testing.T) {
	e, _ := fixture(t, "good", 2*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e.SourceID() != "fx" {
		t.Fatalf("SourceID = %q", e.SourceID())
	}
	got, err := e.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2", len(got))
	}
	for _, s := range got {
		if s.Source != "fx" || s.NodeID != "n1" || s.EventID == "" {
			t.Fatalf("unstamped sample %+v", s)
		}
	}
	want := client.EventID("fx", "n1", "", "fx_load", time.Unix(got[0].TimestampUnix, 0))
	if got[0].MetricType != "fx_load" || got[0].EventID != want {
		t.Fatalf("EventID = %q, want %q", got[0].EventID, want)
	}
	av := e.AvailableMetrics()
	if len(av) != 2 || av[0] != "fx_load" || av[1] != "fx_share" {
		t.Fatalf("AvailableMetrics = %v", av)
	}
}

func TestExecHelloErrorRefused(t *testing.T) {
	e, _ := fixture(t, "hello-error", 2*time.Second, nil)
	err := e.Start()
	if err == nil || !strings.Contains(err.Error(), "fixture refuses: no device") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestExecProtocolMismatchRefused(t *testing.T) {
	e, _ := fixture(t, "bad-protocol", 2*time.Second, nil)
	err := e.Start()
	if err == nil || !strings.Contains(err.Error(), "protocol 2") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestExecMissingProgramRefused(t *testing.T) {
	e := NewExecCollector(Env{NodeID: "n1"}, "fx", spec.ExecSpec{Command: filepath.Join(t.TempDir(), "absent")}, time.Second, nil)
	defer e.Close()
	if err := e.Start(); err == nil {
		t.Fatal("Start of a missing program succeeded")
	}
}

func TestExecGarbageLineIsAFault(t *testing.T) {
	e, _ := fixture(t, "garbage", 2*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Collect(); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("Collect error = %v, want a JSON fault", err)
	}
	if e.Restarts() != 0 {
		t.Fatalf("Restarts = %d before the next Collect", e.Restarts())
	}
	if _, err := e.Collect(); err == nil {
		t.Fatal("second Collect of garbage succeeded")
	}
	if e.Restarts() != 1 {
		t.Fatalf("Restarts = %d, want 1", e.Restarts())
	}
}

func TestExecTimeoutKillsAndRestarts(t *testing.T) {
	e, _ := fixture(t, "hang", 200*time.Millisecond, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := e.proc
	start := time.Now()
	_, err := e.Collect()
	if err == nil {
		t.Fatal("Collect of a hanging plugin succeeded")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Collect took %s", d)
	}
	if !exited(p) {
		t.Fatal("hung process was not reaped")
	}
	if _, err := e.Collect(); err == nil || e.Restarts() != 1 {
		t.Fatalf("second Collect: err %v, Restarts %d; want a restarted, again hanging plugin", err, e.Restarts())
	}
}

func TestExecSeqMismatchDiscarded(t *testing.T) {
	e, _ := fixture(t, "stale-seq", 2*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := e.Collect()
	if err == nil || len(got) != 0 {
		t.Fatalf("Collect = %d samples, err %v; want a fault and none", len(got), err)
	}
}

func TestExecCrashRestartsWithHello(t *testing.T) {
	e, dir := fixture(t, "crash-once", 2*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Collect(); err == nil {
		t.Fatal("Collect across a crash succeeded")
	}
	got, err := e.Collect()
	if err != nil || len(got) != 2 {
		t.Fatalf("Collect after restart = %d samples, err %v", len(got), err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "hellos"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "hello\n"); n != 2 {
		t.Fatalf("fixture saw %d hellos, want 2", n)
	}
	if e.Restarts() != 1 {
		t.Fatalf("Restarts = %d, want 1", e.Restarts())
	}
}

func TestExecOversizedLineIsAFault(t *testing.T) {
	e, _ := fixture(t, "huge", 5*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := e.proc
	if _, err := e.Collect(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Collect error = %v, want an oversized-line fault", err)
	}
	if !exited(p) {
		t.Fatal("process with an oversized line was not reaped")
	}
}

func TestExecStderrRateLimited(t *testing.T) {
	sink := &logSink{}
	e, _ := fixture(t, "noisy-stderr", 2*time.Second, sink)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// The relay runs beside the reply; give it time to drain all 100 lines.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count("collector[fx]: ") < 21 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	n := sink.count("collector[fx]: ")
	if n < 20 || n > 21 {
		t.Fatalf("relayed %d stderr lines, want 20 plus at most one notice", n)
	}
	if sink.count("collector[fx]: noise line 0") != 1 {
		t.Fatal("first stderr line not relayed verbatim")
	}
}

func TestExecEnvIsMinimal(t *testing.T) {
	e, _ := fixture(t, "good", 2*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := e.Collect()
	if err != nil || len(got) == 0 {
		t.Fatalf("Collect: %d, %v", len(got), err)
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "LANG": true,
		"DI_NODE_ID": true, "DI_COLLECTOR_NAME": true, "DI_FIXTURE": true, "DI_FIXTURE_DIR": true}
	keys := strings.Split(got[0].Labels["env"], ",")
	seen := map[string]bool{}
	for _, k := range keys {
		if !allowed[k] {
			t.Errorf("child saw %q", k)
		}
		seen[k] = true
	}
	if !seen["DI_NODE_ID"] || !seen["DI_COLLECTOR_NAME"] {
		t.Fatalf("child env %v lacks DI_NODE_ID or DI_COLLECTOR_NAME", keys)
	}
}

func TestExecCloseShutsDown(t *testing.T) {
	e, _ := fixture(t, "good", 2*time.Second, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := e.proc
	start := time.Now()
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %s", d)
	}
	if !exited(p) {
		t.Fatal("process still running after Close")
	}
	if _, err := e.Collect(); err == nil {
		t.Fatal("Collect after Close succeeded")
	}
}

func TestExecEmptySamplesIsNotAFault(t *testing.T) {
	r := rawOf(t, CollectReply{Seq: 1, Samples: []*types.MetricSample{}})
	got, avail, _ := stampReply("fx", "n1", 1_700_000_000, r)
	if got == nil || len(got) != 0 || len(avail) != 0 {
		t.Fatalf("stampReply = %v, %v", got, avail)
	}
}

func TestConfigJSON(t *testing.T) {
	m, err := configJSON(nil)
	if err != nil || m == nil || len(m) != 0 {
		t.Fatalf("configJSON(nil) = %v, %v", m, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("device: 0\nopts: {fast: true, list: [a, b]}\n"), &doc); err != nil {
		t.Fatal(err)
	}
	m, err = configJSON(doc.Content[0])
	if err != nil || m["device"] != 0 {
		t.Fatalf("configJSON = %v, %v", m, err)
	}
	var seq yaml.Node
	if err := yaml.Unmarshal([]byte("[1, 2]\n"), &seq); err != nil {
		t.Fatal(err)
	}
	if _, err := configJSON(seq.Content[0]); err == nil {
		t.Fatal("a sequence config was accepted")
	}
}

func TestExecExitedBetweenTicksRestartsFirst(t *testing.T) {
	sink := &logSink{}
	e, dir := fixture(t, "exit-after-collect", 2*time.Second, sink)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := e.proc
	if _, err := e.Collect(); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not exit after its reply")
	}
	got, err := e.Collect()
	if err != nil || len(got) != 2 {
		t.Fatalf("Collect after a quiet exit = %d samples, err %v; want a restart and a good tick", len(got), err)
	}
	if e.Restarts() != 1 {
		t.Fatalf("Restarts = %d, want 1", e.Restarts())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hellos"))
	if n := strings.Count(string(b), "hello\n"); n != 2 {
		t.Fatalf("fixture saw %d hellos, want 2", n)
	}
	// Not a fault, but not silent either: the operator is told, with the status.
	if sink.count("collector[fx]: WARNING plugin exited between ticks (exit status 0)") != 1 {
		t.Fatalf("log = %q, want one warning naming the exit status", sink.lines)
	}
}

// A bare command name found on PATH runs in the agent's working directory; a
// command given as a path runs in its own directory.
func TestExecWorkingDir(t *testing.T) {
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct{ command, dir string }{
		{filepath.Base(bin), ""},
		{bin, filepath.Dir(bin)},
	} {
		e := NewExecCollector(Env{NodeID: "n1"}, "fx", spec.ExecSpec{Command: tc.command, Args: []string{"-test.run=^$"}}, time.Second, nil)
		e.extraEnv = []string{"DI_FIXTURE=good"}
		p, err := e.startProcess()
		if err != nil {
			t.Fatalf("startProcess %s: %v", tc.command, err)
		}
		e.proc = p
		if p.cmd.Dir != tc.dir {
			t.Errorf("command %q: Dir = %q, want %q", tc.command, p.cmd.Dir, tc.dir)
		}
		e.Close()
	}
}

// Close escalates against a plugin that does not exit on stdin closing, and says
// so in its error, so the supervisor's joined Close error names it.
func TestExecCloseEscalates(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"linger", "SIGTERM"},
		{"linger-ignore-term", "killed"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e, _ := fixture(t, tc.mode, 2*time.Second, nil)
			e.grace = 200 * time.Millisecond
			if err := e.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			p := e.proc
			err := e.Close()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Close = %v, want an error naming %s", err, tc.want)
			}
			if !exited(p) {
				t.Fatal("process still running after Close")
			}
		})
	}
}

// A plugin that can no longer be started — its binary removed between ticks — is
// a fault of the tick that tried: the restart is counted, the error says it was a
// restart, and no process is left behind.
func TestExecRestartFailure(t *testing.T) {
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "plugin")
	if err := os.Symlink(bin, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	e := NewExecCollector(Env{NodeID: "n1", Interval: 10 * time.Second}, "fx",
		spec.ExecSpec{Command: link, Args: []string{"-test.run=^$"}}, 2*time.Second, nil)
	e.extraEnv = []string{"DI_FIXTURE=exit-after-collect"}
	t.Cleanup(func() { e.Close() })
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := e.proc
	if _, err := e.Collect(); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not exit after its reply")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	_, err = e.Collect()
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("Collect with the binary gone = %v, want a restart error", err)
	}
	if e.Restarts() != 1 {
		t.Fatalf("Restarts = %d, want the attempt counted", e.Restarts())
	}
	if e.proc != nil || !exited(p) {
		t.Fatal("a process was left behind")
	}
}
