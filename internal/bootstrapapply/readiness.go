package bootstrapapply

import (
	"os"
	"runtime"

	gitopsrender "github.com/dipeshbabu/bareplane/internal/render/gitops"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
)

// Readiness reports applicable local success records, not live cluster health.
type Readiness struct {
	BootstrapPresent bool
	KubernetesReady  bool
	ArgoReady        bool
	HandedOff        bool
	Stage            string
	Problem          string
}

func RecordedReadiness(configPath string) (Readiness, error) {
	result := Readiness{}
	progress, present, err := ReadProgress(configPath)
	if err != nil {
		return result, err
	}
	gitops, gitopsPresent, err := ReadGitOpsProgress(configPath)
	if err != nil {
		return result, err
	}
	result.BootstrapPresent = present
	if !present {
		if gitopsPresent {
			result.Problem = "GitOps progress has no matching bootstrap record"
		}
		return result, nil
	}
	result.Stage = "bootstrap-in-progress"
	if _, pending, err := ReadReset(configPath); err != nil {
		return result, err
	} else if pending {
		result.Problem = "an explicit bootstrap reset is pending"
		return result, nil
	}
	cfg, err := loadConfiguration(configPath)
	if err != nil {
		result.Problem = "bootstrap configuration no longer matches its recorded contract"
		return result, nil
	}
	_, contract, err := verifyBundle(configPath, cfg)
	if err != nil {
		result.Problem = "bootstrap render is missing, stale, or modified"
		return result, nil
	}
	trustPath, err := sshtrust.KnownHostsPathFor(configPath)
	if err != nil {
		return result, err
	}
	info, err := os.Lstat(trustPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		result.Problem = "recorded bootstrap trust is missing or unsafe"
		return result, nil
	}
	trust, err := fileFingerprint(trustPath, sshtrust.MaximumKnownHostsSize)
	if err != nil || contract != progress.Contract || trust != progress.Trust || cfg.Metadata.Name != progress.Cluster {
		result.Problem = "bootstrap configuration or trust differs from recorded ownership"
		return result, nil
	}
	result.KubernetesReady = progress.Completed == len(phases) && progress.Active == ""
	if result.KubernetesReady {
		result.Stage = "kubernetes-ready"
	}
	if !gitopsPresent {
		return result, nil
	}
	files, err := gitopsrender.Render(cfg)
	if err != nil {
		result.Problem = "GitOps configuration no longer matches recorded ownership"
		return result, nil
	}
	argo, _ := argoContract(cfg, files)
	if gitops.Cluster != progress.Cluster || gitops.BootstrapContract != contract || gitops.Trust != trust || gitops.Contract != argo {
		result.Problem = "GitOps inputs differ from the recorded installation"
		return result, nil
	}
	if gitops.HandoffContract != "" {
		current, _ := handoffContract(cfg, files, argo)
		if current != gitops.HandoffContract {
			result.Problem = "handoff payload differs from the recorded contract"
			return result, nil
		}
	}
	result.Stage = gitops.Stage
	result.ArgoReady = result.KubernetesReady && gitops.Stage != "installing"
	result.HandedOff = result.ArgoReady && gitops.Stage == "gitops-handed-off"
	return result, nil
}
