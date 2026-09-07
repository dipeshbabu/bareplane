package bootstrapapply

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCredentialRecoveryOnlyRunsKubeconfigAndHealth(t *testing.T) {
	f := initializedResetFixture(t)
	var calls []string
	f.options.RecoverCredentials = true
	f.options.Runner = func(_ context.Context, request Request) error { calls = append(calls, request.Phase); return nil }
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"kubeconfig", "health"}) {
		t.Fatalf("credential recovery reconfigured hosts: %v", calls)
	}
}

func TestInterruptedCredentialRecoveryCanResumeWithoutReset(t *testing.T) {
	f := initializedResetFixture(t)
	f.options.RecoverCredentials = true
	f.options.Runner = func(context.Context, Request) error { return errors.New("interrupted") }
	if err := Apply(context.Background(), f.options); err == nil {
		t.Fatal("fixture did not fail")
	}
	f.options.RecoverCredentials = false
	var calls []string
	f.options.Runner = func(_ context.Context, request Request) error { calls = append(calls, request.Phase); return nil }
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"kubeconfig", "health"}) {
		t.Fatalf("unsafe credential resume: %v", calls)
	}
}

func TestCredentialRecoveryRequiresRecordedClusterFormation(t *testing.T) {
	f := newFixture(t)
	f.options.RecoverCredentials = true
	f.options.Runner = func(context.Context, Request) error { t.Fatal("unformed cluster retrieved credentials"); return nil }
	if err := Apply(context.Background(), f.options); err == nil {
		t.Fatal("unformed cluster accepted")
	}
}
