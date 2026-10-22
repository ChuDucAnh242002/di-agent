package groups

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAVirtualInterfaceBelongsToThePodAtItsOtherEnd: a host veth's iflink is its peer's
// index inside the pod's network namespace, and the pod's /proc/<pid>/net/igmp lists
// eth0 under that index. A NIC with a device is hardware.
func TestAVirtualInterfaceBelongsToThePodAtItsOtherEnd(t *testing.T) {
	root := t.TempDir()
	sys, proc, cg := filepath.Join(root, "sys"), filepath.Join(root, "proc"), filepath.Join(root, "cgroup")
	write(t, filepath.Join(sys, "class/net/enp0s1/device/vendor"), "0x1af4\n")
	write(t, filepath.Join(sys, "class/net/enp0s1/iflink"), "2\n")
	write(t, filepath.Join(sys, "class/net/veth1/iflink"), "3\n")
	write(t, filepath.Join(sys, "class/net/veth2/iflink"), "7\n")
	const uid = "0d7b544e-0000-4000-8000-000000000001"
	// cgroupfs layout; the pod's own cgroup.procs is empty and a container's holds the pid.
	write(t, filepath.Join(cg, "kubepods/burstable/pod"+uid+"/cgroup.procs"), "")
	write(t, filepath.Join(cg, "kubepods/burstable/pod"+uid+"/abc123/cgroup.procs"), "4242\n")
	write(t, filepath.Join(proc, "4242/net/igmp"),
		"Idx\tDevice    : Count Querier\tGroup    Users Timer\tReporter\n"+
			"1\tlo        :     1      V3\n\t\t\t\t010000E0     1 0:00000000\t\t0\n"+
			"3\teth0      :     1      V3\n\t\t\t\t010000E0     1 0:00000000\t\t0\n")
	src := Linux{SysRoot: sys, ProcRoot: proc, CgroupRoot: cg}
	if p := src.Physical(); !p["enp0s1"] || p["veth1"] {
		t.Fatalf("physical %v; want enp0s1 only", p)
	}
	got := src.VethPods()
	if got["veth1"] != "pod:"+uid || got["veth2"] != "" {
		t.Fatalf("veth pods %v; want veth1 on the pod, veth2 on none", got)
	}
}
