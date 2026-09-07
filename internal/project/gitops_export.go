package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const gitOpsExportManifest = ".bareplane-export.json"
const maxGitOpsFileBytes = 8 << 20

type exportManifest struct {
	Version int               `json:"version"`
	Cluster string            `json:"cluster"`
	Files   map[string]string `json:"files"`
}

// GitOpsExportDirectory is intentionally fixed: [path] selects configuration,
// never a destination to overwrite. The repository rootPath stays repository-
// relative inside this export and cannot redirect the local filesystem write.
func GitOpsExportDirectory(configPath string) (string, error) {
	if strings.TrimSpace(configPath) == "" {
		return "", errors.New("configuration path is empty")
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(absolute)
	for current := parent; ; current = filepath.Dir(current) {
		switch strings.ToLower(filepath.Base(current)) {
		case ".bareplane", ".git":
			return "", errors.New("GitOps export cannot be placed inside Bareplane execution state or Git metadata")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return filepath.Join(parent, "gitops"), nil
}

// WriteGitOpsExport preserves any edited, extra, redirected, or unrecognized
// output. Its digest inventory authorizes replacement of exactly the previous
// export, including when the config or renderer legitimately changes.
func WriteGitOpsExport(configPath, cluster string, files map[string][]byte) (destination string, err error) {
	destination, err = GitOpsExportDirectory(configPath)
	if err != nil {
		return "", err
	}
	if cluster == "" || len(files) == 0 || len(files) > 4096 {
		return "", errors.New("invalid GitOps export identity or file count")
	}
	manifest := exportManifest{Version: 1, Cluster: cluster, Files: make(map[string]string, len(files))}
	payload := make(map[string][]byte, len(files)+1)
	for name, data := range files {
		if err := validateGeneratedPath(name, true); err != nil {
			return "", err
		}
		if name == gitOpsExportManifest || len(data) > maxGitOpsFileBytes {
			return "", errors.New("reserved or oversized GitOps export file")
		}
		digest := sha256.Sum256(data)
		manifest.Files[name] = hex.EncodeToString(digest[:])
		payload[name] = data
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	payload[gitOpsExportManifest] = append(encoded, '\n')
	lock, err := AcquireBootstrapOperation(configPath, "gitops-render")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	if err := verifyGitOpsExport(destination, cluster); err != nil {
		return "", fmt.Errorf("refusing to replace GitOps export; copy or move user changes to a separate checkout: %w", err)
	}
	if err := ReplaceGeneratedTree(destination, "gitops", payload); err != nil {
		return "", err
	}
	return destination, nil
}

func verifyGitOpsExport(destination, cluster string) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnmanagedDestination
	}
	managed, err := generatedDirectoryMatches(destination, "gitops")
	if err != nil || !managed {
		return errors.Join(ErrUnmanagedDestination, err)
	}
	data, err := readExportFile(filepath.Join(destination, gitOpsExportManifest), 1<<20)
	if err != nil {
		return errors.Join(ErrUnmanagedDestination, err)
	}
	var manifest exportManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return ErrUnmanagedDestination
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) || manifest.Version != 1 || manifest.Cluster != cluster || len(manifest.Files) == 0 || len(manifest.Files) > 4096 {
		return ErrUnmanagedDestination
	}
	directories := map[string]bool{".": true}
	for name, digest := range manifest.Files {
		decoded, err := hex.DecodeString(digest)
		if validateGeneratedPath(name, true) != nil || name == gitOpsExportManifest || err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
			return ErrUnmanagedDestination
		}
		for parent := filepath.ToSlash(filepath.Dir(name)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			directories[parent] = true
		}
	}
	seen := 0
	err = filepath.WalkDir(destination, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(destination, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return ErrUnmanagedDestination
		}
		if entry.IsDir() {
			if !directories[relative] {
				return fmt.Errorf("unrecognized export directory %s", relative)
			}
			return nil
		}
		if relative == gitOpsExportManifest || relative == GeneratedMarkerFilename {
			return nil
		}
		expected, ok := manifest.Files[relative]
		if !ok {
			return fmt.Errorf("unrecognized export file %s", relative)
		}
		content, err := readExportFile(current, maxGitOpsFileBytes)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != expected {
			return fmt.Errorf("modified export file %s", relative)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(manifest.Files) {
		return errors.New("GitOps export has missing files")
	}
	return nil
}

func readExportFile(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, ErrUnmanagedDestination
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.Join(ErrUnmanagedDestination, err)
	}
	return data, nil
}
