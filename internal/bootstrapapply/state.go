package bootstrapapply

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dipeshbabu/bareplane/internal/project"
)

const ProgressFilename = "progress.json"

// Progress records only non-secret configuration/trust fingerprints and the
// completed phase prefix. Active remains set after failure or interruption.
type Progress struct {
	Version   int    `json:"version"`
	Cluster   string `json:"cluster"`
	Contract  string `json:"contractSHA256"`
	Trust     string `json:"trustSHA256"`
	Completed int    `json:"completed"`
	Active    string `json:"active"`
	Log       string `json:"log,omitempty"`
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func (p Progress) validate() error {
	if p.Version != 1 || p.Cluster == "" || !validDigest(p.Contract) || !validDigest(p.Trust) || p.Completed < 0 || p.Completed > len(phases) {
		return errors.New("bootstrap progress metadata is invalid")
	}
	if p.Active != "" {
		next := p.Completed
		if next == len(phases) {
			next-- // A completed cluster may re-run its health gate.
		}
		if p.Active != phases[next] && !(p.Completed >= 6 && p.Active == "kubeconfig") {
			return errors.New("bootstrap progress is not a valid phase prefix")
		}
	}
	if p.Log != "" && (filepath.Base(p.Log) != p.Log || filepath.Ext(p.Log) != ".log") {
		return errors.New("bootstrap progress log reference is invalid")
	}
	return nil
}

func encodeProgress(p Progress) ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	return append(data, '\n'), err
}

// ReadProgress is read-only and refuses ambiguous or redirected local state.
func ReadProgress(configPath string) (Progress, bool, error) {
	dir, err := project.InspectBootstrapState(configPath)
	if err != nil {
		return Progress{}, false, err
	}
	path := filepath.Join(dir, ProgressFilename)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Progress{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return Progress{}, false, errors.New("bootstrap progress must be a bounded regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Progress{}, false, errors.New("bootstrap progress must have owner-only permissions")
	}
	file, err := os.Open(path)
	if err != nil {
		return Progress{}, false, errors.New("bootstrap progress is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return Progress{}, false, errors.New("bootstrap progress could not be read safely")
	}
	var p Progress
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return Progress{}, false, errors.New("bootstrap progress is malformed or unmanaged")
	}
	canonical, err := encodeProgress(p)
	if err != nil || !bytes.Equal(data, canonical) {
		return Progress{}, false, errors.New("bootstrap progress is malformed or non-canonical; inspect recovery state")
	}
	return p, true, nil
}

func saveProgress(configPath string, p Progress) error {
	dir, err := project.InspectBootstrapState(configPath)
	if err != nil {
		return err
	}
	data, err := encodeProgress(p)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".progress-stage-")
	if err != nil {
		return errors.New("cannot stage private bootstrap progress")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
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
	if _, _, err := ReadProgress(configPath); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(dir, ProgressFilename)); err != nil {
		return fmt.Errorf("publish private bootstrap progress: %w", err)
	}
	return nil
}
