package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/DiyazY/di-agent/compliance"
	"github.com/DiyazY/di-agent/pkg/collectors"
	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/types"
)

// ErrNotCertified is returned by `collector check` when any check failed. The
// checks have already been printed, so main exits 1 without repeating it.
var ErrNotCertified = errors.New("not certified")

// newCollectorCmd returns `mapctl collector`, the commands that work on a collector
// plugin directly rather than on a running agent.
func newCollectorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "collector",
		Short: "Work with collector plugins (no agent needed)",
	}
	c.AddCommand(newCollectorCheckCmd())
	return c
}

type checkOpts struct {
	exec     string
	http     string
	config   string
	ticks    int
	interval time.Duration
	nodeID   string
}

// newCollectorCheckCmd returns `mapctl collector check`: the certification a Go
// collector passes under `go test`, run over any plugin through the agent's own
// exec or http adapter.
func newCollectorCheckCmd() *cobra.Command {
	o := &checkOpts{}
	c := &cobra.Command{
		Use:   "check (--exec CMD [-- ARGS...] | --http URL)",
		Short: "Certify a collector plugin against the collector compliance suite",
		Long: "Runs the plugin behind the agent's real exec or http adapter and puts it through\n" +
			"the same compliance suite every Go collector passes, plus the wire-protocol\n" +
			"checks: hello round trip, seq echo, reply within the timeout, unit and range\n" +
			"stable over --ticks collects, no foreign node_id, timestamps in Unix seconds\n" +
			"within 2 x --interval of now, no restart between collects, and available\n" +
			"covering every emitted type. A malformed or duplicate sample, which the agent\n" +
			"would drop, fails the suite's NoMalformedSamples or NoDuplicateSamplesInBatch.\n" +
			"Prints PASS or FAIL per check, then the verdict; exits 0 only when certified.\n" +
			"Each check starts the plugin afresh.",
		Example: "  mapctl collector check --exec ./loadavg.py\n" +
			"  mapctl collector check --exec ./gpu --config gpu.yaml -- --device 0\n" +
			"  mapctl collector check --http http://localhost:9100/collect",
		RunE: func(cmd *cobra.Command, args []string) error {
			if (o.exec == "") == (o.http == "") {
				return errors.New("name exactly one plugin: --exec CMD or --http URL")
			}
			if o.http != "" && len(args) > 0 {
				return fmt.Errorf("arguments %q are for an --exec plugin", args)
			}
			if o.ticks < 1 {
				return errors.New("--ticks must be at least 1")
			}
			if o.interval < time.Second {
				return errors.New("--interval must be at least 1s: timestamps are whole seconds")
			}
			config, err := loadPluginConfig(o.config)
			if err != nil {
				return err
			}
			results := runPluginChecks(o, args, config, cmd.ErrOrStderr())
			failed := printResults(cmd.OutOrStdout(), results)
			if failed > 0 {
				cmd.SilenceErrors = true
				return ErrNotCertified
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&o.exec, "exec", "", "plugin program to run; its arguments follow --")
	f.StringVar(&o.http, "http", "", "URL of a plugin reached over HTTP")
	f.StringVar(&o.config, "config", "", "YAML file whose mapping is sent as the hello's config")
	f.IntVar(&o.ticks, "ticks", 5, "collects per protocol check")
	f.DurationVar(&o.interval, "interval", 10*time.Second, "interval announced in hello; sets the default timeout and the tick spacing (interval/10, at least 100ms)")
	f.StringVar(&o.nodeID, "node-id", "check", "node id the agent side announces and stamps")
	return c
}

// loadPluginConfig reads the config block a plugin receives in hello. No file is
// an empty object, as for an entry with no config.
func loadPluginConfig(path string) (map[string]any, error) {
	m := map[string]any{}
	if path == "" {
		return m, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("--config %s must be a YAML mapping: %w", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	if _, err := json.Marshal(m); err != nil {
		return nil, fmt.Errorf("--config %s is not representable as JSON: %w", path, err)
	}
	return m, nil
}

// pluginCollector is what both adapters are.
type pluginCollector interface {
	contracts.CollectorContract
	Start() error
	Close() error
}

// runPluginChecks runs every check over fresh instances of the plugin and returns
// their outcomes in order. The plugin's stderr goes to errOut.
func runPluginChecks(o *checkOpts, args []string, config map[string]any, errOut io.Writer) []compliance.Result {
	var logMu sync.Mutex
	env := collectors.Env{NodeID: o.nodeID, Interval: o.interval, Logf: func(format string, a ...any) {
		// The exec adapter relays stderr from its own goroutine.
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(errOut, format+"\n", a...)
	}}
	newPlugin := func() pluginCollector {
		if o.http != "" {
			return collectors.NewHTTPCollector(env, "http", spec.HTTPSpec{URL: o.http}, 0, config)
		}
		name := strings.TrimSuffix(filepath.Base(o.exec), filepath.Ext(o.exec))
		return collectors.NewExecCollector(env, name, spec.ExecSpec{Command: o.exec, Args: args}, 0, config)
	}
	// start is the factory every check builds its plugin with: started, and closed
	// when the check ends however it ends.
	start := func(r compliance.Reporter) pluginCollector {
		p := newPlugin()
		r.(*scopedReporter).defer_(func() { p.Close() })
		if err := p.Start(); err != nil {
			r.Fatalf("start: %v", err)
		}
		return p
	}

	rec := compliance.NewRecorder()
	root := &scopedReporter{Reporter: rec}

	// Without a completed hello every other check would fail for the same reason;
	// one line saying so is the useful answer.
	if !root.Run("HelloRoundTrip", func(r compliance.Reporter) { start(r) }) {
		return rec.Results()
	}

	// The every-batch checks run over --ticks collects, so a plugin that goes bad
	// on a later tick is not certified on the strength of its first reply.
	compliance.RunCollectorChecksTicks(root, func(r compliance.Reporter) contracts.CollectorContract { return start(r) }, o.ticks)

	timeout := collectors.DefaultTimeout(o.interval)
	spacing := max(o.interval/10, 100*time.Millisecond)

	// A plugin that echoes a fixed seq passes a single collect, so take two at least.
	root.Run("SeqEcho", func(r compliance.Reporter) {
		ticks(r, start(r), max(o.ticks, 2), 0, func(int, []*types.MetricSample, []types.MetricType) {})
	})

	root.Run("ReplyWithinTimeout", func(r compliance.Reporter) {
		p := start(r)
		for i := 0; i < o.ticks; i++ {
			began := time.Now()
			if _, err := p.Collect(); err != nil {
				r.Fatalf("collect %d: %v", i+1, err)
			}
			if took := time.Since(began); took > timeout {
				r.Errorf("collect %d took %s; the timeout at --interval %s is %s", i+1, took.Round(time.Millisecond), o.interval, timeout)
			}
		}
	})

	root.Run("UnitRangeStableOverTicks", func(r compliance.Reporter) {
		type decl struct {
			unit string
			rng  string
		}
		seen := map[types.MetricType]decl{}
		ticks(r, start(r), o.ticks, spacing, func(i int, samples []*types.MetricSample, _ []types.MetricType) {
			for _, s := range samples {
				d := decl{unit: s.Unit, rng: "none"}
				if s.Range != nil {
					d.rng = fmt.Sprint(*s.Range)
				}
				if prev, ok := seen[s.MetricType]; ok && prev != d {
					r.Errorf("collect %d: %s declared unit %q range %s, earlier %q %s", i+1, s.MetricType, d.unit, d.rng, prev.unit, prev.rng)
				}
				seen[s.MetricType] = d
			}
		})
	})

	// The adapter fills only an empty node_id, so one the plugin set itself is
	// still in the sample and the agent would count it as a violation.
	root.Run("NoForeignNodeID", func(r compliance.Reporter) {
		ticks(r, start(r), o.ticks, 0, func(i int, samples []*types.MetricSample, _ []types.MetricType) {
			for _, s := range samples {
				if s.NodeID != o.nodeID {
					r.Errorf("collect %d: %s carries node_id %q; the agent's is %q (leave it empty)", i+1, s.MetricType, s.NodeID, o.nodeID)
				}
			}
		})
	})

	// timestamp_unix is seconds. Either side of each collect bounds the request's
	// now_unix, and a sample observed within two intervals of it passes; one in
	// milliseconds lands decades out and fails. The adapter gives every collect a
	// now_unix of its own, so collects spaced under a second apart run ahead of
	// the clock by up to one second each: the upper bound allows for that.
	root.Run("TimestampNearNow", func(r compliance.Reporter) {
		p := start(r)
		slack := int64(2 * o.interval / time.Second)
		for i := 0; i < o.ticks; i++ {
			before := time.Now().Unix()
			samples, err := p.Collect()
			if err != nil {
				r.Fatalf("collect %d: %v", i+1, err)
			}
			after := time.Now().Unix()
			for _, s := range samples {
				if s.TimestampUnix < before-slack || s.TimestampUnix > after+slack+int64(i+1) {
					r.Errorf("collect %d: %s timestamp_unix %d is more than 2 x --interval from now_unix %d; it must be Unix seconds", i+1, s.MetricType, s.TimestampUnix, before)
				}
			}
		}
	})

	// A plugin that exits after every reply still works — the adapter restarts it
	// before the next collect — but each restart re-runs hello and the plugin's
	// setup, and under the agent it shows as restarts climbing in GET /collectors.
	// Only an exec plugin has a process to restart.
	root.Run("NoUnexpectedRestarts", func(r compliance.Reporter) {
		p := start(r)
		rs, ok := p.(interface{ Restarts() int64 })
		if !ok {
			return
		}
		ticks(r, p, max(o.ticks, 2), spacing, func(int, []*types.MetricSample, []types.MetricType) {})
		if n := rs.Restarts(); n > 0 {
			r.Errorf("the plugin was restarted %d time(s) over %d collects; it must keep running between requests and exit only when stdin closes", n, max(o.ticks, 2))
		}
	})

	root.Run("AvailableCoversEmitted", func(r compliance.Reporter) {
		ticks(r, start(r), o.ticks, 0, func(i int, samples []*types.MetricSample, available []types.MetricType) {
			avail := map[types.MetricType]bool{}
			for _, mt := range available {
				avail[mt] = true
			}
			for _, s := range samples {
				if !avail[s.MetricType] {
					r.Errorf("collect %d: emitted %s, which its available list %v leaves out", i+1, s.MetricType, available)
				}
			}
		})
	})

	return rec.Results()
}

// ticks collects n times, spacing apart, handing each batch and the available
// list read after it to fn. A failed collect ends the check.
func ticks(r compliance.Reporter, p pluginCollector, n int, spacing time.Duration,
	fn func(i int, samples []*types.MetricSample, available []types.MetricType)) {
	for i := 0; i < n; i++ {
		if i > 0 && spacing > 0 {
			time.Sleep(spacing)
		}
		samples, err := p.Collect()
		if err != nil {
			r.Fatalf("collect %d: %v", i+1, err)
		}
		fn(i, samples, p.AvailableMetrics())
	}
}

// printResults writes one line per check and the verdict, and returns how many
// checks failed.
func printResults(w io.Writer, results []compliance.Result) int {
	failed := 0
	for _, res := range results {
		if res.Passed() {
			fmt.Fprintf(w, "PASS %s\n", res.Name)
			continue
		}
		failed++
		fmt.Fprintf(w, "FAIL %s: %s\n", res.Name, strings.Join(res.Failures, "; "))
	}
	if failed == 0 {
		fmt.Fprintln(w, "certified")
	} else {
		fmt.Fprintf(w, "not certified (%d failed)\n", failed)
	}
	return failed
}

// scopedReporter closes what a check started when that check ends, including when
// it ends by Fatalf, so each check's plugin process is gone before the next starts.
type scopedReporter struct {
	compliance.Reporter
	cleanups []func()
}

func (s *scopedReporter) defer_(f func()) { s.cleanups = append(s.cleanups, f) }

func (s *scopedReporter) Run(name string, fn func(compliance.Reporter)) bool {
	return s.Reporter.Run(name, func(r compliance.Reporter) {
		child := &scopedReporter{Reporter: r}
		defer func() {
			for i := len(child.cleanups) - 1; i >= 0; i-- {
				child.cleanups[i]()
			}
		}()
		fn(child)
	})
}
