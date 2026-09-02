package terraformexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxTerraformVersionOutputBytes = 64 * 1024

type terraformVersionOutput struct {
	TerraformVersion string `json:"terraform_version"`
}

func readTerraformVersion(
	ctx context.Context,
	runner Runner,
	binary string,
	directory string,
	baseEnvironment []string,
	dataDir string,
) (string, error) {
	var stdout limitedBuffer
	exitCode, err := runner.Run(ctx, Command{
		Binary: binary,
		Args:   []string{"version", "-json"},
		Dir:    directory,
		Env:    terraformVersionEnvironment(baseEnvironment, dataDir),
		Stdout: &stdout,
		Stderr: ioDiscard{},
	})
	if err != nil {
		return "", fmt.Errorf("run terraform version: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("terraform version exited with code %d", exitCode)
	}
	if stdout.exceeded {
		return "", fmt.Errorf("terraform version output exceeds %d bytes", maxTerraformVersionOutputBytes)
	}

	var version terraformVersionOutput
	decoder := json.NewDecoder(bytes.NewReader(stdout.data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&version); err != nil {
		return "", fmt.Errorf("decode terraform version output: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", fmt.Errorf("decode terraform version output: %w", err)
	}
	version.TerraformVersion = strings.TrimSpace(version.TerraformVersion)
	if version.TerraformVersion == "" {
		return "", errors.New("terraform version output did not include terraform_version")
	}
	return version.TerraformVersion, nil
}

func terraformVersionEnvironment(base []string, dataDir string) []string {
	if base == nil {
		base = os.Environ()
	}
	filtered := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key := environmentKey(entry)
		if key == providerTokenEnv || key == "TF_DATA_DIR" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "TF_DATA_DIR="+dataDir)
}

type limitedBuffer struct {
	data     []byte
	exceeded bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if len(w.data) < maxTerraformVersionOutputBytes {
		remaining := maxTerraformVersionOutputBytes - len(w.data)
		if remaining > len(p) {
			remaining = len(p)
		}
		w.data = append(w.data, p[:remaining]...)
	}
	if len(p) > maxTerraformVersionOutputBytes-len(w.data) {
		w.exceeded = true
	}
	return len(p), nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
