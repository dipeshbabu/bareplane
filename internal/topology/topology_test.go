package topology

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestBuildIsDeterministicAcrossGroupOrder(t *testing.T) {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes = []config.NodeGroup{
		{Name: "workers", Role: "worker", Count: 2, CPU: 8, MemoryGB: 32, DiskGB: 128, GPU: true},
		{Name: "control", Role: "control-plane", Count: 1, CPU: 4, MemoryGB: 8, DiskGB: 64},
	}

	first, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	cfg.Spec.Nodes[0], cfg.Spec.Nodes[1] = cfg.Spec.Nodes[1], cfg.Spec.Nodes[0]
	second, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("topology changed with input group order:\nfirst=%#v\nsecond=%#v", first, second)
	}

	wantNames := []string{"lab-control-1", "lab-workers-1", "lab-workers-2"}
	for i, want := range wantNames {
		if got := first.Machines[i].Name; got != want {
			t.Fatalf("machine %d name = %q, want %q", i, got, want)
		}
	}
}

func TestBuildPreservesResourceIntent(t *testing.T) {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes = []config.NodeGroup{
		{Name: "gpu", Role: "worker", Count: 1, CPU: 16, MemoryGB: 64, DiskGB: 512, GPU: true},
	}

	topology, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	machine := topology.Machines[0]
	if machine.Group != "gpu" || machine.Ordinal != 1 || machine.Role != "worker" {
		t.Fatalf("unexpected identity fields: %#v", machine)
	}
	if machine.CPU != 16 || machine.MemoryGB != 64 || machine.DiskGB != 512 || !machine.GPU {
		t.Fatalf("unexpected resources: %#v", machine)
	}
}

func TestBuildNamesStayStableWhenGroupGrows(t *testing.T) {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes[0].Name = "worker"
	cfg.Spec.Nodes[0].Role = "worker"
	cfg.Spec.Nodes[0].Count = 9

	nine, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Nodes[0].Count = 10
	ten, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	for i := range nine.Machines {
		if nine.Machines[i].Name != ten.Machines[i].Name {
			t.Fatalf("machine %d renamed from %q to %q", i, nine.Machines[i].Name, ten.Machines[i].Name)
		}
	}
	if got := ten.Machines[9].Name; got != "lab-worker-10" {
		t.Fatalf("unexpected tenth machine name %q", got)
	}
}

func TestBuildSupportsLargeButBoundedCounts(t *testing.T) {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes[0].Name = "worker"
	cfg.Spec.Nodes[0].Role = "worker"
	cfg.Spec.Nodes[0].Count = 1000

	topology, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Machines) != 1000 {
		t.Fatalf("expected 1000 machines, got %d", len(topology.Machines))
	}
	if got := topology.Machines[999].Name; got != "lab-worker-1000" {
		t.Fatalf("unexpected final machine name %q", got)
	}
}

func TestBuildRejectsMachineLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Spec.Nodes[0].Count = MaxMachines + 1

	_, err := Build(cfg)
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected maximum machine error, got %v", err)
	}
}

func TestBuildRejectsGeneratedNameOver63Characters(t *testing.T) {
	cfg := config.Default()
	cfg.Metadata.Name = strings.Repeat("a", 32)
	cfg.Spec.Nodes[0].Name = strings.Repeat("b", 30)

	_, err := Build(cfg)
	if err == nil || !strings.Contains(err.Error(), "exceeds 63") {
		t.Fatalf("expected generated name length error, got %v", err)
	}
}
