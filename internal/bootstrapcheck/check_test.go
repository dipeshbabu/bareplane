package bootstrapcheck

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dipeshbabu/bareplane/internal/doctor"
)

type fakeConn struct {
	reader      io.Reader
	deadlineErr error
	deadline    time.Time
	closed      bool
}

func (c *fakeConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *fakeConn) Close() error               { c.closed = true; return nil }
func (c *fakeConn) SetReadDeadline(deadline time.Time) error {
	c.deadline = deadline
	return c.deadlineErr
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestCheckProbesMachinesInDeterministicOrder(t *testing.T) {
	configPath := writeCheckConfig(t, 0)
	var addresses []string
	var connections []*fakeConn
	report := Check(context.Background(), Options{
		ConfigPath: configPath,
		Dial: func(_ context.Context, network, address string) (Conn, error) {
			if network != "tcp" {
				t.Fatalf("network = %q", network)
			}
			addresses = append(addresses, address)
			conn := &fakeConn{reader: bytes.NewReader([]byte("SSH-2.0-OpenSSH_9.6\r\n"))}
			connections = append(connections, conn)
			return conn, nil
		},
	})
	if report.HasFailures() {
		t.Fatalf("unexpected failures: %#v", report.Results)
	}
	wantAddresses := []string{"10.0.0.10:22", "[2001:db8::20]:22", "worker.example.com:22"}
	if !reflect.DeepEqual(addresses, wantAddresses) {
		t.Fatalf("addresses = %#v, want %#v", addresses, wantAddresses)
	}
	wantNames := []string{"lab-control-1", "lab-worker-1", "lab-worker-2"}
	for i, result := range report.Results {
		if result.Name != wantNames[i] || result.Status != doctor.StatusPass {
			t.Fatalf("result[%d] = %#v", i, result)
		}
	}
	for i, conn := range connections {
		if !conn.closed {
			t.Fatalf("connection %d was not closed", i)
		}
		if conn.deadline.IsZero() {
			t.Fatalf("connection %d did not receive a read deadline", i)
		}
	}
}

func TestCheckUsesCustomPort(t *testing.T) {
	configPath := writeCheckConfig(t, 2222)
	var addresses []string
	report := Check(context.Background(), Options{
		ConfigPath: configPath,
		Dial: func(_ context.Context, _, address string) (Conn, error) {
			addresses = append(addresses, address)
			return &fakeConn{reader: bytes.NewReader([]byte("SSH-1.99-compatible\r\n"))}, nil
		},
	})
	if report.HasFailures() {
		t.Fatalf("unexpected failures: %#v", report.Results)
	}
	for _, address := range addresses {
		if !strings.HasSuffix(address, ":2222") {
			t.Fatalf("custom port not used: %q", address)
		}
	}
}

func TestCheckUsesOneDeadlineForDialAndBannerRead(t *testing.T) {
	configPath := writeCheckConfig(t, 0)
	var dialDeadlines []time.Time
	var connections []*fakeConn
	report := Check(context.Background(), Options{
		ConfigPath: configPath,
		Timeout:    time.Second,
		Dial: func(ctx context.Context, _, _ string) (Conn, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("dial context has no deadline")
			}
			dialDeadlines = append(dialDeadlines, deadline)
			conn := &fakeConn{reader: bytes.NewReader([]byte("SSH-2.0-test\r\n"))}
			connections = append(connections, conn)
			return conn, nil
		},
	})
	if report.HasFailures() {
		t.Fatalf("unexpected failures: %#v", report.Results)
	}
	for i, conn := range connections {
		if !conn.deadline.Equal(dialDeadlines[i]) {
			t.Fatalf("connection %d read deadline %v differs from dial deadline %v", i, conn.deadline, dialDeadlines[i])
		}
	}
}

func TestCheckReportsConnectionFailureWithoutDetails(t *testing.T) {
	configPath := writeCheckConfig(t, 0)
	report := Check(context.Background(), Options{
		ConfigPath: configPath,
		Dial: func(context.Context, string, string) (Conn, error) {
			return nil, errors.New("secret transport detail")
		},
	})
	if len(report.Results) != 3 {
		t.Fatalf("results = %d", len(report.Results))
	}
	for _, result := range report.Results {
		if result.Status != doctor.StatusFail || result.Message != "SSH service is unreachable" {
			t.Fatalf("unexpected result: %#v", result)
		}
		if strings.Contains(result.Message, "secret transport detail") {
			t.Fatal("transport error detail leaked")
		}
	}
}

func TestReadSSHBannerRejectsInvalidResponses(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"non ssh":     "HTTP/1.1 200 OK\r\n",
		"lf only":     "SSH-2.0-test\n",
		"extra cr":    "SSH-2.0-test\r\r\n",
		"truncated":   "SSH-2.0-test",
		"oversized":   strings.Repeat("A", MaxBannerBytes),
		"unsupported": "SSH-1.5-old\r\n",
	}
	for name, banner := range cases {
		t.Run(name, func(t *testing.T) {
			if err := readSSHBanner(strings.NewReader(banner)); err == nil {
				t.Fatalf("expected invalid banner %q to fail", banner)
			}
		})
	}
}

func TestReadSSHBannerAcceptsMaximumLengthIdentification(t *testing.T) {
	banner := "SSH-2.0-" + strings.Repeat("A", MaxBannerBytes-len("SSH-2.0-\r\n")) + "\r\n"
	if len(banner) != MaxBannerBytes {
		t.Fatalf("test banner length = %d", len(banner))
	}
	if err := readSSHBanner(strings.NewReader(banner)); err != nil {
		t.Fatalf("maximum-length identification rejected: %v", err)
	}
}

func TestCheckReportsBannerReadFailureWithoutDetails(t *testing.T) {
	configPath := writeCheckConfig(t, 0)
	var connections []*fakeConn
	report := Check(context.Background(), Options{
		ConfigPath: configPath,
		Dial: func(context.Context, string, string) (Conn, error) {
			conn := &fakeConn{reader: failingReader{err: errors.New("secret read detail")}}
			connections = append(connections, conn)
			return conn, nil
		},
	})
	for _, result := range report.Results {
		if result.Status != doctor.StatusFail || result.Message != "reachable endpoint did not present a valid SSH identification line" {
			t.Fatalf("unexpected result: %#v", result)
		}
		if strings.Contains(result.Message, "secret read detail") {
			t.Fatal("read error detail leaked")
		}
	}
	for i, conn := range connections {
		if !conn.closed {
			t.Fatalf("connection %d was not closed after a read failure", i)
		}
	}
}

func TestCheckReportsReadDeadlineFailure(t *testing.T) {
	configPath := writeCheckConfig(t, 0)
	var connections []*fakeConn
	report := Check(context.Background(), Options{
		ConfigPath: configPath,
		Dial: func(context.Context, string, string) (Conn, error) {
			conn := &fakeConn{reader: bytes.NewReader([]byte("SSH-2.0-test\r\n")), deadlineErr: errors.New("deadline")}
			connections = append(connections, conn)
			return conn, nil
		},
	})
	for _, result := range report.Results {
		if result.Status != doctor.StatusFail || result.Message != "could not set SSH banner read deadline" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	for i, conn := range connections {
		if !conn.closed {
			t.Fatalf("connection %d was not closed after a deadline failure", i)
		}
	}
}

func TestCheckInvalidBootstrapDoesNotDial(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bareplane.yaml")
	content := `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: lab
spec:
  domain: lab.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
  nodes:
    - name: control
      role: control-plane
      count: 1
      cpu: 4
      memoryGB: 8
      diskGB: 64
  features:
    observability: true
    gpu: false
  profiles: [minimal]
  dns:
    provider: manual
  secrets:
    provider: sops
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	report := Check(context.Background(), Options{ConfigPath: path, Dial: func(context.Context, string, string) (Conn, error) {
		called = true
		return nil, nil
	}})
	if called || len(report.Results) != 1 || report.Results[0].Name != "bootstrap-config" || report.Results[0].Status != doctor.StatusFail {
		t.Fatalf("unexpected invalid-config behavior: called=%t report=%#v", called, report.Results)
	}
}

func TestDefaultDialerCanReadLocalSSHBanner(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_, _ = conn.Write([]byte("SSH-2.0-test-server\r\n"))
			_ = conn.Close()
		}
		close(accepted)
	}()

	dialer := &net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := readSSHBanner(conn); err != nil {
		t.Fatal(err)
	}
	<-accepted
}

func writeCheckConfig(t *testing.T, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	portLine := ""
	if port != 0 {
		portLine = "      port: " + strconv.Itoa(port) + "\n"
	}
	content := `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: lab
spec:
  domain: lab.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
  nodes:
    - name: worker
      role: worker
      count: 2
      cpu: 8
      memoryGB: 16
      diskGB: 128
    - name: control
      role: control-plane
      count: 1
      cpu: 4
      memoryGB: 8
      diskGB: 64
  bootstrap:
    ssh:
      user: debian
      privateKeyFile: ~/.ssh/id_ed25519
` + portLine + `      hosts:
        lab-worker-2: worker.example.com
        lab-control-1: 10.0.0.10
        lab-worker-1: 2001:db8::20
  features:
    observability: true
    gpu: false
  profiles: [minimal]
  dns:
    provider: manual
  secrets:
    provider: sops
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
