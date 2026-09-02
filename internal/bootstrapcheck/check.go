package bootstrapcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

const (
	DefaultTimeout  = 5 * time.Second
	MaxBannerBytes  = 255
	sshProtocol2    = "SSH-2.0-"
	sshProtocol199  = "SSH-1.99-"
)

type Conn interface {
	Read([]byte) (int, error)
	Close() error
	SetReadDeadline(time.Time) error
}

type DialFunc func(context.Context, string, string) (Conn, error)

type Options struct {
	ConfigPath string
	Timeout    time.Duration
	Dial       DialFunc
}

func Check(ctx context.Context, options Options) doctor.Report {
	if strings.TrimSpace(options.ConfigPath) == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Dial == nil {
		dialer := &net.Dialer{Timeout: options.Timeout}
		options.Dial = func(ctx context.Context, network, address string) (Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}
	}

	cfg, err := loadConfig(options.ConfigPath)
	if err != nil {
		return doctor.Report{Results: []doctor.Result{{
			Name:    "bootstrap-config",
			Status:  doctor.StatusFail,
			Message: err.Error(),
		}}}
	}

	topo, err := topology.Build(cfg)
	if err != nil {
		return doctor.Report{Results: []doctor.Result{{
			Name:    "topology",
			Status:  doctor.StatusFail,
			Message: err.Error(),
		}}}
	}
	machines := append([]topology.Machine(nil), topo.Machines...)
	sort.Slice(machines, func(i, j int) bool { return machines[i].Name < machines[j].Name })

	results := make([]doctor.Result, 0, len(machines))
	port := cfg.Spec.Bootstrap.SSH.EffectivePort()
	for _, machine := range machines {
		if err := ctx.Err(); err != nil {
			results = append(results, doctor.Result{Name: machine.Name, Status: doctor.StatusFail, Message: err.Error()})
			break
		}
		host := cfg.Spec.Bootstrap.SSH.Hosts[machine.Name]
		results = append(results, probeMachine(ctx, options.Dial, machine.Name, host, port, options.Timeout))
	}
	return doctor.Report{Results: results}
}

func loadConfig(path string) (config.Config, error) {
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

func probeMachine(ctx context.Context, dial DialFunc, name, host string, port int, timeout time.Duration) doctor.Result {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dial(probeCtx, "tcp", address)
	if err != nil {
		return doctor.Result{Name: name, Status: doctor.StatusFail, Message: "SSH service is unreachable"}
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return doctor.Result{Name: name, Status: doctor.StatusFail, Message: "could not set SSH banner read deadline"}
	}
	if err := readSSHBanner(conn); err != nil {
		return doctor.Result{Name: name, Status: doctor.StatusFail, Message: "reachable endpoint did not present a valid SSH identification line"}
	}
	return doctor.Result{Name: name, Status: doctor.StatusPass, Message: "SSH service is reachable"}
}

func readSSHBanner(reader io.Reader) error {
	buffer := make([]byte, 0, MaxBannerBytes)
	one := []byte{0}
	for len(buffer) < MaxBannerBytes {
		n, err := reader.Read(one)
		if n > 0 {
			buffer = append(buffer, one[0])
			if one[0] == '\n' {
				if len(buffer) < 2 || buffer[len(buffer)-2] != '\r' {
					return errors.New("SSH identification line must end with CRLF")
				}
				line := string(buffer[:len(buffer)-2])
				if strings.HasPrefix(line, sshProtocol2) || strings.HasPrefix(line, sshProtocol199) {
					return nil
				}
				return errors.New("unsupported SSH identification")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("SSH identification ended before CRLF")
			}
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return fmt.Errorf("SSH identification exceeds %d bytes", MaxBannerBytes)
}
