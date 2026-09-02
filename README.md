# Bareplane

Build and operate a private Kubernetes platform on hardware you own.

Bareplane is an opinionated, GitOps-first platform for provisioning and managing Kubernetes, AI, and data infrastructure across self-hosted environments.

> Status: early development. The initial implementation is intentionally small while the configuration and provider contracts stabilize.

## Direction

Bareplane is designed around one user-owned configuration file and a clear ownership model:

```text
bareplane.yaml
      |
      v
Bareplane CLI
      |
      +--> infrastructure provider
      +--> Kubernetes bootstrap
      +--> GitOps desired state
```

The first target is Proxmox-backed Kubernetes. Provider-specific behavior will remain isolated so additional environments can be added without rewriting the CLI or configuration model.

## CLI

```bash
go run ./cmd/bareplane --help
go run ./cmd/bareplane version
```

Planned commands include `init`, `validate`, `doctor`, `bootstrap`, `status`, and profile or application management.

## Development

Requires Go 1.25 or newer.

```bash
make check
make build
```

See [docs/architecture.md](docs/architecture.md) for the ownership model and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance.

## License

MIT
