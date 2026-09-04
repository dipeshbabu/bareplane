package sshtrust

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestPrepareAndCommitFirstTrustDeterministically(t *testing.T) {
	configPath := writeTrustConfig(t, 2222, []string{"10.0.0.10", "2001:db8::20", "worker.example.com"})
	var calls []string
	scan := func(_ context.Context, host string, port int) ([]byte, error) {
		calls = append(calls, host)
		return []byte(
			testKeyLine(knownHostsName(host, port), "ssh-rsa", byte(len(host)+1)) +
				testKeyLine(knownHostsName(host, port), "ssh-ed25519", byte(len(host)+2)),
		), nil
	}

	plan, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: scan})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"10.0.0.10", "2001:db8::20", "worker.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scan calls = %#v, want %#v", got, want)
	}
	if plan.Cluster != "lab" || plan.EntryCount() != 6 || plan.Unchanged() || plan.RequiresRotation() {
		t.Fatalf("unexpected first trust plan: %#v", plan)
	}
	if len(plan.Keys) != 6 || plan.Keys[0].Machine != "lab-node-1" || plan.Keys[0].Type != "ssh-ed25519" {
		t.Fatalf("keys are not deterministic: %#v", plan.Keys)
	}
	if plan.Keys[2].Endpoint != "[2001:db8::20]:2222" {
		t.Fatalf("unexpected IPv6 endpoint %q", plan.Keys[2].Endpoint)
	}

	if err := plan.Commit("wrong-cluster"); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected approval error, got %v", err)
	}
	if _, err := os.Stat(filepath.Dir(plan.KnownHostsPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed approval created trust state: %v", err)
	}
	if err := plan.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(plan.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(stored, []byte(managedHeaderPrefix)) {
		t.Fatalf("managed header missing: %q", stored)
	}
	for _, expected := range []string{
		"[10.0.0.10]:2222 ssh-ed25519 ",
		"[2001:db8::20]:2222 ssh-rsa ",
		"[worker.example.com]:2222 ssh-ed25519 ",
	} {
		if !bytes.Contains(stored, []byte(expected)) {
			t.Fatalf("known_hosts missing %q: %s", expected, stored)
		}
	}
	if bytes.Contains(stored, []byte("do-not-read-private-key")) {
		t.Fatal("private-key path leaked into known_hosts")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(plan.KnownHostsPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != knownHostsFileMode {
			t.Fatalf("known_hosts permissions = %04o", got)
		}
	}
}

func TestRepeatedTrustIsIdempotent(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"10.0.0.10"})
	scan := singleKeyScan(1)
	first, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: scan})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: scan})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Unchanged() || second.RequiresRotation() || len(second.Changes) != 0 {
		t.Fatalf("unexpected repeat plan: %#v", second)
	}
	if err := second.Commit(""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(second.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("idempotent trust changed known_hosts")
	}
}

func TestChangedKeyRequiresExplicitRotationAndApproval(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	first, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(first.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !changed.RequiresRotation() || changed.Unchanged() || len(changed.Changes) != 1 {
		t.Fatalf("key change was not identified: %#v", changed)
	}
	if changed.Changes[0].PreviousFingerprint == "" || changed.Changes[0].CurrentFingerprint == "" {
		t.Fatalf("rotation fingerprints missing: %#v", changed.Changes)
	}
	if err := changed.Commit("lab"); !errors.Is(err, ErrRotationRequired) {
		t.Fatalf("expected rotation error, got %v", err)
	}
	assertFileEquals(t, first.KnownHostsPath, original)

	rotation, err := Prepare(context.Background(), Options{
		ConfigPath:    configPath,
		Scan:          singleKeyScan(2),
		AllowRotation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rotation.Commit("wrong"); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected approval error, got %v", err)
	}
	assertFileEquals(t, first.KnownHostsPath, original)
	if err := rotation.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(first.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(updated, original) {
		t.Fatal("approved rotation did not update known_hosts")
	}
}

func TestKeySetAdditionAndRemovalRequireRotation(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	initialScan := scanWithKeys(
		testKeyLine("ignored", "ssh-ed25519", 1),
		testKeyLine("ignored", "ssh-rsa", 1),
	)
	initial, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: initialScan})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Commit("lab"); err != nil {
		t.Fatal(err)
	}

	replacementScan := scanWithKeys(
		testKeyLine("ignored", "ssh-ed25519", 1),
		testKeyLine("ignored", "ecdsa-sha2-nistp256", 2),
	)
	plan, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: replacementScan})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresRotation() || len(plan.Changes) != 2 {
		t.Fatalf("key-set changes = %#v", plan.Changes)
	}
	var additions, removals int
	for _, change := range plan.Changes {
		if change.PreviousFingerprint == "" {
			additions++
		}
		if change.CurrentFingerprint == "" {
			removals++
		}
	}
	if additions != 1 || removals != 1 {
		t.Fatalf("additions=%d removals=%d changes=%#v", additions, removals, plan.Changes)
	}
}

func TestDuplicateEndpointIsScannedOnce(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com", "host.example.com"})
	calls := 0
	plan, err := Prepare(context.Background(), Options{
		ConfigPath: configPath,
		Scan: func(_ context.Context, host string, port int) ([]byte, error) {
			calls++
			return []byte(testKeyLine(knownHostsName(host, port), "ssh-ed25519", 1)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || plan.EntryCount() != 1 || len(plan.Keys) != 2 {
		t.Fatalf("calls=%d entries=%d keys=%#v", calls, plan.EntryCount(), plan.Keys)
	}
}

func TestPrepareRejectsMalformedKeyscanOutput(t *testing.T) {
	validED25519 := testEncodedPublicKey("ssh-ed25519", 1)
	mismatchedRSA := testEncodedPublicKey("ssh-rsa", 1)
	tests := []struct {
		name   string
		output []byte
	}{
		{name: "empty", output: nil},
		{name: "comments only", output: []byte("# scanner comment\n")},
		{name: "missing field", output: []byte("host ssh-ed25519\n")},
		{name: "extra field", output: []byte("host ssh-ed25519 " + validED25519 + " unexpected\n")},
		{name: "wrong endpoint", output: []byte("other.example.com ssh-ed25519 " + validED25519 + "\n")},
		{name: "unsupported type", output: []byte("host ssh-dss " + validED25519 + "\n")},
		{name: "bad base64", output: []byte("host ssh-ed25519 not-base64!\n")},
		{name: "wire type mismatch", output: []byte("host ssh-ed25519 " + mismatchedRSA + "\n")},
		{name: "truncated wire key", output: []byte("host ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 20, 's'}) + "\n")},
		{name: "invalid algorithm payload", output: []byte("host ssh-ed25519 " + testEncodedED25519WithLength(31) + "\n")},
		{name: "oversized line", output: []byte("host ssh-ed25519 " + strings.Repeat("A", maximumKeyLineSize) + "\n")},
		{name: "oversized output", output: bytes.Repeat([]byte{'A'}, MaximumScanOutput+1)},
		{
			name: "conflicting keys of same type",
			output: []byte(
				"host ssh-ed25519 " + testEncodedPublicKey("ssh-ed25519", 1) + "\n" +
					"host ssh-ed25519 " + testEncodedPublicKey("ssh-ed25519", 2) + "\n",
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
			_, err := Prepare(context.Background(), Options{
				ConfigPath: configPath,
				Scan: func(context.Context, string, int) ([]byte, error) {
					return test.output, nil
				},
			})
			if err == nil {
				t.Fatal("expected malformed keyscan output to fail")
			}
		})
	}
}

func TestMalformedKeyTypeIsNotEchoed(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	untrustedType := strings.Repeat("attacker-controlled-", 100)
	_, err := Prepare(context.Background(), Options{
		ConfigPath: configPath,
		Scan: func(context.Context, string, int) ([]byte, error) {
			return []byte("host.example.com " + untrustedType + " AAAA\n"), nil
		},
	})
	if err == nil || strings.Contains(err.Error(), untrustedType) {
		t.Fatalf("untrusted key type leaked in error: %v", err)
	}
}

func TestParseKeyscanOutputAcceptsSupportedTypesAndDeduplicates(t *testing.T) {
	const scannedHost = "[2001:db8::10]:2222"
	output := []byte(
		"# harmless scanner comment\r\n" +
			testKeyLine(scannedHost, "ssh-rsa", 1) +
			testKeyLine(scannedHost, "ecdsa-sha2-nistp256", 1) +
			testKeyLine(scannedHost, "ssh-ed25519", 1) +
			strings.TrimSuffix(testKeyLine(scannedHost, "ssh-ed25519", 1), "\n"),
	)
	keys, err := parseKeyscanOutput("2001:db8::10", 2222, output)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("keys = %#v", keys)
	}
	if got := []string{keys[0].keyType, keys[1].keyType, keys[2].keyType}; !reflect.DeepEqual(got, []string{"ecdsa-sha2-nistp256", "ssh-ed25519", "ssh-rsa"}) {
		t.Fatalf("key order = %#v", got)
	}
	for _, key := range keys {
		if key.host != "[2001:db8::10]:2222" || !strings.HasPrefix(key.fingerprint, "SHA256:") {
			t.Fatalf("unexpected parsed key %#v", key)
		}
	}
}

func TestParseHostKeyAcceptsEverySupportedKeyType(t *testing.T) {
	for keyType := range supportedKeyTypes {
		t.Run(keyType, func(t *testing.T) {
			key, err := parseHostKey("host.example.com", keyType, testEncodedPublicKey(keyType, 1))
			if err != nil {
				t.Fatal(err)
			}
			if key.keyType != keyType || !strings.HasPrefix(key.fingerprint, "SHA256:") {
				t.Fatalf("unexpected key %#v", key)
			}
		})
	}
}

func TestKeyscanArgumentsContainOnlyDiscoveryControls(t *testing.T) {
	got := keyscanArguments("2001:db8::10", 2222, 5)
	want := []string{
		"-q",
		"-T", "5",
		"-p", "2222",
		"-t", "ecdsa,ed25519,ecdsa-sk,ed25519-sk,rsa",
		"2001:db8::10",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keyscan arguments = %#v, want %#v", got, want)
	}
}

func TestKnownHostsNameFormatting(t *testing.T) {
	tests := []struct {
		host string
		port int
		want string
	}{
		{host: "10.0.0.10", port: 22, want: "10.0.0.10"},
		{host: "2001:db8::10", port: 22, want: "2001:db8::10"},
		{host: "host.example.com", port: 22, want: "host.example.com"},
		{host: "10.0.0.10", port: 2222, want: "[10.0.0.10]:2222"},
		{host: "2001:db8::10", port: 2222, want: "[2001:db8::10]:2222"},
		{host: "host.example.com", port: 2222, want: "[host.example.com]:2222"},
	}
	for _, test := range tests {
		if got := knownHostsName(test.host, test.port); got != test.want {
			t.Fatalf("knownHostsName(%q, %d) = %q, want %q", test.host, test.port, got, test.want)
		}
	}
}

func TestPrepareBoundsTimeoutAndRedactsScanErrors(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	called := false
	_, err := Prepare(context.Background(), Options{
		ConfigPath: configPath,
		Timeout:    MaximumScanTimeout + time.Second,
		Scan: func(context.Context, string, int) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	if err == nil || called {
		t.Fatalf("unbounded timeout was not rejected before scanning: called=%t err=%v", called, err)
	}

	_, err = Prepare(context.Background(), Options{
		ConfigPath: configPath,
		Scan: func(context.Context, string, int) ([]byte, error) {
			return nil, errors.New("secret scanner detail")
		},
	})
	if err == nil || strings.Contains(err.Error(), "secret scanner detail") || !strings.Contains(err.Error(), errScanFailed.Error()) {
		t.Fatalf("scan error was not safely reported: %v", err)
	}

	_, err = Prepare(context.Background(), Options{
		ConfigPath: configPath,
		Timeout:    5 * time.Millisecond,
		Scan: func(ctx context.Context, _ string, _ int) ([]byte, error) {
			<-ctx.Done()
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), errScanTimedOut.Error()) {
		t.Fatalf("timeout was not reported safely: %v", err)
	}
}

func TestPrepareRejectsUnmanagedOrTamperedKnownHostsBeforeScanning(t *testing.T) {
	tests := []struct {
		name     string
		contents []byte
	}{
		{name: "unmanaged", contents: []byte("host ssh-ed25519 key\n")},
		{name: "tampered", contents: append(renderManagedKnownHosts(map[string]hostKey{
			hostKeyIdentifier("host.example.com", "ssh-ed25519"): mustTestHostKey("host.example.com", "ssh-ed25519", 1),
		}), []byte("tampered\n")...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
			path, err := KnownHostsPathFor(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.contents, knownHostsFileMode); err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = Prepare(context.Background(), Options{
				ConfigPath: configPath,
				Scan: func(context.Context, string, int) ([]byte, error) {
					called = true
					return nil, nil
				},
			})
			if !errors.Is(err, ErrUnmanagedTrust) || called {
				t.Fatalf("called=%t err=%v", called, err)
			}
		})
	}
}

func TestPrepareRejectsSymlinkedTrustFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	path, err := KnownHostsPathFor(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(target, []byte("unmanaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err = Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if !errors.Is(err, ErrUnmanagedTrust) {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestPrepareRejectsSymlinkedTrustDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	root := filepath.Dir(configPath)
	if err := os.Mkdir(filepath.Join(root, ".bareplane"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".bareplane", "state")); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if !errors.Is(err, ErrUnmanagedTrust) {
		t.Fatalf("expected state symlink rejection, got %v", err)
	}
}

func TestPrepareRejectsBroadKnownHostsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are not applicable")
	}
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	plan, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(plan.KnownHostsPath, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if !errors.Is(err, ErrUnmanagedTrust) {
		t.Fatalf("expected permission rejection, got %v", err)
	}
}

func TestCommitDetectsConcurrentTrustCreation(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	first, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	if err := second.Commit("lab"); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("expected concurrent state change error, got %v", err)
	}
}

func TestRequireKnownHostsVerifiesManagedTrust(t *testing.T) {
	configPath := writeTrustConfig(t, 0, []string{"host.example.com"})
	if _, err := RequireKnownHosts(configPath); !errors.Is(err, ErrTrustNotFound) {
		t.Fatalf("expected missing trust error, got %v", err)
	}
	plan, err := Prepare(context.Background(), Options{ConfigPath: configPath, Scan: singleKeyScan(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Commit("lab"); err != nil {
		t.Fatal(err)
	}
	path, err := RequireKnownHosts(configPath)
	if err != nil || path != plan.KnownHostsPath {
		t.Fatalf("path=%q err=%v", path, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("tampered\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireKnownHosts(configPath); !errors.Is(err, ErrUnmanagedTrust) {
		t.Fatalf("expected tampered trust error, got %v", err)
	}
	if _, err := KnownHostsPathFor(""); err == nil {
		t.Fatal("expected empty configuration path error")
	}
}

func TestParseManagedKnownHostsRejectsNonCanonicalOrder(t *testing.T) {
	first := mustTestHostKey("z.example.com", "ssh-ed25519", 1)
	second := mustTestHostKey("a.example.com", "ssh-ed25519", 2)
	body := []byte(
		first.host + " " + first.keyType + " " + first.encoded + "\n" +
			second.host + " " + second.keyType + " " + second.encoded + "\n",
	)
	digest := sha256.Sum256(body)
	contents := []byte(managedHeaderPrefix + hex.EncodeToString(digest[:]) + "\n" + string(body))
	if _, err := parseManagedKnownHosts(contents); err == nil || !strings.Contains(err.Error(), "canonical order") {
		t.Fatalf("expected canonical-order error, got %v", err)
	}
}

func TestBoundedBufferDropsExcessWithoutGrowing(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	written, err := buffer.Write([]byte("123456"))
	if err != nil || written != 6 || buffer.String() != "1234" || !buffer.overflow {
		t.Fatalf("unexpected bounded write: written=%d err=%v value=%q overflow=%t", written, err, buffer.String(), buffer.overflow)
	}
	written, err = buffer.Write([]byte("789"))
	if err != nil || written != 3 || buffer.String() != "1234" {
		t.Fatalf("unexpected full-buffer write: written=%d err=%v value=%q", written, err, buffer.String())
	}
}

func writeTrustConfig(t *testing.T, port int, hosts []string) string {
	t.Helper()
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes = []config.NodeGroup{{
		Name:     "node",
		Role:     "control-plane",
		Count:    len(hosts),
		CPU:      4,
		MemoryGB: 8,
		DiskGB:   64,
	}}
	hostMap := make(map[string]string, len(hosts))
	for i, host := range hosts {
		hostMap["lab-node-"+strconv.Itoa(i+1)] = host
	}
	cfg.Spec.Bootstrap = &config.BootstrapConfig{SSH: &config.SSHBootstrap{
		User:           "debian",
		PrivateKeyFile: "do-not-read-private-key",
		Port:           port,
		Hosts:          hostMap,
	}}

	var encoded bytes.Buffer
	if err := config.Encode(&encoded, cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func singleKeyScan(seed byte) ScanFunc {
	return func(_ context.Context, host string, _ int) ([]byte, error) {
		return []byte(testKeyLine(host, "ssh-ed25519", seed)), nil
	}
}

func scanWithKeys(lines ...string) ScanFunc {
	return func(_ context.Context, host string, port int) ([]byte, error) {
		var output strings.Builder
		for _, line := range lines {
			separator := strings.IndexByte(line, ' ')
			if separator < 0 {
				output.WriteString(line)
				continue
			}
			output.WriteString(knownHostsName(host, port))
			output.WriteString(line[separator:])
		}
		return []byte(output.String()), nil
	}
}

func testKeyLine(host, keyType string, seed byte) string {
	return host + " " + keyType + " " + testEncodedPublicKey(keyType, seed) + "\n"
}

func testEncodedPublicKey(keyType string, seed byte) string {
	if seed == 0 {
		seed = 1
	}
	data := appendSSHString(nil, []byte(keyType))
	switch keyType {
	case "ssh-ed25519":
		data = appendSSHString(data, bytes.Repeat([]byte{seed}, 32))
	case "ssh-rsa":
		data = appendSSHString(data, []byte{1, 0, 1})
		modulusByte := seed & 0x7f
		if modulusByte == 0 {
			modulusByte = 1
		}
		modulus := bytes.Repeat([]byte{modulusByte}, 128)
		modulus[len(modulus)-1] |= 1
		data = appendSSHString(data, modulus)
	case "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ecdsa-sha2-nistp256@openssh.com":
		curveName, curve := testCurve(keyType)
		data = appendSSHString(data, []byte(curveName))
		x, y := curve.ScalarBaseMult([]byte{seed})
		data = appendSSHString(data, elliptic.Marshal(curve, x, y))
		if strings.HasPrefix(keyType, "sk-") {
			data = appendSSHString(data, []byte("ssh:"))
		}
	case "sk-ssh-ed25519@openssh.com":
		data = appendSSHString(data, bytes.Repeat([]byte{seed}, 32))
		data = appendSSHString(data, []byte("ssh:"))
	default:
		data = appendSSHString(data, []byte{seed})
	}
	return base64.StdEncoding.EncodeToString(data)
}

func testCurve(keyType string) (string, elliptic.Curve) {
	switch keyType {
	case "ecdsa-sha2-nistp256", "sk-ecdsa-sha2-nistp256@openssh.com":
		return "nistp256", elliptic.P256()
	case "ecdsa-sha2-nistp384":
		return "nistp384", elliptic.P384()
	case "ecdsa-sha2-nistp521":
		return "nistp521", elliptic.P521()
	default:
		panic("unsupported test curve")
	}
}

func testEncodedED25519WithLength(length int) string {
	data := appendSSHString(nil, []byte("ssh-ed25519"))
	data = appendSSHString(data, bytes.Repeat([]byte{1}, length))
	return base64.StdEncoding.EncodeToString(data)
}

func appendSSHString(destination, value []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	destination = append(destination, length[:]...)
	return append(destination, value...)
}

func mustTestHostKey(host, keyType string, seed byte) hostKey {
	key, err := parseHostKey(host, keyType, testEncodedPublicKey(keyType, seed))
	if err != nil {
		panic(err)
	}
	return key
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %s changed unexpectedly", path)
	}
}
