package bootstrapapply

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestGitOpsProgressIsCanonicalPrivateAndRejectsUnmanagedState(t *testing.T) {
	f := newArgoFixture(t)
	_, bootstrapContract, err := verifyBundle(f.path, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	record := GitOpsProgress{Version: 1, Cluster: "lab", BootstrapContract: bootstrapContract, Trust: strings.Repeat("a", 64), Contract: strings.Repeat("b", 64), Stage: "installing"}
	if err := saveGitOpsProgress(f.path, record); err != nil {
		t.Fatal(err)
	}
	state, _ := project.BootstrapStateDirFor(f.path)
	path := filepath.Join(state, GitOpsProgressFilename)
	canonical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{[]byte("unmanaged"), append(append([]byte(nil), canonical...), '\n'), []byte(`{"version":1,"version":1}`), []byte(strings.Repeat("x", 4097))} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadGitOpsProgress(f.path); err == nil {
			t.Fatal("unmanaged progress accepted")
		}
		if err := saveGitOpsProgress(f.path, record); err == nil {
			t.Fatal("unmanaged state overwritten")
		}
	}
	if err := os.WriteFile(path, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadGitOpsProgress(f.path); err == nil {
			t.Fatal("public progress accepted")
		}
	}
}
