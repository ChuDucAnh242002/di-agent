package minimal

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/types"
)

// NetdataCollector is a CollectorContract implementation that polls a Netdata daemon's
// HTTP API v1 and turns whatever its include patterns select into MetricSamples.
//
// Netdata collects thousands of series per node; which of them reach the map is the
// operator's choice, made here and not in the map. The include list is a set of
// path.Match patterns over Netdata chart contexts (system.cpu, disk.io, net.net,
// k8s.cgroup.cpu): DefaultNetdataInclude is a short curated list — CPU, memory,
// pressure, I/O, network, disk — and "*" is every chart Netdata has. One request per
// tick reads them all (/api/v1/allmetrics), so a wide list costs the map properties,
// not round trips.
//
// Each dimension of a selected chart becomes one property:
//
//   - Name. <context>.<dimension>, e.g. system.cpu.user, disk.io.reads, with every
//     byte outside the metric charset replaced by '_'.
//   - Subject. A chart that is one of several instances of its context becomes a
//     subject: disk.sda → disk:sda, net.enp0s1 → net:enp0s1, cpu.cpu0 → cpu:cpu0,
//     app.sshd_cpu_utilization → app:sshd. A kubelet container's cgroup becomes
//     container:<12-hex id> labelled with its pod UID: Netdata charts containers, and a
//     pod with two containers (or its pause sandbox) is not one series. A chart whose
//     id is its context (system.cpu) is the node's own.
//   - Unit. Converted where the map has a unit for it: a percentage to a fraction on
//     [0,1], KiB/s, MiB/s and kilobits/s to bytes/s, KiB, MiB and GiB to bytes; a PSI
//     stall-time chart (milliseconds stalled per second) to a fraction of the second.
//     Anything else keeps Netdata's unit and declares no range, because a rate or a
//     count has none; the map then assumes [0,1] and says so where it normalises.
//   - Sign. Netdata draws the outgoing half of a rate chart (sent, writes, out) below
//     the axis by negating it; a rate is taken as its magnitude.
//   - Chart. Every sample carries its chart id, so dimensions of one chart that are
//     arithmetic of each other can be grouped by the proposer.
//   - Computed series are skipped: a CPU chart's idle (the rest of the 100 %), and the
//     60 s and 300 s averages of a counter whose shorter form is there (PSI "some 60",
//     load5). They carry nothing the others lack.
//
// With Collapse on, the node's system charts also feed the map's own node-level types
// — cpu_utilization, memory_utilization, cpu/io/memory_pressure_ratio, cpu_steal_ratio,
// network_rx_bps/tx_bps — so the declared constructs are summarised when Netdata is the
// only source. The daemon turns it off when the cgroup collector runs too: Netdata's
// system.cpu counts time the root cgroup does not (iowait, interrupts), so the two
// would feed one property two different quantities.
//
// HTTP errors and non-200 responses are transient unavailability: Collect returns
// (nil, nil) and the next tick tries again.
type NetdataCollector struct {
	nodeID     string
	baseURL    string
	httpClient *http.Client
	sid        string
	opts       NetdataOptions

	mu       sync.Mutex
	avail    map[types.MetricType]bool        // every type emitted so far
	declared map[types.MetricType]netdataDecl // unit and range, fixed at a type's first sample
	warned   bool
}

type netdataDecl struct {
	unit string
	rng  *[2]float64
}

// NetdataOptions configures what the Netdata collector selects.
type NetdataOptions struct {
	// Include is a list of path.Match patterns over chart contexts. nil means
	// DefaultNetdataInclude; "*" selects every chart.
	Include []string
	// Collapse also emits the map's own node-level types from the system charts.
	Collapse bool
	// HTTPClient polls Netdata; nil means a client with a 5-second timeout.
	HTTPClient *http.Client
	// Logf receives the collector's own lines; nil means the standard logger.
	Logf func(format string, args ...any)
}

// DefaultNetdataInclude is the curated default: CPU, memory, pressure, I/O, network
// and disk, for the node and per disk, interface and core.
var DefaultNetdataInclude = []string{
	"system.cpu", "system.ram", "system.io", "system.net",
	"system.*_some_pressure_stall_time",
	"disk.io", "disk.util", "disk.await",
	"net.net",
	"cpu.cpu",
}

// ValidNetdataInclude reports the first pattern path.Match cannot parse.
func ValidNetdataInclude(patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("-netdata-include pattern %q: %v", p, err)
		}
	}
	return nil
}

// NewNetdataCollector creates a collector with the default include list and the map's
// own node-level types collapsed from the system charts.
func NewNetdataCollector(nodeID, baseURL string, httpClient *http.Client) *NetdataCollector {
	return NewNetdataCollectorWithOptions(nodeID, baseURL, NetdataOptions{Collapse: true, HTTPClient: httpClient})
}

// NewNetdataCollectorWithOptions creates a collector polling the Netdata daemon at
// baseURL (e.g. "http://localhost:19999") for the given node.
func NewNetdataCollectorWithOptions(nodeID, baseURL string, opts NetdataOptions) *NetdataCollector {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if opts.Include == nil {
		opts.Include = DefaultNetdataInclude
	}
	return &NetdataCollector{
		nodeID: nodeID, baseURL: strings.TrimRight(baseURL, "/"), httpClient: opts.HTTPClient,
		sid: "netdata:" + nodeID, opts: opts,
		avail: map[types.MetricType]bool{}, declared: map[types.MetricType]netdataDecl{},
	}
}

func (n *NetdataCollector) log(format string, args ...any) {
	if n.opts.Logf != nil {
		n.opts.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (n *NetdataCollector) SourceID() string { return n.sid }

// AvailableMetrics is every type this collector has emitted: what Netdata offers
// under the include list is known only by asking it, and grows as disks, interfaces
// and workloads appear.
func (n *NetdataCollector) AvailableMetrics() []types.MetricType {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]types.MetricType, 0, len(n.avail))
	for mt := range n.avail {
		out = append(out, mt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Collect reads every chart once and returns a sample per selected dimension.
func (n *NetdataCollector) Collect() ([]*types.MetricSample, error) {
	charts, ok := n.fetchAll()
	if !ok {
		return nil, nil
	}
	contexts := map[string][]string{}
	for id, c := range charts {
		contexts[c.Context] = append(contexts[c.Context], id)
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*types.MetricSample
	ids := make([]string, 0, len(charts))
	for id := range charts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := charts[id]
		if !n.included(c.Context) {
			continue
		}
		subject, labels := netdataSubject(id, c.Context)
		conv := netdataUnit(c.Context, c.Units)
		dims := make([]string, 0, len(c.Dimensions))
		for d := range c.Dimensions {
			dims = append(dims, d)
		}
		sort.Strings(dims)
		for _, d := range dims {
			if computedSeries(c.Context, d) {
				continue
			}
			v := c.Dimensions[d].Value
			if v == nil || math.IsNaN(*v) {
				continue
			}
			mt := types.MetricType(sanitiseMetric(c.Context + "." + d))
			if s := n.sampleLocked(mt, conv.apply(*v), conv, c.LastUpdated, subject, id, labels); s != nil {
				out = append(out, s)
			}
		}
	}
	if n.opts.Collapse {
		out = append(out, n.collapseLocked(charts)...)
	}
	return out, nil
}

func (n *NetdataCollector) included(context string) bool {
	for _, p := range n.opts.Include {
		if ok, _ := path.Match(p, context); ok {
			return true
		}
	}
	return false
}

// collapseLocked derives the map's own node-level types from the system charts.
func (n *NetdataCollector) collapseLocked(charts map[string]netdataChart) []*types.MetricSample {
	var out []*types.MetricSample
	frac := netdataConv{scale: 1, unit: "fraction", rng: &[2]float64{0, 1}}
	add := func(mt types.MetricType, v float64, c netdataChart, chart string) {
		if s := n.sampleLocked(mt, clamp(v, 0, 1), frac, c.LastUpdated, "", chart, nil); s != nil {
			out = append(out, s)
		}
	}
	if c, ok := charts["system.cpu"]; ok {
		// Busy is every dimension except idle and iowait: a CPU waiting on I/O is idle.
		var busy float64
		var seen bool
		for d, x := range c.Dimensions {
			if x.Value == nil || d == "idle" || d == "iowait" {
				continue
			}
			busy += *x.Value
			seen = true
		}
		if seen {
			add(types.CPUUtilization, busy/100, c, "system.cpu")
		}
		if s := c.dim("steal"); s != nil {
			add(types.CPUStealRatio, *s/100, c, "system.cpu")
		}
	}
	if c, ok := charts["system.ram"]; ok {
		used := c.dim("used")
		var sum float64
		for _, x := range c.Dimensions {
			if x.Value != nil {
				sum += *x.Value
			}
		}
		if used != nil && sum > 0 {
			add(types.MemoryUtilization, *used/sum, c, "system.ram")
		}
	}
	for chart, mt := range map[string]types.MetricType{
		"system.cpu_some_pressure_stall_time":    types.CPUPressureRatio,
		"system.io_some_pressure_stall_time":     types.IOPressureRatio,
		"system.memory_some_pressure_stall_time": types.MemoryPressureRatio,
	} {
		if c, ok := charts[chart]; ok {
			if t := c.dim("time"); t != nil {
				add(mt, *t/1000, c, chart) // milliseconds stalled per second
			}
		}
	}
	// The network rates are declared against a 1 Gbps link (125,000,000 bytes/s).
	if c, ok := charts["system.net"]; ok {
		const refBps = 125_000_000.0
		if v := c.dim("InOctets"); v != nil {
			add(types.NetworkRxBps, math.Abs(*v)*125/refBps, c, "system.net")
		}
		if v := c.dim("OutOctets"); v != nil {
			add(types.NetworkTxBps, math.Abs(*v)*125/refBps, c, "system.net")
		}
	}
	return out
}

// sampleLocked builds one sample, or nil when it would contradict the unit and range
// its type was first declared with (Netdata changed a chart's units under it).
func (n *NetdataCollector) sampleLocked(mt types.MetricType, value float64, conv netdataConv, ts int64,
	subject, chart string, labels map[string]string) *types.MetricSample {
	if !types.ValidMetricType(string(mt)) || !types.ValidSubject(subject) {
		return nil
	}
	d := netdataDecl{unit: conv.unit, rng: conv.rng}
	if prev, ok := n.declared[mt]; ok {
		if prev.unit != d.unit || (prev.rng == nil) != (d.rng == nil) || (prev.rng != nil && *prev.rng != *d.rng) {
			return nil
		}
	} else {
		n.declared[mt] = d
	}
	n.avail[mt] = true
	key := fmt.Sprintf("%s:%s:%s:%s:%d", n.sid, n.nodeID, subject, mt, ts)
	h := sha256.Sum256([]byte(key))
	s := &types.MetricSample{
		NodeID: n.nodeID, MetricType: mt, Value: value, TimestampUnix: ts,
		EventID: fmt.Sprintf("%x", h[:8]), Subject: subject, Unit: conv.unit,
		Source: n.sid, Chart: chart, Labels: labels,
	}
	if conv.rng != nil {
		rng := *conv.rng
		s.Range = &rng
	}
	return s
}

// fetchAll calls GET <baseURL>/api/v1/allmetrics?format=json: every chart's latest
// values, context and units in one response.
func (n *NetdataCollector) fetchAll() (map[string]netdataChart, bool) {
	resp, err := n.httpClient.Get(n.baseURL + "/api/v1/allmetrics?format=json")
	if err != nil {
		n.warnOnce("netdata collector: %s is unreachable (%v); retrying every tick", n.baseURL, err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		n.warnOnce("netdata collector: %s answered %d; retrying every tick", n.baseURL, resp.StatusCode)
		return nil, false
	}
	var charts map[string]netdataChart
	if err := json.NewDecoder(resp.Body).Decode(&charts); err != nil {
		n.warnOnce("netdata collector: %s sent an unreadable response (%v); retrying every tick", n.baseURL, err)
		return nil, false
	}
	n.mu.Lock()
	n.warned = false
	n.mu.Unlock()
	return charts, true
}

func (n *NetdataCollector) warnOnce(format string, args ...any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.warned {
		n.log(format, args...)
		n.warned = true
	}
}

// ── Netdata's shapes ──────────────────────────────────────────────────────────

// netdataChart is one entry of /api/v1/allmetrics?format=json.
type netdataChart struct {
	Context     string                  `json:"context"`
	Units       string                  `json:"units"`
	LastUpdated int64                   `json:"last_updated"`
	Dimensions  map[string]netdataValue `json:"dimensions"`
}

// netdataValue's value is null for a dimension that has not been collected yet.
type netdataValue struct {
	Value *float64 `json:"value"`
}

func (c netdataChart) dim(name string) *float64 {
	if d, ok := c.Dimensions[name]; ok {
		return d.Value
	}
	return nil
}

// netdataConv converts a Netdata unit to the map's.
type netdataConv struct {
	scale float64
	abs   bool
	unit  string
	rng   *[2]float64
}

func (c netdataConv) apply(v float64) float64 {
	if c.abs {
		v = math.Abs(v)
	}
	v *= c.scale
	if c.rng != nil {
		v = clamp(v, c.rng[0], c.rng[1])
	}
	return v
}

// netdataUnit is the unit table: what a Netdata unit becomes in the map.
func netdataUnit(context, units string) netdataConv {
	unitRange := &[2]float64{0, 1}
	if strings.HasSuffix(context, "_pressure_stall_time") {
		return netdataConv{scale: 0.001, abs: true, unit: "fraction", rng: unitRange} // ms stalled per second
	}
	rate := strings.HasSuffix(units, "/s")
	switch units {
	case "percentage", "%", "% of time working":
		return netdataConv{scale: 0.01, abs: true, unit: "fraction", rng: unitRange}
	case "KiB/s":
		return netdataConv{scale: 1 << 10, abs: true, unit: "bytes/s"}
	case "MiB/s":
		return netdataConv{scale: 1 << 20, abs: true, unit: "bytes/s"}
	case "kilobits/s":
		return netdataConv{scale: 125, abs: true, unit: "bytes/s"}
	case "KiB":
		return netdataConv{scale: 1 << 10, unit: "bytes"}
	case "MiB":
		return netdataConv{scale: 1 << 20, unit: "bytes"}
	case "GiB":
		return netdataConv{scale: 1 << 30, unit: "bytes"}
	}
	if units == "" {
		units = "1"
	}
	return netdataConv{scale: 1, abs: rate, unit: units}
}

// pressureAverage and loadAverage are the longer averages of a series whose shorter
// form is kept.
var pressureAverage = regexp.MustCompile(`^(some|full) (60|300)$`)

// computedSeries reports a dimension that is arithmetic of the chart's others or a
// longer average of one it keeps.
func computedSeries(context, dim string) bool {
	switch {
	case dim == "idle" && (context == "system.cpu" || context == "cpu.cpu"):
		return true
	case pressureAverage.MatchString(dim):
		return true
	case context == "system.load" && (dim == "load5" || dim == "load15"):
		return true
	}
	return false
}

var kubeContainerRe = regexp.MustCompile(`pod([0-9a-f]{8}[-_][0-9a-f]{4}[-_][0-9a-f]{4}[-_][0-9a-f]{4}[-_][0-9a-f]{12})_([0-9a-f]{12})[0-9a-f]*$`)

// netdataSubject maps a chart to the subject it describes: none for a chart whose id
// is its context (the node's own), otherwise <family>:<instance>, the instance being
// what tells this chart apart from the others of its context. Netdata spells it in
// either half of the id — disk_util.sda, cpu.cpu0_interrupts, app.sshd_cpu_utilization,
// systemd_containerd.cpu_utilization — so the half that is not a tail of the context
// is the instance, with the context's tail trimmed from it.
func netdataSubject(id, context string) (string, map[string]string) {
	if id == context {
		return "", nil
	}
	typ, rest, _ := strings.Cut(id, ".")
	segs := strings.Split(context, ".")
	family := strings.TrimSuffix(segs[0], "_ext")
	if m := kubeContainerRe.FindStringSubmatch(typ); m != nil {
		uid := strings.ReplaceAll(m[1], "_", "-")
		return "container:" + m[2], map[string]string{"kind": "container", "pod_uid": uid, "netdata_chart": id}
	}
	instance := rest
	if isContextTail(rest, segs) || rest == "" {
		instance = strings.TrimPrefix(typ, segs[0]+"_")
	} else {
		for i := 1; i < len(segs); i++ { // the longest tail first
			if tail := "_" + strings.Join(segs[i:], "_"); strings.HasSuffix(rest, tail) && len(rest) > len(tail) {
				instance = strings.TrimSuffix(rest, tail)
				break
			}
		}
	}
	return family + ":" + sanitiseIdentity(instance), map[string]string{"kind": family, "netdata_chart": id}
}

// isContextTail reports whether s spells the last segments of a context, joined by
// '_' or '.' (cpu_utilization for systemd.service.cpu.utilization).
func isContextTail(s string, segs []string) bool {
	for i := 1; i < len(segs); i++ {
		if s == strings.Join(segs[i:], "_") || s == strings.Join(segs[i:], ".") {
			return true
		}
	}
	return false
}

// sanitiseMetric replaces every byte outside the metric charset [A-Za-z0-9._-] with
// '_': Netdata's dimension names carry spaces ("some 10") and other punctuation.
func sanitiseMetric(name string) string {
	b := []byte(name)
	for i, ch := range b {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-') {
			b[i] = '_'
		}
	}
	return string(b)
}
