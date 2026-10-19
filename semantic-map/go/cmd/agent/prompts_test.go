package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/pkg/domain"
)

// TestPromptsNameNoConstructs: the prompts describe the map, and the constructs come
// from the domain model loaded at startup ({{DOMAIN}}). A prompt that lists a domain's
// constructs, or the seeded-prior arithmetic the agent no longer has, tells a language
// model about a map that does not exist.
func TestPromptsNameNoConstructs(t *testing.T) {
	for _, name := range []string{"explain-v1.md", "planner-v1.md", "critic-v1.md"} {
		b, err := os.ReadFile(filepath.Join("prompts", name))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "{{DOMAIN}}") {
			t.Errorf("%s: no {{DOMAIN}} placeholder for the loaded domain model", name)
		}
		for _, stale := range []string{"7 constructs", "Connectivity & Offline", "15 propositions",
			"prior_weight", "(1 - c)", "(effective - prior)", "usually 'master'"} {
			if strings.Contains(s, stale) {
				t.Errorf("%s: still says %q", name, stale)
			}
		}
	}
}

// TestToolDescriptionsNameNoConstructs: the same holds for what the tools tell the model.
func TestToolDescriptionsNameNoConstructs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "pkg", "explain", "tools.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"RC/PS/CO/SC/MU/RR/CE", "prior_weight", "usually 'master'"} {
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
