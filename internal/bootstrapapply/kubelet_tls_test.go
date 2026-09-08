package bootstrapapply

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestServingTLSRequiresCompletedBootstrapAndCannotMixRecovery(t *testing.T) {
	f := newFixture(t)
	f.options.KubeletServingTLS = true
	f.options.Runner = func(context.Context, Request) error { t.Fatal("TLS replayed unfinished bootstrap"); return nil }
	if err := Apply(context.Background(), f.options); err == nil {
		t.Fatal("unfinished bootstrap accepted")
	}
	f.options.RecoverCredentials = true
	if err := Apply(context.Background(), f.options); err == nil {
		t.Fatal("mixed recovery accepted")
	}
}

func TestServingTLSUsesOneClosedMaintenancePlayAndResumesWithoutKubeadm(t *testing.T) {
	f := initializedResetFixture(t)
	f.options.KubeletServingTLS = true
	failed := true
	var calls []string
	f.options.Runner = func(_ context.Context, request Request) error {
		if request.Phase != "health" || !request.KubeletServingTLS {
			t.Fatal("serving TLS replayed a formation phase")
		}
		args, err := phaseArguments(request)
		if err != nil || args[len(args)-1] != "kubelet_tls.yaml" || !strings.Contains(strings.Join(args, " "), "bareplane_kubelet_tls_approved") {
			t.Fatalf("unsafe TLS runner arguments: %v %v", args, err)
		}
		calls = append(calls, request.Phase)
		if failed {
			return errors.New("interrupted")
		}
		return nil
	}
	if err := Apply(context.Background(), f.options); err == nil {
		t.Fatal("interrupted TLS accepted")
	}
	failed = false
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"health", "health"}) {
		t.Fatal("TLS retry replayed bootstrap")
	}
}

func TestServingTLSRunnerRejectsMixedOwnershipAndUnapprovedPhase(t *testing.T) {
	for _, request := range []Request{
		{Phase: "join", KubeletServingTLS: true},
		{Phase: "health", KubeletServingTLS: true, Argo: &ArgoRequest{}},
		{Phase: "health", KubeletServingTLS: true, RecoveryID: strings.Repeat("a", 32)},
		{Phase: "kubelet_tls"},
	} {
		if _, err := phaseArguments(request); err == nil {
			t.Fatal("unsafe TLS runner request accepted")
		}
	}
}
