package agentmain

import (
	"strings"
	"testing"
)

// TestCheckNoPositionalArgs pins the guard against a stray argument after
// -config: Go's flag package stops at the first argument that is not a flag, so
// it and every flag after it would be dropped without warning.
func TestCheckNoPositionalArgs(t *testing.T) {
	if err := checkNoPositionalArgs(0, ""); err != nil {
		t.Errorf("no positional args should be accepted, got %v", err)
	}

	err := checkNoPositionalArgs(1, "false")
	if err == nil {
		t.Fatal(`positional arg "false" should be rejected`)
	}
	// The message names the argument and what the agent does take.
	for _, want := range []string{`"false"`, "-config", "-print-config=false"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q missing %q", err.Error(), want)
		}
	}
}
