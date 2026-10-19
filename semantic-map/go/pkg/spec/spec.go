// Package spec is the agent's one configuration document, agent.yaml. It
// replaces the command line: every setting the daemon takes lives here, with a
// default that is exactly what the flag it replaced defaulted to.
//
// Defaults come first and the file overlays them, so a file names only what it
// changes. Unknown keys are refused rather than ignored, because a misspelt
// threshold that silently keeps its default is a setting that describes nothing.
package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/DiyazY/di-agent/internal/minimal"
	"github.com/DiyazY/di-agent/pkg/autonomy"
)

// Redaction is what a secret is replaced by when a spec is shown.
const Redaction = "[redacted]"

// Spec is everything one agent is told about itself.
type Spec struct {
	Node    Node         `yaml:"node"`
	Profile Profile      `yaml:"profile"`
	Sources Sources      `yaml:"sources"`
	Facts   []FactSource `yaml:"facts"`
	Map     MapSpec      `yaml:"map"`
	Persist Persist      `yaml:"persist"`
	Peers   Peers        `yaml:"peers"`
	Domain  Domain       `yaml:"domain"`
	Explain Explain      `yaml:"explain"`
	Runtime RuntimeSpec  `yaml:"runtime"`
	Loops   Loops        `yaml:"loops"`
	Policy  PolicySpec   `yaml:"policy"`
}

// Node is the machine the agent models. ID empty falls back to the hostname.
type Node struct {
	Addr        string `yaml:"addr"`
	ID          string `yaml:"id"`
	IngestScope string `yaml:"ingest_scope"`
}

// Profile is the deployment profile and the dynamics of the map's estimates.
type Profile struct {
	Name        string  `yaml:"name"`
	KD          string  `yaml:"kd"`
	Priors      string  `yaml:"priors"`
	Regime      string  `yaml:"regime"`
	Alpha       float64 `yaml:"alpha"`
	AlphaSlow   float64 `yaml:"alpha_slow"`
	Convergence float64 `yaml:"convergence"`
	MinTrust    float64 `yaml:"min_trust"`
}

// Sources is where telemetry comes from. Interval 0 disables the collection loop.
type Sources struct {
	Interval time.Duration `yaml:"interval"`
	Cgroup   Cgroup        `yaml:"cgroup"`
	Netdata  Netdata       `yaml:"netdata"`
	Script   string        `yaml:"script"`
}

// Cgroup configures the cgroup collector; an empty Root disables it.
type Cgroup struct {
	Root        string   `yaml:"root"`
	Subjects    bool     `yaml:"subjects"`
	Units       []string `yaml:"units"`
	MaxSubjects int      `yaml:"max_subjects"`
	CmdLabel    bool     `yaml:"cmd_label"`
	PodLogs     string   `yaml:"pod_logs"`
}

// Netdata configures the Netdata collector; an empty URL disables it.
type Netdata struct {
	URL     string   `yaml:"url"`
	Include []string `yaml:"include"`
}

// FactSource names one source of the machine's facts and where its filesystem
// is mounted, for an agent in a container reading its host.
type FactSource struct {
	Kind string `yaml:"kind"`
	Root string `yaml:"root"`
}

// MapSpec is how the semantic map learns, discovers and forgets.
type MapSpec struct {
	Proposer      Proposer      `yaml:"proposer"`
	PairWindow    time.Duration `yaml:"pair_window"`
	PairSupport   int           `yaml:"pair_support"`
	PairHistory   int           `yaml:"pair_history"`
	StaleAfter    time.Duration `yaml:"stale_after"`
	RetireAfter   time.Duration `yaml:"retire_after"`
	Learn         bool          `yaml:"learn"`
	Admit         bool          `yaml:"admit"`
	SweepInterval time.Duration `yaml:"sweep_interval"`
	JournalSize   int           `yaml:"journal_size"`
	Tuner         bool          `yaml:"tuner"`
}

// Proposer is relationship discovery. Enabled false turns it off for low-CPU devices.
type Proposer struct {
	Enabled   bool    `yaml:"enabled"`
	Threshold float64 `yaml:"threshold"`
	MinPairs  int     `yaml:"min_pairs"`
	MinSpread float64 `yaml:"min_spread"`
	FDR       float64 `yaml:"fdr"`
	MaxPairs  int     `yaml:"max_pairs"`
}

// Persist is where the map and its journal are snapshotted. An empty File keeps
// everything in memory.
type Persist struct {
	File     string        `yaml:"file"`
	Interval time.Duration `yaml:"interval"`
}

// Peers are the other agents this one knows of.
type Peers struct {
	URLs       []string      `yaml:"urls"`
	StatePoll  time.Duration `yaml:"state_poll"`
	StateStale time.Duration `yaml:"state_stale"`
}

// Domain is the path to domain_spec.json; empty searches upward from the working directory.
type Domain struct {
	Path string `yaml:"path"`
}

// Explain is the natural-language explain backend.
type Explain struct {
	Provider      string `yaml:"provider"`
	URL           string `yaml:"url"`
	Model         string `yaml:"model"`
	Prompt        string `yaml:"prompt"`
	APIKey        string `yaml:"api_key" secret:"true"`
	PlannerPrompt string `yaml:"planner_prompt"`
	CriticPrompt  string `yaml:"critic_prompt"`
	KeepAlive     string `yaml:"keep_alive"`
	Sessions      bool   `yaml:"sessions"`
}

// RuntimeSpec is the orchestrator the agent acts on. Node is the orchestrator's
// name for this machine; empty means Node.ID.
type RuntimeSpec struct {
	Kind       string `yaml:"kind"`
	Kubeconfig string `yaml:"kubeconfig"`
	Node       string `yaml:"node"`
}

// Loops are the agent's autonomous activities.
type Loops struct {
	Review Review `yaml:"review"`
	Relief Relief `yaml:"relief"`
}

// Review is the reviewer of proposer candidates.
type Review struct {
	Level      string          `yaml:"level"`
	Interval   time.Duration   `yaml:"interval"`
	PerCycle   int             `yaml:"per_cycle"`
	Rules      []string        `yaml:"rules"`
	Renderer   string          `yaml:"renderer"`
	Thresholds ReviewThreshold `yaml:"thresholds"`
	Decision   Decision        `yaml:"decision"`
	Reasoning  Reasoning       `yaml:"reasoning"`
}

// ReviewThreshold is the probability each verdict needs from a model tier.
type ReviewThreshold struct {
	Confirm float64 `yaml:"confirm"`
	Reject  float64 `yaml:"reject"`
	Defer   float64 `yaml:"defer"`
}

// Decision is review tier 1, the fast decision model.
type Decision struct {
	Backend         string `yaml:"backend"`
	URL             string `yaml:"url"`
	Model           string `yaml:"model"`
	KeepAlive       string `yaml:"keep_alive"`
	ReasoningEffort string `yaml:"reasoning_effort"`
	APIKey          string `yaml:"api_key" secret:"true"`
}

// Reasoning is review tier 2, the model that may call read-only tools.
type Reasoning struct {
	Backend   string   `yaml:"backend"`
	URL       string   `yaml:"url"`
	Model     string   `yaml:"model"`
	KeepAlive string   `yaml:"keep_alive"`
	Tools     []string `yaml:"tools"`
	APIKey    string   `yaml:"api_key" secret:"true"`
}

// Relief is the loop that throttles a workload when the node's CPU stays hot.
type Relief struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
	CPU      CPURelief     `yaml:"cpu"`
}

// CPURelief tunes CPU relief. Enter and Exit are shares of node capacity in
// [0,1], with Exit below Enter so a hot node is not released the moment it
// cools by a hair. A throttle that has not cut pressure by MinEffect is judged
// to have failed attribution.
type CPURelief struct {
	Enter       float64       `yaml:"enter"`
	Exit        float64       `yaml:"exit"`
	EnterFor    time.Duration `yaml:"enter_for"`
	ExitFor     time.Duration `yaml:"exit_for"`
	Step        float64       `yaml:"step"`
	MinEffect   float64       `yaml:"min_effect"`
	VictimShare float64       `yaml:"victim_share"`
	Settle      time.Duration `yaml:"settle"`
	Expiry      time.Duration `yaml:"expiry"`
}

// Floor is the least a limit may be cut to: an absolute number of cores, or a
// share of the workload's original limit.
type Floor struct {
	CPUCores   float64 `yaml:"cpu_cores"`
	OfOriginal float64 `yaml:"of_original"`
}

// Settings is what a policy permits. A rule's zero values inherit the defaults.
type Settings struct {
	Allow             []string      `yaml:"allow"`
	Deny              []string      `yaml:"deny"`
	Floor             *Floor        `yaml:"floor"`
	MaxActionsPerHour int           `yaml:"max_actions_per_hour"`
	Cooldown          time.Duration `yaml:"cooldown"`
}

// Match selects workloads. Every field set must hold; a label matches when the
// workload carries any one of its values.
type Match struct {
	Kind   string                `yaml:"kind"`
	Name   string                `yaml:"name"`
	Labels map[string]StringList `yaml:"labels"`
}

// Rule applies Settings to the workloads it matches, or freezes them: a frozen
// workload is never acted on.
type Rule struct {
	Match    Match `yaml:"match"`
	Freeze   bool  `yaml:"freeze"`
	Settings `yaml:",inline"`
}

// PolicySpec is what the agent may do to a workload.
type PolicySpec struct {
	Defaults Settings `yaml:"defaults"`
	Rules    []Rule   `yaml:"rules"`
}

// StringList reads either one string or a list of them, so a label can be
// written as `a: x` or `a: [x, y]`.
type StringList []string

// UnmarshalYAML accepts a scalar or a sequence.
func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*l = StringList{s}
		return nil
	}
	var ss []string
	if err := n.Decode(&ss); err != nil {
		return err
	}
	*l = ss
	return nil
}

// Default is today's behaviour: each value is the default of the flag it replaced.
// Slices are empty rather than nil so that a printed spec loads back equal.
func Default() Spec {
	return Spec{
		Node: Node{Addr: ":8080", IngestScope: "self"},
		Profile: Profile{
			Name: "edge-minimal", Alpha: 0.2, AlphaSlow: 0.001, Convergence: 500, MinTrust: 0.5,
		},
		Sources: Sources{
			Interval: 10 * time.Second,
			Cgroup: Cgroup{
				Root: "/sys/fs/cgroup", Subjects: true, Units: []string{},
				MaxSubjects: 256, PodLogs: minimal.DefaultPodLogRoot,
			},
			Netdata: Netdata{Include: slices.Clone(minimal.DefaultNetdataInclude)},
		},
		Facts: []FactSource{{Kind: "host"}},
		Map: MapSpec{
			Proposer: Proposer{
				Enabled: true, Threshold: 0.85, MinPairs: 30,
				MinSpread: minimal.DefaultMinSpread, FDR: minimal.DefaultFDR,
			},
			PairWindow: 15 * time.Second,
			StaleAfter: 2 * time.Minute, RetireAfter: 10 * time.Minute,
			Learn: true, Admit: true, Tuner: true,
		},
		Persist: Persist{Interval: time.Minute},
		Peers:   Peers{URLs: []string{}, StatePoll: 30 * time.Second, StateStale: 90 * time.Second},
		Explain: Explain{
			Provider: "none", URL: "http://localhost:11434/v1", Model: "qwen2.5:7b-instruct",
			KeepAlive: "30m", Sessions: true,
		},
		Runtime: RuntimeSpec{Kind: "dry-run"},
		Loops: Loops{
			Review: Review{
				Level: "off", Interval: time.Minute, PerCycle: 10,
				Rules: []string{"composition"}, Renderer: "text",
				Thresholds: ReviewThreshold{
					Confirm: autonomy.DefaultThresholds.Confirm,
					Reject:  autonomy.DefaultThresholds.Reject,
					Defer:   autonomy.DefaultThresholds.Defer,
				},
				Decision: Decision{
					Backend: "systemone", URL: "http://localhost:11434",
					Model: "tev1:4b", ReasoningEffort: "none",
				},
				Reasoning: Reasoning{
					Backend: "none", URL: "http://localhost:11434/v1",
					Model: "qwen3.8:27b-nvfp4", KeepAlive: "10m",
					Tools: slices.Clone(autonomy.DefaultTools),
				},
			},
			Relief: Relief{
				Interval: 15 * time.Second,
				CPU: CPURelief{
					Enter: 0.25, Exit: 0.10, EnterFor: time.Minute, ExitFor: 3 * time.Minute,
					Step: 0.25, MinEffect: 0.03, VictimShare: 0.05,
					Settle: 2 * time.Minute, Expiry: time.Hour,
				},
			},
		},
		Policy: PolicySpec{
			Defaults: Settings{
				Allow: []string{"cpu.limit"}, Deny: []string{"evict"},
				Floor:             &Floor{CPUCores: 0.1, OfOriginal: 0.5},
				MaxActionsPerHour: 6, Cooldown: 10 * time.Minute,
			},
			Rules: []Rule{
				{
					Match:    Match{Labels: map[string]StringList{"di-agent/freeze": {"true"}}},
					Freeze:   true,
					Settings: Settings{Allow: []string{}, Deny: []string{}},
				},
				{
					Match: Match{Labels: map[string]StringList{
						"k8s/priority-class": {"system-node-critical", "system-cluster-critical"},
					}},
					Freeze:   true,
					Settings: Settings{Allow: []string{}, Deny: []string{}},
				},
			},
		},
	}
}

// Parse overlays one document on Default(). ${VAR} is replaced from the
// environment before decoding, so a secret can stay out of the file; an unset
// variable becomes empty.
func Parse(b []byte) (Spec, error) {
	s := Default()
	b = []byte(os.Expand(string(b), os.Getenv))
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return Spec{}, fmt.Errorf("spec: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// Load reads a spec file; an empty path is the default spec.
func Load(path string) (Spec, error) {
	if path == "" {
		return Default(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("spec: %w", err)
	}
	s, err := Parse(b)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Validate reports every value that cannot be what the operator meant.
func (s Spec) Validate() error {
	var errs []error
	share := func(name string, v float64) {
		if v < 0 || v > 1 {
			errs = append(errs, fmt.Errorf("%s = %v, want a value in [0,1]", name, v))
		}
	}
	positive := func(name string, d time.Duration) {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s = %v, want a positive duration", name, d))
		}
	}
	notNegative := func(name string, d time.Duration) {
		if d < 0 {
			errs = append(errs, fmt.Errorf("%s = %v, want a duration of zero or more", name, d))
		}
	}
	floor := func(name string, f *Floor) {
		if f == nil {
			return
		}
		if f.CPUCores < 0 {
			errs = append(errs, fmt.Errorf("%s.cpu_cores = %v, want zero or more", name, f.CPUCores))
		}
		if f.OfOriginal <= 0 || f.OfOriginal > 1 {
			errs = append(errs, fmt.Errorf("%s.of_original = %v, want a value in (0,1]", name, f.OfOriginal))
		}
	}

	notNegative("sources.interval", s.Sources.Interval)
	notNegative("map.stale_after", s.Map.StaleAfter)
	notNegative("map.retire_after", s.Map.RetireAfter)
	notNegative("persist.interval", s.Persist.Interval)
	notNegative("peers.state_poll", s.Peers.StatePoll)

	if !slices.Contains([]string{"off", "suggest", "rules", "bounded", "full"}, s.Loops.Review.Level) {
		errs = append(errs, fmt.Errorf("loops.review.level = %q, want off, suggest, rules, bounded or full", s.Loops.Review.Level))
	}
	share("loops.review.thresholds.confirm", s.Loops.Review.Thresholds.Confirm)
	share("loops.review.thresholds.reject", s.Loops.Review.Thresholds.Reject)
	share("loops.review.thresholds.defer", s.Loops.Review.Thresholds.Defer)

	r := s.Loops.Relief
	positive("loops.relief.interval", r.Interval)
	c := r.CPU
	share("loops.relief.cpu.enter", c.Enter)
	share("loops.relief.cpu.exit", c.Exit)
	share("loops.relief.cpu.step", c.Step)
	share("loops.relief.cpu.min_effect", c.MinEffect)
	share("loops.relief.cpu.victim_share", c.VictimShare)
	if c.Exit >= c.Enter {
		errs = append(errs, fmt.Errorf("loops.relief.cpu.exit = %v must be below enter = %v", c.Exit, c.Enter))
	}
	positive("loops.relief.cpu.enter_for", c.EnterFor)
	positive("loops.relief.cpu.exit_for", c.ExitFor)
	positive("loops.relief.cpu.settle", c.Settle)
	positive("loops.relief.cpu.expiry", c.Expiry)

	positive("policy.defaults.cooldown", s.Policy.Defaults.Cooldown)
	if s.Policy.Defaults.MaxActionsPerHour < 0 {
		errs = append(errs, fmt.Errorf("policy.defaults.max_actions_per_hour = %d, want zero or more", s.Policy.Defaults.MaxActionsPerHour))
	}
	floor("policy.defaults.floor", s.Policy.Defaults.Floor)
	for i, rule := range s.Policy.Rules {
		name := fmt.Sprintf("policy.rules[%d]", i)
		notNegative(name+".cooldown", rule.Cooldown)
		floor(name+".floor", rule.Floor)
	}
	return errors.Join(errs...)
}

// Redacted returns a copy with every field tagged secret:"true" replaced by
// Redaction when it holds something, so a spec can be logged or served.
func (s Spec) Redacted() Spec {
	redact(reflect.ValueOf(&s).Elem())
	return s
}

func redact(v reflect.Value) {
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if t.Field(i).Tag.Get("secret") == "true" && f.Kind() == reflect.String {
			if f.String() != "" {
				f.SetString(Redaction)
			}
			continue
		}
		redact(f)
	}
}

// YAML renders the spec as a document Parse reads back to the same spec.
func (s Spec) YAML() ([]byte, error) {
	return yaml.Marshal(s)
}
