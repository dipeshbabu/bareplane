package bootstrapapply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
)

const PhaseTimeout = 45 * time.Minute

var (
	ErrApprovalRequired = errors.New("bootstrap approval must exactly match the configured cluster name")
	ErrCheckUnsupported = errors.New("bootstrap apply cannot safely model kubeadm or health workloads in check mode; use bootstrap doctor, bootstrap preflight, and validate.yaml")
	phases              = [...]string{"host_prepare", "kubernetes_install", "api_vip", "control_plane_init", "cilium", "join", "kubeconfig", "health"}
)

func Phases() []string { return append([]string(nil), phases[:]...) }

type Request struct {
	Phase               string
	BundleDir           string
	StateDir            string
	PrivateKeyFile      string
	KnownHostsFile      string
	Log                 io.Writer
	RecoveryID          string
	AllowUnavailableAPI bool
}

type Runner func(context.Context, Request) error
type Preflight func(context.Context, bootstrappreflight.Options) doctor.Report
type Event struct{ Phase, Status, Message string }

type Options struct {
	ConfigPath         string
	Approval           string
	Check              bool
	RecoverCredentials bool
	Runner             Runner
	Doctor             func(bootstrapdoctor.Options) doctor.Report
	Preflight          Preflight
	ResolveTrust       func(string) (string, error)
	LookPath           bootstrapdoctor.LookPathFunc
	HomeDir            bootstrappreflight.UserHomeDirFunc
	Event              func(Event)
}

type PhaseError struct{ Phase, LogPath, Reason string }

func (e *PhaseError) Error() string {
	message := fmt.Sprintf("bootstrap phase %s failed or was interrupted; inspect %s privately; incomplete kubeadm state requires explicit recovery before retry", e.Phase, e.LogPath)
	if e.Reason != "" {
		message += "; " + e.Reason
	}
	return message
}

func Apply(ctx context.Context, options Options) (returnErr error) {
	if ctx == nil {
		return errors.New("bootstrap context is nil")
	}
	if options.Check {
		return ErrCheckUnsupported
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	path, err := filepath.Abs(options.ConfigPath)
	if err != nil || !safeControllerPath(path) {
		return errors.New("invalid bootstrap configuration path")
	}
	options.ConfigPath = path
	cfg, err := loadConfiguration(path)
	if err != nil {
		return err
	}
	if options.Approval != cfg.Metadata.Name {
		return ErrApprovalRequired
	}
	if options.Runner == nil && runtime.GOOS != "linux" {
		return errors.New("bootstrap apply must run on a Linux or WSL controller")
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
	if options.Preflight == nil {
		options.Preflight = bootstrappreflight.Inspect
	}
	if options.ResolveTrust == nil {
		options.ResolveTrust = sshtrust.RequireKnownHosts
	}
	if options.Event == nil {
		options.Event = func(Event) {}
	}
	realRunner := options.Runner == nil
	if options.Runner == nil {
		binary, err := options.LookPath("ansible-playbook")
		if err != nil {
			return errors.New("ansible-playbook is required")
		}
		options.Runner = commandRunner(binary)
	}
	bundle, contract, err := verifyBundle(path, cfg)
	if err != nil {
		return err
	}
	knownHosts, err := options.ResolveTrust(path)
	if err != nil {
		return err
	}
	expectedTrust, err := sshtrust.KnownHostsPathFor(path)
	if err != nil || filepath.Clean(knownHosts) != expectedTrust {
		return errors.New("bootstrap requires the project-scoped SSH trust file")
	}
	trust, err := fileFingerprint(knownHosts, sshtrust.MaximumKnownHostsSize)
	if err != nil {
		return err
	}
	lock, err := project.AcquireBootstrapOperation(path, "apply")
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	state, exists, err := ReadProgress(path)
	if err != nil {
		return err
	}
	if _, pending, err := ReadReset(path); err != nil {
		return err
	} else if pending {
		return errors.New("an explicit reset is pending; run bootstrap diagnose and finish that reset before apply")
	}
	if exists && (state.Cluster != cfg.Metadata.Name || state.Contract != contract || state.Trust != trust) {
		return errors.New("bootstrap progress belongs to different configuration or SSH identities; use explicit recovery, not implicit adoption")
	}
	if !exists {
		state = Progress{Version: 1, Cluster: cfg.Metadata.Name, Contract: contract, Trust: trust}
	}
	stateDir, err := project.InspectBootstrapState(path)
	if err != nil {
		return err
	}
	start := state.Completed
	if options.RecoverCredentials {
		if !exists || state.Completed < 6 {
			return errors.New("kubeconfig recovery requires recorded complete node formation")
		}
		start = 6
	} else if state.Active == "kubeconfig" && state.Completed >= 6 {
		start = 6
	}
	if start == len(phases) {
		start--
	}
	for index := start; index < len(phases); index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		phase := phases[index]
		options.Event(Event{phase, "CHECK", "current local, trust, bundle, and authenticated host prerequisites"})
		current, err := loadConfiguration(path)
		if err != nil {
			return err
		}
		currentBundle, currentContract, err := verifyBundle(path, current)
		if err != nil {
			return err
		}
		if currentBundle != bundle || currentContract != contract {
			return errors.New("bootstrap configuration changed during execution")
		}
		if options.Doctor(bootstrapdoctor.Options{ConfigPath: path, LookPath: options.LookPath, UserHomeDir: bootstrapdoctor.UserHomeDirFunc(options.HomeDir)}).HasFailures() {
			return errors.New("current local bootstrap doctor failed; run bareplane bootstrap doctor")
		}
		currentTrustPath, err := options.ResolveTrust(path)
		if err != nil {
			return err
		}
		if currentTrustPath != knownHosts {
			return errors.New("bootstrap SSH trust path changed")
		}
		currentTrust, err := fileFingerprint(currentTrustPath, sshtrust.MaximumKnownHostsSize)
		if err != nil || currentTrustPath != knownHosts || currentTrust != trust {
			return errors.New("trusted SSH identities changed during bootstrap; stop and review recovery")
		}
		key, err := bootstrappreflight.ResolvePrivateKey(path, current.Spec.Bootstrap.SSH.PrivateKeyFile, options.HomeDir)
		if err != nil {
			return err
		}
		if !safeControllerPath(key) {
			return errors.New("private-key path contains an unsupported OpenSSH expansion")
		}
		request := Request{Phase: phase, BundleDir: bundle, StateDir: stateDir, PrivateKeyFile: key, KnownHostsFile: knownHosts}
		if realRunner {
			if err := validateControllerTools(ctx, request, current.Spec.Kubernetes.Version, options.LookPath); err != nil {
				return err
			}
		}
		report := options.Preflight(ctx, bootstrappreflight.Options{ConfigPath: path, UserHomeDir: options.HomeDir,
			ResolveKnownHosts: options.ResolveTrust, Preparation: index == 0, Resume: exists})
		for _, result := range report.Results {
			if result.Status != doctor.StatusPass {
				options.Event(Event{phase, string(result.Status), result.Name + ": " + result.Message})
			}
		}
		if report.HasFailures() {
			return errors.New("authenticated bootstrap preflight failed; inspect reported host prerequisites")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		latest, err := loadConfiguration(path)
		if err != nil {
			return err
		}
		_, latestContract, err := verifyBundle(path, latest)
		if err != nil || latestContract != contract {
			return errors.New("bootstrap inputs changed during authenticated preflight; stop and rerender")
		}
		latestTrust, err := fileFingerprint(knownHosts, sshtrust.MaximumKnownHostsSize)
		if err != nil || latestTrust != trust {
			return errors.New("SSH identities changed during authenticated preflight")
		}
		observed, observedExists, err := ReadProgress(path)
		if err != nil {
			return err
		}
		if observedExists != exists || (exists && observed != state) {
			return errors.New("bootstrap progress changed during execution")
		}
		log, logPath, err := createPhaseLog(stateDir, phase)
		if err != nil {
			return err
		}
		state.Active, state.Log = phase, filepath.Base(logPath)
		if err := saveProgress(path, state); err != nil {
			log.Close()
			return err
		}
		exists = true
		phaseCtx, cancel := context.WithTimeout(ctx, PhaseTimeout)
		request.Log = log
		err = options.Runner(phaseCtx, request)
		contextErr := phaseCtx.Err()
		cancel()
		closeErr := log.Close()
		if err != nil || contextErr != nil || closeErr != nil {
			return &PhaseError{Phase: phase, LogPath: logPath}
		}
		finishedConfig, err := loadConfiguration(path)
		if err != nil {
			return &PhaseError{Phase: phase, LogPath: logPath, Reason: "configuration changed while the phase ran; completion was not recorded"}
		}
		_, finishedContract, bundleErr := verifyBundle(path, finishedConfig)
		finishedTrust, trustErr := fileFingerprint(knownHosts, sshtrust.MaximumKnownHostsSize)
		if bundleErr != nil || finishedContract != contract || trustErr != nil || finishedTrust != trust {
			return &PhaseError{Phase: phase, LogPath: logPath, Reason: "configuration, bundle, or SSH identities changed while the phase ran; completion was not recorded"}
		}
		observed, _, err = ReadProgress(path)
		if err != nil || observed != state {
			return errors.New("bootstrap progress changed while a phase was running")
		}
		if index+1 > state.Completed {
			state.Completed = index + 1
		}
		state.Active = ""
		if err := saveProgress(path, state); err != nil {
			return fmt.Errorf("phase finished but its progress could not be recorded: %w", err)
		}
		options.Event(Event{phase, "PASS", "phase completed and progress recorded"})
	}
	return nil
}
