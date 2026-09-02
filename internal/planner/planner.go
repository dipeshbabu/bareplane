package planner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/ownership"
	"github.com/dipeshbabu/bareplane/internal/provider"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

func Build(desired topology.Topology, observed provider.Inventory) (provider.Plan, error) {
	if strings.TrimSpace(desired.Cluster) == "" {
		return provider.Plan{}, fmt.Errorf("desired topology cluster is empty")
	}

	byName := make(map[string][]provider.Node, len(observed.Nodes))
	for _, node := range observed.Nodes {
		byName[node.Name] = append(byName[node.Name], node)
	}
	for name := range byName {
		sort.Slice(byName[name], func(i, j int) bool {
			return byName[name][i].ID < byName[name][j].ID
		})
	}

	seenDesired := make(map[string]struct{}, len(desired.Machines))
	changes := make([]provider.Change, 0, len(desired.Machines))
	for _, machine := range desired.Machines {
		if machine.Name == "" {
			return provider.Plan{}, fmt.Errorf("desired topology contains a machine with an empty name")
		}
		if _, exists := seenDesired[machine.Name]; exists {
			return provider.Plan{}, fmt.Errorf("desired topology contains duplicate machine name %q", machine.Name)
		}
		seenDesired[machine.Name] = struct{}{}

		matches := byName[machine.Name]
		switch len(matches) {
		case 0:
			changes = append(changes, provider.Change{
				Action: provider.ActionCreate,
				Kind:   "machine",
				Name:   machine.Name,
				Reason: "machine is not present",
			})
		case 1:
			changes = append(changes, compareMachine(desired.Cluster, machine, matches[0]))
		default:
			ids := make([]string, 0, len(matches))
			for _, match := range matches {
				ids = append(ids, match.ID)
			}
			changes = append(changes, provider.Change{
				Action: provider.ActionConflict,
				Kind:   "machine",
				Name:   machine.Name,
				Reason: fmt.Sprintf("multiple observed resources share this name: %s", strings.Join(ids, ", ")),
			})
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Name == changes[j].Name {
			return changes[i].Action < changes[j].Action
		}
		return changes[i].Name < changes[j].Name
	})
	return provider.Plan{Changes: changes}, nil
}

func compareMachine(cluster string, desired topology.Machine, observed provider.Node) provider.Change {
	change := provider.Change{
		Kind: "machine",
		ID:   observed.ID,
		Name: desired.Name,
	}

	if !ownership.Matches(observed.Tags, cluster) {
		change.Action = provider.ActionConflict
		change.Reason = fmt.Sprintf("observed %s %s with this name is not explicitly owned by cluster %q", observedKind(observed), observed.ID, cluster)
		return change
	}
	if observed.Template {
		change.Action = provider.ActionConflict
		change.Reason = fmt.Sprintf("owned resource %s is a template, not a mutable machine", observed.ID)
		return change
	}

	drift := capacityDrift(desired, observed)
	if len(drift) == 0 {
		change.Action = provider.ActionNoop
		change.Reason = "managed machine matches observable capacity"
		return change
	}

	change.Action = provider.ActionUpdate
	change.Reason = strings.Join(drift, "; ")
	return change
}

func capacityDrift(desired topology.Machine, observed provider.Node) []string {
	var drift []string
	if observed.CPU != desired.CPU {
		drift = append(drift, fmt.Sprintf("cpu %d -> %d", observed.CPU, desired.CPU))
	}
	if observed.MemoryGB != desired.MemoryGB {
		drift = append(drift, fmt.Sprintf("memory %dGiB -> %dGiB", observed.MemoryGB, desired.MemoryGB))
	}
	if observed.DiskGB != desired.DiskGB {
		drift = append(drift, fmt.Sprintf("disk %dGiB -> %dGiB", observed.DiskGB, desired.DiskGB))
	}
	return drift
}

func observedKind(node provider.Node) string {
	if strings.TrimSpace(node.Kind) == "" {
		return "resource"
	}
	return node.Kind
}
