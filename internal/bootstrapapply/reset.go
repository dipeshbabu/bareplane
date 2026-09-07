package bootstrapapply

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
)

const ResetFilename = "reset.json"

type ResetOptions struct {
	Options
	Scope              string
	ConfirmDestructive bool
}

type ResetRecord struct {
	Version int      `json:"version"`
	ID      string   `json:"id"`
	Stage   string   `json:"stage"`
	Source  Progress `json:"source"`
}

func validResetID(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == value
}

func encodeReset(record ResetRecord) ([]byte, error) {
	if record.Version != 1 || !validResetID(record.ID) || (record.Stage != "planned" && record.Stage != "validated") || record.Source.validate() != nil {
		return nil, errors.New("reset record is invalid")
	}
	data, err := json.MarshalIndent(record, "", "  ")
	return append(data, '\n'), err
}

func ReadReset(configPath string) (ResetRecord, bool, error) {
	directory, err := project.InspectBootstrapState(configPath)
	if err != nil {
		return ResetRecord{}, false, err
	}
	path := filepath.Join(directory, ResetFilename)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ResetRecord{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return ResetRecord{}, false, errors.New("reset state must be a bounded regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return ResetRecord{}, false, errors.New("reset state must be owner-only")
	}
	file, err := os.Open(path)
	if err != nil {
		return ResetRecord{}, false, errors.New("reset state is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return ResetRecord{}, false, errors.New("reset state is unreadable or oversized")
	}
	var record ResetRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return ResetRecord{}, false, errors.New("reset state is malformed")
	}
	canonical, err := encodeReset(record)
	if err != nil || !bytes.Equal(data, canonical) {
		return ResetRecord{}, false, errors.New("reset state is not canonical")
	}
	return record, true, nil
}

func saveReset(configPath string, record ResetRecord) error {
	directory, err := project.InspectBootstrapState(configPath)
	if err != nil {
		return err
	}
	data, err := encodeReset(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".reset-stage-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if _, _, err := ReadReset(configPath); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, ResetFilename))
}

func removeReset(configPath, id string) error {
	record, present, err := ReadReset(configPath)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if record.ID != id {
		return errors.New("reset ownership changed; refusing to remove its record")
	}
	directory, _ := project.BootstrapStateDirFor(configPath)
	return os.Remove(filepath.Join(directory, ResetFilename))
}

// Reset is deliberately limited to an abandoned bootstrap-only full cluster.
// Node-specific quorum operations and application/PV recovery remain lifecycle
// operations; the remote guard refuses them instead of guessing ownership.
func Reset(ctx context.Context, options ResetOptions) (returnErr error) {
	if ctx == nil {
		return errors.New("reset context is nil")
	}
	if options.Check {
		return errors.New("destructive reset cannot run in check mode; use bootstrap diagnose")
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	path, err := filepath.Abs(options.ConfigPath)
	if err != nil || !safeControllerPath(path) {
		return errors.New("invalid reset configuration path")
	}
	cfg, err := loadConfiguration(path)
	if err != nil {
		return err
	}
	if options.Approval != cfg.Metadata.Name {
		return ErrApprovalRequired
	}
	if !options.ConfirmDestructive || options.Scope != "cluster" {
		return errors.New("reset requires --scope cluster and --confirm-destructive; node-specific reset is not supported")
	}
	realRunner := options.Runner == nil
	if realRunner && runtime.GOOS != "linux" {
		return errors.New("reset must run on the original Linux/WSL controller")
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
		options.Runner = commandRunner(binary)
	}
	bundle, contract, err := verifyBundle(path, cfg)
	if err != nil {
		return err
	}
	trustPath, err := options.ResolveTrust(path)
	if err != nil {
		return err
	}
	expectedTrust, _ := sshtrust.KnownHostsPathFor(path)
	if trustPath != expectedTrust {
		return errors.New("reset requires project-scoped SSH trust")
	}
	trust, err := fileFingerprint(trustPath, sshtrust.MaximumKnownHostsSize)
	if err != nil {
		return err
	}
	lock, err := project.AcquireBootstrapOperation(path, "reset")
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	progress, exists, err := ReadProgress(path)
	if err != nil {
		return err
	}
	if !exists || progress.Completed < 2 || progress.Cluster != cfg.Metadata.Name || progress.Contract != contract || progress.Trust != trust {
		return errors.New("reset requires matching recorded bootstrap ownership after toolchain installation")
	}
	if options.Doctor(bootstrapdoctor.Options{ConfigPath: path, LookPath: options.LookPath, UserHomeDir: bootstrapdoctor.UserHomeDirFunc(options.HomeDir)}).HasFailures() {
		return errors.New("local doctor must pass before reset")
	}
	key, err := bootstrappreflight.ResolvePrivateKey(path, cfg.Spec.Bootstrap.SSH.PrivateKeyFile, options.HomeDir)
	if err != nil {
		return err
	}
	directory, _ := project.BootstrapStateDirFor(path)
	record, pending, err := ReadReset(path)
	if err != nil {
		return err
	}
	if pending && (record.Source.Cluster != cfg.Metadata.Name || record.Source.Contract != contract || record.Source.Trust != trust) {
		return errors.New("pending reset belongs to different configuration or SSH identities")
	}
	if !pending {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		record = ResetRecord{Version: 1, ID: hex.EncodeToString(nonce[:]), Stage: "planned", Source: progress}
		if err := saveReset(path, record); err != nil {
			return err
		}
	}
	request := Request{BundleDir: bundle, StateDir: directory, PrivateKeyFile: key, KnownHostsFile: trustPath, RecoveryID: record.ID}
	if realRunner {
		if err := validateControllerTools(ctx, request, cfg.Spec.Kubernetes.Version, options.LookPath); err != nil {
			return err
		}
	}
	for _, phase := range []string{"reset_validate", "reset_execute"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := loadConfiguration(path)
		if err != nil {
			return err
		}
		_, currentContract, err := verifyBundle(path, current)
		if err != nil || currentContract != contract {
			return errors.New("reset configuration or bundle changed")
		}
		currentTrust, err := fileFingerprint(trustPath, sshtrust.MaximumKnownHostsSize)
		if err != nil || currentTrust != trust {
			return errors.New("reset SSH identities changed")
		}
		observedReset, observedResetPresent, err := ReadReset(path)
		observedProgress, observedProgressPresent, progressErr := ReadProgress(path)
		if err != nil || progressErr != nil || !observedResetPresent || !observedProgressPresent || observedReset != record || observedProgress != progress {
			return errors.New("private reset or progress state changed during recovery")
		}
		request.Phase = phase
		request.AllowUnavailableAPI = record.Stage == "validated" || record.Source.Completed < 6
		options.Event(Event{phase, "CHECK", "full-cluster ownership, application-storage, and recovery guards"})
		log, logPath, err := createPhaseLog(directory, phase)
		if err != nil {
			return err
		}
		request.Log = log
		phaseCtx, cancel := context.WithTimeout(ctx, PhaseTimeout)
		err = options.Runner(phaseCtx, request)
		contextErr := phaseCtx.Err()
		cancel()
		closeErr := log.Close()
		if err != nil || contextErr != nil || closeErr != nil {
			if record.Stage == "planned" {
				_ = removeReset(path, record.ID)
			}
			return &PhaseError{Phase: phase, LogPath: logPath, Reason: "reset did not complete; use bootstrap diagnose before retry"}
		}
		current, err = loadConfiguration(path)
		if err != nil {
			return errors.New("configuration changed while reset ran")
		}
		_, finishedContract, bundleErr := verifyBundle(path, current)
		finishedTrust, trustErr := fileFingerprint(trustPath, sshtrust.MaximumKnownHostsSize)
		observedReset, observedResetPresent, err = ReadReset(path)
		observedProgress, observedProgressPresent, progressErr = ReadProgress(path)
		if bundleErr != nil || trustErr != nil || finishedContract != contract || finishedTrust != trust || err != nil || progressErr != nil || !observedResetPresent || !observedProgressPresent || observedReset != record || observedProgress != progress {
			return errors.New("reset inputs or ownership records changed; completion was not recorded")
		}
		if phase == "reset_validate" {
			record.Stage = "validated"
			if err := saveReset(path, record); err != nil {
				return err
			}
			archive := filepath.Join(directory, "recovery")
			if err := ensurePrivateDirectory(archive); err != nil {
				return err
			}
			archive = filepath.Join(archive, "reset-"+record.ID)
			if err := ensurePrivateDirectory(archive); err != nil {
				return err
			}
			data, _ := encodeReset(record)
			backup := filepath.Join(archive, "reset.json")
			file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err == nil {
				_, writeErr := file.Write(data)
				syncErr := file.Sync()
				closeErr := file.Close()
				if writeErr != nil || syncErr != nil || closeErr != nil {
					return errors.New("cannot preserve private reset metadata")
				}
			}
		}
		options.Event(Event{phase, "PASS", "completed"})
	}
	progress.Completed, progress.Active = 2, ""
	if err := saveProgress(path, progress); err != nil {
		return err
	}
	return removeReset(path, record.ID)
}
