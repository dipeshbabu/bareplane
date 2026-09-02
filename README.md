# Bareplane

Build and operate a private Kubernetes platform on hardware you own.

Bareplane is an opinionated, GitOps-first platform for provisioning and managing Kubernetes, AI, and data infrastructure across self-hosted environments.

> Status: early development. Infrastructure discovery and planning are read only; Bareplane does not mutate Proxmox resources yet.

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

The first target is Proxmox-backed Kubernetes. Provider-specific behavior remains isolated so additional environments can be added without rewriting the CLI or configuration model.

## CLI

```bash
go run ./cmd/bareplane init
go run ./cmd/bareplane validate
go run ./cmd/bareplane doctor
go run ./cmd/bareplane plan
go run ./cmd/bareplane version
```

`init` creates a safe starter configuration without overwriting existing files. `validate` checks the strict configuration contract. `doctor` verifies local tooling, provider configuration, credentials, and Proxmox reachability. `plan` discovers current guests and prints a read-only, ownership-aware infrastructure plan.

See [docs/configuration.md](docs/configuration.md), [docs/doctor.md](docs/doctor.md), [docs/topology.md](docs/topology.md), [docs/ownership.md](docs/ownership.md), [docs/proxmox.md](docs/proxmox.md), and [docs/plan.md](docs/plan.md) for the current contracts.

## Development

Requires Go 1.23 or newer.

```bash
make check
make build
```

See [docs/architecture.md](docs/architecture.md) for the ownership model and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance.

## License

MIT
