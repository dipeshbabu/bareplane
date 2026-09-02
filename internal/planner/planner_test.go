package planner

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/ownership"
	"github.com/dipeshbabu/bareplane/internal/provider"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

func TestBuildCreatesMissingMachines(t *testing.T) {
	desired := testTopology(t)
	plan, err := Build(desired, provider.Inventory{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(plan.Changes))
	}
	for _, change := range plan.Changes {
		if change.Action != provider.ActionCreate {
			t.Fatalf("expected create, got %#v", change)
		}
	}
}

func TestBuildNoopsExactManagedMachine(t *testing.T) {
	desired := testTopology(t)
	machine := desired.Machines[0]
	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{
		managedNode(t, desired.Cluster, machine),
	}})
	if err != nil {
		t.Fatal(err)
	}

	change := findChange(t, plan, machine.Name)
	if change.Action != provider.ActionNoop {
		t.Fatalf("expected noop, got %#v", change)
	}
}

func TestBuildReportsManagedCapacityDrift(t *testing.T) {
	desired := testTopology(t)
	machine := desired.Machines[0]
	observed := managedNode(t, desired.Cluster, machine)
	observed.CPU = machine.CPU - 1
	observed.MemoryGB = machine.MemoryGB - 2
	observed.DiskGB = machine.DiskGB - 10

	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{observed}})
	if err != nil {
		t.Fatal(err)
	}
	change := findChange(t, plan, machine.Name)
	if change.Action != provider.ActionUpdate {
		t.Fatalf("expected update, got %#v", change)
	}
	want := "cpu 3 -> 4; memory 6GiB -> 8GiB; disk 54GiB -> 64GiB"
	if change.Reason != want {
		t.Fatalf("reason = %q, want %q", change.Reason, want)
	}
}

func TestBuildConflictsOnUnmanagedNameCollision(t *testing.T) {
	desired := testTopology(t)
	machine := desired.Machines[0]
	observed := managedNode(t, desired.Cluster, machine)
	observed.Tags = nil

	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{observed}})
	if err != nil {
		t.Fatal(err)
	}
	change := findChange(t, plan, machine.Name)
	if change.Action != provider.ActionConflict || !plan.HasConflicts() {
		t.Fatalf("expected conflict, got %#v", change)
	}
	if !strings.Contains(change.Reason, "not explicitly owned") {
		t.Fatalf("unexpected conflict reason %q", change.Reason)
	}
}

func TestBuildConflictsOnDuplicateObservedNames(t *testing.T) {
	desired := testTopology(t)
	machine := desired.Machines[0]
	first := managedNode(t, desired.Cluster, machine)
	first.ID = "qemu/201"
	second := first
	second.ID = "qemu/101"

	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	change := findChange(t, plan, machine.Name)
	if change.Action != provider.ActionConflict {
		t.Fatalf("expected conflict, got %#v", change)
	}
	if change.Reason != "multiple observed resources share this name: qemu/101, qemu/201" {
		t.Fatalf("unexpected deterministic duplicate reason %q", change.Reason)
	}
}

func TestBuildConflictsOnOwnedTemplate(t *testing.T) {
	desired := testTopology(t)
	machine := desired.Machines[0]
	observed := managedNode(t, desired.Cluster, machine)
	observed.Template = true

	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{observed}})
	if err != nil {
		t.Fatal(err)
	}
	if change := findChange(t, plan, machine.Name); change.Action != provider.ActionConflict {
		t.Fatalf("expected template conflict, got %#v", change)
	}
}

func TestBuildIgnoresStatusHostAndUndiscoverableGPUForDrift(t *testing.T) {
	desired := testTopology(t)
	machine := desired.Machines[0]
	machine.GPU = true
	desired.Machines[0] = machine
	observed := managedNode(t, desired.Cluster, machine)
	observed.GPU = false
	observed.Status = "stopped"
	observed.Host = "different-host"

	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{observed}})
	if err != nil {
		t.Fatal(err)
	}
	if change := findChange(t, plan, machine.Name); change.Action != provider.ActionNoop {
		t.Fatalf("runtime status, placement, or undiscoverable GPU should not cause drift: %#v", change)
	}
}

func TestBuildDoesNotDeleteExtraInventory(t *testing.T) {
	desired := testTopology(t)
	tags, err := ownership.Tags(desired.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	extra := provider.Node{ID: "qemu/999", Name: "old-machine", Tags: tags}

	plan, err := Build(desired, provider.Inventory{Nodes: []provider.Node{extra}})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.Changes {
		if change.Action == provider.ActionDelete || change.Name == extra.Name {
			t.Fatalf("initial planner must not delete or act on extra inventory: %#v", change)
		}
	}
}

func TestBuildOrderingIsDeterministic(t *testing.T) {
	desired := testTopology(t)
	observed := []provider.Node{
		managedNode(t, desired.Cluster, desired.Machines[1]),
		managedNode(t, desired.Cluster, desired.Machines[0]),
	}

	first, err := Build(desired, provider.Inventory{Nodes: observed})
	if err != nil {
		t.Fatal(err)
	}
	observed[0], observed[1] = observed[1], observed[0]
	second, err := Build(desired, provider.Inventory{Nodes: observed})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan changed with observed order:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func testTopology(t *testing.T) topology.Topology {
	t.Helper()
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes = []config.NodeGroup{
		{Name: "control", Role: "control-plane", Count: 1, CPU: 4, MemoryGB: 8, DiskGB: 64},
		{Name: "worker", Role: "worker", Count: 1, CPU: 8, MemoryGB: 16, DiskGB: 128},
	}
	result, err := topology.Build(cfg)
	if err != nil {
		t.Fatalf("topology.Build() error = %v", err)
	}
	return result
}

func managedNode(t *testing.T, cluster string, machine topology.Machine) provider.Node {
	t.Helper()
	tags, err := ownership.Tags(cluster)
	if err != nil {
		t.Fatal(err)
	}
	return provider.Node{
		ID:       "qemu/100",
		Name:     machine.Name,
		Kind:     "qemu",
		CPU:      machine.CPU,
		MemoryGB: machine.MemoryGB,
		DiskGB:   machine.DiskGB,
		GPU:      machine.GPU,
		Tags:     tags,
	}
}

func findChange(t *testing.T, plan provider.Plan, name string) provider.Change {
	t.Helper()
	for _, change := range plan.Changes {
		if change.Name == name {
			return change
		}
	}
	t.Fatalf("change %q not found in %#v", name, plan.Changes)
	return provider.Change{}
}
