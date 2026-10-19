package statemap

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCanSaveCreatesTheDirectoryAndLeavesNothing: the start-up check makes the
// snapshot's directory and leaves no probe file behind.
func TestCanSaveCreatesTheDirectoryAndLeavesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "di-agent")
	if err := CanSave(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}
}

// TestCanSaveRefusesAnUnwritableDirectory: a path the agent cannot write is
// reported at start, not discovered at the first periodic save.
func TestCanSaveRefusesAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := CanSave(filepath.Join(dir, "state.json")); err == nil {
		t.Fatal("CanSave succeeded on a read-only directory")
	}
	if err := CanSave(""); err == nil {
		t.Fatal("CanSave succeeded with no path")
	}
}
