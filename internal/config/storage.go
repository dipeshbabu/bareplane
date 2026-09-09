package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// StorageConfig explicitly inventories retained local directories. It does not
// authorize formatting disks, dynamically creating paths, or adopting data.
type StorageConfig struct {
	Strategy string        `yaml:"strategy"`
	Volumes  []LocalVolume `yaml:"volumes"`
}

type LocalVolume struct {
	Name        string `yaml:"name"`
	Node        string `yaml:"node"`
	CapacityGiB int    `yaml:"capacityGiB"`
}

func (v LocalVolume) Directory(cluster string) string {
	return "/var/lib/bareplane/local-volumes/" + cluster + "/" + v.Name + "/data"
}

func (c Config) storageNodeDisk(node string) (int, bool) {
	if !validName(node) {
		return 0, false
	}
	disk, matches := 0, 0
	for _, group := range c.Spec.Nodes {
		prefix := c.Metadata.Name + "-" + group.Name + "-"
		if !strings.HasPrefix(node, prefix) {
			continue
		}
		ordinalText := strings.TrimPrefix(node, prefix)
		ordinal, err := strconv.Atoi(ordinalText)
		if err == nil && ordinal >= 1 && ordinal <= group.Count && strconv.Itoa(ordinal) == ordinalText {
			disk = group.DiskGB
			matches++
		}
	}
	return disk, matches == 1
}

func (c Config) validateStorage() []string {
	settings := c.Spec.Storage
	if settings == nil {
		return nil
	}
	var problems []string
	if settings.Strategy != "local-static" {
		problems = append(problems, "spec.storage.strategy must explicitly be local-static; dynamic provisioning and disk formatting are not supported")
	}
	if len(settings.Volumes) == 0 || len(settings.Volumes) > 32 {
		return append(problems, "spec.storage.volumes must explicitly select 1 to 32 local directories")
	}
	seen := make(map[string]bool, len(settings.Volumes))
	totals := make(map[string]int)
	for index, volume := range settings.Volumes {
		prefix := fmt.Sprintf("spec.storage.volumes[%d]", index)
		if !validName(volume.Name) || len(volume.Name) > 32 || seen[volume.Name] {
			problems = append(problems, prefix+".name must be a unique lowercase DNS label of at most 32 characters")
		}
		seen[volume.Name] = true
		disk, exists := c.storageNodeDisk(volume.Node)
		if !exists {
			problems = append(problems, prefix+".node must identify exactly one desired Bareplane machine")
		}
		if volume.CapacityGiB < 1 || volume.CapacityGiB > 1024 {
			problems = append(problems, prefix+".capacityGiB must be between 1 and 1024")
			continue
		}
		totals[volume.Node] += volume.CapacityGiB
		if exists && (disk < 4 || totals[volume.Node] > disk-4) {
			problems = append(problems, prefix+": declared local capacity must leave at least 4 GiB of configured node disk capacity outside storage")
		}
	}
	return problems
}

func (c Config) RequireStorage() (StorageConfig, error) {
	if c.Spec.Storage == nil {
		return StorageConfig{}, fmt.Errorf("storage requires explicit spec.storage.strategy and volume inventory")
	}
	if problems := c.validateStorage(); len(problems) > 0 {
		return StorageConfig{}, &ValidationError{Problems: problems}
	}
	settings := *c.Spec.Storage
	settings.Volumes = append([]LocalVolume(nil), settings.Volumes...)
	sort.Slice(settings.Volumes, func(i, j int) bool { return settings.Volumes[i].Name < settings.Volumes[j].Name })
	return settings, nil
}
