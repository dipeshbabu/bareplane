package sshtrust

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

const (
	KnownHostsFilename    = "known_hosts"
	DefaultScanTimeout    = 5 * time.Second
	MaximumScanTimeout    = 30 * time.Second
	MaximumScanOutput     = 64 * 1024
	MaximumKnownHostsSize = 16 * 1024 * 1024
	maximumKeyLineSize    = 24 * 1024
	maximumDecodedKeySize = 16 * 1024
	managedHeaderPrefix   = "# bareplane-managed known_hosts v1 sha256="
	privateDirectoryMode  = 0o700
	knownHostsFileMode    = 0o600
)

var (
	ErrApprovalRequired = errors.New("SSH host trust approval must exactly match the cluster name")
	ErrRotationRequired = errors.New("existing SSH host trust differs; rerun with --rotate after reviewing the changes")
	ErrStateChanged     = errors.New("SSH host trust changed after discovery; rerun the trust command")
	ErrTrustNotFound    = errors.New("trusted SSH host keys are missing; run bareplane bootstrap trust")
	ErrUnmanagedTrust   = errors.New("SSH host trust is not managed by Bareplane")
	errScanFailed       = errors.New("SSH host-key discovery failed")
	errScanTimedOut     = errors.New("SSH host-key discovery timed out")
	errScanOutputLimit  = errors.New("SSH host-key discovery exceeded the output limit")
)

var supportedKeyTypes = map[string]struct{}{
	"ecdsa-sha2-nistp256":                {},
	"ecdsa-sha2-nistp384":                {},
	"ecdsa-sha2-nistp521":                {},
	"sk-ecdsa-sha2-nistp256@openssh.com": {},
	"sk-ssh-ed25519@openssh.com":         {},
	"ssh-ed25519":                        {},
	"ssh-rsa":                            {},
}

// ScanFunc discovers public host keys for one validated host and port.
type ScanFunc func(context.Context, string, int) ([]byte, error)

// Options controls host-key discovery and whether an existing trust set may rotate.
type Options struct {
	ConfigPath    string
	Timeout       time.Duration
	Scan          ScanFunc
	AllowRotation bool
}

// Key is a fingerprint-only host key record safe for operator review output.
type Key struct {
	Machine     string
	Endpoint    string
	Type        string
	Fingerprint string
}

// Change describes one added, removed, or replaced endpoint key fingerprint.
type Change struct {
	Endpoint            string
	Type                string
	PreviousFingerprint string
	CurrentFingerprint  string
}

// Plan binds discovered keys to the trust state observed before operator approval.
type Plan struct {
	Cluster        string
	KnownHostsPath string
	Keys           []Key
	Changes        []Change

	allowRotation   bool
	entryCount      int
	existing        bool
	unchanged       bool
	contents        []byte
	existingContent []byte
}

type hostKey struct {
	host        string
	keyType     string
	encoded     string
	fingerprint string
}

// Prepare validates configuration and existing trust, then discovers public host keys without authenticating.
func Prepare(ctx context.Context, options Options) (Plan, error) {
	if ctx == nil {
		return Plan{}, errors.New("trust context is nil")
	}
	if strings.TrimSpace(options.ConfigPath) == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultScanTimeout
	}
	if options.Timeout > MaximumScanTimeout {
		return Plan{}, fmt.Errorf("SSH host-key discovery timeout must not exceed %s", MaximumScanTimeout)
	}

	cfg, err := loadBootstrapConfig(options.ConfigPath)
	if err != nil {
		return Plan{}, err
	}
	knownHostsPath, err := KnownHostsPathFor(options.ConfigPath)
	if err != nil {
		return Plan{}, err
	}
	existingKeys, existingContent, existing, err := loadManagedKnownHosts(knownHostsPath)
	if err != nil {
		return Plan{}, err
	}

	scan := options.Scan
	if scan == nil {
		binary, err := exec.LookPath("ssh-keyscan")
		if err != nil {
			return Plan{}, errors.New("ssh-keyscan executable was not found")
		}
		scan = commandScanner(binary)
	}

	topo, err := topology.Build(cfg)
	if err != nil {
		return Plan{}, fmt.Errorf("build bootstrap topology: %w", err)
	}
	machines := append([]topology.Machine(nil), topo.Machines...)
	sort.Slice(machines, func(i, j int) bool { return machines[i].Name < machines[j].Name })

	port := cfg.Spec.Bootstrap.SSH.EffectivePort()
	discovered := make(map[string]hostKey)
	knownHostsSize := len(managedHeaderPrefix) + sha256.Size*2 + 1
	displayKeys := make([]Key, 0, len(machines)*3)
	cache := make(map[string][]hostKey)
	for _, machine := range machines {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		host := cfg.Spec.Bootstrap.SSH.Hosts[machine.Name]
		cacheKey := net.JoinHostPort(host, strconv.Itoa(port))
		keys, ok := cache[cacheKey]
		if !ok {
			probeCtx, cancel := context.WithTimeout(ctx, options.Timeout)
			output, scanErr := scan(probeCtx, host, port)
			contextErr := probeCtx.Err()
			cancel()
			if scanErr != nil || contextErr != nil {
				return Plan{}, fmt.Errorf("discover SSH host keys for machine %q: %w", machine.Name, safeScanError(scanErr, contextErr))
			}
			keys, err = parseKeyscanOutput(host, port, output)
			if err != nil {
				return Plan{}, fmt.Errorf("discover SSH host keys for machine %q: %w", machine.Name, err)
			}
			cache[cacheKey] = keys
		}
		for _, key := range keys {
			identifier := hostKeyIdentifier(key.host, key.keyType)
			previous, exists := discovered[identifier]
			if exists && previous.encoded != key.encoded {
				return Plan{}, fmt.Errorf("configured endpoint %s returned conflicting %s host keys", cacheKey, key.keyType)
			}
			if !exists {
				knownHostsSize += len(key.host) + len(key.keyType) + len(key.encoded) + 3
				if knownHostsSize > MaximumKnownHostsSize {
					return Plan{}, fmt.Errorf("discovered SSH host trust exceeds %d bytes", MaximumKnownHostsSize)
				}
			}
			discovered[identifier] = key
			displayKeys = append(displayKeys, Key{
				Machine:     machine.Name,
				Endpoint:    cacheKey,
				Type:        key.keyType,
				Fingerprint: key.fingerprint,
			})
		}
	}

	sort.Slice(displayKeys, func(i, j int) bool {
		if displayKeys[i].Machine != displayKeys[j].Machine {
			return displayKeys[i].Machine < displayKeys[j].Machine
		}
		if displayKeys[i].Type != displayKeys[j].Type {
			return displayKeys[i].Type < displayKeys[j].Type
		}
		return displayKeys[i].Fingerprint < displayKeys[j].Fingerprint
	})
	contents := renderManagedKnownHosts(discovered)
	unchanged := existing && bytes.Equal(contents, existingContent)

	return Plan{
		Cluster:         cfg.Metadata.Name,
		KnownHostsPath:  knownHostsPath,
		Keys:            displayKeys,
		Changes:         compareHostKeys(existingKeys, discovered),
		allowRotation:   options.AllowRotation,
		entryCount:      len(discovered),
		existing:        existing,
		unchanged:       unchanged,
		contents:        contents,
		existingContent: existingContent,
	}, nil
}

// EntryCount returns the number of unique known_hosts entries to persist.
func (p Plan) EntryCount() int {
	return p.entryCount
}

// Unchanged reports whether discovery exactly matches the managed trust file.
func (p Plan) Unchanged() bool {
	return p.unchanged
}

// RequiresRotation reports whether existing trust would change.
func (p Plan) RequiresRotation() bool {
	return p.existing && !p.unchanged
}

// Commit persists the prepared keys only after exact approval and a state recheck.
func (p Plan) Commit(approval string) error {
	if !p.unchanged && approval != p.Cluster {
		return ErrApprovalRequired
	}
	if p.RequiresRotation() && !p.allowRotation {
		return ErrRotationRequired
	}

	_, currentContent, currentExists, err := loadManagedKnownHosts(p.KnownHostsPath)
	if err != nil {
		return err
	}
	if currentExists != p.existing || !bytes.Equal(currentContent, p.existingContent) {
		return ErrStateChanged
	}
	if p.unchanged {
		return nil
	}
	return writeManagedKnownHosts(p.KnownHostsPath, p.contents, p.existing)
}

func loadBootstrapConfig(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open configuration: %w", err)
	}
	cfg, loadErr := config.Load(file)
	closeErr := file.Close()
	if loadErr != nil {
		return config.Config{}, loadErr
	}
	if closeErr != nil {
		return config.Config{}, fmt.Errorf("close configuration: %w", closeErr)
	}
	if err := cfg.ValidateBootstrap(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// KnownHostsPathFor returns the persistent project-scoped trust file path.
func KnownHostsPathFor(configPath string) (string, error) {
	if strings.TrimSpace(configPath) == "" {
		return "", errors.New("configuration path is empty")
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolve configuration path: %w", err)
	}
	return filepath.Join(filepath.Dir(filepath.Clean(absoluteConfig)), ".bareplane", "state", "bootstrap", KnownHostsFilename), nil
}

// RequireKnownHosts returns a path only when its managed trust file passes integrity checks.
func RequireKnownHosts(configPath string) (string, error) {
	path, err := KnownHostsPathFor(configPath)
	if err != nil {
		return "", err
	}
	_, _, exists, err := loadManagedKnownHosts(path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrTrustNotFound
	}
	return path, nil
}

func commandScanner(binary string) ScanFunc {
	return func(ctx context.Context, host string, port int) ([]byte, error) {
		seconds := 1
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining > 0 {
				seconds = int((remaining + time.Second - 1) / time.Second)
			}
		}

		var stdout boundedBuffer
		stdout.limit = MaximumScanOutput
		command := exec.CommandContext(
			ctx,
			binary,
			keyscanArguments(host, port, seconds)...,
		)
		command.Stdout = &stdout
		command.Stderr = io.Discard
		err := command.Run()
		if ctx.Err() != nil {
			return nil, errScanTimedOut
		}
		if stdout.overflow {
			return nil, errScanOutputLimit
		}
		if err != nil {
			return nil, errScanFailed
		}
		return bytes.Clone(stdout.Bytes()), nil
	}
}

func keyscanArguments(host string, port, timeoutSeconds int) []string {
	return []string{
		"-q",
		"-T", strconv.Itoa(timeoutSeconds),
		"-p", strconv.Itoa(port),
		"-t", "ecdsa,ed25519,ecdsa-sk,ed25519-sk,rsa",
		host,
	}
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.limit - b.Len()
	if remaining < len(data) {
		b.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		data = data[:remaining]
	}
	if len(data) > 0 {
		_, _ = b.Buffer.Write(data)
	}
	return originalLength, nil
}

func safeScanError(scanErr, contextErr error) error {
	if errors.Is(scanErr, errScanTimedOut) || errors.Is(contextErr, context.DeadlineExceeded) {
		return errScanTimedOut
	}
	if errors.Is(scanErr, errScanOutputLimit) {
		return errScanOutputLimit
	}
	return errScanFailed
}

func parseKeyscanOutput(host string, port int, output []byte) ([]hostKey, error) {
	if len(output) > MaximumScanOutput {
		return nil, errScanOutputLimit
	}
	knownHost := knownHostsName(host, port)
	byType := make(map[string]hostKey)
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if len(line) > maximumKeyLineSize {
			return nil, errors.New("SSH host-key response contains an oversized line")
		}
		fields := bytes.Fields(line)
		if len(fields) != 3 {
			return nil, errors.New("SSH host-key response is malformed")
		}
		if string(fields[0]) != knownHost {
			return nil, errors.New("SSH host-key response does not match the configured endpoint")
		}
		key, err := parseHostKey(knownHost, string(fields[1]), string(fields[2]))
		if err != nil {
			return nil, err
		}
		if previous, exists := byType[key.keyType]; exists {
			if previous.encoded != key.encoded {
				return nil, errors.New("SSH host-key response contains conflicting keys for one key type")
			}
			continue
		}
		byType[key.keyType] = key
	}
	if len(byType) == 0 {
		return nil, errors.New("SSH host-key response contained no supported public keys")
	}

	keys := make([]hostKey, 0, len(byType))
	for _, key := range byType {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].keyType != keys[j].keyType {
			return keys[i].keyType < keys[j].keyType
		}
		return keys[i].fingerprint < keys[j].fingerprint
	})
	return keys, nil
}

func parseHostKey(host, keyType, encoded string) (hostKey, error) {
	if _, supported := supportedKeyTypes[keyType]; !supported {
		return hostKey{}, errors.New("SSH host-key response uses an unsupported key type")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maximumDecodedKeySize) {
		return hostKey{}, errors.New("SSH host-key response contains oversized key material")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maximumDecodedKeySize {
		return hostKey{}, errors.New("SSH host-key response contains malformed key material")
	}
	if base64.StdEncoding.EncodeToString(decoded) != encoded {
		return hostKey{}, errors.New("SSH host-key response contains non-canonical key material")
	}
	if !validPublicKeyBlob(decoded, keyType) {
		return hostKey{}, errors.New("SSH host-key response contains invalid key material for its declared type")
	}
	digest := sha256.Sum256(decoded)
	return hostKey{
		host:        host,
		keyType:     keyType,
		encoded:     encoded,
		fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]),
	}, nil
}

type sshWireReader struct {
	data   []byte
	offset int
}

func (r *sshWireReader) readString() ([]byte, bool) {
	if len(r.data)-r.offset < 4 {
		return nil, false
	}
	length := binary.BigEndian.Uint32(r.data[r.offset : r.offset+4])
	r.offset += 4
	if uint64(length) > uint64(len(r.data)-r.offset) {
		return nil, false
	}
	value := r.data[r.offset : r.offset+int(length)]
	r.offset += int(length)
	return value, true
}

func (r *sshWireReader) done() bool {
	return r.offset == len(r.data)
}

func validPublicKeyBlob(data []byte, declaredType string) bool {
	reader := sshWireReader{data: data}
	wireType, ok := reader.readString()
	if !ok || string(wireType) != declaredType {
		return false
	}

	switch declaredType {
	case "ssh-ed25519":
		key, ok := reader.readString()
		return ok && len(key) == 32 && reader.done()
	case "ssh-rsa":
		exponent, exponentOK := reader.readString()
		modulus, modulusOK := reader.readString()
		return exponentOK && modulusOK && validRSAExponent(exponent) && validPositiveMPInt(modulus) && len(modulus) >= 128 && modulus[len(modulus)-1]&1 == 1 && reader.done()
	case "ecdsa-sha2-nistp256":
		return validECDSAKey(&reader, "nistp256", 65) && reader.done()
	case "ecdsa-sha2-nistp384":
		return validECDSAKey(&reader, "nistp384", 97) && reader.done()
	case "ecdsa-sha2-nistp521":
		return validECDSAKey(&reader, "nistp521", 133) && reader.done()
	case "sk-ssh-ed25519@openssh.com":
		key, keyOK := reader.readString()
		application, applicationOK := reader.readString()
		return keyOK && applicationOK && len(key) == 32 && validSecurityKeyApplication(application) && reader.done()
	case "sk-ecdsa-sha2-nistp256@openssh.com":
		if !validECDSAKey(&reader, "nistp256", 65) {
			return false
		}
		application, ok := reader.readString()
		return ok && validSecurityKeyApplication(application) && reader.done()
	default:
		return false
	}
}

func validECDSAKey(reader *sshWireReader, expectedCurve string, pointLength int) bool {
	curve, curveOK := reader.readString()
	point, pointOK := reader.readString()
	if !curveOK || !pointOK || string(curve) != expectedCurve || len(point) != pointLength || point[0] != 4 {
		return false
	}
	var ellipticCurve elliptic.Curve
	switch expectedCurve {
	case "nistp256":
		ellipticCurve = elliptic.P256()
	case "nistp384":
		ellipticCurve = elliptic.P384()
	case "nistp521":
		ellipticCurve = elliptic.P521()
	default:
		return false
	}
	x, y := elliptic.Unmarshal(ellipticCurve, point)
	return x != nil && y != nil
}

func validRSAExponent(value []byte) bool {
	if !validPositiveMPInt(value) || len(value) > 8 {
		return false
	}
	var exponent uint64
	for _, current := range value {
		exponent = exponent<<8 | uint64(current)
	}
	return exponent >= 3 && exponent&1 == 1
}

func validSecurityKeyApplication(value []byte) bool {
	if len(value) == 0 || len(value) > 1024 {
		return false
	}
	return !bytes.ContainsAny(value, "\x00\r\n")
}

func validPositiveMPInt(value []byte) bool {
	if len(value) == 0 || value[0]&0x80 != 0 {
		return false
	}
	if len(value) > 1 && value[0] == 0 && value[1]&0x80 == 0 {
		return false
	}
	for _, current := range value {
		if current != 0 {
			return true
		}
	}
	return false
}

func knownHostsName(host string, port int) string {
	if port == config.DefaultSSHPort {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

func hostKeyIdentifier(host, keyType string) string {
	return host + "\x00" + keyType
}

func renderManagedKnownHosts(keys map[string]hostKey) []byte {
	body := renderKnownHostsBody(keys)
	digest := sha256.Sum256(body)
	contents := make([]byte, 0, len(managedHeaderPrefix)+hex.EncodedLen(len(digest))+1+len(body))
	contents = append(contents, managedHeaderPrefix...)
	contents = hex.AppendEncode(contents, digest[:])
	contents = append(contents, '\n')
	contents = append(contents, body...)
	return contents
}

func renderKnownHostsBody(keys map[string]hostKey) []byte {
	ordered := make([]hostKey, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].host != ordered[j].host {
			return ordered[i].host < ordered[j].host
		}
		if ordered[i].keyType != ordered[j].keyType {
			return ordered[i].keyType < ordered[j].keyType
		}
		return ordered[i].encoded < ordered[j].encoded
	})

	var body bytes.Buffer
	for _, key := range ordered {
		body.Grow(len(key.host) + len(key.keyType) + len(key.encoded) + 3)
		body.WriteString(key.host)
		body.WriteByte(' ')
		body.WriteString(key.keyType)
		body.WriteByte(' ')
		body.WriteString(key.encoded)
		body.WriteByte('\n')
	}
	return body.Bytes()
}

func loadManagedKnownHosts(path string) (map[string]hostKey, []byte, bool, error) {
	if err := inspectStatePath(path); err != nil {
		return nil, nil, false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]hostKey{}, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("inspect SSH host trust: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, false, fmt.Errorf("%w: %s must be a regular file and not a symlink", ErrUnmanagedTrust, path)
	}
	if info.Size() > MaximumKnownHostsSize {
		return nil, nil, false, fmt.Errorf("%w: known_hosts exceeds %d bytes", ErrUnmanagedTrust, MaximumKnownHostsSize)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, nil, false, fmt.Errorf("%w: known_hosts permissions must be 0600 or stricter", ErrUnmanagedTrust)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("open SSH host trust: %w", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, MaximumKnownHostsSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, nil, false, fmt.Errorf("read SSH host trust: %w", readErr)
	}
	if closeErr != nil {
		return nil, nil, false, fmt.Errorf("close SSH host trust: %w", closeErr)
	}
	if len(contents) > MaximumKnownHostsSize {
		return nil, nil, false, fmt.Errorf("%w: known_hosts exceeds %d bytes", ErrUnmanagedTrust, MaximumKnownHostsSize)
	}

	keys, err := parseManagedKnownHosts(contents)
	if err != nil {
		return nil, nil, false, fmt.Errorf("%w: %v", ErrUnmanagedTrust, err)
	}
	return keys, contents, true, nil
}

func parseManagedKnownHosts(contents []byte) (map[string]hostKey, error) {
	headerEnd := bytes.IndexByte(contents, '\n')
	if headerEnd < 0 || !bytes.HasPrefix(contents[:headerEnd], []byte(managedHeaderPrefix)) {
		return nil, errors.New("known_hosts management header is missing")
	}
	digestText := contents[len(managedHeaderPrefix):headerEnd]
	if len(digestText) != sha256.Size*2 {
		return nil, errors.New("known_hosts management digest is malformed")
	}
	expectedDigest := make([]byte, sha256.Size)
	if _, err := hex.Decode(expectedDigest, digestText); err != nil {
		return nil, errors.New("known_hosts management digest is malformed")
	}
	body := contents[headerEnd+1:]
	actualDigest := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(expectedDigest, actualDigest[:]) != 1 {
		return nil, errors.New("known_hosts contents do not match the management digest")
	}

	keys := make(map[string]hostKey)
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if len(line) > maximumKeyLineSize {
			return nil, errors.New("known_hosts contains an oversized line")
		}
		fields := bytes.Fields(line)
		if len(fields) != 3 {
			return nil, errors.New("known_hosts contains a malformed entry")
		}
		key, err := parseHostKey(string(fields[0]), string(fields[1]), string(fields[2]))
		if err != nil {
			return nil, err
		}
		identifier := hostKeyIdentifier(key.host, key.keyType)
		if _, duplicate := keys[identifier]; duplicate {
			return nil, errors.New("known_hosts contains a duplicate host key type")
		}
		keys[identifier] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("known_hosts contains no public keys")
	}
	if !bytes.Equal(body, renderKnownHostsBody(keys)) {
		return nil, errors.New("known_hosts entries are not in canonical order")
	}
	return keys, nil
}

func compareHostKeys(previous, current map[string]hostKey) []Change {
	identifiers := make(map[string]struct{}, len(previous)+len(current))
	for identifier := range previous {
		identifiers[identifier] = struct{}{}
	}
	for identifier := range current {
		identifiers[identifier] = struct{}{}
	}

	changes := make([]Change, 0)
	for identifier := range identifiers {
		oldKey, hadOld := previous[identifier]
		newKey, hasNew := current[identifier]
		if hadOld && hasNew && oldKey.encoded == newKey.encoded {
			continue
		}
		change := Change{}
		if hasNew {
			change.Endpoint = newKey.host
			change.Type = newKey.keyType
			change.CurrentFingerprint = newKey.fingerprint
		} else {
			change.Endpoint = oldKey.host
			change.Type = oldKey.keyType
		}
		if hadOld {
			change.PreviousFingerprint = oldKey.fingerprint
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Endpoint != changes[j].Endpoint {
			return changes[i].Endpoint < changes[j].Endpoint
		}
		return changes[i].Type < changes[j].Type
	})
	return changes
}

func inspectStatePath(knownHostsPath string) error {
	bootstrapState := filepath.Dir(knownHostsPath)
	stateRoot := filepath.Dir(bootstrapState)
	bareplaneRoot := filepath.Dir(stateRoot)
	for _, path := range []string{bareplaneRoot, stateRoot, bootstrapState} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect SSH trust state path %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: SSH trust state path %s must be a regular directory", ErrUnmanagedTrust, path)
		}
	}
	return nil
}

func ensureStateDirectories(knownHostsPath string) error {
	bootstrapState := filepath.Dir(knownHostsPath)
	stateRoot := filepath.Dir(bootstrapState)
	bareplaneRoot := filepath.Dir(stateRoot)
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{path: bareplaneRoot, mode: 0o755},
		{path: stateRoot, mode: privateDirectoryMode},
		{path: bootstrapState, mode: privateDirectoryMode},
	} {
		info, err := os.Lstat(directory.path)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%w: SSH trust state path %s must be a regular directory", ErrUnmanagedTrust, directory.path)
			}
			if directory.mode == privateDirectoryMode {
				if err := os.Chmod(directory.path, directory.mode); err != nil {
					return fmt.Errorf("secure SSH trust state directory %s: %w", directory.path, err)
				}
			}
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(directory.path, directory.mode); err != nil {
				return fmt.Errorf("create SSH trust state directory %s: %w", directory.path, err)
			}
		default:
			return fmt.Errorf("inspect SSH trust state directory %s: %w", directory.path, err)
		}
	}
	return nil
}

func writeManagedKnownHosts(path string, contents []byte, replacing bool) error {
	if err := ensureStateDirectories(path); err != nil {
		return err
	}
	if !replacing {
		return writeExclusiveFile(path, contents)
	}

	temporary, err := os.CreateTemp(filepath.Dir(path), ".known-hosts-stage-")
	if err != nil {
		return fmt.Errorf("create staged known_hosts: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(knownHostsFileMode); err != nil {
		return fmt.Errorf("secure staged known_hosts: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write staged known_hosts: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync staged known_hosts: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged known_hosts: %w", err)
	}

	backup, err := reserveBackupPath(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("stage previous known_hosts: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		rollbackErr := os.Rename(backup, path)
		if rollbackErr != nil {
			return fmt.Errorf("install known_hosts: %v; rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("install known_hosts: %w", err)
	}
	keepTemporary = false
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("remove previous known_hosts backup: %w", err)
	}
	return nil
}

func writeExclusiveFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, knownHostsFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrStateChanged
		}
		return fmt.Errorf("create known_hosts: %w", err)
	}
	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write known_hosts: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync known_hosts: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close known_hosts: %w", err)
	}
	removeOnError = false
	return nil
}

func reserveBackupPath(parent string) (string, error) {
	file, err := os.CreateTemp(parent, ".known-hosts-backup-")
	if err != nil {
		return "", fmt.Errorf("reserve known_hosts backup: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close known_hosts backup reservation: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("prepare known_hosts backup path: %w", err)
	}
	return path, nil
}
