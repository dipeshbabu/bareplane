package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider"
)

type LookPathFunc func(string) (string, error)
type ProviderProbe func(context.Context, config.Config) Result

type Options struct {
	ConfigPath    string
	LookPath      LookPathFunc
	Registry      *provider.Registry
	ProviderProbe ProviderProbe
}

func Checks(opts Options) ([]Check, error) {
	if opts.ConfigPath == "" {
		return nil, fmt.Errorf("configuration path is empty")
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Registry == nil {
		return nil, fmt.Errorf("provider registry is nil")
	}

	source := &configSource{path: opts.ConfigPath}
	checks := []Check{
		configCheck(source),
		providerCheck(source, opts.Registry),
	}
	if opts.ProviderProbe != nil {
		checks = append(checks, providerRuntimeCheck(source, opts.ProviderProbe))
	}
	checks = append(checks,
		executableCheck("terraform", true, opts.LookPath),
		executableCheck("ansible-playbook", true, opts.LookPath),
		executableCheck("kubectl", true, opts.LookPath),
		executableCheck("helm", false, opts.LookPath),
	)
	return checks, nil
}

type configSource struct {
	path string
	once sync.Once
	cfg  config.Config
	err  error
}

func (s *configSource) load() (config.Config, error) {
	s.once.Do(func() {
		file, err := os.Open(s.path)
		if err != nil {
			s.err = fmt.Errorf("open %s: %w", s.path, err)
			return
		}
		defer file.Close()

		s.cfg, s.err = config.Load(file)
		if s.err != nil {
			s.err = fmt.Errorf("load %s: %w", s.path, s.err)
		}
	})
	return s.cfg, s.err
}

func configCheck(source *configSource) Check {
	return CheckFunc(func(context.Context) Result {
		cfg, err := source.load()
		if err != nil {
			return Result{Name: "configuration", Status: StatusFail, Message: err.Error()}
		}
		return Result{
			Name:    "configuration",
			Status:  StatusPass,
			Message: fmt.Sprintf("valid cluster %q", cfg.Metadata.Name),
		}
	})
}

func providerCheck(source *configSource, registry *provider.Registry) Check {
	return CheckFunc(func(context.Context) Result {
		cfg, err := source.load()
		if err != nil {
			return Result{
				Name:    "provider",
				Status:  StatusWarn,
				Message: "skipped because configuration is invalid",
			}
		}

		resolved, err := registry.Resolve(cfg.Spec.Provider)
		if err != nil {
			return Result{Name: "provider", Status: StatusFail, Message: err.Error()}
		}
		return Result{
			Name:    "provider",
			Status:  StatusPass,
			Message: fmt.Sprintf("%s configuration is valid", resolved.Type()),
		}
	})
}

func providerRuntimeCheck(source *configSource, probe ProviderProbe) Check {
	return CheckFunc(func(ctx context.Context) Result {
		cfg, err := source.load()
		if err != nil {
			return Result{
				Name:    "provider runtime",
				Status:  StatusWarn,
				Message: "skipped because configuration is invalid",
			}
		}
		return probe(ctx, cfg)
	})
}

func executableCheck(name string, required bool, lookPath LookPathFunc) Check {
	return CheckFunc(func(context.Context) Result {
		path, err := lookPath(name)
		if err == nil {
			return Result{Name: name, Status: StatusPass, Message: path}
		}
		if required {
			return Result{
				Name:    name,
				Status:  StatusFail,
				Message: fmt.Sprintf("required executable %q was not found in PATH", name),
			}
		}
		return Result{
			Name:    name,
			Status:  StatusWarn,
			Message: fmt.Sprintf("optional executable %q was not found in PATH", name),
		}
	})
}
