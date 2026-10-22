package groups

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Linux reads what Input needs from a Linux node: which interfaces are hardware, and
// which pod each host-side virtual interface leads to. Roots are /sys, /proc and
// /sys/fs/cgroup in production. What cannot be read is left out, not guessed.
type Linux struct {
	SysRoot, ProcRoot, CgroupRoot string
}

// Physical names the interfaces that have a device behind them.
func (l Linux) Physical() map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(l.SysRoot, "class/net"))
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(l.SysRoot, "class/net", e.Name(), "device")); err == nil {
			out[e.Name()] = true
		}
	}
	return out
}

var podDir = regexp.MustCompile(`pod([0-9a-f]{8}[-_][0-9a-f]{4}[-_][0-9a-f]{4}[-_][0-9a-f]{4}[-_][0-9a-f]{12})`)

// VethPods maps each host interface whose peer is a pod's eth0 to that pod: the host
// side's iflink is the peer's index in the pod's network namespace, which the pod's
// /proc/<pid>/net/igmp lists against eth0.
func (l Linux) VethPods() map[string]string {
	byIndex := map[int]string{} // eth0's index in each pod's namespace → pod subject
	for _, kp := range []string{"kubepods", "kubepods.slice"} {
		_ = filepath.WalkDir(filepath.Join(l.CgroupRoot, kp), func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			m := podDir.FindStringSubmatch(d.Name())
			if m == nil {
				return nil
			}
			subject := "pod:" + strings.ReplaceAll(m[1], "_", "-")
			if idx, ok := l.eth0Index(p); ok {
				byIndex[idx] = subject
			}
			return filepath.SkipDir
		})
	}
	out := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(l.SysRoot, "class/net"))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(l.SysRoot, "class/net", e.Name(), "iflink"))
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && byIndex[n] != "" {
			out[e.Name()] = byIndex[n]
		}
	}
	return out
}

// eth0Index is eth0's index in the network namespace of any process in the pod's cgroup.
func (l Linux) eth0Index(podCgroup string) (int, bool) {
	idx, found := 0, false
	_ = filepath.WalkDir(podCgroup, func(p string, d fs.DirEntry, err error) error {
		if found || err != nil || !d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(filepath.Join(p, "cgroup.procs"))
		if err != nil {
			return nil
		}
		for _, pid := range strings.Fields(string(b)) {
			if i, ok := l.igmpEth0(pid); ok {
				idx, found = i, true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return idx, found
}

func (l Linux) igmpEth0(pid string) (int, bool) {
	f, err := os.Open(filepath.Join(l.ProcRoot, pid, "net/igmp"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[1] == "eth0" {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
