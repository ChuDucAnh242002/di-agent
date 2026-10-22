package collectors

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/types"
)

const (
	// maxExecLine bounds one reply line. A plugin that writes more is faulted rather
	// than allowed to grow the agent's memory without limit.
	maxExecLine = 4 << 20

	// stderrBurst lines per stderrWindow are copied to the log; the rest are counted.
	stderrBurst  = 20
	stderrWindow = 10 * time.Second
	// stderrMaxLine truncates one relayed stderr line.
	stderrMaxLine = 4 << 10

	// shutdownGrace is waited after closing stdin, and again after SIGTERM, before
	// the next, harder step of Close.
	shutdownGrace = 2 * time.Second
	// relayDrain bounds how long a reaped process's stderr may still be relayed
	// before the pipe is closed under it, in case a grandchild holds it open.
	relayDrain = 200 * time.Millisecond
)

// ExecCollector runs an out-of-process plugin speaking wire protocol v1 over its
// stdin and stdout, and presents it as a CollectorContract.
//
// The process is started once and kept. Any fault — a timeout, a crash, a line
// that is not JSON, a reply to another seq, an error reply, an oversized line —
// kills it and returns an error; the next Collect starts it again and re-sends
// hello first. Spacing those restarts is the supervisor's backoff, not this type's.
type ExecCollector struct {
	env     Env
	name    string
	sp      spec.ExecSpec
	timeout time.Duration
	config  map[string]any
	logf    func(string, ...any)

	// extraEnv is appended to the child's environment; tests use it to select a
	// fixture mode.
	extraEnv []string

	// mu serialises Start, Collect and Close, which own proc.
	mu      sync.Mutex
	proc    *execProc
	started bool
	closed  bool
	seq     uint64

	availMu   sync.Mutex
	available []types.MetricType

	restarts atomic.Int64

	// The stderr rate limit is per member, not per process, so a plugin that
	// crashes in a loop cannot reset its allowance by restarting.
	logMu       sync.Mutex
	windowStart time.Time
	windowLines int
	suppressed  int
}

// execProc is one running instance of the plugin.
type execProc struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr *os.File
	lines  chan lineResult
	quit   chan struct{} // closed when the agent abandons the process
	done   chan struct{} // closed once Wait has returned
	relay  chan struct{} // closed when the stderr relay has ended
}

type lineResult struct {
	line []byte
	err  error
}

// NewExecCollector prepares the plugin entry name; nothing runs until Start. A
// non-positive timeout takes the default, min(interval/2, 5s).
func NewExecCollector(env Env, name string, sp spec.ExecSpec, timeout time.Duration, config map[string]any) *ExecCollector {
	if timeout <= 0 {
		timeout = defaultTimeout
		if half := env.Interval / 2; half > 0 && half < timeout {
			timeout = half
		}
	}
	if config == nil {
		config = map[string]any{}
	}
	logf := env.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &ExecCollector{env: env, name: name, sp: sp, timeout: timeout, config: config, logf: logf}
}

// Start spawns the plugin and completes hello. An error means the plugin could
// not be run or refused the hello; the daemon refuses to start on it.
func (e *ExecCollector) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("collector closed")
	}
	if e.proc != nil {
		return nil
	}
	e.started = true
	return e.spawn()
}

// Collect sends one collect request and returns the stamped samples. A plugin
// faulted earlier is restarted first.
func (e *ExecCollector) Collect() ([]*types.MetricSample, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("collector closed")
	}
	if e.proc == nil {
		if e.started {
			e.restarts.Add(1)
		}
		e.started = true
		if err := e.spawn(); err != nil {
			return nil, fmt.Errorf("restart: %w", err)
		}
	}
	e.seq++
	seq := e.seq
	deadline := time.Now().Add(e.timeout)
	line, err := e.roundTrip(CollectRequest{Op: "collect", Seq: seq, NowUnix: time.Now().Unix()}, deadline)
	if err != nil {
		return nil, e.fault(err)
	}
	var r CollectReply
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, e.fault(fmt.Errorf("collect reply is not JSON: %w", err))
	}
	if r.Error != "" {
		return nil, e.fault(fmt.Errorf("plugin error: %s", r.Error))
	}
	if r.Seq != seq {
		return nil, e.fault(fmt.Errorf("reply seq %d, want %d", r.Seq, seq))
	}
	samples, avail := stampReply(e.name, e.env.NodeID, &r)
	e.availMu.Lock()
	e.available = avail
	e.availMu.Unlock()
	return samples, nil
}

// SourceID is the entry name, so it is stable across restarts.
func (e *ExecCollector) SourceID() string { return e.name }

// AvailableMetrics is the last successful reply's available list, or the types
// that reply carried.
func (e *ExecCollector) AvailableMetrics() []types.MetricType {
	e.availMu.Lock()
	defer e.availMu.Unlock()
	return append([]types.MetricType(nil), e.available...)
}

// Restarts counts how many times a faulted plugin has been started again. It is
// an atomic so status can read it while a Collect is holding the process lock.
func (e *ExecCollector) Restarts() int64 { return e.restarts.Load() }

// Close shuts the plugin down: stdin is closed so a well-behaved plugin exits on
// EOF; then SIGTERM, then SIGKILL, each after shutdownGrace.
func (e *ExecCollector) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	p := e.proc
	e.proc = nil
	if p == nil {
		return nil
	}
	close(p.quit)
	p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(shutdownGrace):
		p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(shutdownGrace):
			p.cmd.Process.Kill()
			<-p.done
		}
	}
	p.release()
	return nil
}

// spawn starts the process and sends hello; on any failure the process is reaped
// and e.proc stays nil. Called with mu held.
func (e *ExecCollector) spawn() error {
	p, err := e.startProcess()
	if err != nil {
		return err
	}
	e.proc = p
	hello := Hello{Op: "hello", Protocol: ProtocolVersion, Name: e.name, NodeID: e.env.NodeID,
		IntervalS: int(e.env.Interval / time.Second), Config: e.config}
	line, err := e.roundTrip(hello, time.Now().Add(e.timeout))
	if err != nil {
		return e.fault(fmt.Errorf("hello: %w", err))
	}
	var r HelloReply
	if err := json.Unmarshal(line, &r); err != nil {
		return e.fault(fmt.Errorf("hello reply is not JSON: %w", err))
	}
	if r.Error != "" {
		return e.fault(fmt.Errorf("hello refused: %s", r.Error))
	}
	if r.Protocol != ProtocolVersion {
		return e.fault(fmt.Errorf("plugin speaks protocol %d, agent speaks %d", r.Protocol, ProtocolVersion))
	}
	return nil
}

// startProcess runs the command with a minimal environment in its own directory.
// The pipes are made here rather than by os/exec so the agent owns their ends:
// Wait then never closes a pipe under a reader, and stdin can take a deadline.
func (e *ExecCollector) startProcess() (*execProc, error) {
	path := e.sp.Command
	dir := ""
	if strings.ContainsRune(path, filepath.Separator) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		path, dir = abs, filepath.Dir(abs)
	}
	cmd := exec.Command(path, e.sp.Args...)
	cmd.Dir = dir
	cmd.Env = e.childEnv()

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	err = cmd.Start()
	// The child holds its own copies; the agent keeps only its ends, so EOF on
	// stdout and stderr means the child (and anything it spawned) let go.
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("start %s: %w", e.sp.Command, err)
	}
	p := &execProc{cmd: cmd, stdin: inW, stdout: outR, stderr: errR,
		lines: make(chan lineResult), quit: make(chan struct{}),
		done: make(chan struct{}), relay: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(p.done)
	}()
	go readLines(outR, p.lines, p.quit)
	go func() {
		e.relayStderr(errR)
		close(p.relay)
	}()
	return p, nil
}

// childEnv is the minimal environment: PATH, HOME and LANG from the agent if set,
// the node and entry name, and nothing else the agent happens to carry.
func (e *ExecCollector) childEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "DI_NODE_ID="+e.env.NodeID, "DI_COLLECTOR_NAME="+e.name)
	return append(env, e.extraEnv...)
}

// roundTrip writes one request line and reads one reply line, both before
// deadline. Called with mu held and e.proc set.
func (e *ExecCollector) roundTrip(req any, deadline time.Time) ([]byte, error) {
	p := e.proc
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// A pipe from os.Pipe accepts a deadline on the platforms the agent runs on;
	// where it does not, the request is a single small write and the read below
	// still bounds the call.
	p.stdin.SetWriteDeadline(deadline)
	if _, err := p.stdin.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case r := <-p.lines:
		if r.err != nil {
			return nil, r.err
		}
		return r.line, nil
	case <-t.C:
		return nil, fmt.Errorf("no reply within %s", e.timeout)
	}
}

// fault kills the current process, reaps it, and returns err, noting the exit
// status when the process had already ended on its own. Called with mu held.
func (e *ExecCollector) fault(err error) error {
	p := e.proc
	e.proc = nil
	if p == nil {
		return err
	}
	exitedAlready := false
	select {
	case <-p.done:
		exitedAlready = true
	default:
	}
	close(p.quit)
	p.cmd.Process.Kill()
	p.stdin.Close()
	<-p.done
	p.release()
	if exitedAlready && p.cmd.ProcessState != nil {
		return fmt.Errorf("%w (plugin %s)", err, p.cmd.ProcessState)
	}
	return err
}

// release closes the agent's pipe ends of a reaped process. stderr gets a short
// drain first so a crashing plugin's last words reach the log; closing then ends
// both reader goroutines even if a grandchild still holds the other ends.
func (p *execProc) release() {
	select {
	case <-p.relay:
	case <-time.After(relayDrain):
	}
	p.stdout.Close()
	p.stderr.Close()
}

// readLines delivers stdout one line at a time until EOF, an error, or a line
// longer than maxExecLine, each of which ends it. It stops sending once the agent
// has abandoned the process.
func readLines(r io.Reader, out chan<- lineResult, quit <-chan struct{}) {
	br := bufio.NewReaderSize(r, 64<<10)
	send := func(lr lineResult) bool {
		select {
		case out <- lr:
			return true
		case <-quit:
			return false
		}
	}
	for {
		var line []byte
		for {
			chunk, err := br.ReadSlice('\n')
			if len(line)+len(chunk) > maxExecLine+1 {
				send(lineResult{err: fmt.Errorf("reply line exceeds %d MiB", maxExecLine>>20)})
				return
			}
			line = append(line, chunk...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
					err = errors.New("plugin closed stdout")
				}
				send(lineResult{err: err})
				return
			}
			break
		}
		if !send(lineResult{line: line}) {
			return
		}
	}
}

// relayStderr copies the plugin's stderr to the log, prefixed with the entry
// name, at most stderrBurst lines per stderrWindow. The first line dropped in a
// window is announced once; the count of dropped lines opens the next window and
// counts against its allowance.
func (e *ExecCollector) relayStderr(r io.Reader) {
	br := bufio.NewReaderSize(r, stderrMaxLine)
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			line := strings.TrimRight(string(chunk), "\r\n")
			// Swallow the rest of an overlong line so it is relayed once, truncated.
			for err == bufio.ErrBufferFull {
				_, err = br.ReadSlice('\n')
			}
			e.logStderr(line)
		}
		if err != nil {
			return
		}
	}
}

func (e *ExecCollector) logStderr(line string) {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	now := time.Now()
	if now.Sub(e.windowStart) >= stderrWindow {
		e.windowStart, e.windowLines = now, 0
		if e.suppressed > 0 {
			e.logf("collector[%s]: %d stderr lines suppressed", e.name, e.suppressed)
			e.suppressed = 0
			e.windowLines++
		}
	}
	if e.windowLines < stderrBurst {
		e.windowLines++
		e.logf("collector[%s]: %s", e.name, line)
		return
	}
	e.suppressed++
	if e.suppressed == 1 {
		e.logf("collector[%s]: stderr exceeds %d lines per %s; suppressing the rest of this window", e.name, stderrBurst, stderrWindow)
	}
}
