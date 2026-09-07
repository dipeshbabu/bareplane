package bootstrapapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	gitopsrender "github.com/dipeshbabu/bareplane/internal/render/gitops"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
)

type ArgoRequest struct {
	Repository string
	Revision   string
	RootPath   string
	PayloadDir string
	Contract   string
}

func argoContract(cfg config.Config, files map[string][]byte) (string, map[string][]byte) {
	payload := make(map[string][]byte)
	names := make([]string, 0)
	for name, data := range files {
		if relative, ok := strings.CutPrefix(name, "components/argocd/"); ok {
			payload[relative] = data
			names = append(names, relative)
		}
	}
	sort.Strings(names)
	hash := sha256.New()
	contract, _ := json.Marshal([]string{cfg.Metadata.Name, cfg.Spec.GitOps.RepoURL, cfg.Spec.GitOps.Revision, cfg.Spec.GitOps.RootPath, gitopsrender.ArgoVersion})
	hash.Write(contract)
	for _, name := range names {
		fmt.Fprintf(hash, "%d:%s:%d:", len(name), name, len(payload[name]))
		hash.Write(payload[name])
	}
	return hex.EncodeToString(hash.Sum(nil)), payload
}

func verifyArgoInput(directory string, payload map[string][]byte) error {
	if err := project.RequireGeneratedDirectory(directory, "argocd-input"); err != nil {
		return errors.New("private Argo input is not a managed directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(payload)+1 {
		return errors.New("private Argo input file set changed")
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0) {
			return errors.New("private Argo input must contain owned regular files")
		}
		if entry.Name() == project.GeneratedMarkerFilename {
			continue
		}
		expected, ok := payload[entry.Name()]
		if !ok || info.Size() != int64(len(expected)) {
			return errors.New("private Argo input changed")
		}
		digest := sha256.Sum256(expected)
		actual, err := fileFingerprint(filepath.Join(directory, entry.Name()), int64(len(expected)))
		if err != nil || actual != hex.EncodeToString(digest[:]) {
			return errors.New("private Argo input content changed")
		}
	}
	return nil
}

// InstallArgo does not bootstrap an unfinished cluster or advance its phase
// prefix. The owned argocd playbook runs a fresh full health gate under the same
// operation lock before it can install the reviewed minimal control plane.
func InstallArgo(ctx context.Context, options Options) (returnErr error) {
	if ctx == nil || options.Check || options.RecoverCredentials {
		return errors.New("Argo installation requires a real operation context; check mode and credential recovery are unsupported")
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	path, err := filepath.Abs(options.ConfigPath)
	if err != nil || !safeControllerPath(path) {
		return errors.New("invalid GitOps configuration path")
	}
	cfg, err := loadConfiguration(path)
	if err != nil {
		return err
	}
	if options.Approval != cfg.Metadata.Name {
		return ErrApprovalRequired
	}
	files, err := gitopsrender.Render(cfg)
	if err != nil {
		return err
	}
	contract, payload := argoContract(cfg, files)
	realRunner := options.Runner == nil
	if realRunner && runtime.GOOS != "linux" {
		return errors.New("Argo installation must run on the original Linux/WSL bootstrap controller")
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.HomeDir == nil {
		options.HomeDir = os.UserHomeDir
	}
	if options.Doctor == nil {
		options.Doctor = bootstrapdoctor.Inspect
	}
	if options.ResolveTrust == nil {
		options.ResolveTrust = sshtrust.RequireKnownHosts
	}
	if options.Event == nil {
		options.Event = func(Event) {}
	}
	if realRunner {
		binary, err := options.LookPath("ansible-playbook")
		if err != nil {
			return errors.New("ansible-playbook is required")
		}
		if _, err := options.LookPath("git"); err != nil {
			return errors.New("Git is required for anonymous repository verification")
		}
		options.Runner = commandRunner(binary)
	}
	lock, err := project.AcquireBootstrapOperation(path, "argocd-install")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Release()) }()
	bundle, bootstrapContract, err := verifyBundle(path, cfg)
	if err != nil {
		return err
	}
	trustPath, err := options.ResolveTrust(path)
	if err != nil {
		return err
	}
	expectedTrust, err := sshtrust.KnownHostsPathFor(path)
	if err != nil || trustPath != expectedTrust {
		return errors.New("Argo installation requires project-scoped bootstrap trust")
	}
	trust, err := fileFingerprint(trustPath, sshtrust.MaximumKnownHostsSize)
	if err != nil {
		return err
	}
	progress, exists, err := ReadProgress(path)
	if err != nil {
		return err
	}
	if !exists || progress.Completed != len(phases) || progress.Active != "" || progress.Cluster != cfg.Metadata.Name || progress.Contract != bootstrapContract || progress.Trust != trust {
		return errors.New("complete matching Kubernetes bootstrap and health are required; run bootstrap apply first")
	}
	if _, pending, err := ReadReset(path); err != nil || pending {
		return errors.New("pending or invalid reset blocks Argo installation")
	}
	if _, err := project.RequireGitOpsExport(path, cfg.Metadata.Name, files); err != nil {
		return err
	}
	if options.Doctor(bootstrapdoctor.Options{ConfigPath: path, LookPath: options.LookPath, UserHomeDir: bootstrapdoctor.UserHomeDirFunc(options.HomeDir)}).HasFailures() {
		return errors.New("local bootstrap doctor must pass before Argo installation")
	}
	key, err := bootstrappreflight.ResolvePrivateKey(path, cfg.Spec.Bootstrap.SSH.PrivateKeyFile, options.HomeDir)
	if err != nil {
		return err
	}
	if !safeControllerPath(key) {
		return errors.New("unsupported controller private-key path")
	}
	directory, err := project.InspectBootstrapState(path)
	if err != nil {
		return err
	}
	request := Request{Phase: "argocd", BundleDir: bundle, StateDir: directory, PrivateKeyFile: key, KnownHostsFile: trustPath,
		Argo: &ArgoRequest{Repository: cfg.Spec.GitOps.RepoURL, Revision: cfg.Spec.GitOps.Revision, RootPath: cfg.Spec.GitOps.RootPath,
			PayloadDir: filepath.Join(directory, "argocd-input"), Contract: contract}}
	if realRunner {
		if err := validateControllerTools(ctx, request, cfg.Spec.Kubernetes.Version, options.LookPath); err != nil {
			return err
		}
	}
	record, present, err := ReadGitOpsProgress(path)
	if err != nil {
		return err
	}
	if present && (record.Cluster != cfg.Metadata.Name || record.BootstrapContract != bootstrapContract || record.Trust != trust) {
		return errors.New("existing GitOps installation belongs to different inputs; implicit adoption or upgrades are not supported")
	}
	if present && record.Contract != contract {
		// A failed read-only prerequisite may be corrected before the module
		// publishes any resource-creation intent. The module still requires all
		// target resources to be absent; deleting a receipt cannot grant adoption.
		_, receiptErr := os.Lstat(filepath.Join(directory, "argocd-ownership.json"))
		if record.Stage != "installing" || !errors.Is(receiptErr, os.ErrNotExist) {
			return errors.New("existing Argo creation intent binds a different GitOps contract; implicit upgrades are refused")
		}
	}
	if !present {
		record = GitOpsProgress{Version: 1, Cluster: cfg.Metadata.Name, BootstrapContract: bootstrapContract, Trust: trust, Contract: contract, Stage: "installing"}
	}
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := loadConfiguration(path)
		if err != nil {
			return err
		}
		_, currentBootstrap, err := verifyBundle(path, current)
		if err != nil || currentBootstrap != bootstrapContract {
			return errors.New("bootstrap inputs changed during Argo installation")
		}
		currentFiles, err := gitopsrender.Render(current)
		if err != nil {
			return err
		}
		currentContract, _ := argoContract(current, currentFiles)
		if currentContract != contract {
			return errors.New("GitOps repository contract or payload changed during installation")
		}
		if _, err := project.RequireGitOpsExport(path, cfg.Metadata.Name, currentFiles); err != nil {
			return err
		}
		currentTrustPath, err := options.ResolveTrust(path)
		if err != nil || currentTrustPath != trustPath {
			return errors.New("SSH trust changed during Argo installation")
		}
		currentTrust, err := fileFingerprint(trustPath, sshtrust.MaximumKnownHostsSize)
		if err != nil || currentTrust != trust {
			return errors.New("SSH identities changed during Argo installation")
		}
		observed, observedExists, err := ReadProgress(path)
		if err != nil || !observedExists || observed != progress {
			return errors.New("bootstrap progress changed during Argo installation")
		}
		if _, pending, err := ReadReset(path); err != nil || pending {
			return errors.New("reset state changed during Argo installation")
		}
		observedGitOps, observedPresent, err := ReadGitOpsProgress(path)
		if err != nil || observedPresent != present || (present && observedGitOps != record) {
			return errors.New("GitOps progress changed during installation")
		}
		return nil
	}
	if err := verify(); err != nil {
		return err
	}
	if err := project.ReplaceGeneratedTree(request.Argo.PayloadDir, "argocd-input", payload); err != nil {
		return err
	}
	if err := verifyArgoInput(request.Argo.PayloadDir, payload); err != nil {
		return err
	}
	log, logPath, err := createPhaseLog(directory, "argocd")
	if err != nil {
		return err
	}
	record.Stage, record.Log, record.Contract = "installing", filepath.Base(logPath), contract
	if err := saveGitOpsProgress(path, record); err != nil {
		log.Close()
		return err
	}
	present = true
	request.Log = log
	options.Event(Event{"argocd", "CHECK", "fresh Kubernetes health, anonymous Git reachability, and minimal Argo ownership"})
	phaseCtx, cancel := context.WithTimeout(ctx, PhaseTimeout)
	err = options.Runner(phaseCtx, request)
	contextErr := phaseCtx.Err()
	cancel()
	closeErr := log.Close()
	if err != nil || contextErr != nil || closeErr != nil {
		return fmt.Errorf("Argo installation failed or was interrupted; inspect %s privately; readiness was not recorded", logPath)
	}
	if err := verify(); err != nil {
		return err
	}
	if err := verifyArgoInput(request.Argo.PayloadDir, payload); err != nil {
		return err
	}
	record.Stage = "argocd-ready"
	if err := saveGitOpsProgress(path, record); err != nil {
		return err
	}
	options.Event(Event{"argocd", "PASS", "minimal pinned Argo is ready; root Application handoff has not been performed"})
	return nil
}
