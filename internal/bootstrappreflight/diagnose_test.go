package bootstrappreflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/doctor"
)

func diagnosisFixture() string {
	return "BAREPLANE_DIAGNOSIS_V1\ninit_intent=false\ninit_complete=false\njoin_intent=false\njoin_complete=false\ncilium_intent=false\ncilium_complete=false\nreset_receipt=false\nkubelet_active=false\ninit_marker_digest=none\nca_record_match=none\n"
}

func TestDiagnosisSummariesRefuseForeignAndMalformedState(t *testing.T) {
	base := diagnosisFixture()
	result, err := summarizeDiagnosis([]byte(base), "lab")
	if err != nil || result.Status != doctor.StatusPass {
		t.Fatalf("fresh summary: %+v %v", result, err)
	}
	partial := strings.Replace(base, "join_intent=false", "join_intent=true", 1)
	result, err = summarizeDiagnosis([]byte(partial), "lab")
	if err != nil || result.Status != doctor.StatusWarn || !strings.Contains(result.Message, "join=incomplete") {
		t.Fatalf("partial summary: %+v %v", result, err)
	}
	digest := sha256.Sum256([]byte("foreign\n"))
	foreign := strings.Replace(base, "init_marker_digest=none", "init_marker_digest="+hex.EncodeToString(digest[:]), 1)
	result, err = summarizeDiagnosis([]byte(foreign), "lab")
	if err != nil || result.Status != doctor.StatusFail {
		t.Fatalf("foreign summary: %+v %v", result, err)
	}
	for _, data := range []string{"PRIVATE-SECRET", base + "extra=true\n", strings.Replace(base, "join_intent=false", "join_intent=PRIVATE-SECRET", 1), strings.Repeat("x", MaximumOutputSize+1)} {
		if _, err := summarizeDiagnosis([]byte(data), "lab"); err == nil || strings.Contains(err.Error(), "PRIVATE-SECRET") {
			t.Fatalf("unsafe parse error: %v", err)
		}
	}
}

func TestRemoteDiagnosisIsReadOnlyAndBounded(t *testing.T) {
	path, _ := writePreflightConfig(t)
	report := Diagnose(context.Background(), Options{ConfigPath: path,
		ResolveKnownHosts: func(string) (string, error) { return "/project/known_hosts", nil },
		Runner: func(ctx context.Context, request Request) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("diagnosis is not bounded")
			}
			return []byte(diagnosisFixture()), nil
		},
	})
	if report.HasFailures() {
		t.Fatalf("diagnosis failed: %+v", report)
	}
	for _, forbidden := range []string{"kubeadm reset", "kubeadm init", "kubeadm join", "systemctl stop", "systemctl restart", "rm -", "apt-get"} {
		if strings.Contains(remoteDiagnosisScript, forbidden) {
			t.Fatalf("diagnosis mutates hosts: %s", forbidden)
		}
	}
}
