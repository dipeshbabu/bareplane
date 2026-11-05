package gitops

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestExistingStorageOnlyReadsOneExternalClass(t *testing.T) {
	cfg := storageFixture(t)
	cfg.Spec.Storage = &config.StorageConfig{Strategy: "existing-class", ClassName: "fast-data"}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["components/storage/resources.yaml"]; ok {
		t.Fatal("external strategy emitted a local-volume payload")
	}
	if _, ok := files["components/storage-existing/kustomization.yaml"]; !ok {
		t.Fatal("external strategy has no independent Kustomize root")
	}
	var application map[string]any
	if err := yaml.Unmarshal(files["examples/gitops/storage-root/applications/storage.yaml"], &application); err != nil {
		t.Fatal(err)
	}
	spec := application["spec"].(map[string]any)
	if spec["source"].(map[string]any)["path"] != "components/storage-existing" || spec["destination"].(map[string]any)["namespace"] != "storage-contract" {
		t.Fatal("external payload shares the local path or privileged namespace")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(files["components/storage-existing/resources.yaml"]))
	seen := map[string]int{}
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		kind := object["kind"].(string)
		seen[kind]++
		switch kind {
		case "StorageClass", "PersistentVolume", "PersistentVolumeClaim", "Secret", "DaemonSet":
			t.Fatalf("external storage acquired or provisioned resources: %s", kind)
		case "Namespace":
			if object["metadata"].(map[string]any)["labels"].(map[string]any)["pod-security.kubernetes.io/enforce"] != "restricted" {
				t.Fatal("class inspection weakened namespace pod security")
			}
		case "ClusterRole":
			rules := object["rules"].([]any)
			if len(rules) != 1 {
				t.Fatal("class reader has extra permission rules")
			}
			rule := rules[0].(map[string]any)
			if !reflect.DeepEqual(rule["verbs"], []any{"get"}) || !reflect.DeepEqual(rule["resources"], []any{"storageclasses"}) || !reflect.DeepEqual(rule["resourceNames"], []any{"fast-data"}) {
				t.Fatal("class reader can list, mutate or read unrelated resources")
			}
		case "Job":
			pod := object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if pod["nodeName"] != nil || pod["hostNetwork"] != nil || pod["hostPID"] != nil || pod["securityContext"].(map[string]any)["runAsNonRoot"] != true {
				t.Fatal("external reader acquired host or root access")
			}
			for _, raw := range pod["volumes"].([]any) {
				if raw.(map[string]any)["hostPath"] != nil || raw.(map[string]any)["persistentVolumeClaim"] != nil {
					t.Fatal("external contract inspection mounted host or workload data")
				}
			}
		case "CiliumNetworkPolicy":
			egress := object["spec"].(map[string]any)["egress"].([]any)
			if len(egress) != 1 || !reflect.DeepEqual(egress[0].(map[string]any)["toEntities"], []any{"kube-apiserver"}) {
				t.Fatal("class inspection can reach non-API entities")
			}
		}
	}
	if seen["ClusterRole"] != 1 || seen["ClusterRoleBinding"] != 1 || seen["Job"] != 1 || seen["Namespace"] != 1 {
		t.Fatal("incomplete external class inspection graph")
	}
}
