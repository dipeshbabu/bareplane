package topology

import (
	"fmt"
	"sort"

	"github.com/dipeshbabu/bareplane/internal/config"
)

const MaxMachines = 10000

type Topology struct {
	Cluster  string
	Machines []Machine
}

type Machine struct {
	Name     string
	Group    string
	Ordinal  int
	Role     string
	CPU      int
	MemoryGB int
	DiskGB   int
	GPU      bool
}

func Build(cfg config.Config) (Topology, error) {
	if err := cfg.Validate(); err != nil {
		return Topology{}, fmt.Errorf("validate configuration: %w", err)
	}

	groups := append([]config.NodeGroup(nil), cfg.Spec.Nodes...)
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})

	total := 0
	for _, group := range groups {
		if group.Count < 1 {
			return Topology{}, fmt.Errorf("node group %q count must be at least 1", group.Name)
		}
		if total > MaxMachines-group.Count {
			return Topology{}, fmt.Errorf("topology exceeds maximum of %d machines", MaxMachines)
		}
		total += group.Count
	}

	machines := make([]Machine, 0, total)
	seen := make(map[string]struct{}, total)
	for _, group := range groups {
		for ordinal := 1; ordinal <= group.Count; ordinal++ {
			name := fmt.Sprintf("%s-%s-%d", cfg.Metadata.Name, group.Name, ordinal)
			if len(name) > 63 {
				return Topology{}, fmt.Errorf("generated machine name %q exceeds 63 characters", name)
			}
			if _, exists := seen[name]; exists {
				return Topology{}, fmt.Errorf("generated duplicate machine name %q", name)
			}
			seen[name] = struct{}{}
			machines = append(machines, Machine{
				Name:     name,
				Group:    group.Name,
				Ordinal:  ordinal,
				Role:     group.Role,
				CPU:      group.CPU,
				MemoryGB: group.MemoryGB,
				DiskGB:   group.DiskGB,
				GPU:      group.GPU,
			})
		}
	}

	return Topology{
		Cluster:  cfg.Metadata.Name,
		Machines: machines,
	}, nil
}
