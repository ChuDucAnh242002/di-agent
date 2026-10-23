//go:build unix

package collectors

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecTimeoutKillsProcessGroup(t *testing.T) {
	e, dir := fixture(t, "hang-grandchild", 300*time.Millisecond, nil)
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Collect(); err == nil {
		t.Fatal("Collect of a hanging plugin succeeded")
	}
	b, err := os.ReadFile(filepath.Join(dir, "grandchild"))
	if err != nil {
		t.Fatalf("fixture did not record its grandchild: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// The orphaned grandchild is reaped by init, so it may linger as a zombie
	// briefly; it is gone once signal 0 reports no such process.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the fault (kill 0: %v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
