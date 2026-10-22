package groups

import (
	"slices"
	"testing"

	"github.com/DiyazY/di-agent/pkg/runtime"
)

func pod(subject string, labels map[string]string) runtime.Workload {
	return runtime.Workload{Subject: subject, Labels: labels}
}

func members(t *testing.T, top Topology, id string) []string {
	t.Helper()
	for _, g := range top.Groups {
		if g.ID == id {
			return g.Members
		}
	}
	return nil
}

func TestPodsGroupByTheirLabel(t *testing.T) {
	top := Compute(DefaultConfig(), Input{
		Workloads: []runtime.Workload{
			pod("pod:pg", map[string]string{"di-agent/group": "storage"}),
			pod("pod:redis", map[string]string{"di-agent/group": "storage", "app.kubernetes.io/part-of": "cache"}),
			pod("pod:mongo", map[string]string{"app.kubernetes.io/part-of": "storage"}),
			pod("pod:web", map[string]string{"app": "web"}),
			pod("pod:dns", map[string]string{"k8s/namespace": "kube-system"}),
		},
	})
	if got := members(t, top, "group:storage"); !slices.Equal(got, []string{"pod:mongo", "pod:pg", "pod:redis"}) {
		t.Fatalf("storage %v; di-agent/group first, then app.kubernetes.io/part-of", got)
	}
	if got := members(t, top, "group:kube-system"); !slices.Equal(got, []string{"pod:dns"}) {
		t.Fatalf("kube-system %v; a system pod with no group label is Kubernetes' own", got)
	}
	for _, g := range top.Groups {
		if slices.Contains(g.Members, "pod:web") {
			t.Fatalf("pod:web is in %s; an unlabelled workload belongs to no group", g.ID)
		}
	}
}

func TestUnitsDevicesAndTheirParents(t *testing.T) {
	top := Compute(DefaultConfig(), Input{
		Subjects: []string{"unit:ollama.service", "unit:k0scontroller.service", "cpu:cpu0", "disk:sda",
			"net:enp0s1", "net:kube-bridge", "net:veth1", "net:veth2", "container:pg.db", "container:pg.exporter"},
		Physical:      map[string]bool{"enp0s1": true},
		VethPods:      map[string]string{"veth1": "pod:pg"},
		ContainerPods: map[string]string{"container:pg.db": "pod:pg", "container:pg.exporter": "pod:pg"},
	})
	if got := members(t, top, "group:system"); !slices.Equal(got, []string{"unit:k0scontroller.service", "unit:ollama.service"}) {
		t.Fatalf("system %v", got)
	}
	if got := members(t, top, "group:hardware"); !slices.Equal(got, []string{"cpu:cpu0", "disk:sda", "net:enp0s1"}) {
		t.Fatalf("hardware %v; cores, disks and physical interfaces", got)
	}
	if got := members(t, top, "group:network"); !slices.Equal(got, []string{"net:kube-bridge", "net:veth2"}) {
		t.Fatalf("network %v; virtual interfaces no pod owns", got)
	}
	want := map[string]string{"net:veth1": "pod:pg", "container:pg.db": "pod:pg", "container:pg.exporter": "pod:pg"}
	for s, p := range want {
		if top.Parents[s] != p {
			t.Errorf("parent of %s %q; want %s", s, top.Parents[s], p)
		}
	}
	for _, g := range top.Groups {
		if (g.ID == "group:hardware" || g.ID == "group:network") == g.Consumers {
			t.Errorf("%s consumers=%v; only pods and units use the machine", g.ID, g.Consumers)
		}
	}
}
