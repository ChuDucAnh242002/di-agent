package spec

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultsAreTodaysFlags(t *testing.T) {
	d := Default()
	if d.Node.Addr != ":8080" {
		t.Errorf("Node.Addr = %q", d.Node.Addr)
	}
	if d.Loops.Review.Level != "off" {
		t.Errorf("Review.Level = %q", d.Loops.Review.Level)
	}
	if d.Loops.Relief.CPU.Enter != 0.25 {
		t.Errorf("Relief.CPU.Enter = %v", d.Loops.Relief.CPU.Enter)
	}
	if d.Loops.Relief.Enabled {
		t.Error("relief must default to off")
	}
	if d.Policy.Defaults.MaxActionsPerHour != 6 {
		t.Errorf("MaxActionsPerHour = %d", d.Policy.Defaults.MaxActionsPerHour)
	}
	if d.Runtime.Kind != "dry-run" {
		t.Errorf("Runtime.Kind = %q", d.Runtime.Kind)
	}
	if len(d.Policy.Rules) != 2 || !d.Policy.Rules[0].Freeze || !d.Policy.Rules[1].Freeze {
		t.Errorf("default rules = %+v", d.Policy.Rules)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestAnEmptyFileIsTheDefault(t *testing.T) {
	got, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Error("an empty document differs from Default()")
	}
}

func TestAFileOverlaysTheDefaults(t *testing.T) {
	got, err := Parse([]byte("loops: {relief: {cpu: {enter: 0.4}}}"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Loops.Relief.CPU.Enter != 0.4 {
		t.Errorf("Enter = %v", got.Loops.Relief.CPU.Enter)
	}
	if got.Loops.Relief.CPU.Exit != 0.10 {
		t.Errorf("Exit = %v, want the default 0.10", got.Loops.Relief.CPU.Exit)
	}
}

func TestEnvironmentIsExpanded(t *testing.T) {
	t.Setenv("KEY", "s3cret")
	got, err := Parse([]byte("explain: {api_key: ${KEY}}"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Explain.APIKey != "s3cret" {
		t.Errorf("APIKey = %q", got.Explain.APIKey)
	}
}

func TestUnknownKeysAreRefused(t *testing.T) {
	_, err := Parse([]byte("loops: {relief: {cpu: {entre: 0.3}}}"))
	if err == nil || !strings.Contains(err.Error(), "entre") {
		t.Fatalf("err = %v, want one naming entre", err)
	}
}

func TestInvalidValuesAreRefused(t *testing.T) {
	for name, doc := range map[string]string{
		"exit above enter":    "loops: {relief: {cpu: {exit: 0.3}}}",
		"of_original zero":    "policy: {defaults: {floor: {of_original: 0}}}",
		"threshold above 1":   "loops: {relief: {cpu: {enter: 1.5}}}",
		"zero settle":         "loops: {relief: {cpu: {settle: 0s}}}",
		"unknown level":       "loops: {review: {level: sometimes}}",
		"negative cooldown":   "policy: {defaults: {cooldown: -1m}}",
		"zero relief period":  "loops: {relief: {interval: 0s}}",
		"defaults floor null": "policy: {defaults: {floor: null}}",
		"rule of_original 0":  "policy: {rules: [{match: {name: a}, floor: {of_original: 0}}]}",
		"rule cores negative": "policy: {rules: [{match: {name: a}, floor: {cpu_cores: -1}}]}",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := Parse([]byte("loops: {relief: {cpu: {exit: 0.3}}}"))
	if err == nil || !strings.Contains(err.Error(), "exit") {
		t.Errorf("err = %v, want one mentioning exit", err)
	}
}

func TestPrintedSpecLoadsBack(t *testing.T) {
	b, err := Default().YAML()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("printed spec does not load back to the default:\n%s", b)
	}
}

func TestSecretsAreRedacted(t *testing.T) {
	s := Default()
	s.Explain.APIKey = "k"
	s.Loops.Review.Decision.APIKey = "k"
	s.Loops.Review.Reasoning.APIKey = "k"
	r := s.Redacted()
	for name, v := range map[string]string{
		"explain":   r.Explain.APIKey,
		"decision":  r.Loops.Review.Decision.APIKey,
		"reasoning": r.Loops.Review.Reasoning.APIKey,
	} {
		if v != "[redacted]" {
			t.Errorf("%s key = %q", name, v)
		}
	}
	if s.Explain.APIKey != "k" {
		t.Error("Redacted changed its receiver")
	}
	if e := Default().Redacted(); e.Explain.APIKey != "" || e.Loops.Review.Decision.APIKey != "" {
		t.Error("an empty key must stay empty")
	}
}

func TestALabelMatchTakesOneOrMany(t *testing.T) {
	got, err := Parse([]byte("policy: {rules: [{match: {labels: {a: x, b: [x, y]}}, freeze: true}]}"))
	if err != nil {
		t.Fatal(err)
	}
	l := got.Policy.Rules[0].Match.Labels
	if !reflect.DeepEqual(l["a"], StringList{"x"}) || !reflect.DeepEqual(l["b"], StringList{"x", "y"}) {
		t.Errorf("labels = %v", l)
	}
}

// TestARuleFloorNamesOneBound: a rule's floor overlays the default floor field by
// field, so the effective floor is what is validated.
func TestARuleFloorNamesOneBound(t *testing.T) {
	for _, doc := range []string{
		"policy: {rules: [{match: {name: \"batch-*\"}, floor: {cpu_cores: 0.5}}]}",
		"policy: {rules: [{match: {name: \"batch-*\"}, floor: {of_original: 0.8}}]}",
		"policy: {defaults: {floor: {cpu_cores: 0.2}}}",
	} {
		if _, err := Parse([]byte(doc)); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
}
