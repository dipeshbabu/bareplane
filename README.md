# Bareplane

Provision Kubernetes on your own Proxmox infrastructure and hand platform configuration over to GitOps.

[![CI](https://github.com/dipeshbabu/bareplane/actions/workflows/ci.yml/badge.svg)](https://github.com/dipeshbabu/bareplane/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Bareplane brings infrastructure planning, Kubernetes bootstrap, and Argo CD handoff to your terminal. One `bareplane.yaml` describes your cluster. Terraform manages the VMs, embedded Ansible bootstraps Kubernetes with kube-vip and Cilium, and your Git repository owns platform configuration after handoff.

**Early development:** Proxmox is the only implemented provider. Run infrastructure and bootstrap workflows from a supported Linux or WSL controller. Read the [compatibility record](docs/compatibility.json) and [known limitations](docs/known-limitations.md) before using a real environment.

## Installation

Build from source with Go 1.23 or newer:

```bash
git clone https://github.com/dipeshbabu/bareplane.git
cd bareplane
go build -o bin/bareplane ./cmd/bareplane
./bin/bareplane version
```

Add the repository's `bin` directory to your `PATH` to use `bareplane` from your project directory. You can also run `go run ./cmd/bareplane` from this checkout.

Packaged releases and package-manager installation are planned. The commands below use a locally built binary. Infrastructure execution also requires Terraform and Proxmox credentials; bootstrap requires SSH, Ansible, and the supported controller tools. See [doctor](docs/doctor.md), [bootstrap doctor](docs/bootstrap.md), and the compatibility record for requirements.

## Create a project

```bash
mkdir my-cluster
cd my-cluster
bareplane init
bareplane validate
bareplane status
```

`init` writes a valid starter configuration and refuses to overwrite existing files. Edit `bareplane.yaml` with your Proxmox endpoint, placement targets, image, resources, and SSH settings before provisioning. The starter is a configuration example; it does not create infrastructure.

Start with the [configuration guide](docs/configuration.md) and [example configuration](examples/bareplane.yaml). For the full Kubernetes-to-GitOps path, adapt the [acceptance example](examples/acceptance.yaml), including explicit machine addresses, a free API VIP, Kubernetes settings, and your public GitOps repository. Keep API credentials in the documented [environment variables](docs/proxmox.md).

## Provision infrastructure

Inspect provider readiness and compare the desired machines with existing Proxmox guests:

```bash
bareplane doctor
bareplane plan
```

Render Terraform, review the saved provider-backed plan, and apply it with your exact configured cluster name:

```bash
bareplane render
bareplane terraform plan
bareplane terraform apply --approve my-cluster
```

Replace `my-cluster` with `metadata.name` from your configuration. `render` writes deterministic output to `.bareplane/terraform`. `terraform plan` creates a saved plan and SHA-256 attestation in private project state. Apply verifies the current inputs and attestation, then invalidates the attestation before mutation. Run a new plan before every further apply.

See [read-only planning](docs/plan.md), [Terraform rendering](docs/render.md), [saved plans](docs/terraform-plan.md), and [guarded apply](docs/terraform-apply.md).

## Bootstrap Kubernetes

After provisioning the machines and configuring their SSH address mapping:

```bash
bareplane bootstrap render
bareplane bootstrap doctor
bareplane bootstrap check
bareplane bootstrap trust
bareplane bootstrap preflight
bareplane bootstrap apply --approve my-cluster
```

Review SSH host fingerprints against the machine consoles before accepting them in `bootstrap trust`. Fresh-host preflight verifies authenticated remote prerequisites. Apply runs the owned host, Kubernetes, networking, join, kubeconfig, and health phases, recording progress for safe retries.

Bareplane retains ownership of Kubernetes bootstrap, kube-vip, and Cilium. Project credentials and execution state stay under `.bareplane/`; keep that directory private and out of Git. See the [bootstrap guide](docs/bootstrap.md), [health checks](docs/bootstrap-health.md), and [recovery procedures](docs/bootstrap-recovery.md).

## Hand over to GitOps

Configure `spec.gitops` with an anonymous HTTPS repository, revision, and root path, then render a public export:

```bash
bareplane gitops render
```

Review the generated `gitops/` payload, copy it into your configured repository at the configured root path, and commit and publish it yourself. Rendering preserves edited or unmanaged output and performs no Git push or Kubernetes mutation.

Once the reviewed payload is available remotely:

```bash
bareplane gitops install --approve my-cluster
bareplane gitops handoff --approve my-cluster
bareplane status
```

Install verifies fresh bootstrap health and installs the minimal pinned Argo CD control plane. Handoff verifies the published snapshot and Argo reconciliation, then transfers steady-state platform ownership to your Git repository. `status` reports local success records; use the documented health checks for live readiness.

See the [GitOps contract](docs/gitops.md), [installation](docs/gitops-install.md), and [handoff](docs/gitops-handoff.md).

## Select platform components

The `minimal` profile starts with Argo CD. Explicit component selections can add implemented integrations with their required configuration:

| Capability | Guide |
| --- | --- |
| Certificates and issuers | [cert-manager](docs/certificates.md) |
| Resource metrics | [Metrics Server](docs/metrics-server.md), [kubelet serving TLS](docs/kubelet-tls.md) |
| DNS records | [External DNS](docs/dns.md) |
| Encrypted secret delivery | [SOPS](docs/sops.md) |
| External secret delivery | [Vault integration](docs/vault.md) |
| Bounded metrics and alerts | [Observability](docs/observability.md) |
| Retained static local volumes | [Storage](docs/storage.md) |

See [component selection](docs/component-selection.md) for dependency and configuration rules. AI/GPU and data profiles, broad storage systems, upgrades, and release distribution remain future work.

## Command reference

```bash
bareplane help
bareplane terraform help
bareplane bootstrap help
bareplane gitops help
```

Commands accept an optional configuration path; the default is `bareplane.yaml` in the current directory. Mutating workflows require exact cluster-name approval. Bareplane refuses unmanaged resources, stale saved plans, and implicit ownership adoption.

For details, see [configuration](docs/configuration.md), [status](docs/status.md), [ownership](docs/ownership.md), and [architecture](docs/architecture.md).

## Development

```bash
make check
make build
```

`make check` verifies Go formatting, runs vet and tests, and builds all packages. CI also exercises generated Terraform and GitOps output, Python/Ansible checks, and disposable Kubernetes VMs. The [acceptance baseline](docs/acceptance.md) describes the separate operator-run Proxmox procedure required for release certification.

Keep fixes focused and include regression coverage. See [CONTRIBUTING.md](CONTRIBUTING.md) and the [release checklist](docs/release-checklist.md).

## License

[MIT](LICENSE)

