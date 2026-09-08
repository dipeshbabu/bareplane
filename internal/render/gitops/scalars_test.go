package gitops

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestClusterIdentifiersKeepYAMLStringTypes(t *testing.T) {
	for _, name := range []string{"false", "true", "null", "on", "off", "yes", "no", "123", "0xdead", "1e3", "2026-01-01", "lab"} {
		t.Run(name, func(t *testing.T) {
			cfg := fixture(t)
			cfg.Metadata.Name = name
			cfg.Spec.Components = &config.ComponentSelection{Enabled: []string{"metrics-server"}}
			files, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for path, data := range files {
				if !strings.HasSuffix(path, ".yaml") {
					continue
				}
				for _, line := range bytes.Split(data, []byte("\n")) {
					line = bytes.TrimSpace(line)
					if !bytes.HasPrefix(line, []byte("bareplane.io/cluster:")) {
						continue
					}
					seen++
					var value map[string]any
					if err := yaml.Unmarshal(line, &value); err != nil || value["bareplane.io/cluster"] != name {
						t.Fatalf("%s changed cluster identifier type: %q: %v (%T)", path, line, err, value["bareplane.io/cluster"])
					}
					// Kubernetes' YAML converter also recognizes YAML 1.1 booleans.
					if name == "on" || name == "off" || name == "yes" || name == "no" {
						if string(line) == "bareplane.io/cluster: "+name {
							t.Fatal("legacy YAML boolean was not explicitly quoted")
						}
					}
				}
			}
			if seen < 90 {
				t.Fatalf("component ownership annotations were not fully exercised: %d", seen)
			}
		})
	}
}

func TestClusterPlaceholdersAreStandaloneYAMLScalars(t *testing.T) {
	if err := fs.WalkDir(assets, "assets", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if bytes.Contains(line, []byte("BAREPLANE_CLUSTER_NAME")) && string(bytes.TrimSpace(line)) != "bareplane.io/cluster: BAREPLANE_CLUSTER_NAME" {
				t.Fatalf("%s embeds cluster placeholder outside a standalone scalar", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
