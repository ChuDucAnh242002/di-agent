package facts

import (
	"context"
	"testing"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

// A Raspberry Pi's root, as the host source sees it from a container with the host
// mounted: the model from the device tree, the cores counted, memory in bytes, and a
// swapless machine reporting no swap rather than a zero.
func TestHostReadsAPi(t *testing.T) {
	got, err := (&Host{Root: "testdata/pi"}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]statemap.Fact{}
	for _, f := range got {
		by[f.Key] = f
	}
	for key, want := range map[string]string{
		Hostname: "node-1", OS: "Ubuntu 22.04.2 LTS", Kernel: "5.15.0-1034-raspi",
		MachineModel: "Raspberry Pi 4 Model B Rev 1.4", CPUModel: "Arm Cortex-A72",
		CPUCores: "4", MemoryTotal: "8191406080", BootTime: "2023-11-14T22:13:20Z",
	} {
		if by[key].Value != want {
			t.Errorf("%s = %q, want %q", key, by[key].Value, want)
		}
	}
	if f := by[MemoryTotal]; f.Number == nil || *f.Number != 8191406080 || f.Unit != "bytes" {
		t.Errorf("memory_total is not a number in bytes: %+v", f)
	}
	if _, ok := by[SwapTotal]; ok {
		t.Error("a machine without swap reported swap_total")
	}
	if by[CPUCores].Source != "host:/proc/cpuinfo" {
		t.Errorf("cpu_cores source = %q", by[CPUCores].Source)
	}
}

type failing struct{}

func (failing) Name() string                                  { return "failing" }
func (failing) Read(context.Context) ([]statemap.Fact, error) { return nil, context.Canceled }

// One source failing does not keep another's facts out of the map.
func TestLoadKeepsWhatItCanRead(t *testing.T) {
	m := statemap.New(statemap.Config{}, nil)
	errs := Load(context.Background(), m, []Source{failing{}, &Host{Root: "testdata/pi"}})
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want the failing source's alone", errs)
	}
	if f, ok := m.Fact(CPUCores); !ok || f.Value != "4" {
		t.Fatalf("cpu_cores = %+v", f)
	}
}

func TestUnknownSourceIsRefused(t *testing.T) {
	if _, err := Build("inventory", ""); err == nil {
		t.Fatal("built a source nobody registered")
	}
}
