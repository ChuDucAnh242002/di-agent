package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/collectors/plugin"
	"github.com/DiyazY/di-agent/pkg/types"
)

// fixtureArg marks the test binary's own command line as a plugin run. The exec
// adapter gives a plugin a minimal environment, so the mode travels as an argument
// rather than a variable.
const fixtureArg = "mapctl-fixture="

// TestMain doubles the test binary as an exec plugin written with plugin.Serve, so
// `collector check` certifies a real child process and plugin.Serve is exercised
// end to end against the agent's own adapter.
func TestMain(m *testing.M) {
	if last := os.Args[len(os.Args)-1]; strings.HasPrefix(last, fixtureArg) {
		if err := fixturePlugin(strings.TrimPrefix(last, fixtureArg)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fixturePlugin serves one ranged and one unranged node-level type; in mode
// no-unit the unranged one leaves its unit out, which the contract forbids.
func fixturePlugin(mode string) error {
	return plugin.Serve(func(plugin.Hello) (plugin.CollectFunc, error) {
		return func(_ uint64, now time.Time) ([]*types.MetricSample, []types.MetricType, error) {
			load := &types.MetricSample{MetricType: "fx_load", Value: 0.5, TimestampUnix: now.Unix(), Unit: "load"}
			if mode == "no-unit" {
				load.Unit = ""
			}
			share := &types.MetricSample{MetricType: "fx_share", Value: 0.25, TimestampUnix: now.Unix(),
				Unit: "share", Range: &[2]float64{0, 1}}
			return []*types.MetricSample{load, share}, nil, nil
		}, nil
	})
}

func runCheck(t *testing.T, mode string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"collector", "check", "--exec", os.Args[0], "--ticks", "2", "--interval", "1s",
		"--", "-test.run=^$", fixtureArg + mode})
	err := root.Execute()
	t.Logf("stdout:\n%s\nstderr:\n%s", out, errOut)
	return out.String(), err
}

func TestCollectorCheckCertifiesGoodFixture(t *testing.T) {
	t.Parallel()
	out, err := runCheck(t, "good")
	if err != nil {
		t.Fatalf("check of a well-behaved plugin failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[len(lines)-1] != "certified" {
		t.Fatalf("last line %q, want certified", lines[len(lines)-1])
	}
	for _, name := range []string{"SamplesDeclareUnitAndRange", "CollectProducesSamples", "HelloRoundTrip", "SeqEcho",
		"ReplyWithinTimeout", "UnitRangeStableOverTicks", "NoForeignNodeID", "AvailableCoversEmitted"} {
		if !strings.Contains(out, "PASS "+name+"\n") {
			t.Errorf("output lacks PASS %s", name)
		}
	}
}

func TestCollectorCheckFailsWithoutUnit(t *testing.T) {
	t.Parallel()
	out, err := runCheck(t, "no-unit")
	if !errors.Is(err, ErrNotCertified) {
		t.Fatalf("check returned %v, want ErrNotCertified", err)
	}
	if !strings.Contains(out, "FAIL SamplesDeclareUnitAndRange: ") {
		t.Fatalf("output lacks the failing check")
	}
	if !strings.Contains(out, "not certified (") {
		t.Fatalf("output lacks the verdict")
	}
}

// A plugin that is not there is not certified, and says why, rather than erroring
// before any check line is printed.
func TestCollectorCheckMissingCommand(t *testing.T) {
	root := NewRootCmd()
	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"collector", "check", "--exec", "/nonexistent/plugin", "--ticks", "1", "--interval", "1s"})
	if err := root.Execute(); !errors.Is(err, ErrNotCertified) {
		t.Fatalf("err = %v, want ErrNotCertified", err)
	}
	if !strings.Contains(out.String(), "FAIL HelloRoundTrip: ") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestCollectorCheckNeedsOneTransport(t *testing.T) {
	for _, args := range [][]string{
		{"collector", "check"},
		{"collector", "check", "--exec", "x", "--http", "http://localhost:1"},
	} {
		root := NewRootCmd()
		root.SetOut(new(bytes.Buffer))
		root.SetErr(new(bytes.Buffer))
		root.SetArgs(args)
		if err := root.Execute(); err == nil || errors.Is(err, ErrNotCertified) {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
}
