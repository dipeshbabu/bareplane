package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/sshtrust"
)

const maximumTrustApprovalBytes = 256

func runBootstrapTrust(args []string, stdin io.Reader, stdout, stderr io.Writer, scanFns ...sshtrust.ScanFunc) int {
	configPath, rotate, ok := parseBootstrapTrustArgs(args)
	if !ok {
		fmt.Fprintln(stderr, "usage: bareplane bootstrap trust [--rotate] [path]")
		return 2
	}

	var scan sshtrust.ScanFunc
	if len(scanFns) > 0 {
		scan = scanFns[0]
	}
	plan, err := sshtrust.Prepare(context.Background(), sshtrust.Options{
		ConfigPath:    configPath,
		Scan:          scan,
		AllowRotation: rotate,
	})
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap trust %s: %v\n", configPath, err)
		return 1
	}

	for _, key := range plan.Keys {
		fmt.Fprintf(stdout, "FOUND  %-28s %-38s %s  %s\n", key.Machine, key.Type, key.Fingerprint, key.Endpoint)
	}
	if plan.Unchanged() {
		if err := plan.Commit(""); err != nil {
			fmt.Fprintf(stderr, "bootstrap trust %s: %v\n", configPath, err)
			return 1
		}
		fmt.Fprintf(stdout, "trusted SSH host keys are unchanged at %s\n", plan.KnownHostsPath)
		return 0
	}
	if plan.RequiresRotation() {
		for _, change := range plan.Changes {
			previous := change.PreviousFingerprint
			if previous == "" {
				previous = "<none>"
			}
			current := change.CurrentFingerprint
			if current == "" {
				current = "<none>"
			}
			fmt.Fprintf(stdout, "CHANGE %s %s %s -> %s\n", change.Endpoint, change.Type, previous, current)
		}
		if !rotate {
			fmt.Fprintf(stderr, "bootstrap trust %s: %v\n", configPath, sshtrust.ErrRotationRequired)
			return 1
		}
	}

	fmt.Fprintf(stdout, "Type cluster name %q to approve SSH host trust: ", plan.Cluster)
	approval, err := readTrustApproval(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap trust %s: read approval: %v\n", configPath, err)
		return 1
	}
	if err := plan.Commit(approval); err != nil {
		fmt.Fprintf(stderr, "bootstrap trust %s: %v\n", configPath, err)
		return 1
	}
	fmt.Fprintf(stdout, "saved %d trusted SSH host keys to %s\n", plan.EntryCount(), plan.KnownHostsPath)
	return 0
}

func parseBootstrapTrustArgs(args []string) (string, bool, bool) {
	configPath := "bareplane.yaml"
	pathSet := false
	rotate := false
	for _, arg := range args {
		switch {
		case arg == "--rotate" && !rotate:
			rotate = true
		case strings.HasPrefix(arg, "-"):
			return "", false, false
		case pathSet:
			return "", false, false
		default:
			configPath = arg
			pathSet = true
		}
	}
	return configPath, rotate, true
}

func readTrustApproval(input io.Reader) (string, error) {
	if input == nil {
		return "", errors.New("approval input is unavailable")
	}
	reader := bufio.NewReaderSize(io.LimitReader(input, maximumTrustApprovalBytes+1), maximumTrustApprovalBytes+1)
	line, err := reader.ReadString('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maximumTrustApprovalBytes {
		return "", fmt.Errorf("approval exceeds %d bytes", maximumTrustApprovalBytes)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}
