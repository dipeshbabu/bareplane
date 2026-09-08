package gitops

import (
	"bytes"
	"sort"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func renderCertificateIssuers(cfg config.Config, files map[string][]byte) error {
	if cfg.Spec.Certificates == nil || len(cfg.Spec.Certificates.Issuers) == 0 {
		return nil
	}
	issuers := append([]config.CertificateIssuer(nil), cfg.Spec.Certificates.Issuers...)
	sort.Slice(issuers, func(i, j int) bool { return issuers[i].Name < issuers[j].Name })
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, issuer := range issuers {
		spec := map[string]any{"selfSigned": map[string]any{}}
		if issuer.Type == "ca" {
			spec = map[string]any{"ca": map[string]string{"secretName": issuer.SecretName}}
		}
		object := map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{
			"name": issuer.Name, "annotations": map[string]string{"bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": "cert-manager", "argocd.argoproj.io/sync-wave": "10"},
		}, "spec": spec}
		if err := encoder.Encode(object); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	files["components/cert-manager/issuers.yaml"] = output.Bytes()
	data, err := yaml.Marshal(kustomization("upstream.yaml", "issuers.yaml"))
	if err != nil {
		return err
	}
	files["components/cert-manager/kustomization.yaml"] = data
	return nil
}
