package gitops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func storageFixture(t *testing.T) config.Config {
	t.Helper()
	data, err := os.ReadFile("../../../examples/storage-fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLocalStorageUsesRetainedNodeBoundVolumesAndReadOnlyChecks(t *testing.T) {
	files, err := Render(storageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(files["components/storage/resources.yaml"]))
	volumes, jobs := 0, 0
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		switch obj["kind"] {
		case "Secret", "ClusterRole", "ClusterRoleBinding", "PersistentVolumeClaim":
			t.Fatal("storage acquired runtime credentials, cluster privileges or workload claims")
		case "StorageClass":
			if obj["provisioner"] != "kubernetes.io/no-provisioner" || obj["reclaimPolicy"] != "Retain" || obj["volumeBindingMode"] != "WaitForFirstConsumer" || obj["allowVolumeExpansion"] != false {
				t.Fatal("unsafe storage class")
			}
		case "PersistentVolume":
			volumes++
			spec := obj["spec"].(map[string]any)
			if spec["persistentVolumeReclaimPolicy"] != "Retain" || spec["local"].(map[string]any)["path"] != "/var/lib/bareplane/local-volumes/lab/first/data" || spec["nodeAffinity"] == nil {
				t.Fatal("unsafe local volume")
			}
		case "Job":
			jobs++
			pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if pod["automountServiceAccountToken"] != false || pod["nodeName"] != "lab-control-1" || pod["hostPID"] != nil || pod["hostNetwork"] != nil {
				t.Fatal("unscoped node check")
			}
			container := pod["containers"].([]any)[0].(map[string]any)
			if container["image"] != StorageCheckImage || container["securityContext"].(map[string]any)["readOnlyRootFilesystem"] != true {
				t.Fatal("unsafe inspector image or filesystem")
			}
			for _, raw := range container["volumeMounts"].([]any) {
				if raw.(map[string]any)["readOnly"] != true {
					t.Fatal("inspector received a writable volume")
				}
				if raw.(map[string]any)["name"] == "host" && raw.(map[string]any)["recursiveReadOnly"] != "Enabled" {
					t.Fatal("inspector permits writable host submounts")
				}
			}
			for _, raw := range pod["volumes"].([]any) {
				volume := raw.(map[string]any)
				if host, ok := volume["hostPath"].(map[string]any); ok && host["type"] != "Directory" {
					t.Fatal("inspector could create a host directory")
				}
			}
		}
	}
	if volumes != 1 || jobs != 1 {
		t.Fatal("unexpected inventory size")
	}
}

func TestStorageRequiresExplicitInventoryAndQuotesScalarNames(t *testing.T) {
	cfg := storageFixture(t)
	cfg.Metadata.Name = "false"
	cfg.Spec.Storage.Volumes[0].Node = "false-control-1"
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files["components/storage/resources.yaml"]), `bareplane.io/cluster: "false"`) {
		t.Fatal("cluster identifier lost its YAML string type")
	}
	cfg.Spec.Components.Enabled = []string{"storage"}
	cfg.Spec.Storage = nil
	if files, err := Render(cfg); err == nil || files != nil {
		t.Fatal("unconfigured storage rendered")
	}
}

func TestStorageIgnoreRuleSurvivesArgoApplicationJSONRoundTrip(t *testing.T) {
	files, err := Render(storageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var application map[string]any
	if err := yaml.Unmarshal(files["examples/gitops/storage-root/applications/storage.yaml"], &application); err != nil {
		t.Fatal(err)
	}
	spec := application["spec"].(map[string]any)
	before, err := json.Marshal(spec["ignoreDifferences"])
	if err != nil {
		t.Fatal(err)
	}
	// Match the pinned Argo Application API's optional core-group field. Its
	// controller serializes this typed structure when updating Applications.
	var rules []struct {
		Group        string   `json:"group,omitempty"`
		Kind         string   `json:"kind"`
		JSONPointers []string `json:"jsonPointers,omitempty"`
	}
	if err := json.Unmarshal(before, &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Group != "" || rules[0].Kind != "PersistentVolume" || !reflect.DeepEqual(rules[0].JSONPointers, []string{"/spec/claimRef"}) {
		t.Fatal("storage must ignore only the live PV claim binding")
	}
	after, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	var desired, observed any
	if err := json.Unmarshal(before, &desired); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &observed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(desired, observed) {
		t.Fatal("controller JSON normalization would leave the parent permanently OutOfSync")
	}
	policy := spec["syncPolicy"].(map[string]any)
	if !strings.Contains(fmt.Sprint(policy["syncOptions"]), "RespectIgnoreDifferences=true") {
		t.Fatal("storage synchronization must preserve the live claim binding")
	}
}
