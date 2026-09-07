package sshtrust

import (
	"context"
	"errors"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestTrustCommitCannotRaceAnActiveBootstrap(t *testing.T) {
	path := writeTrustConfig(t, 22, []string{"192.0.2.11"})
	plan, err := Prepare(context.Background(), Options{ConfigPath: path, Scan: singleKeyScan(1)})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := project.AcquireBootstrapOperation(path, "apply")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Commit(plan.Cluster); !errors.Is(err, project.ErrBootstrapOperationLocked) {
		t.Fatalf("trust changed during bootstrap: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Commit(plan.Cluster); err != nil {
		t.Fatal(err)
	}
}
