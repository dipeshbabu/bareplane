package gitops

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestUpstreamAssetMatchesReviewedDigest(t *testing.T) {
	data, err := assets.ReadFile("assets/argocd/upstream.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if digest := fmt.Sprintf("%x", sha256.Sum256(data)); digest != "aa39fe0ced03e0dfba6ac87f1fedd074134dc6f2dc725b5d32e28858372b1f54" {
		t.Fatalf("vendored asset changed without reviewed regeneration: %s", digest)
	}
}

func fixture(t *testing.T) config.Config {
	t.Helper()
	data, err := os.ReadFile("../../../examples/bareplane.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Features = config.Features{}
	cfg.Spec.Profiles = []string{"minimal"}
	cfg.Spec.DNS.Provider = "manual"
	cfg.Spec.Secrets.Provider = "sops"
	return cfg
}

func TestRenderDeterministicIndependentPublicPayload(t *testing.T) {
	cfg := fixture(t)
	cfg.Spec.Provider.Endpoint = "https://PROVIDER-SECRET@example.com"
	cfg.Spec.Bootstrap.SSH.PrivateKeyFile = "/PRIVATE-SSH-KEY"
	first, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		next, err := Render(cfg)
		if err != nil || !reflect.DeepEqual(first, next) {
			t.Fatalf("nondeterministic output: %v", err)
		}
	}
	for name, data := range first {
		if strings.Contains(name, "cilium") || strings.HasPrefix(name, ".bareplane") || bytes.Contains(data, []byte("\r")) {
			t.Fatalf("unexpected file or line endings: %s", name)
		}
		for _, forbidden := range []string{"PROVIDER-SECRET", "PRIVATE-SSH-KEY", "client-key-data:", "client-certificate-data:", "BEGIN OPENSSH PRIVATE KEY", "terraform.tfstate", "BAREPLANE_CLUSTER_NAME"} {
			if bytes.Contains(data, []byte(forbidden)) {
				t.Fatalf("private input or unexpanded placeholder in %s", name)
			}
		}
	}
	first["components/argocd/upstream.yaml"][0] = '!'
	next, err := Render(cfg)
	if err != nil || next["components/argocd/upstream.yaml"][0] == '!' {
		t.Fatal("callers can mutate embedded assets")
	}
}

func TestRenderApplicationsUseOnlyUserRepositoryAndSeparateRoot(t *testing.T) {
	cfg := fixture(t)
	cfg.Spec.GitOps.RootPath = "environments/private/lab"
	cfg.Spec.GitOps.Revision = "release/v1"
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for filename, sourcePath := range map[string]string{
		"bootstrap/" + cfg.Metadata.Name + "-root-application.yaml":     cfg.Spec.GitOps.RootPath,
		path.Join(cfg.Spec.GitOps.RootPath, "applications/argocd.yaml"): "components/argocd",
	} {
		var app struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name        string
				Namespace   string
				Annotations map[string]string
				Finalizers  []string
			}
			Spec struct {
				Project string
				Source  struct {
					RepoURL        string `yaml:"repoURL"`
					TargetRevision string `yaml:"targetRevision"`
					Path           string
				}
				Destination struct {
					Server    string
					Namespace string
				}
				SyncPolicy struct {
					Automated struct {
						Prune      bool
						SelfHeal   bool `yaml:"selfHeal"`
						AllowEmpty bool `yaml:"allowEmpty"`
					}
					SyncOptions []string `yaml:"syncOptions"`
					Retry       struct {
						Limit   int
						Backoff map[string]string
					}
				} `yaml:"syncPolicy"`
			}
		}
		decoder := yaml.NewDecoder(bytes.NewReader(files[filename]))
		decoder.KnownFields(true)
		if err := decoder.Decode(&app); err != nil {
			t.Fatalf("invalid Application %s: %v", filename, err)
		}
		if app.Kind != "Application" || app.APIVersion != "argoproj.io/v1alpha1" || app.Metadata.Namespace != "argocd" || len(app.Metadata.Finalizers) != 0 || app.Spec.Project != "default" || app.Spec.Source.RepoURL != cfg.Spec.GitOps.RepoURL || app.Spec.Source.TargetRevision != cfg.Spec.GitOps.Revision || app.Spec.Source.Path != sourcePath || app.Spec.Destination.Server != "https://kubernetes.default.svc" || app.Spec.Destination.Namespace != "argocd" || app.Spec.SyncPolicy.Automated.Prune || app.Spec.SyncPolicy.Automated.SelfHeal || app.Spec.SyncPolicy.Automated.AllowEmpty {
			t.Fatalf("Application violated handoff contract: %s", filename)
		}
	}
	if _, exists := files[path.Join(cfg.Spec.GitOps.RootPath, "root-application.yaml")]; exists {
		t.Fatal("root application recursively owns itself")
	}
}

func TestRenderPinnedMinimalArgoResources(t *testing.T) {
	cfg := fixture(t)
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(files["components/argocd/upstream.yaml"]))
	seen := map[string]bool{}
	workloads := map[string]bool{}
	for {
		var obj map[string]any
		err := decoder.Decode(&obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kind := obj["kind"].(string)
		meta := obj["metadata"].(map[string]any)
		name := meta["name"].(string)
		key := kind + "/" + name
		if seen[key] {
			t.Fatalf("duplicate identity %s", key)
		}
		seen[key] = true
		for _, excluded := range []string{"cilium", "kube-vip", "dex-server", "notifications", "applicationset-controller", "image-updater"} {
			if strings.Contains(name, excluded) {
				t.Fatalf("unexpected component %s", key)
			}
		}
		if meta["annotations"].(map[string]any)["bareplane.io/cluster"] != cfg.Metadata.Name {
			t.Fatalf("missing ownership: %s", key)
		}
		if kind != "CustomResourceDefinition" && kind != "ClusterRole" && kind != "ClusterRoleBinding" && meta["namespace"] != "argocd" {
			t.Fatalf("wrong namespace: %s", key)
		}
		if kind == "Secret" && (obj["data"] != nil || obj["stringData"] != nil) {
			t.Fatal("secret values emitted")
		}
		if kind == "Service" && obj["spec"].(map[string]any)["type"] != nil && obj["spec"].(map[string]any)["type"] != "ClusterIP" {
			t.Fatal("public Argo exposure")
		}
		if kind == "Deployment" || kind == "StatefulSet" {
			workloads[name] = true
			spec := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			for _, field := range []string{"containers", "initContainers"} {
				containers, _ := spec[field].([]any)
				for _, item := range containers {
					image := item.(map[string]any)["image"]
					if image != "quay.io/argoproj/argocd:v"+ArgoVersion && image != "public.ecr.aws/docker/library/redis:8.2.3-alpine" {
						t.Fatalf("unpinned image %v", image)
					}
				}
			}
		}
	}
	if !reflect.DeepEqual(workloads, map[string]bool{"argocd-application-controller": true, "argocd-server": true, "argocd-repo-server": true, "argocd-redis": true}) {
		t.Fatalf("wrong minimal workload set: %v", workloads)
	}
	for _, key := range []string{"CustomResourceDefinition/applications.argoproj.io", "CustomResourceDefinition/appprojects.argoproj.io", "Secret/argocd-secret"} {
		if !seen[key] {
			t.Fatalf("missing %s", key)
		}
	}
}

func TestRenderRejectsUnsupportedProfilesAndOverlappingPaths(t *testing.T) {
	for _, profile := range []string{"ai", "data", "full"} {
		cfg := fixture(t)
		cfg.Spec.Profiles = []string{"minimal", profile}
		if files, err := Render(cfg); err == nil || files != nil {
			t.Fatalf("partial %s accepted", profile)
		}
	}
	for _, root := range []string{"bootstrap", "bootstrap/lab", "components", "components/argocd", "profiles/minimal", "readme.md", ".bareplane/state", "../escape", "/absolute"} {
		cfg := fixture(t)
		cfg.Spec.GitOps.RootPath = root
		if files, err := Render(cfg); err == nil || files != nil {
			t.Fatalf("unsafe root %s accepted", root)
		}
	}
	for _, change := range []func(*config.Config){
		func(c *config.Config) { c.Spec.Features.GPU = true },
		func(c *config.Config) { c.Spec.Features.Observability = true },
		func(c *config.Config) { c.Spec.DNS.Provider = "cloudflare" },
		func(c *config.Config) { c.Spec.Secrets.Provider = "vault" },
		func(c *config.Config) { c.Spec.GitOps = nil },
	} {
		cfg := fixture(t)
		change(&cfg)
		if files, err := Render(cfg); err == nil || files != nil {
			t.Fatal("unavailable contract accepted")
		}
	}
	cfg := fixture(t)
	cfg.Spec.Profiles = nil
	if _, err := Render(cfg); err != nil {
		t.Fatalf("default minimal rejected: %v", err)
	}
}

func TestLongApplicationNamesAreStableAndDistinct(t *testing.T) {
	a := ApplicationName(strings.Repeat("a", 63), "argocd")
	b := ApplicationName(strings.Repeat("a", 62)+"b", "argocd")
	if len(a) > 63 || len(b) > 63 || a == b || a != ApplicationName(strings.Repeat("a", 63), "argocd") {
		t.Fatalf("unsafe names %q %q", a, b)
	}
}
