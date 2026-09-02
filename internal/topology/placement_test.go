package topology

import (
	"reflect"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestBuildAssignsSortedProviderTargetsRoundRobin(t *testing.T) {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Provider.Targets = []string{"pve3", "pve1", "pve2"}
	cfg.Spec.Nodes[0].Name = "worker"
	cfg.Spec.Nodes[0].Role = "worker"
	cfg.Spec.Nodes[0].Count = 5

	topology, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := machineTargets(topology.Machines)
	want := []string{"pve1", "pve2", "pve3", "pve1", "pve2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
}

func TestBuildTargetOrderDoesNotDependOnYAMLOrder(t *testing.T) {
	cfg := config.Default()
	cfg.Spec.Provider.Targets = []string{"pve2", "pve1"}
	cfg.Spec.Nodes[0].Count = 4
	first, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Spec.Provider.Targets[0], cfg.Spec.Provider.Targets[1] = cfg.Spec.Provider.Targets[1], cfg.Spec.Provider.Targets[0]
	second, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(machineTargets(first.Machines), machineTargets(second.Machines)) {
		t.Fatalf("placement changed with target ordering: %#v vs %#v", first.Machines, second.Machines)
	}
}

func TestBuildUsesNodeGroupTargetOverride(t *testing.T) {
	cfg := config.Default()
	cfg.Spec.Provider.Targets = []string{"cpu1", "gpu1", "gpu2"}
	cfg.Spec.Nodes[0].Name = "gpu"
	cfg.Spec.Nodes[0].Role = "worker"
	cfg.Spec.Nodes[0].Count = 3
	cfg.Spec.Nodes[0].GPU = true
	cfg.Spec.Nodes[0].Targets = []string{"gpu2", "gpu1"}

	topology, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := machineTargets(topology.Machines)
	want := []string{"gpu1", "gpu2", "gpu1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
}

func TestBuildExistingPlacementStaysStableWhenGroupGrows(t *testing.T) {
	cfg := config.Default()
	cfg.Spec.Provider.Targets = []string{"pve2", "pve1"}
	cfg.Spec.Nodes[0].Name = "worker"
	cfg.Spec.Nodes[0].Role = "worker"
	cfg.Spec.Nodes[0].Count = 3

	three, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Nodes[0].Count = 5
	five, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	for i := range three.Machines {
		if three.Machines[i].Target != five.Machines[i].Target {
			t.Fatalf("machine %q moved from %q to %q", three.Machines[i].Name, three.Machines[i].Target, five.Machines[i].Target)
		}
	}
}

func TestBuildWithoutTargetsLeavesPlacementUnset(t *testing.T) {
	topology, err := Build(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, machine := range topology.Machines {
		if machine.Target != "" {
			t.Fatalf("unexpected target %q for %q", machine.Target, machine.Name)
		}
	}
}

func machineTargets(machines []Machine) []string {
	targets := make([]string, len(machines))
	for i, machine := range machines {
		targets[i] = machine.Target
	}
	return targets
}
