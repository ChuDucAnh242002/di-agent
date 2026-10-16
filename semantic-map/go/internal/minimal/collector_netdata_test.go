package minimal_test

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DiyazY/di-agent/internal/minimal"
	"github.com/DiyazY/di-agent/pkg/types"
)

// netdataServer serves testdata/netdata_allmetrics.json as /api/v1/allmetrics: 22
// charts as a Netdata 1.43 agent on an arm64 k0s VM reported them, with the pod and
// container ids replaced.
func netdataServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/netdata_allmetrics.json")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/allmetrics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			http.Error(w, "format", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func collectNetdata(t *testing.T, opts minimal.NetdataOptions) map[string]*types.MetricSample {
	t.Helper()
	c := minimal.NewNetdataCollectorWithOptions("n1", netdataServer(t).URL, opts)
	samples, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*types.MetricSample{}
	for _, s := range samples {
		id := string(s.MetricType)
		if s.Subject != "" {
			id += "@" + s.Subject
		}
		if _, dup := out[id]; dup {
			t.Errorf("two samples for %s in one Collect", id)
		}
		out[id] = s
	}
	return out
}

// TestNetdataDefaultSelection: the curated default reads the node's CPU, memory, I/O,
// network and pressure, and each disk, interface and core as a subject; it leaves the
// rest of what Netdata offers (load, link state, apps, cgroups) out.
func TestNetdataDefaultSelection(t *testing.T) {
	got := collectNetdata(t, minimal.NetdataOptions{})
	for _, id := range []string{
		"system.cpu.user", "system.cpu.iowait", "system.ram.used", "system.io.in", "system.net.InOctets",
		"system.cpu_some_pressure_stall_time.time",
		"disk.io.reads@disk:sda", "disk.util.utilization@disk:sda", "disk.await.writes@disk:sda",
		"net.net.received@net:enp0s1", "cpu.cpu.user@cpu:cpu0", "cpu.cpu.user@cpu:cpu1",
	} {
		if got[id] == nil {
			t.Errorf("%s not collected", id)
		}
	}
	for id := range got {
		for _, out := range []string{"system.load", "net_carrier", "net.carrier", "app.", "k8s.", "systemd.", "mem.available"} {
			if len(id) >= len(out) && id[:len(out)] == out {
				t.Errorf("%s collected; the default list does not include it", id)
			}
		}
	}
}

// TestNetdataNamesSubjectsAndCharts: each dimension is <context>.<dimension>, a chart
// that is one instance of its context is a subject named from whichever half of the
// chart id tells it apart, and every sample carries its chart.
func TestNetdataNamesSubjectsAndCharts(t *testing.T) {
	got := collectNetdata(t, minimal.NetdataOptions{Include: []string{"*"}})
	for id, chart := range map[string]string{
		"system.cpu.user":                                         "system.cpu",
		"disk.io.reads@disk:sda":                                  "disk.sda",
		"disk.util.utilization@disk:sda":                          "disk_util.sda",
		"cpu.interrupts.IPI0@cpu:cpu0":                            "cpu.cpu0_interrupts",
		"app.cpu_utilization.user@app:netdata":                    "app.netdata_cpu_utilization",
		"systemd.service.cpu.utilization.user@systemd:containerd": "systemd_containerd.cpu_utilization",
		"disk.space.used@disk:_boot":                              "disk_space._boot",
		"system.cpu_some_pressure.some_10":                        "system.cpu_some_pressure",
		"k8s.cgroup.cpu.user@container:0123456789ab":              "cgroup_k8s_kubepods_burstable_pod8f3c1234-aaaa-bbbb-cccc-1234567890ab_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.cpu",
	} {
		s := got[id]
		if s == nil {
			t.Errorf("%s not collected", id)
			continue
		}
		if s.Chart != chart {
			t.Errorf("%s chart %q; want %q", id, s.Chart, chart)
		}
	}
	if s := got["k8s.cgroup.cpu.user@container:0123456789ab"]; s != nil && s.Labels["pod_uid"] != "8f3c1234-aaaa-bbbb-cccc-1234567890ab" {
		t.Errorf("container labels %v; want its pod's UID", s.Labels)
	}
}

// TestNetdataConvertsUnitsAndSkipsComputedSeries: a percentage becomes a fraction on
// [0,1], KiB/s and kilobits/s become bytes/s with no range, a PSI stall time becomes
// the stalled share of the second, an outgoing rate is a magnitude; idle, the 60 s and
// 300 s pressure averages and load5/load15 are not collected.
func TestNetdataConvertsUnitsAndSkipsComputedSeries(t *testing.T) {
	got := collectNetdata(t, minimal.NetdataOptions{Include: []string{"*"}})
	near := func(id string, want float64, unit string, ranged bool) {
		t.Helper()
		s := got[id]
		if s == nil {
			t.Errorf("%s not collected", id)
			return
		}
		if math.Abs(s.Value-want) > 1e-9*math.Max(1, math.Abs(want)) || s.Unit != unit || (s.Range != nil) != ranged {
			t.Errorf("%s = %v %q range %v; want %v %q ranged=%v", id, s.Value, s.Unit, s.Range, want, unit, ranged)
		}
	}
	near("system.cpu.user", 0.297029703, "fraction", true)
	near("system.io.out", 3600*1024, "bytes/s", false) // fixture -3600 KiB/s: drawn below the axis
	near("system.net.OutOctets", 1.6716*125, "bytes/s", false)
	near("system.cpu_some_pressure_stall_time.time", 0.4219839, "fraction", true)
	for _, id := range []string{"system.cpu.idle", "cpu.cpu.idle@cpu:cpu0", "system.cpu_some_pressure.some_60",
		"system.cpu_some_pressure.some_300", "system.load.load5", "system.load.load15"} {
		if got[id] != nil {
			t.Errorf("%s collected; it is computed from what is kept", id)
		}
	}
	if got["system.load.load1"] == nil || got["system.cpu_some_pressure.some_10"] == nil {
		t.Error("the shortest average of each series is kept")
	}
}

// TestNetdataCollapsesOntoTheMapsTypes: with Collapse on, the system charts also feed
// the node's own types, so the declared constructs are summarised from Netdata alone;
// busy CPU excludes idle and iowait. With it off, none are emitted.
func TestNetdataCollapsesOntoTheMapsTypes(t *testing.T) {
	got := collectNetdata(t, minimal.NetdataOptions{Collapse: true})
	for mt, want := range map[types.MetricType]float64{
		types.CPUUtilization:      (100 - 68.8118812 - 0.0) / 100,
		types.CPUPressureRatio:    0.4219839,
		types.MemoryUtilization:   -1, // checked for presence only
		types.IOPressureRatio:     -1,
		types.MemoryPressureRatio: -1,
		types.CPUStealRatio:       0,
		types.NetworkRxBps:        -1,
	} {
		s := got[string(mt)]
		if s == nil {
			t.Errorf("%s not collapsed", mt)
			continue
		}
		if s.Unit != "fraction" || s.Range == nil || s.Subject != "" {
			t.Errorf("%s declared %q %v subject %q; want a node-level fraction on [0,1]", mt, s.Unit, s.Range, s.Subject)
		}
		if want >= 0 && math.Abs(s.Value-want) > 1e-6 {
			t.Errorf("%s = %v; want %v", mt, s.Value, want)
		}
	}
	off := collectNetdata(t, minimal.NetdataOptions{})
	if off[string(types.CPUUtilization)] != nil {
		t.Error("cpu_utilization collapsed with Collapse off")
	}
}

// TestNetdataUnreachableIsSilentAndTypesGrow: an unreachable Netdata yields no samples
// and no error, and AvailableMetrics is what has been emitted, so it is empty before
// the first read and then lists every type collected.
func TestNetdataUnreachableIsSilentAndTypesGrow(t *testing.T) {
	var lines []string
	down := minimal.NewNetdataCollectorWithOptions("n1", "http://127.0.0.1:1", minimal.NetdataOptions{
		Logf: func(f string, a ...any) { lines = append(lines, f) }})
	for i := 0; i < 3; i++ {
		if s, err := down.Collect(); err != nil || len(s) != 0 {
			t.Fatalf("unreachable Netdata: %d samples, err %v; want none and nil", len(s), err)
		}
	}
	if len(lines) != 1 {
		t.Errorf("logged %d lines over three failed ticks; want one", len(lines))
	}
	c := minimal.NewNetdataCollectorWithOptions("n1", netdataServer(t).URL, minimal.NetdataOptions{})
	if n := len(c.AvailableMetrics()); n != 0 {
		t.Errorf("%d types available before any read; want 0", n)
	}
	samples, _ := c.Collect()
	avail := map[types.MetricType]bool{}
	for _, mt := range c.AvailableMetrics() {
		avail[mt] = true
	}
	for _, s := range samples {
		if !avail[s.MetricType] {
			t.Errorf("%s emitted but not available", s.MetricType)
		}
	}
}

func TestNetdataIncludePatternsAreValidated(t *testing.T) {
	if err := minimal.ValidNetdataInclude([]string{"system.*", "["}); err == nil {
		t.Error("a malformed pattern passed")
	}
	if err := minimal.ValidNetdataInclude(minimal.DefaultNetdataInclude); err != nil {
		t.Errorf("the default list: %v", err)
	}
}
