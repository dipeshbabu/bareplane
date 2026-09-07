package bootstrapapply

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func newHandoffFixture(t *testing.T) fixture {
	t.Helper()
	f := newArgoFixture(t)
	if err := InstallArgo(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestHandoffRequiresArgoReadyAndNeverRunsBootstrapPhases(t *testing.T) {
	f := newArgoFixture(t)
	f.options.Runner = func(context.Context, Request) error { t.Fatal("unready Argo reached handoff"); return nil }
	if err := HandoffGitOps(context.Background(), f.options); err == nil {
		t.Fatal("unready handoff accepted")
	}
	f = newHandoffFixture(t)
	before, _, _ := ReadProgress(f.path)
	f.options.Runner = func(ctx context.Context, request Request) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		record, exists, err := ReadGitOpsProgress(f.path)
		if err != nil || !exists || record.Stage != "handing-off" || record.HandoffContract != request.Argo.HandoffContract {
			t.Fatalf("missing handoff intent: %+v %v", record, err)
		}
		if request.Phase != "handoff" || !request.Argo.Handoff || request.Argo.PayloadDir != filepath.Join(request.StateDir, "handoff-input") {
			t.Fatalf("wrong phase: %+v", request)
		}
		for _, name := range []string{"bootstrap/lab-root-application.yaml", "clusters/lab/kustomization.yaml", "components/argocd/upstream.yaml"} {
			if _, err := os.Stat(filepath.Join(request.Argo.PayloadDir, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := project.AcquireBootstrapOperation(f.path, "reset"); !errors.Is(err, project.ErrBootstrapOperationLocked) {
			t.Fatal("handoff did not hold shared lock")
		}
		return nil
	}
	if err := HandoffGitOps(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	after, _, err := ReadProgress(f.path)
	if err != nil || before != after {
		t.Fatal("handoff changed bootstrap phase progress")
	}
	ready, err := RecordedReadiness(f.path)
	if err != nil || !ready.KubernetesReady || !ready.ArgoReady || !ready.HandedOff {
		t.Fatalf("bad readiness: %+v %v", ready, err)
	}
}

func TestCompletedHandoffCannotBeReclaimedOrDowngradedByFailedVerification(t *testing.T) {
	f := newHandoffFixture(t)
	if err := HandoffGitOps(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	f.options.Runner = func(context.Context, Request) error { t.Fatal("installer reclaimed handed-off Argo"); return nil }
	if err := InstallArgo(context.Background(), f.options); err == nil {
		t.Fatal("installer accepted handed-off state")
	}
	f.options.Runner = func(context.Context, Request) error { return errors.New("PRIVATE-SENTINEL") }
	if err := HandoffGitOps(context.Background(), f.options); err == nil || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
		t.Fatalf("unsafe verification result: %v", err)
	}
	record, _, err := ReadGitOpsProgress(f.path)
	if err != nil || record.Stage != "gitops-handed-off" {
		t.Fatalf("authority was downgraded: %+v %v", record, err)
	}
}

func TestHandoffFailureAndTamperedSnapshotNeverAdvanceReadiness(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		f := newHandoffFixture(t)
		f.options.Runner = func(_ context.Context, request Request) error {
			if tamper {
				return os.WriteFile(filepath.Join(request.Argo.PayloadDir, "bootstrap", "lab-root-application.yaml"), []byte("changed"), 0o600)
			}
			return errors.New("failed handoff")
		}
		if err := HandoffGitOps(context.Background(), f.options); err == nil {
			t.Fatal("invalid handoff succeeded")
		}
		record, _, err := ReadGitOpsProgress(f.path)
		if err != nil || record.Stage != "handing-off" {
			t.Fatalf("failed handoff advanced: %+v %v", record, err)
		}
	}
}

func TestHandoffRunnerUsesClosedPhaseAndBothInputContracts(t *testing.T) {
	request := Request{Phase: "handoff", StateDir: "/owned/state", BundleDir: "/owned/bootstrap", PrivateKeyFile: "/private/key",
		Argo: &ArgoRequest{Handoff: true, Contract: strings.Repeat("a", 64), HandoffContract: strings.Repeat("b", 64), PayloadDir: filepath.Join("/owned/state", "handoff-input")}}
	args, err := phaseArguments(request)
	if err != nil {
		t.Fatal(err)
	}
	var vars map[string]any
	if json.Unmarshal([]byte(args[len(args)-2]), &vars) != nil || vars["bareplane_handoff_approved"] != true || vars["bareplane_handoff_contract"] != request.Argo.HandoffContract {
		t.Fatalf("wrong args: %#v", args)
	}
	request.Phase = "argocd"
	if _, err := phaseArguments(request); err == nil {
		t.Fatal("handoff contract authorized installation")
	}
	request.Phase = "handoff"
	request.Argo.HandoffContract = ""
	if _, err := phaseArguments(request); err == nil {
		t.Fatal("missing snapshot contract accepted")
	}
}

func TestRecordedReadinessIsOfflineAndRefusesChangedTrust(t *testing.T) {
	f := newHandoffFixture(t)
	t.Setenv("PATH", "")
	ready, err := RecordedReadiness(f.path)
	if err != nil || !ready.KubernetesReady || !ready.ArgoReady || ready.HandedOff {
		t.Fatalf("bad recorded readiness: %+v %v", ready, err)
	}
	if err := os.WriteFile(f.trust, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready, err = RecordedReadiness(f.path)
	if err != nil || ready.KubernetesReady || ready.ArgoReady || ready.HandedOff || ready.Problem == "" {
		t.Fatalf("stale ownership accepted: %+v %v", ready, err)
	}
}
