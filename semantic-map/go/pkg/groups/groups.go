// Package groups says what belongs to what on a node: which workloads form a group an
// operator named, which subjects are the node's system and its hardware, and which pod
// a container or a virtual interface is part of. The subjects keep their flat ids —
// decisions and relationships refer to those — and the topology is computed over them,
// each time from what the node runs now.
package groups

import (
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/DiyazY/di-agent/pkg/runtime"
)

// Config says where groups come from.
type Config struct {
	// LabelKeys are the pod labels that name a pod's group, first set one wins.
	LabelKeys []string
	// Units maps a group to globs of unit names (ollama.service) it takes.
	Units map[string][]string
}

// DefaultConfig groups pods by di-agent/group, then app.kubernetes.io/part-of, and every
// unit the map models into system.
func DefaultConfig() Config {
	return Config{
		LabelKeys: []string{"di-agent/group", "app.kubernetes.io/part-of"},
		Units:     map[string][]string{"system": {"*"}},
	}
}

// Input is what the node runs now.
type Input struct {
	Workloads []runtime.Workload // the runtime's, with their labels
	Subjects  []string           // every subject the map holds
	// Physical names the network interfaces that are hardware (have a device).
	Physical map[string]bool
	// VethPods maps a host-side virtual interface to the pod at its other end.
	VethPods map[string]string
	// ContainerPods maps a container subject to its pod.
	ContainerPods map[string]string
}

// Group is a named set of subjects. Consumers says its members use the machine — pods
// and units, which have CPU and memory of their own — so the map holds its sums; the
// hardware and network groups are structure only.
type Group struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Members   []string `json:"members"`
	Consumers bool     `json:"consumers"`
}

// Topology is the node's groups, sorted by id, and each part's whole: a container's or a
// virtual interface's pod.
type Topology struct {
	Groups  []Group           `json:"groups"`
	Parents map[string]string `json:"parents"`
}

// KubeSystem is the group a kube-system pod with no group label belongs to.
const KubeSystem = "kube-system"

// Compute is the topology of in under cfg.
func Compute(cfg Config, in Input) Topology {
	type acc struct {
		source    string
		consumers bool
		members   map[string]bool
	}
	groups := map[string]*acc{}
	add := func(name, source string, consumers bool, subject string) {
		g := groups[name]
		if g == nil {
			g = &acc{source: source, consumers: consumers, members: map[string]bool{}}
			groups[name] = g
		}
		g.members[subject] = true
	}
	for _, w := range in.Workloads {
		if name, key := podGroup(cfg, w.Labels); name != "" {
			add(name, key, true, w.Subject)
		}
	}
	top := Topology{Parents: map[string]string{}}
	for _, s := range in.Subjects {
		kind, id, _ := strings.Cut(s, ":")
		switch kind {
		case "unit":
			if name := unitGroup(cfg, id); name != "" {
				add(name, "units", true, s)
			}
		case "cpu", "disk":
			add("hardware", "device", false, s)
		case "net":
			switch {
			case in.VethPods[id] != "":
				top.Parents[s] = in.VethPods[id]
			case in.Physical[id]:
				add("hardware", "device", false, s)
			default:
				add("network", "virtual interface", false, s)
			}
		case "container":
			if p := in.ContainerPods[s]; p != "" {
				top.Parents[s] = p
			}
		}
	}
	for name, g := range groups {
		ms := make([]string, 0, len(g.members))
		for m := range g.members {
			ms = append(ms, m)
		}
		slices.Sort(ms)
		top.Groups = append(top.Groups, Group{ID: "group:" + Sanitize(name), Name: name, Source: g.source,
			Members: ms, Consumers: g.consumers})
	}
	sort.Slice(top.Groups, func(i, j int) bool { return top.Groups[i].ID < top.Groups[j].ID })
	return top
}

func podGroup(cfg Config, labels map[string]string) (name, source string) {
	for _, k := range cfg.LabelKeys {
		if v := labels[k]; v != "" {
			return v, "label " + k
		}
	}
	if labels["k8s/namespace"] == "kube-system" {
		return KubeSystem, "namespace kube-system"
	}
	return "", ""
}

func unitGroup(cfg Config, unit string) string {
	names := make([]string, 0, len(cfg.Units))
	for n := range cfg.Units {
		names = append(names, n)
	}
	slices.Sort(names) // a unit two globs match goes to the first group by name
	for _, n := range names {
		for _, g := range cfg.Units[n] {
			if ok, _ := path.Match(g, unit); ok {
				return n
			}
		}
	}
	return ""
}

// Sanitize makes a group name a subject identity: [A-Za-z0-9._-], anything else '_'.
func Sanitize(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
}
