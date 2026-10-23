package agentmain

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/pkg/domain"
)

// TestPromptsNameNoConstructs: the prompts describe the map, and the constructs come
// from the domain model loaded at startup ({{DOMAIN}}). A prompt that lists a domain's
// constructs, or the seeded-prior arithmetic the agent no longer has, tells a language
// model about a map that does not exist.
// constructOrProposition matches a Di-Select construct abbreviation or a
// proposition id standing alone, as a worked example would write one.
var constructOrProposition = regexp.MustCompile(`\b(RC|PS|CO|SC|MU|RR|CE|P[0-9]{1,2})\b`)

func TestPromptsNameNoConstructs(t *testing.T) {
	for _, name := range []string{"explain-v2.md", "planner-v2.md", "critic-v2.md"} {
		b, err := os.ReadFile(filepath.Join("prompts", name))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "{{DOMAIN}}") {
			t.Errorf("%s: no {{DOMAIN}} placeholder for the loaded domain model", name)
		}
		for _, stale := range []string{"7 constructs", "Connectivity & Offline", "15 propositions",
			"prior_weight", "(1 - c)", "(effective - prior)", "usually 'master'", "ResourceCost", "Resource & Cost"} {
			if strings.Contains(s, stale) {
				t.Errorf("%s: still says %q", name, stale)
			}
		}
		// Worked examples teach the model what to ask for, so one that queries a
		// construct or a proposition by id teaches it to expect a domain model.
		if ids := constructOrProposition.FindAllString(s, -1); len(ids) > 0 {
			t.Errorf("%s: names construct or proposition ids %v", name, ids)
		}
	}
}

// TestToolDescriptionsNameNoConstructs: the same holds for what the tools tell the model.
func TestToolDescriptionsNameNoConstructs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "pkg", "explain", "tools.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"RC/PS/CO/SC/MU/RR/CE", "prior_weight", "usually 'master'",
		"between the domain model's constructs", "From-construct", "To-construct"} {
		if strings.Contains(string(b), stale) {
			t.Errorf("tools.go: a tool description still says %q", stale)
		}
	}
}

// TestThePromptIsToldTheLoadedDomain: {{DOMAIN}} becomes the loaded spec's summary,
// or the statement that none is loaded, in every prompt that carries it.
func TestThePromptIsToldTheLoadedDomain(t *testing.T) {
	dom := &domain.Spec{Constructs: []domain.Construct{{ConstructID: "XQ", Name: "Example"}}}
	if got := withDomain("before {{DOMAIN}} after", dom); !strings.Contains(got, "XQ (Example)") || strings.Contains(got, "{{DOMAIN}}") {
		t.Fatalf("prompt %q; want the loaded construct in place of the placeholder", got)
	}
	if got := withDomain("{{DOMAIN}}", nil); !strings.Contains(got, "No domain model is loaded") {
		t.Fatalf("prompt %q; want it told none is loaded", got)
	}
	if got := withDomain("", dom); got != "" {
		t.Fatalf("a missing optional prompt stays missing, got %q", got)
	}
}

// TestDefaultExplainPromptExists: the default prompt path is relative to the module
// root, the directory dev.sh and the README run the daemon from. It once still
// named cmd/agent/prompts after the prompts moved, so enabling the explain layer
// without a prompt path failed at startup.
func TestDefaultExplainPromptExists(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not at %s: %v", root, err)
	}
	dir := filepath.Dir(DefaultExplainPrompt)
	for _, p := range []string{DefaultExplainPrompt, filepath.Join(dir, "planner-v2.md"), filepath.Join(dir, "critic-v2.md")} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("default prompt %s is missing relative to the module root: %v", p, err)
		}
	}
}
