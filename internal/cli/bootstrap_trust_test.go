package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/sshtrust"
)

type rejectingApprovalReader struct {
	called bool
}

func (r *rejectingApprovalReader) Read([]byte) (int, error) {
	r.called = true
	return 0, errors.New("approval input should not be read")
}

type endlessApprovalReader struct {
	bytesRead int
}

func (r *endlessApprovalReader) Read(destination []byte) (int, error) {
	for i := range destination {
		destination[i] = 'a'
	}
	r.bytesRead += len(destination)
	return len(destination), nil
}

func TestParseBootstrapTrustArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantPath   string
		wantRotate bool
		wantOK     bool
	}{
		{name: "defaults", wantPath: "bareplane.yaml", wantOK: true},
		{name: "path", args: []string{"cluster.yaml"}, wantPath: "cluster.yaml", wantOK: true},
		{name: "rotate", args: []string{"--rotate"}, wantPath: "bareplane.yaml", wantRotate: true, wantOK: true},
		{name: "rotate then path", args: []string{"--rotate", "cluster.yaml"}, wantPath: "cluster.yaml", wantRotate: true, wantOK: true},
		{name: "path then rotate", args: []string{"cluster.yaml", "--rotate"}, wantPath: "cluster.yaml", wantRotate: true, wantOK: true},
		{name: "duplicate rotate", args: []string{"--rotate", "--rotate"}},
		{name: "unknown flag", args: []string{"--force"}},
		{name: "two paths", args: []string{"one.yaml", "two.yaml"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, rotate, ok := parseBootstrapTrustArgs(test.args)
			if path != test.wantPath || rotate != test.wantRotate || ok != test.wantOK {
				t.Fatalf("got path=%q rotate=%t ok=%t", path, rotate, ok)
			}
		})
	}
}

func TestRunBootstrapTrustRequiresExactApprovalBeforeWriting(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)
	encoded := cliTestEncodedED25519(1)
	scan := cliTestScan(encoded)

	var stdout, stderr bytes.Buffer
	code := runBootstrapTrust([]string{configPath}, strings.NewReader("wrong\n"), &stdout, &stderr, scan)
	if code != 1 || !strings.Contains(stderr.String(), sshtrust.ErrApprovalRequired.Error()) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	knownHostsPath := filepath.Join(root, ".bareplane", "state", "bootstrap", sshtrust.KnownHostsFilename)
	if _, err := os.Stat(knownHostsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed approval wrote known_hosts: %v", err)
	}
	for _, expected := range []string{"lab-control-1", "ssh-ed25519", "SHA256:", "10.0.0.10:22"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("review output missing %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), encoded) || strings.Contains(stdout.String(), "id_ed25519") {
		t.Fatalf("review output leaked raw key or private-key path: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runBootstrapTrust([]string{configPath}, strings.NewReader("lab\n"), &stdout, &stderr, scan)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(knownHostsPath); err != nil {
		t.Fatalf("approved trust did not write known_hosts: %v", err)
	}
	if !strings.Contains(stdout.String(), "saved 1 trusted SSH host keys") {
		t.Fatalf("unexpected success output %q", stdout.String())
	}
}

func TestRunBootstrapTrustRepeatedKeysDoNotPromptOrRewrite(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)
	scan := cliTestScan(cliTestEncodedED25519(1))
	var stdout, stderr bytes.Buffer
	if code := runBootstrapTrust([]string{configPath}, strings.NewReader("lab\n"), &stdout, &stderr, scan); code != 0 {
		t.Fatalf("initial trust failed: code=%d stderr=%q", code, stderr.String())
	}

	input := &rejectingApprovalReader{}
	stdout.Reset()
	stderr.Reset()
	code := runBootstrapTrust([]string{configPath}, input, &stdout, &stderr, scan)
	if code != 0 || input.called || !strings.Contains(stdout.String(), "unchanged") {
		t.Fatalf("code=%d inputCalled=%t stdout=%q stderr=%q", code, input.called, stdout.String(), stderr.String())
	}
}

func TestRunBootstrapTrustChangedKeyRequiresRotationFlow(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)
	var stdout, stderr bytes.Buffer
	if code := runBootstrapTrust([]string{configPath}, strings.NewReader("lab\n"), &stdout, &stderr, cliTestScan(cliTestEncodedED25519(1))); code != 0 {
		t.Fatalf("initial trust failed: code=%d stderr=%q", code, stderr.String())
	}

	input := &rejectingApprovalReader{}
	stdout.Reset()
	stderr.Reset()
	code := runBootstrapTrust([]string{configPath}, input, &stdout, &stderr, cliTestScan(cliTestEncodedED25519(2)))
	if code != 1 || input.called || !strings.Contains(stderr.String(), "--rotate") || !strings.Contains(stdout.String(), "CHANGE") {
		t.Fatalf("code=%d inputCalled=%t stdout=%q stderr=%q", code, input.called, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runBootstrapTrust([]string{"--rotate", configPath}, strings.NewReader("lab\n"), &stdout, &stderr, cliTestScan(cliTestEncodedED25519(2)))
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "saved 1 trusted SSH host keys") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunBootstrapTrustRejectsInvalidArgumentsWithoutScanning(t *testing.T) {
	for _, args := range [][]string{{"one", "two"}, {"--unknown"}, {"--rotate", "--rotate"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			called := false
			var stdout, stderr bytes.Buffer
			code := runBootstrapTrust(args, strings.NewReader(""), &stdout, &stderr, func(context.Context, string, int) ([]byte, error) {
				called = true
				return nil, nil
			})
			if code != 2 || called || !strings.Contains(stderr.String(), "usage: bareplane bootstrap trust") {
				t.Fatalf("code=%d called=%t stderr=%q", code, called, stderr.String())
			}
		})
	}
}

func TestReadTrustApprovalBoundsInputAndPreservesExactValue(t *testing.T) {
	approval, err := readTrustApproval(strings.NewReader(" lab \r\n"))
	if err != nil || approval != " lab " {
		t.Fatalf("approval=%q err=%v", approval, err)
	}
	_, err = readTrustApproval(strings.NewReader(strings.Repeat("a", maximumTrustApprovalBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bounded approval error, got %v", err)
	}
	endless := &endlessApprovalReader{}
	_, err = readTrustApproval(endless)
	if err == nil || endless.bytesRead > maximumTrustApprovalBytes+1 {
		t.Fatalf("unbounded approval read: bytes=%d err=%v", endless.bytesRead, err)
	}
}

func TestBootstrapHelpListsTrust(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap", "help"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "bareplane bootstrap trust") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func cliTestScan(encoded string) sshtrust.ScanFunc {
	return func(_ context.Context, host string, _ int) ([]byte, error) {
		return []byte(host + " ssh-ed25519 " + encoded + "\n"), nil
	}
}

func cliTestEncodedED25519(seed byte) string {
	var data []byte
	data = cliAppendSSHString(data, []byte("ssh-ed25519"))
	data = cliAppendSSHString(data, bytes.Repeat([]byte{seed}, 32))
	return base64.StdEncoding.EncodeToString(data)
}

func cliAppendSSHString(destination, value []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	destination = append(destination, length[:]...)
	return append(destination, value...)
}
