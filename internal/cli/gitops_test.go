package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestGitOpsRenderCLIIsOfflineAndPreservesUserEdits(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.GitOps = &config.GitOpsConfig{RepoURL: "https://git.example.com/team/lab.git", Revision: "main", RootPath: "clusters/lab"}
	cfg.Spec.Features.Observability = false
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	t.Setenv("KUBECONFIG", filepath.Join(root, "must-not-open"))
	var stdout, stderr bytes.Buffer
	for i := 0; i < 2; i++ {
		if code := Run([]string{"gitops", "render", configPath}, &stdout, &stderr); code != 0 {
			t.Fatalf("%d: %s", code, stderr.String())
		}
	}
	if !strings.Contains(stdout.String(), "no commit, push, or cluster mutation") {
		t.Fatalf("unclear result: %s", stdout.String())
	}
	file := filepath.Join(root, "gitops", "clusters", "lab", "applications", "argocd.yaml")
	if err := os.WriteFile(file, []byte("user edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"gitops", "render", configPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("edited output accepted: %d", code)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "user edit" {
		t.Fatal("user content changed")
	}
}

func TestGitOpsUsageAndArgumentErrors(t *testing.T) {
	for _, args := range [][]string{{"gitops"}, {"gitops", "help"}, {"gitops", "--help"}} {
		var out, err bytes.Buffer
		if code := Run(args, &out, &err); code != 0 || !strings.Contains(out.String(), "gitops render [path]") {
			t.Fatalf("bad usage: %d %s", code, err.String())
		}
	}
	for _, args := range [][]string{{"gitops", "push"}, {"gitops", "render", "--force"}, {"gitops", "render", "a", "b"}} {
		var out, err bytes.Buffer
		if code := Run(args, &out, &err); code != 2 {
			t.Fatalf("bad arguments accepted: %d", code)
		}
	}
}
