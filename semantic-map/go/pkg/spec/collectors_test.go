package spec

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const collectorsYAML = `
sources:
  interval: 10s
  collectors:
    - name: cgroup
      type: cgroup
      config: {root: /sys/fs/cgroup, subjects: true, units: [], max_subjects: 256}
    - name: netdata
      type: netdata
      config: {url: "http://localhost:19999", include: [system.cpu], collapse: false}
    - name: gpu
      exec: {command: /opt/di/collectors/gpu, args: [--fast]}
      timeout: 3s
      config: {device: 0}
    - name: redis
      http: {url: "http://localhost:9121/di/collect"}
`

func TestDefaultCollectorsIsCgroup(t *testing.T) {
	c := Default().Sources.Collectors
	if len(c) != 1 || c[0].Name != "cgroup" || c[0].Type != "cgroup" {
		t.Fatalf("default collectors = %+v, want one cgroup entry", c)
	}
}

func TestCollectorsParse(t *testing.T) {
	s, err := Parse([]byte(collectorsYAML))
	if err != nil {
		t.Fatal(err)
	}
	c := s.Sources.Collectors
	// The file's list replaces the default one-entry list; it must not merge.
	if len(c) != 4 {
		t.Fatalf("got %d collectors, want 4", len(c))
	}
	if c[2].Exec == nil || c[2].Exec.Command != "/opt/di/collectors/gpu" || c[2].Timeout != 3*time.Second {
		t.Errorf("exec entry = %+v", c[2])
	}
	if c[3].HTTP == nil || c[3].HTTP.URL != "http://localhost:9121/di/collect" {
		t.Errorf("http entry = %+v", c[3])
	}
	if c[0].Config.Kind != yaml.MappingNode {
		t.Errorf("config kind = %v, want mapping", c[0].Config.Kind)
	}
	for i, want := range []string{"type", "type", "exec", "http"} {
		if got := c[i].Kind(); got != want {
			t.Errorf("[%d].Kind() = %q, want %q", i, got, want)
		}
	}
}

func TestCollectorsValidate(t *testing.T) {
	cases := map[string]struct{ yaml, name string }{
		"duplicate name":      {"collectors: [{name: a, type: cgroup}, {name: a, type: netdata}]", "a"},
		"bad name":            {`collectors: [{name: "a b", type: cgroup}]`, "a b"},
		"no kind":             {"collectors: [{name: none}]", "none"},
		"two kinds":           {"collectors: [{name: both, type: cgroup, exec: {command: x}}]", "both"},
		"empty command":       {`collectors: [{name: e, exec: {command: ""}}]`, "e"},
		"empty url":           {`collectors: [{name: h, http: {url: ""}}]`, "h"},
		"timeout":             {"interval: 10s\n  collectors: [{name: slow, type: cgroup, timeout: 20s}]", "slow"},
		"timeout at interval": {"interval: 10s\n  collectors: [{name: even, type: cgroup, timeout: 10s}]", "even"},
		"script alone":        {"collectors: [{name: script, type: script}, {name: other, type: cgroup}]", "script"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte("sources:\n  " + tc.yaml + "\n"))
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range []string{"sources.collectors[", tc.name} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
	s, err := Parse([]byte("sources:\n  collectors: []\n"))
	if err != nil || len(s.Sources.Collectors) != 0 {
		t.Fatalf("empty list: %v, %+v", err, s.Sources.Collectors)
	}
}

func TestCollectorsUnknownKeyRefused(t *testing.T) {
	_, err := Parse([]byte("sources:\n  collectors:\n    - {name: x, type: cgroup, timout: 1s}\n"))
	if err == nil || !strings.Contains(err.Error(), "timout") {
		t.Fatalf("err = %v, want one naming timout", err)
	}
	if _, err := Parse([]byte("sources:\n  collectors:\n    - {name: x, type: cgroup, config: {anything: 1}}\n")); err != nil {
		t.Fatalf("config keys are the collector's to check: %v", err)
	}
}

// TestExampleConfigsParse pins that the shipped example loads under the current
// schema: it is the file operators copy, so a key it still uses after a rename is
// a refusal on their first start.
func TestExampleConfigsParse(t *testing.T) {
	s, err := Load("../../../agent.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sources.Collectors) < 1 {
		t.Fatal("agent.example.yaml names no collector")
	}
}

// TestCollectorsYAMLRoundTrip pins that the spec as -print-config and GET /config
// render it reads back to the same collectors: an entry's config is opaque to the
// schema, so it must survive the round trip as the collector will decode it.
func TestCollectorsYAMLRoundTrip(t *testing.T) {
	s, err := Parse([]byte(collectorsYAML))
	if err != nil {
		t.Fatal(err)
	}
	// An entry built in code, with no config, is how the default list is written.
	s.Sources.Collectors = append(s.Sources.Collectors, CollectorEntry{Name: "bare", Type: "cgroup"})
	out, err := s.YAML()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("printed spec does not parse: %v\n%s", err, out)
	}
	want, got := s.Sources.Collectors, back.Sources.Collectors
	if len(got) != len(want) {
		t.Fatalf("got %d collectors back, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if g.Name != w.Name || g.Kind() != w.Kind() || g.Type != w.Type || g.Timeout != w.Timeout {
			t.Errorf("[%d] = %+v, want %+v", i, g, w)
		}
		var wc, gc map[string]any
		if err := w.Config.Decode(&wc); err != nil && w.Config.Kind != 0 {
			t.Fatal(err)
		}
		if g.Config.Kind != 0 {
			if err := g.Config.Decode(&gc); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(wc, gc) {
			t.Errorf("[%d] config = %v, want %v", i, gc, wc)
		}
	}
}

// GET /config serves Redacted, and the agent cannot tell which of a plugin's
// values is a credential: config blocks, exec arguments and URL secrets are
// withheld, and the receiver is left as it was.
func TestCollectorsAreRedacted(t *testing.T) {
	s, err := Parse([]byte(`
sources:
  interval: 10s
  collectors:
    - name: cgroup
      type: cgroup
      config: {}
    - name: gpu
      exec: {command: /opt/di/collectors/gpu, args: [--token, s3cret]}
      config: {password: hunter2}
    - name: redis
      http: {url: "http://user:pw@localhost:9121/di/collect?token=abc&b=1"}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := s.Redacted()
	c := r.Sources.Collectors
	if c[0].Config.Kind != yaml.MappingNode || len(c[0].Config.Content) != 0 {
		t.Errorf("an empty config became %+v; only a config with something in it is withheld", c[0].Config)
	}
	if c[1].Config.Kind != yaml.ScalarNode || c[1].Config.Value != Redaction {
		t.Errorf("config = %+v, want %q", c[1].Config, Redaction)
	}
	if c[1].Exec.Command != "/opt/di/collectors/gpu" || !reflect.DeepEqual(c[1].Exec.Args, []string{Redaction, Redaction}) {
		t.Errorf("exec = %+v", *c[1].Exec)
	}
	if got, want := c[2].HTTP.URL, "http://[redacted]@localhost:9121/di/collect?[redacted]"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	out, err := r.YAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "s3cret", "abc", ":pw@"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("redacted YAML still holds %q:\n%s", secret, out)
		}
	}
	// The receiver keeps its values: the running agent still needs them.
	o := s.Sources.Collectors
	if o[1].Exec.Args[1] != "s3cret" || o[1].Config.Kind != yaml.MappingNode || !strings.Contains(o[2].HTTP.URL, "token=abc") {
		t.Errorf("Redacted changed its receiver: %+v %+v", *o[1].Exec, *o[2].HTTP)
	}
}

func TestRedactURL(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                 "",
		"http://localhost:9121/di/collect": "http://localhost:9121/di/collect",
		"http://user:pw@host/p":            "http://[redacted]@host/p",
		"http://ghp_TOKEN@host/":           "http://[redacted]@host/",
		"https://host/p?token=abc&b=1":     "https://host/p?[redacted]",
		"http://host/p?":                   "http://host/p?[redacted]",
		"http://host/p#secret":             "http://host/p",
		"http://u@host:1/p?token=x#frag":   "http://[redacted]@host:1/p?[redacted]",
		"http://%zz":                       Redaction,
		"mailto:someone@example.com":       Redaction,
	} {
		if got := RedactURL(raw); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", raw, got, want)
		}
		for _, secret := range []string{"pw", "ghp_TOKEN", "abc", "secret", "frag"} {
			if strings.Contains(raw, secret) && strings.Contains(RedactURL(raw), secret) {
				t.Errorf("RedactURL(%q) keeps %q", raw, secret)
			}
		}
	}
}
