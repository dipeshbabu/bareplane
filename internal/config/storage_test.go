package config

import "testing"

func storageConfigFixture() Config {
	return Config{Metadata: Metadata{Name: "lab"}, Spec: Spec{Nodes: []NodeGroup{{Name: "control", Count: 1, DiskGB: 24}},
		Storage: &StorageConfig{Strategy: "local-static", Volumes: []LocalVolume{{Name: "data", Node: "lab-control-1", CapacityGiB: 1}}}}}
}

func TestStorageReferencesCannotSelectUnknownNodesPathsOrOversubscribeDisk(t *testing.T) {
	for name, mutate := range map[string]func(*StorageConfig){
		"implicit strategy":    func(s *StorageConfig) { s.Strategy = "" },
		"dynamic strategy":     func(s *StorageConfig) { s.Strategy = "local-path" },
		"empty inventory":      func(s *StorageConfig) { s.Volumes = nil },
		"unbounded inventory":  func(s *StorageConfig) { s.Volumes = make([]LocalVolume, 33) },
		"path traversal":       func(s *StorageConfig) { s.Volumes[0].Name = "../other" },
		"duplicate names":      func(s *StorageConfig) { s.Volumes = append(s.Volumes, s.Volumes[0]) },
		"foreign node":         func(s *StorageConfig) { s.Volumes[0].Node = "foreign-control-1" },
		"nonexistent node":     func(s *StorageConfig) { s.Volumes[0].Node = "lab-control-2" },
		"noncanonical ordinal": func(s *StorageConfig) { s.Volumes[0].Node = "lab-control-01" },
		"no capacity":          func(s *StorageConfig) { s.Volumes[0].CapacityGiB = 0 },
		"capacity overflow":    func(s *StorageConfig) { s.Volumes[0].CapacityGiB = int(^uint(0) >> 1) },
		"disk overcommit":      func(s *StorageConfig) { s.Volumes[0].CapacityGiB = 21 },
		"aggregate overcommit": func(s *StorageConfig) {
			s.Volumes[0].CapacityGiB = 15
			s.Volumes = append(s.Volumes, LocalVolume{Name: "second", Node: "lab-control-1", CapacityGiB: 6})
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := storageConfigFixture()
			mutate(cfg.Spec.Storage)
			if _, err := cfg.RequireStorage(); err == nil {
				t.Fatal("unsafe local storage configuration accepted")
			}
		})
	}
	cfg := storageConfigFixture()
	s, err := cfg.RequireStorage()
	if err != nil || s.Volumes[0].Directory("lab") != "/var/lib/bareplane/local-volumes/lab/data/data" {
		t.Fatal(s, err)
	}
	cfg.Spec.Storage = nil
	if _, err := cfg.RequireStorage(); err == nil {
		t.Fatal("implicit storage inventory accepted")
	}
}
