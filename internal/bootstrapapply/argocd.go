package bootstrapapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	Repository      string
	Revision        string
	RootPath        string
	PayloadDir      string
	Contract        string
	Handoff         bool
	HandoffContract string
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

func handoffContract(cfg config.Config, files map[string][]byte, argo string) (string, map[string][]byte) {
	payload := make(map[string][]byte)
	names := make([]string, 0)
	for name, data := range files {
		if strings.HasPrefix(name, "components/") || strings.HasPrefix(name, cfg.Spec.GitOps.RootPath+"/") || name == "bootstrap/"+cfg.Metadata.Name+"-root-application.yaml" {
			payload[name] = data
			names = append(names, name)
		}
	}
	sort.Strings(names)
	hash := sha256.New()
	seed, _ := json.Marshal([]string{argo})
	hash.Write(seed)
	for _, name := range names {
		fmt.Fprintf(hash, "%d:%s:%d:", len(name), name, len(payload[name]))
		hash.Write(payload[name])
	}
	return hex.EncodeToString(hash.Sum(nil)), payload
}

func verifyGitOpsInput(directory, kind string, payload map[string][]byte) error {
	if err := project.RequireGeneratedDirectory(directory, kind); err != nil {
		return errors.New("private Argo input is not a managed directory")
	}
	directories := map[string]bool{".": true}
	for name := range payload {
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			directories[filepath.ToSlash(parent)] = true
		}
	}
	seen := 0
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("private GitOps input is redirected")
		}
		if entry.IsDir() {
			if !directories[relative] {
				return errors.New("private GitOps input contains an unknown directory")
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0) {
			return errors.New("private Argo input must contain owned regular files")
		}
		if relative == project.GeneratedMarkerFilename {
			return nil
		}
		expected, ok := payload[relative]
		if !ok || info.Size() != int64(len(expected)) {
			return errors.New("private Argo input changed")
		}
		digest := sha256.Sum256(expected)
		actual, err := fileFingerprint(path, int64(len(expected)))
		if err != nil || actual != hex.EncodeToString(digest[:]) {
			return errors.New("private Argo input content changed")
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(payload) {
		return errors.New("private GitOps input is incomplete")
	}
	return nil
}

// InstallArgo does not bootstrap an unfinished cluster or advance its phase
// prefix. The owned argocd playbook runs a fresh full health gate under the same
// operation lock before it can install the reviewed minimal control plane.
func InstallArgo(ctx context.Context, options Options) error {
	return runGitOps(ctx, options, false)
}

func HandoffGitOps(ctx context.Context, options Options) error {
	return runGitOps(ctx, options, true)
}

func runGitOps(ctx context.Context, options Options, handoff bool) (returnErr error) {
	if ctx == nil || options.Check || options.RecoverCredentials || options.KubeletServingTLS {
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
	phase, operation, kind := "argocd", "argocd-install", "argocd-input"
	started, completed := "installing", "argocd-ready"
	fullContract := ""
	if handoff {
		phase, operation, kind = "handoff", "gitops-handoff", "handoff-input"
		started, completed = "handing-off", "gitops-handed-off"
		fullContract, payload = handoffContract(cfg, files, contract)
	}
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
	lock, err := project.AcquireBootstrapOperation(path, operation)
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
	request := Request{Phase: phase, BundleDir: bundle, StateDir: directory, PrivateKeyFile: key, KnownHostsFile: trustPath,
		Argo: &ArgoRequest{Repository: cfg.Spec.GitOps.RepoURL, Revision: cfg.Spec.GitOps.Revision, RootPath: cfg.Spec.GitOps.RootPath,
			PayloadDir: filepath.Join(directory, kind), Contract: contract, Handoff: handoff, HandoffContract: fullContract}}
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
	if handoff && (!present || record.Stage == "installing" || record.Contract != contract) {
		return errors.New("matching verified Argo readiness is required before root handoff")
	}
	if !handoff && present && (record.Stage == "handing-off" || record.Stage == "gitops-handed-off") {
		return errors.New("root handoff already started; Argo installation cannot reclaim GitOps-owned state")
	}
	if handoff && record.HandoffContract != "" && record.HandoffContract != fullContract {
		return errors.New("existing handoff intent binds a different payload; implicit replacement is refused")
	}
	if present && record.Contract != contract {
		// A failed read-only prerequisite may be corrected before the module
		// publishes any resource-creation intent. The module still requires all
		// target resources to be absent; deleting a receipt cannot grant adoption.
		_, receiptErr := os.Lstat(filepath.Join(directory, "argocd-ownership.json"))
		if handoff || record.Stage != "installing" || !errors.Is(receiptErr, os.ErrNotExist) {
			return errors.New("existing Argo creation intent binds a different GitOps contract; implicit upgrades are refused")
		}
	}
	if !present {
		record = GitOpsProgress{Version: 1, Cluster: cfg.Metadata.Name, BootstrapContract: bootstrapContract, Trust: trust, Contract: contract, Stage: "installing"}
	}
	alreadyHandedOff := record.Stage == "gitops-handed-off"
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
		if handoff {
			currentFullContract, _ := handoffContract(current, currentFiles, currentContract)
			if currentFullContract != fullContract {
				return errors.New("root handoff payload changed during execution")
			}
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
	if err := project.ReplaceGeneratedTree(request.Argo.PayloadDir, kind, payload); err != nil {
		return err
	}
	if err := verifyGitOpsInput(request.Argo.PayloadDir, kind, payload); err != nil {
		return err
	}
	log, logPath, err := createPhaseLog(directory, phase)
	if err != nil {
		return err
	}
	record.Stage, record.Log, record.Contract = started, filepath.Base(logPath), contract
	if handoff {
		record.HandoffContract = fullContract
	}
	if alreadyHandedOff {
		record.Stage = completed
	}
	if err := saveGitOpsProgress(path, record); err != nil {
		log.Close()
		return err
	}
	present = true
	request.Log = log
	options.Event(Event{phase, "CHECK", "fresh health, public Git contract, and owned GitOps prerequisites"})
	phaseCtx, cancel := context.WithTimeout(ctx, PhaseTimeout)
	err = options.Runner(phaseCtx, request)
	contextErr := phaseCtx.Err()
	cancel()
	closeErr := log.Close()
	if err != nil || contextErr != nil || closeErr != nil {
		return fmt.Errorf("GitOps %s failed or was interrupted; inspect %s privately; no new readiness was recorded and existing reconciliation is not rolled back", phase, logPath)
	}
	if err := verify(); err != nil {
		return err
	}
	if err := verifyGitOpsInput(request.Argo.PayloadDir, kind, payload); err != nil {
		return err
	}
	record.Stage = completed
	if err := saveGitOpsProgress(path, record); err != nil {
		return err
	}
	message := "minimal pinned Argo is ready; root Application handoff has not been performed"
	if handoff {
		message = "root and child reconciliation verified; Argo owns long-lived platform state"
	}
	options.Event(Event{phase, "PASS", message})
	return nil
}
