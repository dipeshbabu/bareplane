package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func gitOpsFixture(t *testing.T) Config {
	t.Helper()
	cfg, err := Load(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.GitOps = &GitOpsConfig{RepoURL: "https://github.com/example/homelab.git", Revision: "main", RootPath: "clusters/lab"}
	return cfg
}

func TestGitOpsIsOptionalButRequiredForGitOpsOperations(t *testing.T) {
	cfg := gitOpsFixture(t)
	if err := cfg.ValidateGitOps(); err != nil {
		t.Fatal(err)
	}
	cfg.Spec.GitOps = nil
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateGitOps(); err == nil {
		t.Fatal("missing GitOps contract accepted")
	}
}

func TestGitOpsPublicHTTPSAndExplicitSafeRevisions(t *testing.T) {
	for _, repository := range []string{"https://github.com/owner/repo.git", "https://github.com/state/terraform.git", "https://gitlab.com/group/subgroup/repo", "https://git.example.com:8443/repo.git", "https://[2001:db8::1]/repo.git"} {
		cfg := gitOpsFixture(t)
		cfg.Spec.GitOps.RepoURL = repository
		for _, revision := range []string{"main", "release/v1", "refs/tags/v1.0.0", strings.Repeat("a", 40)} {
			cfg.Spec.GitOps.Revision = revision
			if err := cfg.ValidateGitOps(); err != nil {
				t.Fatalf("valid repository/revision rejected: %v", err)
			}
		}
	}
}

func TestGitOpsRejectsUnsafeURLsWithoutEchoingCredentials(t *testing.T) {
	for _, repository := range []string{"", "http://github.com/owner/repo", "ssh://git@github.com/owner/repo", "git@github.com:owner/repo", "file:///tmp/repo", "../repo",
		"https://user:PRIVATE-SECRET@github.com/owner/repo", "https://github.com/owner/repo?token=PRIVATE-SECRET", "https://github.com/owner/repo#ref", "https://github.com/owner/repo#", "https://github.com/owner/repo?",
		"https://github.com", "https://github.com/owner/../repo", "https://github.com/owner/%2e%2e/repo", "https://github.com/owner//repo", "https://github.com/owner/repo/",
		"https://localhost/repo", "https://127.0.0.1/repo", "https://169.254.169.254/repo", "https://git.example.com:0/repo", "https://git.example.com:/repo", "https://git.example.com:0443/repo", "https://github.com/.bareplane/repo"} {
		cfg := gitOpsFixture(t)
		cfg.Spec.GitOps.RepoURL = repository
		if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "PRIVATE-SECRET") {
			t.Fatalf("unsafe URL result: %v", err)
		}
	}
}

func TestGitOpsRejectsUnsafeRevisionsAndStatePaths(t *testing.T) {
	for _, revision := range []string{"", "--upload-pack=bad", "foo..bar", "branch.lock", "refs/.hidden", "refs//main", "branch/", "branch.", "@{1}", "main:secret", "main name", strings.Repeat("a", 201)} {
		cfg := gitOpsFixture(t)
		cfg.Spec.GitOps.Revision = revision
		if err := cfg.Validate(); err == nil {
			t.Fatalf("unsafe revision %q accepted", revision)
		}
	}
	for _, root := range []string{"", ".", "..", "/clusters/lab", "clusters/../lab", "clusters//lab", "clusters/lab/", ".bareplane", "clusters/.bareplane/apps", ".git", "state/apps", "terraform/manifests", `clusters\lab`, "clusters/CON", "clusters/nul", "clusters/lpt1.txt", "clusters/lab."} {
		cfg := gitOpsFixture(t)
		cfg.Spec.GitOps.RootPath = root
		if err := cfg.Validate(); err == nil {
			t.Fatalf("unsafe root %q accepted", root)
		}
	}
}

func TestGitOpsIdentityIsOptionalAndCredentialsAreNotSchemaFields(t *testing.T) {
	cfg := gitOpsFixture(t)
	cfg.Spec.GitOps.Identity = &GitOpsRepositoryIdentity{Owner: "group/subgroup", Name: "homelab"}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(strings.NewReader(string(data)))
	if err != nil || loaded.Spec.GitOps.Identity.Owner != "group/subgroup" {
		t.Fatalf("identity round trip failed: %v", err)
	}
	for _, field := range []string{"password", "token", "privateKey", "credentialsRef", "insecureSkipTLSVerify"} {
		input := strings.Replace(string(data), "gitops:\n", "gitops:\n        "+field+": PRIVATE-SECRET\n", 1)
		_, err := Load(strings.NewReader(input))
		if err == nil || !strings.Contains(err.Error(), "field "+field+" not found") || strings.Contains(err.Error(), "PRIVATE-SECRET") {
			t.Fatalf("credential field %s was accepted or exposed: %v", field, err)
		}
	}
	cfg.Spec.GitOps.Identity.Owner = "../other"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsafe identity accepted")
	}
}
