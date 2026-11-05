package gitops

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func renderExistingStorage(cfg config.Config, settings config.StorageConfig, files map[string][]byte) error {
	script, err := storageCheck.ReadFile("storage/class_check.py")
	if err != nil {
		return err
	}
	script = bytes.ReplaceAll(script, []byte("\r\n"), []byte("\n"))
	namespace := settings.Namespace()
	meta := func(name, wave string, scoped bool) map[string]any {
		value := map[string]any{"name": name, "annotations": map[string]string{
			"bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": "storage", "argocd.argoproj.io/sync-wave": wave,
		}}
		if scoped {
			value["namespace"] = namespace
		}
		return value
	}
	role := ApplicationName(cfg.Metadata.Name, "storage-class-reader")
	account := "storage-class-check"
	identity, err := json.Marshal([]any{cfg.Metadata.Name, settings.ClassName, StorageCheckImage, fmt.Sprintf("%x", sha256.Sum256(script))})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(identity)
	jobName := fmt.Sprintf("existing-check-%x", digest[:16])
	ns := meta(namespace, "-30", false)
	ns["labels"] = map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}
	objects := []map[string]any{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": ns},
		{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta(account, "-20", true), "automountServiceAccountToken": false},
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": meta(role, "-20", false),
			"rules": []map[string]any{{"apiGroups": []string{"storage.k8s.io"}, "resources": []string{"storageclasses"},
				"resourceNames": []string{settings.ClassName}, "verbs": []string{"get"}}}},
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": meta(role, "-20", false),
			"roleRef":  map[string]string{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role},
			"subjects": []map[string]string{{"kind": "ServiceAccount", "name": account, "namespace": namespace}}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta(account, "-20", true), "data": map[string]string{"class_check.py": string(script)}},
		{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("storage-contract-isolation", "-20", true),
			"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"}}},
		{"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy", "metadata": meta("storage-contract-api", "-20", true),
			"spec": map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": account}},
				"egress": []map[string]any{{"toEntities": []string{"kube-apiserver"}, "toPorts": []map[string]any{{
					"ports": []map[string]string{{"port": "443", "protocol": "TCP"}, {"port": "6443", "protocol": "TCP"}},
				}}}}}},
		{"apiVersion": "batch/v1", "kind": "Job", "metadata": meta(jobName, "0", true), "spec": map[string]any{
			"backoffLimit": 0, "activeDeadlineSeconds": 120, "template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": account}},
				"spec": map[string]any{"restartPolicy": "Never", "serviceAccountName": account, "automountServiceAccountToken": true,
					"tolerations": []map[string]string{{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}},
					"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
						"seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"volumes": []map[string]any{{"name": "check", "configMap": map[string]string{"name": account}}},
					"containers": []map[string]any{{"name": "check", "image": StorageCheckImage, "imagePullPolicy": "IfNotPresent",
						"command": []string{"/usr/local/bin/python3", "-I", "-B", "/check/class_check.py"}, "args": []string{settings.ClassName},
						"volumeMounts": []map[string]any{{"name": "check", "mountPath": "/check", "readOnly": true}},
						"securityContext": map[string]any{"readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false,
							"capabilities": map[string]any{"drop": []string{"ALL"}}},
						"resources": map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"},
							"limits": map[string]string{"cpu": "100m", "memory": "64Mi"}},
					}},
				},
			},
		}},
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, object := range objects {
		if err := encoder.Encode(object); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	files["components/storage-existing/resources.yaml"] = output.Bytes()
	return nil
}
