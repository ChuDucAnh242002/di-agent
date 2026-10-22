package agentmain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMachineReaders(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	meminfo := write("meminfo", "MemTotal:        8000000 kB\nMemFree:  100 kB\n")
	if got := memTotal(meminfo); got != 8000000*1024 {
		t.Errorf("MemTotal: %d", got)
	}
	if got := memTotal(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("missing meminfo: %d; want 0", got)
	}
	if got := cgroupNumber(write("max", "max\n")); got != 0 {
		t.Errorf(`"max" is no limit; got %d`, got)
	}
	if got := cgroupNumber(write("limit", "536870912\n")); got != 536870912 {
		t.Errorf("memory.max: %d", got)
	}
}
