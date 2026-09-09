package gitops

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

const VaultOperatorVersion = "2.10.0"

func renderVault(cfg config.Config, files map[string][]byte) error {
	v, err := cfg.RequireVault()
	if err != nil {
		return err
	}
	provider := map[string]any{"server": v.Address, "path": v.KVMount, "version": "v2", "auth": map[string]any{
		"kubernetes": map[string]any{"mountPath": v.AuthMount, "role": v.Role,
			"serviceAccountRef": map[string]any{"name": "bareplane-vault-reader", "audiences": []string{v.Audience}}},
	}}
	if v.VaultNamespace != "" {
		provider["namespace"] = v.VaultNamespace
	}
	if v.CASecret != nil {
		provider["caProvider"] = map[string]string{"type": "Secret", "name": v.CASecret.Name, "key": v.CASecret.Key}
	}
	identity, err := json.Marshal(map[string]any{"cluster": cfg.Metadata.Name, "namespace": v.WorkloadNamespace, "provider": provider})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(identity)
	class := fmt.Sprintf("bareplane-vault-%x", digest[:16])
	// A connection change rolls both the immutable Store name and controller
	// class. Retained old Stores cannot keep requesting JWTs for an old server.
	controller, err := renderVaultController(files["components/vault/controller.yaml"], v.WorkloadNamespace, class)
	if err != nil {
		return err
	}
	files["components/vault/controller.yaml"] = controller
	metadata := func(name, wave string) map[string]any {
		return map[string]any{"name": name, "namespace": v.WorkloadNamespace,
			"annotations": map[string]string{"bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": "vault",
				"bareplane.io/existing-namespace": v.WorkloadNamespace, "argocd.argoproj.io/sync-wave": wave}}
	}
	objects := []map[string]any{{"apiVersion": "external-secrets.io/v1", "kind": "SecretStore", "metadata": metadata(class, "0"),
		"spec": map[string]any{"controller": class, "provider": map[string]any{"vault": provider}},
	}}
	for _, secret := range v.Secrets {
		name := ApplicationName("bareplane-vault", secret.Name)
		fields := make([]map[string]any, 0, len(secret.Keys))
		for _, key := range secret.Keys {
			fields = append(fields, map[string]any{"secretKey": key.SecretKey, "remoteRef": map[string]string{
				"key": secret.Path, "property": key.Property, "decodingStrategy": "None", "conversionStrategy": "Default"}})
		}
		objects = append(objects, map[string]any{"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret", "metadata": metadata(name, "1"),
			"spec": map[string]any{"refreshPolicy": "Periodic", "refreshInterval": v.RefreshInterval,
				"secretStoreRef": map[string]string{"name": class, "kind": "SecretStore"}, "data": fields,
				"target": map[string]any{"name": secret.Name, "creationPolicy": "Orphan", "deletionPolicy": "Retain", "template": map[string]any{
					"type": "Opaque", "metadata": map[string]any{"labels": map[string]string{"bareplane.io/vault-managed": "true", "bareplane.io/vault-source": name}},
				}},
			},
		})
	}
	objects = append(vaultOwnershipPolicy(cfg, v), objects...)
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, obj := range objects {
		if err := encoder.Encode(obj); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	files["components/vault/integration.yaml"] = output.Bytes()
	var cm map[string]any
	if err := yaml.Unmarshal(files["components/argocd/configuration.yaml"], &cm); err != nil {
		return err
	}
	data, ok := cm["data"].(map[string]any)
	if !ok {
		return errors.New("reviewed Argo configuration is missing")
	}
	data["resource.customizations.health.external-secrets.io_SecretStore"] = vaultStoreHealth
	data["resource.customizations.health.external-secrets.io_ExternalSecret"] = vaultSecretHealth
	files["components/argocd/configuration.yaml"], err = yaml.Marshal(cm)
	return err
}

func renderVaultController(data []byte, namespace, class string) ([]byte, error) {
	replace := strings.NewReplacer("BAREPLANE_VAULT_WORKLOAD_NAMESPACE", namespace, "BAREPLANE_VAULT_CONTROLLER_CLASS", class)
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.Contains(node.Value, "BAREPLANE_VAULT_") {
			node.Value, node.Style = replace.Replace(node.Value), yaml.DoubleQuotedStyle
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	var output bytes.Buffer
	decoder := yaml.NewDecoder(bytes.NewReader(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))))
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		visit(&node)
		if err := encoder.Encode(&node); err != nil {
			return nil, err
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	if output.Len() == 0 || bytes.Contains(output.Bytes(), []byte("BAREPLANE_")) {
		return nil, errors.New("Vault controller payload has missing or unresolved public input")
	}
	return output.Bytes(), nil
}

func vaultOwnershipPolicy(cfg config.Config, v config.VaultConfig) []map[string]any {
	names := make([]string, 0, len(v.Secrets))
	sources := make(map[string]string, len(v.Secrets))
	for _, secret := range v.Secrets {
		names = append(names, secret.Name)
		sources[secret.Name] = ApplicationName("bareplane-vault", secret.Name)
	}
	nameJSON, _ := json.Marshal(names)
	sourceJSON, _ := json.Marshal(sources)
	namespaceJSON, _ := json.Marshal(v.WorkloadNamespace)
	const writer = `request.userInfo.username == 'system:serviceaccount:vault-secrets:bareplane-vault'`
	const managed = `has(object.metadata.labels) && 'bareplane.io/vault-managed' in object.metadata.labels && object.metadata.labels['bareplane.io/vault-managed'] == 'true'`
	const oldManaged = `oldObject != null && has(oldObject.metadata.labels) && 'bareplane.io/vault-managed' in oldObject.metadata.labels && oldObject.metadata.labels['bareplane.io/vault-managed'] == 'true'`
	const source = `has(object.metadata.labels) && 'bareplane.io/vault-source' in object.metadata.labels`
	selected := "object.metadata.namespace == " + string(namespaceJSON) + " && object.metadata.name in " + string(nameJSON)
	metadata := func() map[string]any {
		return map[string]any{"name": "bareplane-vault-secret-ownership", "annotations": map[string]string{
			"bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": "vault", "argocd.argoproj.io/sync-wave": "-15"}}
	}
	policy := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy", "metadata": metadata(), "spec": map[string]any{
		"failurePolicy": "Fail", "matchConstraints": map[string]any{"resourceRules": []map[string]any{{"apiGroups": []string{""}, "apiVersions": []string{"v1"},
			"operations": []string{"CREATE", "UPDATE"}, "resources": []string{"secrets"}, "scope": "Namespaced"}}},
		// Match reserved names even before ESO adds its preliminary managed label.
		// Also constrain every operator Secret write, not just labeled payloads.
		"matchConditions": []map[string]string{{"name": "vault-targets", "expression": "(" + writer + ") || (" + selected + ") || (" + managed + ") || (" + oldManaged + ")"}},
		"validations": []map[string]string{
			{"expression": writer, "message": "Vault-managed Secrets must be written by the scoped operator"},
			{"expression": selected, "message": "Vault operator Secret is outside its configured targets"},
			{"expression": "oldObject == null || (" + oldManaged + ")", "message": "Vault refuses to adopt an existing unmanaged Secret"},
			{"expression": managed, "message": "Vault Secret ownership must be preserved"},
			{"expression": "(" + source + ") && object.metadata.name in " + string(nameJSON) + " && object.metadata.labels['bareplane.io/vault-source'] == " + string(sourceJSON) + "[object.metadata.name]", "message": "Vault Secret source identity differs from its contract"},
			{"expression": `oldObject == null || (has(oldObject.metadata.labels) && 'bareplane.io/vault-source' in oldObject.metadata.labels && oldObject.metadata.labels['bareplane.io/vault-source'] == object.metadata.labels['bareplane.io/vault-source'])`, "message": "Vault Secret ownership cannot transfer between sources"},
			{"expression": `!has(object.metadata.ownerReferences) || size(object.metadata.ownerReferences) == 0`, "message": "Retained Vault Secrets must not acquire garbage-collection owners"},
		},
	}}
	binding := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding", "metadata": metadata(), "spec": map[string]any{
		"policyName": "bareplane-vault-secret-ownership", "validationActions": []string{"Deny"},
	}}
	return []map[string]any{policy, binding}
}

const vaultStoreHealth = `local status = obj.status or {}
for _, condition in ipairs(status.conditions or {}) do
  if condition.type == "Ready" then
    if condition.status == "True" then return {status = "Healthy", message = "Vault store authentication is ready"} end
    if condition.status == "False" then return {status = "Degraded", message = "Vault store authentication or trust is unavailable"} end
  end
end
return {status = "Progressing", message = "Waiting for Vault store authentication"}
`

const vaultSecretHealth = `local status = obj.status or {}
for _, condition in ipairs(status.conditions or {}) do
  if condition.type == "Ready" then
    if condition.status == "False" then return {status = "Degraded", message = "Vault Secret synchronization is unavailable; retained data is unchanged"} end
    if condition.status == "True" then
      local observed = (obj.metadata or {}).generation
      local generation = tostring(observed or "")
      local synced = status.syncedResourceVersion
      -- Argo's default health sandbox has no string library. The lexical
      -- interval (generation .. "-", generation .. ".") matches a nonempty
      -- hash after the exact generation prefix ('-' immediately precedes '.').
      if type(observed) == "number" and observed >= 1 and type(synced) == "string" and synced > generation .. "-" and synced < generation .. "." and type(status.refreshTime) == "string" and status.refreshTime ~= "" then
        return {status = "Healthy", message = "Vault Secret generation has synchronized"}
      end
    end
  end
end
return {status = "Progressing", message = "Waiting for the current Vault Secret generation"}
`
