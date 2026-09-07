package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
)

func TestGitOpsInstallRequiresClosedApprovalContract(t *testing.T) {
	for _, args := range [][]string{nil, {"--approve"}, {"--check"}, {"--force", "PRIVATE-SENTINEL"}, {"--approve", "lab", "a", "b"}} {
		var stdout, stderr bytes.Buffer
		if code := runGitOpsInstall(args, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "PRIVATE-SENTINEL") {
			t.Fatalf("bad argument handling: %d %s", code, stderr.String())
		}
	}
}

func TestGitOpsHandoffRequiresClosedApprovalContract(t *testing.T) {
	for _, args := range [][]string{nil, {"--approve"}, {"--check"}, {"--force", "PRIVATE-SENTINEL"}, {"--approve", "lab", "a", "b"}} {
		var stdout, stderr bytes.Buffer
		if code := runGitOpsHandoff(args, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "PRIVATE-SENTINEL") || !strings.Contains(stderr.String(), "gitops handoff") {
			t.Fatalf("bad handoff argument handling: %d %s", code, stderr.String())
		}
	}
}

func TestGitOpsInstallRefusesApprovalMismatchAndUnfinishedBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	writeBootstrapRenderConfig(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A complete public GitOps contract still must not implicitly bootstrap hosts.
	data = bytes.ReplaceAll(data, []byte("observability: true"), []byte("observability: false"))
	data = bytes.Replace(data, []byte("  features:"), []byte("  gitops:\n    repoURL: https://git.example.com/team/lab.git\n    revision: main\n    rootPath: clusters/lab\n  features:"), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, approval := range []string{"other", "lab"} {
		var stdout, stderr bytes.Buffer
		options := bootstrapapply.Options{Runner: func(context.Context, bootstrapapply.Request) error {
			t.Fatal("unfinished bootstrap reached installer")
			return nil
		}}
		if code := runGitOpsInstall([]string{"--approve", approval, path}, &stdout, &stderr, options); code != 1 {
			t.Fatalf("unsafe installation accepted: %d %s", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("failed operation reported progress: %s", stdout.String())
		}
	}
}
