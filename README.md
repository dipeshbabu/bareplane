# Bareplane

Build and operate a private Kubernetes platform on hardware you own.

Bareplane is an opinionated, GitOps-first platform for provisioning and managing Kubernetes, AI, and data infrastructure across self-hosted environments.

> Status: early development. Bareplane can discover Proxmox resources, build provider-native read-only plans, render deterministic Terraform, create provider-backed saved plans, and apply only an attested saved plan after explicit cluster-name approval.

## Direction

Bareplane is designed around one user-owned configuration file and a clear ownership model:

```text
bareplane.yaml
      |
      v
Bareplane CLI
      |
      +--> infrastructure provider
      +--> deterministic Terraform
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
go run ./cmd/bareplane render
go run ./cmd/bareplane terraform plan
go run ./cmd/bareplane terraform apply --approve <cluster-name>
go run ./cmd/bareplane bootstrap render
go run ./cmd/bareplane bootstrap doctor
go run ./cmd/bareplane bootstrap check
go run ./cmd/bareplane bootstrap trust
go run ./cmd/bareplane version
```

`init` creates a safe starter configuration without overwriting existing files. `validate` checks the strict configuration contract. `doctor` verifies local tooling, provider configuration, credentials, and Proxmox reachability. `plan` directly discovers Proxmox guests and prints Bareplane's ownership-aware desired-versus-observed plan without Terraform. `render` produces deterministic Terraform in `.bareplane/terraform`. `terraform plan` uses that generated configuration and the persistent `.bareplane/state/terraform` workspace to create a real provider-backed saved plan plus a SHA-256 attestation. `terraform apply` accepts only that attested saved plan, requires exact cluster-name approval, and invalidates the attestation before mutation begins. Bootstrap commands render deterministic Ansible inventory, verify local and remote readiness, and persist only explicitly approved SSH host identities.

See [docs/configuration.md](docs/configuration.md), [docs/doctor.md](docs/doctor.md), [docs/topology.md](docs/topology.md), [docs/ownership.md](docs/ownership.md), [docs/proxmox.md](docs/proxmox.md), [docs/plan.md](docs/plan.md), [docs/render.md](docs/render.md), [docs/bootstrap.md](docs/bootstrap.md), [docs/kubernetes.md](docs/kubernetes.md), [docs/terraform-workspace.md](docs/terraform-workspace.md), [docs/terraform-plan.md](docs/terraform-plan.md), and [docs/terraform-apply.md](docs/terraform-apply.md) for the current contracts.

## Development

Requires Go 1.23 or newer.

```bash
make check
make build
```

See [docs/architecture.md](docs/architecture.md) for the ownership model and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance.

## License

MIT
