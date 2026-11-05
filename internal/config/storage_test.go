package config

import "testing"

func TestExistingStorageClassContractIsExplicitAndExclusive(t *testing.T) {
	cfg := storageConfigFixture()
	cfg.Spec.Storage = &StorageConfig{Strategy: "existing-class", ClassName: "fast.data-csi"}
	settings, err := cfg.RequireStorage()
	if err != nil || settings.Namespace() != "storage-contract" || len(settings.Volumes) != 0 {
		t.Fatal(settings, err)
	}
	for name, mutate := range map[string]func(*StorageConfig){
		"missing class": func(s *StorageConfig) { s.ClassName = "" },
		"path class":    func(s *StorageConfig) { s.ClassName = "../other" },
		"URL class":     func(s *StorageConfig) { s.ClassName = "https://other" },
		"uppercase":     func(s *StorageConfig) { s.ClassName = "Other" },
		"empty label":   func(s *StorageConfig) { s.ClassName = "a..b" },
		"mixed inventory": func(s *StorageConfig) {
			s.Volumes = []LocalVolume{{Name: "data", Node: "lab-control-1", CapacityGiB: 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := settings
			mutate(&value)
			cfg.Spec.Storage = &value
			if _, err := cfg.RequireStorage(); err == nil {
				t.Fatal("invalid existing class contract accepted")
			}
		})
	}
	local := storageConfigFixture()
	local.Spec.Storage.ClassName = "external"
	if _, err := local.RequireStorage(); err == nil {
		t.Fatal("local inventory accepted an external class selector")
	}
}

func TestExistingStorageNamespaceDoesNotChangeRegistryOrMinimalDefaults(t *testing.T) {
	cfg := storageConfigFixture()
	cfg.Spec.Storage = &StorageConfig{Strategy: "existing-class", ClassName: "fast-data"}
	resolved, err := cfg.ResolvePlatform()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, component := range resolved.Components {
		if component.ID == "storage" {
			found = component.Namespace == "storage-contract"
		}
	}
	if !found {
		t.Fatal("external class inspection did not use its restricted namespace")
	}
	local := storageConfigFixture()
	resolved, err = local.ResolvePlatform()
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range resolved.Components {
		if component.ID == "storage" && component.Namespace != "local-storage" {
			t.Fatal("external configuration mutated another resolution")
		}
	}
}

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
