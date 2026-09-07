package bootstrapapply

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dipeshbabu/bareplane/internal/project"
)

const GitOpsProgressFilename = "gitops.json"

// GitOpsProgress is separate from the immutable eight-phase bootstrap prefix.
// It records local installation status, never repository or cluster credentials.
type GitOpsProgress struct {
	Version           int    `json:"version"`
	Cluster           string `json:"cluster"`
	BootstrapContract string `json:"bootstrapContractSHA256"`
	Trust             string `json:"trustSHA256"`
	Contract          string `json:"gitopsContractSHA256"`
	HandoffContract   string `json:"handoffContractSHA256,omitempty"`
	Stage             string `json:"stage"`
	Log               string `json:"log,omitempty"`
}

func encodeGitOpsProgress(record GitOpsProgress) ([]byte, error) {
	handoff := record.Stage == "handing-off" || record.Stage == "gitops-handed-off"
	if record.Version != 1 || record.Cluster == "" || !validDigest(record.BootstrapContract) || !validDigest(record.Trust) || !validDigest(record.Contract) || (record.Stage != "installing" && record.Stage != "argocd-ready" && !handoff) {
		return nil, errors.New("invalid GitOps progress record")
	}
	if (handoff && !validDigest(record.HandoffContract)) || (!handoff && record.HandoffContract != "") {
		return nil, errors.New("invalid GitOps handoff contract")
	}
	if record.Log != "" && (filepath.Base(record.Log) != record.Log || filepath.Ext(record.Log) != ".log") {
		return nil, errors.New("invalid GitOps progress log reference")
	}
	data, err := json.MarshalIndent(record, "", "  ")
	return append(data, '\n'), err
}

func ReadGitOpsProgress(configPath string) (GitOpsProgress, bool, error) {
	directory, err := project.InspectBootstrapState(configPath)
	if err != nil {
		return GitOpsProgress{}, false, err
	}
	path := filepath.Join(directory, GitOpsProgressFilename)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return GitOpsProgress{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return GitOpsProgress{}, false, errors.New("GitOps progress must be a bounded, private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return GitOpsProgress{}, false, errors.New("GitOps progress is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return GitOpsProgress{}, false, errors.New("GitOps progress is unreadable or oversized")
	}
	var record GitOpsProgress
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil {
		return GitOpsProgress{}, false, errors.New("GitOps progress is malformed")
	}
	canonical, err := encodeGitOpsProgress(record)
	if err != nil || !bytes.Equal(canonical, data) {
		return GitOpsProgress{}, false, errors.New("GitOps progress is not a canonical owned record")
	}
	return record, true, nil
}

func saveGitOpsProgress(configPath string, record GitOpsProgress) error {
	directory, err := project.InspectBootstrapState(configPath)
	if err != nil {
		return err
	}
	data, err := encodeGitOpsProgress(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".gitops-stage-")
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
	if _, _, err := ReadGitOpsProgress(configPath); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, GitOpsProgressFilename))
}
