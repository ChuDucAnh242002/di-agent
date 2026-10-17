package minimal

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/types"
)

// CgroupCollector is the edge-minimal CollectorContract implementation.
// It reads cgroups v2 files directly from cgroupRoot (/sys/fs/cgroup in
// production), requiring no external monitoring daemon. Designed for RPi4
// and similarly constrained nodes.
//
// Beyond the node-level aggregate at the root, the collector can walk the tree
// and read the same three files per subject: a pod cgroup (systemd or cgroupfs
// driver, recognised by recogniseKubepods) and, when allowlisted by glob, a
// direct child of system.slice (recognised by recogniseUnits). A container
// scope below a pod is never a subject — the pod is the unit of workload and
// its cgroup survives container restarts, so the walk collects the pod's
// directory and does not descend into it. The walk is depth-limited and
// capped at CgroupOptions.MaxSubjects; beyond the cap the collector logs once
// and skips the rest.
//
// CPU and memory samples — root or subject — are declared as "fraction" with
// Range [0,1]: a fraction of what the whole node has, never a fraction of a
// subject-local limit. "fraction" is the unit the domain specification declares
// for the routed node-level properties, and the map refuses a reading whose unit
// contradicts the declared one. When the node's capacity is
// unknown (no MemTotal), memory is not reported at all rather than as a share of
// the subject's own memory.max, which is a different quantity. cpu_throttle_ratio
// is throttled periods over elapsed periods and is declared as "share-of-periods"
// on [0,1]. The node's memory share is the one reading that does not
// come from a cgroup file: a cgroup v2 root has no memory.current / memory.max, so
// it is (MemTotal − MemAvailable) / MemTotal from /proc/meminfo. A subject that has vanished between one Collect() and
// the next simply produces no samples this tick; its cgroup snapshot is
// dropped so a later reappearance (a reused pod UID, say) starts clean. There
// is no "gone" event — absence is silence, not a signal.
//
// cpu_pressure_ratio, io_pressure_ratio and memory_pressure_ratio are the PSI "some"
// stall share of the window, from cpu.pressure, io.pressure and memory.pressure,
// declared as "fraction" on [0,1]: the share of the window in which at least one task
// waited for that resource. Each is absent where the kernel exposes no PSI for it.
//
// cpu_steal_ratio is node-level only: the share of CPU time in the window the
// hypervisor spent on someone else, from the steal column of /proc/stat's cpu line,
// declared as "fraction" on [0,1]. It is zero on bare metal and is what separates
// "this VM is busy" from "the host is busy" when pressure rises with nothing local
// to explain it.
//
// The windowed metrics (CPU, throttling, the three pressures, steal) require two consecutive
// Collect() calls per subject to establish a measurement window. The first
// call a subject is seen, it contributes only memory_utilization (which is
// instantaneous); CPU samples follow from the second call onward.
//
// Cgroups v1 is not supported — Ubuntu 22.04 on RPi4 uses v2 by default.
// If a directory's files are unreadable, that read is skipped rather than
// treated as an error (transient unavailability per contract).
type CgroupCollector struct {
	nodeID     string
	cgroupRoot string
	sid        string  // stable source identifier
	numCPU     float64 // logical CPUs on this node
	memTotal   uint64  // node memory capacity in bytes; 0 if unknown
	opts       CgroupOptions
	recognise  []recogniser

	mu   sync.Mutex
	prev map[string]*cpuSnapshot // key: subject ("" = root)
	// Each condition below is said once (or, for the cap, whenever the count
	// changes): a collector that goes silent is the failure this project treats as
	// worse than an error, because nothing about an empty map looks wrong.
	rootWarned   bool
	noPodsWarned bool
	lastSkipped  int

	podNames map[string][2]string // pod UID → namespace, name; from PodLogRoot
}

func (c *CgroupCollector) log(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// DefaultPodLogRoot is where the kubelet keeps one directory per pod.
const DefaultPodLogRoot = "/var/log/pods"

// CgroupOptions configures subject detection. The zero value walks nothing; the
// daemon's defaults are Subjects on with MaxSubjects 256.
type CgroupOptions struct {
	// Subjects enables the walk over pod cgroups (and allowlisted units).
	Subjects bool
	// UnitGlobs allowlists direct children of system.slice as unit:<name> subjects.
	UnitGlobs []string
	// MaxSubjects bounds the walk; beyond it the collector logs once and skips.
	MaxSubjects int
	// CmdLabel stamps cmd=<argv0> from the subject's first process. Needs /proc
	// visibility (hostPID or privileged), hence off by default.
	CmdLabel bool
	// MemTotalBytes is the node's memory capacity. 0 reads /proc/meminfo; when that
	// is unreadable too, no memory sample is emitted for any subject.
	MemTotalBytes uint64
	// ProcRoot is where /proc is mounted (tests point it at a fake tree).
	ProcRoot string
	// PodLogRoot is the kubelet's pod log directory, one <namespace>_<name>_<uid>
	// entry per pod; each pod subject is labelled namespace=<namespace> and
	// pod=<name> from it. Empty names nothing: the subject stays the UID either way.
	PodLogRoot string
	// Now is the clock the CPU windows are measured on; nil means time.Now. Tests
	// advance it instead of sleeping.
	Now func() time.Time
	// Logf receives the collector's own log lines: an unreadable root, a root with
	// no pod cgroups while subjects are on, and subjects skipped by the cap. nil
	// means the standard logger.
	Logf func(format string, args ...any)
}

type cpuSnapshot struct {
	ts          time.Time
	usageUsec   uint64
	nrPeriods   uint64
	nrThrottled uint64
	// stall holds each PSI file's cumulative "some" stall time, in psiResources
	// order; hasStall is false where the file is absent (a kernel booted with psi=0,
	// or a cgroup without it).
	stall    [len(psiResources)]uint64
	hasStall [len(psiResources)]bool
	// stealTicks and cpuTicks are /proc/stat's cumulative steal and total CPU time,
	// read for the node only; hasSteal is false when /proc/stat could not be read.
	stealTicks, cpuTicks uint64
	hasSteal             bool
}

// psiResources are the pressure files read per cgroup, and the metric each becomes.
var psiResources = [...]struct {
	file   string
	metric types.MetricType
}{
	{"cpu.pressure", types.CPUPressureRatio},
	{"io.pressure", types.IOPressureRatio},
	{"memory.pressure", types.MemoryPressureRatio},
}

var cgroupAvailMetrics = []types.MetricType{
	types.CPUUtilization,
	types.MemoryUtilization,
	types.CPUThrottleRatio,
	types.CPUPressureRatio,
	types.IOPressureRatio,
	types.MemoryPressureRatio,
	types.CPUStealRatio,
}

// NewCgroupCollector creates a collector with the daemon defaults: subjects on,
// 256 at most, pods named from the kubelet's log directory, no units, no cmd label.
func NewCgroupCollector(nodeID, cgroupRoot string) *CgroupCollector {
	return NewCgroupCollectorWithOptions(nodeID, cgroupRoot, CgroupOptions{Subjects: true, MaxSubjects: 256, PodLogRoot: DefaultPodLogRoot})
}

// NewCgroupCollectorWithOptions creates a collector reading from cgroupRoot.
//
//	Production:  NewCgroupCollectorWithOptions("node_1", "/sys/fs/cgroup", opts)
//	Testing:     NewCgroupCollectorWithOptions("test-node", t.TempDir(), opts) — fake files
func NewCgroupCollectorWithOptions(nodeID, cgroupRoot string, opts CgroupOptions) *CgroupCollector {
	if opts.MaxSubjects <= 0 {
		opts.MaxSubjects = 256
	}
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	c := &CgroupCollector{
		nodeID: nodeID, cgroupRoot: cgroupRoot, sid: "cgroup:" + nodeID,
		numCPU: float64(runtime.NumCPU()), opts: opts,
		prev: make(map[string]*cpuSnapshot),
	}
	c.memTotal = opts.MemTotalBytes
	if c.memTotal == 0 {
		c.memTotal = readMemTotal(filepath.Join(opts.ProcRoot, "meminfo"))
	}
	c.recognise = []recogniser{recogniseKubepods}
	// A malformed pattern is dropped here with a line (Build refuses it earlier
	// through ValidUnitGlobs); path.Match would otherwise report the error on every
	// call and the pattern would match nothing, silently.
	var globs []string
	for _, g := range opts.UnitGlobs {
		if _, err := path.Match(g, ""); err != nil {
			c.log("cgroup collector: -cgroup-units pattern %q is malformed (%v) and is ignored", g, err)
			continue
		}
		globs = append(globs, g)
	}
	c.opts.UnitGlobs = globs
	if len(globs) > 0 {
		c.recognise = append(c.recognise, recogniseUnits(globs))
	}
	return c
}

func (c *CgroupCollector) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

func (c *CgroupCollector) SourceID() string                     { return c.sid }
func (c *CgroupCollector) AvailableMetrics() []types.MetricType { return cgroupAvailMetrics }

// Collect reads the root and, when enabled, every recognised subject, at one instant.
// A subject exists iff its directory is present, recognised, within the depth bound and
// the subject cap, and readable this tick; nothing is emitted for one that has gone, and
// its snapshot is dropped. Unreadable files are transient: skipped, not errors.
func (c *CgroupCollector) Collect() ([]*types.MetricSample, error) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	out, rootErr := c.collectOneLocked("", c.cgroupRoot, nil, now)
	if rootErr != nil && !c.rootWarned {
		c.log("cgroup collector: root %s is unreadable (%v): no node properties will be produced until it is readable — is a cgroup v2 hierarchy mounted at -cgroup-root?",
			c.cgroupRoot, rootErr)
		c.rootWarned = true
	}
	if !c.opts.Subjects {
		return out, nil
	}
	seen := map[string]bool{}
	root := filepath.Clean(c.cgroupRoot)
	var sawPods, rescanned bool
	var skipped int
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		first, _, _ := strings.Cut(rel, "/")
		unitsEnabled := len(c.opts.UnitGlobs) > 0
		if strings.HasPrefix(first, "kubepods") {
			sawPods = true
		}
		if !strings.HasPrefix(first, "kubepods") && !(first == "system.slice" && unitsEnabled) {
			return filepath.SkipDir
		}
		if strings.Count(rel, "/") > 3 {
			return filepath.SkipDir
		}
		for _, r := range c.recognise {
			info, ok := r(rel)
			if !ok {
				continue
			}
			if len(seen) >= c.opts.MaxSubjects {
				skipped++
				return filepath.SkipDir
			}
			seen[info.subject] = true
			labels := info.labels
			if uid := labels["pod_uid"]; uid != "" {
				if nn, ok := c.podNameLocked(uid, &rescanned); ok {
					labels["namespace"], labels["pod"] = nn[0], nn[1]
				}
			}
			if c.opts.CmdLabel {
				if cmd := c.cmdLabel(p); cmd != "" {
					labels["cmd"] = cmd
				}
			}
			samples, _ := c.collectOneLocked(info.subject, p, labels, now) // unreadable: transient, skipped
			out = append(out, samples...)
			return filepath.SkipDir // a subject's children are not subjects
		}
		return nil
	})
	if !sawPods && !c.noPodsWarned {
		c.log("cgroup collector: no kubepods cgroup under %s while -cgroup-subjects is on; if the agent runs in a container this root is its own cgroup, not the node's — mount the host's cgroup root",
			c.cgroupRoot)
		c.noPodsWarned = true
	}
	if skipped != c.lastSkipped {
		if skipped > 0 {
			c.log("cgroup collector: %d subjects beyond -cgroup-max-subjects=%d skipped this tick; the census under-counts by that many",
				skipped, c.opts.MaxSubjects)
		} else {
			c.log("cgroup collector: no subjects skipped by -cgroup-max-subjects=%d any more", c.opts.MaxSubjects)
		}
		c.lastSkipped = skipped
	}
	for subject := range c.prev {
		if subject != "" && !seen[subject] {
			delete(c.prev, subject)
		}
	}
	return out, nil
}

// collectOneLocked reads one cgroup directory as one subject ("" = the node). The
// error is the cpu.stat read failing, which means no samples at all for this
// directory; the caller decides whether that is worth saying.
func (c *CgroupCollector) collectOneLocked(subject, dir string, labels map[string]string, now time.Time) ([]*types.MetricSample, error) {
	cpu, err := readCPUStat(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return nil, err
	}
	memCurrent, memErr := readMemoryCurrent(dir)
	snap := &cpuSnapshot{ts: now, usageUsec: cpu.usageUsec, nrPeriods: cpu.nrPeriods, nrThrottled: cpu.nrThrottled}
	for i, r := range psiResources {
		v, err := readStallUsec(filepath.Join(dir, r.file))
		snap.stall[i], snap.hasStall[i] = v, err == nil
	}
	if subject == "" {
		snap.stealTicks, snap.cpuTicks, snap.hasSteal = readCPUSteal(filepath.Join(c.opts.ProcRoot, "stat"))
	}
	prev := c.prev[subject]
	c.prev[subject] = snap

	var samples []*types.MetricSample
	switch {
	case memErr == nil && c.memTotal > 0:
		samples = append(samples, c.sample(types.MemoryUtilization, unitNodeShare, clamp(float64(memCurrent)/float64(c.memTotal), 0, 1), now, now, subject, labels))
	case memErr != nil && subject == "":
		// The root of a cgroup v2 hierarchy carries no memory.current / memory.max —
		// the kernel does not create them there — so the node's memory share has to
		// come from /proc/meminfo. Without this the one number an operator asking
		// "how much memory is this machine using" expects was never emitted at all.
		// A subject keeps using its own cgroup files; only the node falls back here,
		// and only when its cgroup cannot answer (a container as root can).
		if share, ok := readMemInUseShare(filepath.Join(c.opts.ProcRoot, "meminfo")); ok {
			samples = append(samples, c.sample(types.MemoryUtilization, unitNodeShare, clamp(share, 0, 1), now, now, subject, labels))
		}
	}
	if prev == nil {
		return samples, nil
	}
	elapsedUs := now.Sub(prev.ts).Microseconds()
	if elapsedUs < 1000 {
		return samples, nil
	}
	if cpu.usageUsec >= prev.usageUsec {
		util := clamp(float64(cpu.usageUsec-prev.usageUsec)/(float64(elapsedUs)*c.numCPU), 0, 1)
		samples = append(samples, c.sample(types.CPUUtilization, unitNodeShare, util, now, prev.ts, subject, labels))
	}
	// Both counters are guarded: a cgroup recreated under the same subject between
	// two ticks restarts them, and an unsigned delta across that wraps to a maximal,
	// plausible-looking ratio.
	if cpu.nrPeriods > prev.nrPeriods && cpu.nrThrottled >= prev.nrThrottled {
		throttle := clamp(float64(cpu.nrThrottled-prev.nrThrottled)/float64(cpu.nrPeriods-prev.nrPeriods), 0, 1)
		samples = append(samples, c.sample(types.CPUThrottleRatio, unitPeriodShare, throttle, now, prev.ts, subject, labels))
	}
	// Pressure is the share of the window in which at least one task in this cgroup
	// waited for the resource: the delta of PSI's cumulative "some" stall time over
	// the elapsed time. The cumulative counter is used rather than avg10 so the
	// reading covers exactly the collection window, whatever the interval. Without
	// it nothing live observes cpu_pressure_ratio, and every construct and
	// proposition it feeds stays unknown on a real node. Guarded against a counter
	// reset like the others.
	for i, r := range psiResources {
		if snap.hasStall[i] && prev.hasStall[i] && snap.stall[i] >= prev.stall[i] {
			pressure := clamp(float64(snap.stall[i]-prev.stall[i])/float64(elapsedUs), 0, 1)
			samples = append(samples, c.sample(r.metric, unitNodeShare, pressure, now, prev.ts, subject, labels))
		}
	}
	// Steal is a share of CPU time, so it is measured in /proc/stat's own ticks on
	// both sides of the ratio rather than against wall time, which would need
	// USER_HZ and the CPU count to agree with the kernel's.
	if snap.hasSteal && prev.hasSteal && snap.cpuTicks > prev.cpuTicks && snap.stealTicks >= prev.stealTicks {
		steal := clamp(float64(snap.stealTicks-prev.stealTicks)/float64(snap.cpuTicks-prev.cpuTicks), 0, 1)
		samples = append(samples, c.sample(types.CPUStealRatio, unitNodeShare, steal, now, prev.ts, subject, labels))
	}
	return samples, nil
}

// cmdLabel returns the basename of argv0 of the first process in the cgroup, or "".
func (c *CgroupCollector) cmdLabel(dir string) string {
	procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return ""
	}
	pid, _, _ := strings.Cut(strings.TrimSpace(string(procs)), "\n")
	if pid == "" {
		return ""
	}
	cmdline, err := os.ReadFile(filepath.Join(c.opts.ProcRoot, pid, "cmdline"))
	if err != nil {
		return ""
	}
	argv0, _, _ := strings.Cut(string(cmdline), "\x00")
	if argv0 == "" {
		return ""
	}
	return filepath.Base(argv0)
}

// readMemInUseShare parses MemTotal and MemAvailable from /proc/meminfo and returns
// (MemTotal − MemAvailable) / MemTotal — memory the node cannot hand to a new
// workload without reclaiming, as a share of what it has. Reports false when either
// key is missing, malformed or zero: a node memory share nobody can compute is better
// left unsaid than guessed at.
//
// This counts page cache differently from cgroup accounting — reclaimable cache is
// excluded here, whereas memory.current includes a cgroup's own page cache — so the
// node figure and the sum of its subjects' figures are not the same quantity and
// should not be expected to add up.
func readMemInUseShare(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var total, available uint64
	var haveTotal, haveAvailable bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, haveTotal = kb, true
		case "MemAvailable:":
			available, haveAvailable = kb, true
		}
	}
	if !haveTotal || !haveAvailable || total == 0 || available > total {
		return 0, false
	}
	return float64(total-available) / float64(total), true
}

// readMemTotal parses MemTotal from /proc/meminfo (a kB figure) and returns it in
// bytes. 0 when unavailable.
func readMemTotal(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb * 1024
		}
	}
	return 0
}

var shareRange = [2]float64{0, 1}

const (
	// unitNodeShare: a fraction of what the whole node has (CPU, memory), for the
	// node and for every subject alike — a pod's share is of the node, never of its
	// own limit. It is "fraction" because that is the unit the domain specification
	// declares for the routed node-level properties, and the one every other
	// node-level producer (Netdata, replay, the scripted system) uses. The map
	// refuses an observation that contradicts a declared unit, so any other label
	// had every node-level reading refused; and the Collector contract keeps a
	// type's unit stable across subjects, so the subjects carry the same label.
	unitNodeShare = "fraction"
	// unitPeriodShare: throttled CFS periods over elapsed periods, a fraction of
	// the subject's own scheduling periods rather than of the node.
	unitPeriodShare = "share-of-periods"
)

// sample builds a MetricSample with a deterministic event_id over
// (source, node, subject, metric, anchor), declared in the unit the caller names
// on [0,1] — this collector's meaning, not the map's rule.
func (c *CgroupCollector) sample(mt types.MetricType, unit string, value float64, ts, anchorTs time.Time, subject string, labels map[string]string) *types.MetricSample {
	key := fmt.Sprintf("%s:%s:%s:%s:%d", c.sid, c.nodeID, subject, string(mt), anchorTs.Unix())
	h := sha256.Sum256([]byte(key))
	rng := shareRange
	return &types.MetricSample{
		NodeID: c.nodeID, MetricType: mt, Value: value, TimestampUnix: ts.Unix(),
		EventID: fmt.Sprintf("%x", h[:8]),
		Subject: subject, Unit: unit, Range: &rng, Source: c.sid, Labels: labels,
	}
}

// ── cgroup v2 file readers ────────────────────────────────────────────────────

type rawCPUStat struct {
	usageUsec   uint64
	nrPeriods   uint64
	nrThrottled uint64
}

// readCPUStat parses the cgroups v2 cpu.stat key-value file.
// Unknown keys are silently ignored — the format may include additional fields
// on newer kernels.
func readCPUStat(path string) (*rawCPUStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat := &rawCPUStat{}
	var sawUsage bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "usage_usec":
			stat.usageUsec = v
			sawUsage = true
		case "nr_periods":
			stat.nrPeriods = v
		case "nr_throttled":
			stat.nrThrottled = v
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawUsage {
		// Parsed to 0, this would become cpu_utilization = 0 two ticks later — a
		// reading of nothing presented as a measurement of idleness.
		return nil, fmt.Errorf("%s has no usage_usec", path)
	}
	return stat, nil
}

// readStallUsec returns the cumulative "some" stall time, in microseconds, from a
// cgroup v2 PSI file (cpu.pressure, io.pressure, memory.pressure):
//
//	some avg10=0.35 avg60=0.28 avg300=0.34 total=11060241
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
func readStallUsec(path string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "some" {
			continue
		}
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "total="); ok {
				return strconv.ParseUint(v, 10, 64)
			}
		}
	}
	return 0, fmt.Errorf("%s has no some total", path)
}

// readCPUSteal returns the cumulative steal and total CPU time from /proc/stat's
// aggregate line, in the kernel's ticks:
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
//
// The total is the first eight columns: guest and guest_nice are already counted in
// user and nice. Reports false when the line is missing or has no steal column
// (kernels before 2.6.11), since a steal of zero would then be a guess.
func readCPUSteal(path string) (steal, total uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 9 {
			return 0, 0, false
		}
		for i, f := range fields[1:9] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
			if i == 7 {
				steal = v
			}
		}
		return steal, total, true
	}
	return 0, 0, false
}

// readMemoryCurrent returns the cgroup's memory.current in bytes. memory.max is not
// read: a share of a subject's own limit is not a quantity this collector reports.
func readMemoryCurrent(cgroupRoot string) (uint64, error) {
	cur, err := os.ReadFile(filepath.Join(cgroupRoot, "memory.current"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(cur)), 10, 64)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// podNameLocked is a pod's namespace and name, from the kubelet's log directory. The
// directory is read again on the first pod of a tick that it does not name, not on
// every pod: a pod's entry exists from before its containers start, so a miss is a
// pod new since the last read, or one the kubelet keeps no logs for.
func (c *CgroupCollector) podNameLocked(uid string, rescanned *bool) ([2]string, bool) {
	if c.opts.PodLogRoot == "" {
		return [2]string{}, false
	}
	if nn, ok := c.podNames[uid]; ok {
		return nn, true
	}
	if *rescanned {
		return [2]string{}, false
	}
	*rescanned = true
	entries, err := os.ReadDir(c.opts.PodLogRoot)
	if err != nil {
		return [2]string{}, false
	}
	c.podNames = make(map[string][2]string, len(entries))
	for _, e := range entries {
		// Namespaces and pod names are DNS labels and subdomains, which never
		// contain '_', so the split is unambiguous.
		parts := strings.Split(e.Name(), "_")
		if !e.IsDir() || len(parts) != 3 {
			continue
		}
		c.podNames[strings.ToLower(parts[2])] = [2]string{parts[0], parts[1]}
	}
	nn, ok := c.podNames[uid]
	return nn, ok
}
