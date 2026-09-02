package terraformexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return controlledTerraformEnvironment(base, dataDir)
}

type limitedBuffer struct {
	data     []byte
	exceeded bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	available := maxTerraformVersionOutputBytes - len(w.data)
	if available > 0 {
		copyBytes := len(p)
		if copyBytes > available {
			copyBytes = available
		}
		w.data = append(w.data, p[:copyBytes]...)
	}
	if len(p) > available {
		w.exceeded = true
	}
	return len(p), nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
