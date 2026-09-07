// Package gitops renders public, user-owned desired state without network or
// filesystem access. Runtime bootstrap state is deliberately not an input.
package gitops

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

const ArgoVersion = "3.5.2"

//go:embed assets
var assets embed.FS

// ApplicationName stays within label-length limits without colliding when
// cluster names share a long prefix. The hash includes the complete identity.
func ApplicationName(cluster, component string) string {
	name := cluster + "-" + component
	if len(name) <= 63 {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	return strings.TrimRight(name[:54], "-") + fmt.Sprintf("-%x", digest[:4])
}

func Render(cfg config.Config) (map[string][]byte, error) {
	if err := cfg.ValidateGitOps(); err != nil {
		return nil, err
	}
	// The component graph replaces this small, explicit availability gate in #72.
	// Never present an unimplemented profile as a successfully rendered cluster.
	for _, profile := range cfg.Spec.Profiles {
		if profile != "minimal" {
			return nil, fmt.Errorf("GitOps profile %q is unavailable until its platform components are implemented", profile)
		}
	}
	if cfg.Spec.Features.GPU || cfg.Spec.Features.Observability || cfg.Spec.DNS.Provider != "manual" || cfg.Spec.Secrets.Provider != "sops" {
		return nil, fmt.Errorf("GitOps rendering currently supports only minimal with GPU/observability disabled, manual DNS, and the SOPS extension boundary; requested platform components are unavailable")
	}
	root := cfg.Spec.GitOps.RootPath
	switch strings.Split(root, "/")[0] {
	case "bootstrap", "components", "profiles", "readme.md":
		return nil, fmt.Errorf("spec.gitops.rootPath overlaps a reserved generated repository path")
	}
	files := make(map[string][]byte)
	if err := fs.WalkDir(assets, "assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			return err
		}
		// Inputs are validated DNS labels, not arbitrary YAML fragments.
		data = []byte(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "BAREPLANE_CLUSTER_NAME", cfg.Metadata.Name))
		files["components/"+strings.TrimPrefix(name, "assets/")] = data
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read embedded GitOps assets: %w", err)
	}
	objects := map[string]any{
		"bootstrap/" + cfg.Metadata.Name + "-root-application.yaml": application(cfg, "root", root, "-30"),
		path.Join(root, "kustomization.yaml"):                       kustomization("applications/argocd.yaml"),
		path.Join(root, "applications/argocd.yaml"):                 application(cfg, "argocd", "components/argocd", "-30"),
	}
	for name, obj := range objects {
		data, err := yaml.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", name, err)
		}
		files[name] = data
	}
	files["readme.md"] = []byte(fmt.Sprintf(`# GitOps desired state for %s

Review these public files, then copy them into your user-controlled repository:
%s at revision %s. The root Application reads %s.

Do not initialize Git inside Bareplane's managed export. Copy the payload to a
separate checkout, excluding .bareplane-*.json ownership metadata. Bareplane
never commits or pushes this export. Re-rendering refuses edited/extra files.

Only the pinned Argo CD %s control plane is available in the initial minimal
profile. Cilium, kube-vip, CoreDNS, kubeadm and Linux stay bootstrap-owned.
No SSH keys, kubeconfig, Terraform state, Git credentials or secret values are
part of this repository. Empty Argo Secret declarations acquire values only at
runtime; never commit those values. Argo's required Redis cache is ephemeral.

The root Application is outside its own source path. Argo self-manages the same
reviewed control-plane manifests after handoff. Bootstrap may install only this
minimal control plane and the root Application, never the platform payload.
Automated synchronization is enabled with pruning and self-heal disabled for
initial handoff. Application deletion has no cascading resources finalizer.
Use the guarded handoff workflow once implemented; do not directly apply this
whole export. Rendering does not check repository reachability or cluster health.
`, cfg.Metadata.Name, cfg.Spec.GitOps.RepoURL, cfg.Spec.GitOps.Revision, root, ArgoVersion))
	files["profiles/minimal/readme.md"] = []byte("# Minimal profile\n\nThe initial profile selects only components/argocd. It does not promise unimplemented observability, DNS automation, storage, AI or data services. Component additions and dependency resolution are separate tracked issues.\n")
	return files, nil
}

func kustomization(resources ...string) map[string]any {
	return map[string]any{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": resources}
}

func application(cfg config.Config, component, sourcePath, wave string) map[string]any {
	result := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{
			"name": ApplicationName(cfg.Metadata.Name, component), "namespace": "argocd",
			"annotations": map[string]string{"argocd.argoproj.io/sync-wave": wave, "bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": component},
		},
		"spec": map[string]any{
			"project":     "default",
			"source":      map[string]string{"repoURL": cfg.Spec.GitOps.RepoURL, "targetRevision": cfg.Spec.GitOps.Revision, "path": sourcePath},
			"destination": map[string]string{"server": "https://kubernetes.default.svc", "namespace": "argocd"},
			"syncPolicy": map[string]any{
				"automated":   map[string]bool{"prune": false, "selfHeal": false, "allowEmpty": false},
				"syncOptions": []string{"ServerSideApply=true", "FailOnSharedResource=true", "DisableClientSideApplyMigration=true"},
				"retry":       map[string]any{"limit": 5, "backoff": map[string]string{"duration": "5s", "maxDuration": "1m"}},
			},
		},
	}
	if component == "argocd" {
		// The nonce is controller provenance, not Git desired state. Ignoring
		// only this annotation lets Argo self-manage without erasing the UID-
		// bound bootstrap receipt's ownership marker.
		result["spec"].(map[string]any)["ignoreDifferences"] = []map[string]any{{
			"group": "*", "kind": "*", "jsonPointers": []string{"/metadata/annotations/bareplane.io~1installation"},
		}}
		policy := result["spec"].(map[string]any)["syncPolicy"].(map[string]any)
		policy["syncOptions"] = append(policy["syncOptions"].([]string), "RespectIgnoreDifferences=true")
	}
	return result
}
