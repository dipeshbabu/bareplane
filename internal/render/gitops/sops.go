package gitops

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

const SOPSVersion = "3.13.3"

//go:embed sops/generate.py sops/gpg sops/plugin.yaml
var sopsIntegration embed.FS

func renderSOPS(cfg config.Config, files map[string][]byte) error {
	s, err := cfg.RequireSOPS()
	if err != nil {
		return err
	}
	configuration := map[string]any{}
	if err := yaml.Unmarshal(files["components/argocd/configuration.yaml"], &configuration); err != nil {
		return fmt.Errorf("decode reviewed Argo configuration: %w", err)
	}
	data, ok := configuration["data"].(map[string]any)
	if !ok {
		return fmt.Errorf("reviewed Argo configuration data is missing")
	}
	for _, entry := range []struct{ source, key string }{{"generate.py", "sops-generate.py"}, {"gpg", "sops-gpg"}, {"plugin.yaml", "sops-plugin.yaml"}} {
		value, err := sopsIntegration.ReadFile("sops/" + entry.source)
		if err != nil {
			return err
		}
		data[entry.key] = strings.ReplaceAll(string(value), "\r\n", "\n")
	}
	policy, err := json.Marshal(map[string]any{"age": s.AgeKey != nil, "pgp": s.PGPKey != nil, "passphrase": s.PGPPassphrase != nil, "namespaces": s.Namespaces})
	if err != nil {
		return err
	}
	data["sops-policy.json"] = string(policy)
	configurationBytes, err := yaml.Marshal(configuration)
	if err != nil {
		return err
	}
	files["components/argocd/configuration.yaml"] = configurationBytes

	volumes := []map[string]any{
		{"name": "sops-tmp", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "256Mi"}},
		{"name": "sops-config", "configMap": map[string]any{"name": "argocd-cm", "defaultMode": 0o555, "items": []map[string]string{
			{"key": "sops-generate.py", "path": "generate.py"}, {"key": "sops-gpg", "path": "gpg"},
			{"key": "sops-plugin.yaml", "path": "plugin.yaml"}, {"key": "sops-policy.json", "path": "policy.json"},
		}}},
	}
	mounts := []map[string]any{
		{"name": "var-files", "mountPath": "/var/run/argocd", "readOnly": true},
		{"name": "plugins", "mountPath": "/home/argocd/cmp-server/plugins"},
		{"name": "sops-tmp", "mountPath": "/tmp"},
		{"name": "sops-config", "mountPath": "/home/argocd/cmp-server/config", "readOnly": true},
	}
	for _, slot := range []struct {
		name string
		ref  *config.SOPSKeyReference
	}{{"age", s.AgeKey}, {"pgp", s.PGPKey}, {"passphrase", s.PGPPassphrase}} {
		if slot.ref == nil {
			continue
		}
		volumes = append(volumes, map[string]any{"name": "sops-" + slot.name, "secret": map[string]any{
			"secretName": slot.ref.Name, "optional": true, "defaultMode": 0o440,
			"items": []map[string]string{{"key": slot.ref.Key, "path": "key"}},
		}})
		mounts = append(mounts, map[string]any{"name": "sops-" + slot.name, "mountPath": "/var/run/bareplane-sops/" + slot.name, "readOnly": true})
	}
	patch := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]string{"name": "argocd-repo-server", "namespace": "argocd"},
		"spec": map[string]any{"template": map[string]any{
			"metadata": map[string]any{"annotations": map[string]string{
				"bareplane.io/sops-config-sha256": fmt.Sprintf("%x", sha256.Sum256(configurationBytes)), "bareplane.io/sops-keys-revision": s.Revision,
			}},
			"spec": map[string]any{"automountServiceAccountToken": false, "securityContext": map[string]any{"fsGroup": 999}, "volumes": volumes,
				"containers": []map[string]any{{
					"name": "bareplane-sops", "image": "ghcr.io/getsops/sops:v" + SOPSVersion, "imagePullPolicy": "IfNotPresent",
					"command":      []string{"/var/run/argocd/argocd-cmp-server"},
					"env":          []map[string]string{{"name": "ARGOCD_EXEC_TIMEOUT", "value": "70s"}},
					"volumeMounts": mounts,
					"securityContext": map[string]any{"runAsUser": 999, "runAsGroup": 999, "runAsNonRoot": true,
						"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
						"capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"memory": "512Mi"}},
				}},
			},
		}},
	}
	patchBytes, err := yaml.Marshal(patch)
	if err != nil {
		return err
	}
	var kustomize map[string]any
	if err := yaml.Unmarshal(files["components/argocd/kustomization.yaml"], &kustomize); err != nil {
		return err
	}
	patches, ok := kustomize["patches"].([]any)
	if !ok {
		return fmt.Errorf("reviewed Argo patches are missing")
	}
	kustomize["patches"] = append(patches, map[string]any{"patch": string(patchBytes), "target": map[string]string{"group": "apps", "version": "v1", "kind": "Deployment", "name": "argocd-repo-server"}})
	files["components/argocd/kustomization.yaml"], err = yaml.Marshal(kustomize)
	if err != nil {
		return err
	}
	return renderSOPSOwnership(cfg, s, files)
}

func renderSOPSOwnership(cfg config.Config, s config.SOPSConfig, files map[string][]byte) error {
	namespaces, err := json.Marshal(s.Namespaces)
	if err != nil {
		return err
	}
	const managed = `has(object.metadata.labels) && 'bareplane.io/sops-managed' in object.metadata.labels && object.metadata.labels['bareplane.io/sops-managed'] == 'true'`
	const oldManaged = `oldObject != null && has(oldObject.metadata.labels) && 'bareplane.io/sops-managed' in oldObject.metadata.labels && oldObject.metadata.labels['bareplane.io/sops-managed'] == 'true'`
	const tracking = `has(object.metadata.annotations) && 'argocd.argoproj.io/tracking-id' in object.metadata.annotations && object.metadata.annotations['argocd.argoproj.io/tracking-id'] != ''`
	const ownership = `oldObject == null || (has(oldObject.metadata.annotations) && 'argocd.argoproj.io/tracking-id' in oldObject.metadata.annotations && oldObject.metadata.annotations['argocd.argoproj.io/tracking-id'] == object.metadata.annotations['argocd.argoproj.io/tracking-id'])`
	metadata := func() map[string]any {
		return map[string]any{"name": "bareplane-sops-secret-ownership", "annotations": map[string]string{"bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": "secrets-sops"}}
	}
	policy := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy", "metadata": metadata(), "spec": map[string]any{
		"failurePolicy":    "Fail",
		"matchConstraints": map[string]any{"resourceRules": []map[string]any{{"apiGroups": []string{""}, "apiVersions": []string{"v1"}, "operations": []string{"CREATE", "UPDATE"}, "resources": []string{"secrets"}, "scope": "Namespaced"}}},
		"matchConditions":  []map[string]string{{"name": "sops-managed", "expression": "(" + managed + ") || (" + oldManaged + ")"}},
		"validations": []map[string]string{
			{"expression": `request.userInfo.username == 'system:serviceaccount:argocd:argocd-application-controller'`, "message": "SOPS-managed Secrets must be written by Argo"},
			{"expression": "object.metadata.namespace in " + string(namespaces), "message": "SOPS Secret namespace is not allowed"},
			{"expression": managed, "message": "SOPS ownership cannot be removed"},
			{"expression": "oldObject == null || (" + oldManaged + ")", "message": "SOPS refuses to adopt an existing unmanaged Secret"},
			{"expression": "(" + tracking + ") && (" + ownership + ")", "message": "SOPS refuses to transfer Secret ownership between Applications"},
		},
	}}
	binding := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding", "metadata": metadata(), "spec": map[string]any{
		"policyName": "bareplane-sops-secret-ownership", "validationActions": []string{"Deny"},
	}}
	first, err := yaml.Marshal(policy)
	if err != nil {
		return err
	}
	second, err := yaml.Marshal(binding)
	if err != nil {
		return err
	}
	files["components/secrets-sops/ownership.yaml"] = append(append(first, []byte("---\n")...), second...)
	return nil
}
