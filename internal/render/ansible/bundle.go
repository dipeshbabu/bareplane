package ansible

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/topology"
	"gopkg.in/yaml.v3"
)

//go:embed assets
var bundleAssets embed.FS

// RenderBundle returns a complete offline workspace. It intentionally copies only
// bootstrap inputs: credentials, key paths, and provider endpoints stay outside it.
func RenderBundle(cfg config.Config) (map[string][]byte, error) {
	if err := cfg.ValidateKubernetesBootstrap(); err != nil {
		return nil, fmt.Errorf("validate Kubernetes bootstrap configuration: %w", err)
	}
	inventory, err := RenderInventory(cfg)
	if err != nil {
		return nil, err
	}
	topo, err := topology.Build(cfg)
	if err != nil {
		return nil, err
	}
	control := make([]string, 0)
	workers := make([]string, 0)
	for _, machine := range topo.Machines {
		if machine.Role == "control-plane" {
			control = append(control, machine.Name)
		} else {
			workers = append(workers, machine.Name)
		}
	}
	sort.Strings(control)
	sort.Strings(workers)
	settings := cfg.Spec.Kubernetes
	// Use an explicit allowlist, never marshal Config (which includes key references).
	vars := struct {
		Version              int      `yaml:"bareplane_bundle_version"`
		Cluster              string   `yaml:"bareplane_cluster"`
		KubernetesVersion    string   `yaml:"bareplane_kubernetes_version"`
		APIVIP               string   `yaml:"bareplane_api_vip"`
		APIPort              int      `yaml:"bareplane_api_port"`
		PodCIDR              string   `yaml:"bareplane_pod_cidr"`
		ServiceCIDR          string   `yaml:"bareplane_service_cidr"`
		KubeVIPVersion       string   `yaml:"bareplane_kube_vip_version"`
		CiliumVersion        string   `yaml:"bareplane_cilium_version"`
		KubeProxyReplacement bool     `yaml:"bareplane_kube_proxy_replacement"`
		ControlPlanes        []string `yaml:"bareplane_control_planes"`
		Workers              []string `yaml:"bareplane_workers"`
		Primary              string   `yaml:"bareplane_primary_control_plane"`
	}{1, cfg.Metadata.Name, settings.Version, settings.APIVIP, 6443,
		settings.PodCIDR, settings.ServiceCIDR, settings.KubeVIPVersion, settings.CiliumVersion,
		settings.KubeProxyReplacement, control, workers, control[0]}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(vars); err != nil {
		return nil, fmt.Errorf("encode bootstrap variables: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	files := map[string][]byte{InventoryFilename: inventory, "group_vars/all/cluster.yaml": encoded.Bytes()}
	err = fs.WalkDir(bundleAssets, "assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := bundleAssets.ReadFile(name)
		if err != nil {
			return err
		}
		// Git checkouts on Windows can use CRLF; the emitted contract always uses LF.
		files[strings.TrimPrefix(name, "assets/")] = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read embedded bootstrap bundle: %w", err)
	}
	return files, nil
}
