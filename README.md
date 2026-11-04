<p align="center">
  <a href="https://github.com/dipeshbabu/bareplane">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="./docs/assets/bareplane-wordmark-dark.svg">
      <img src="./docs/assets/bareplane-wordmark-light.svg" alt="Bareplane CLI" width="360">
    </picture>
  </a>
</p>

<p align="center">
  Build Kubernetes on your own infrastructure, from your terminal.
</p>

<p align="center">
  <a href="https://github.com/dipeshbabu/bareplane/actions/workflows/ci.yml"><img alt="Build" src="https://img.shields.io/github/actions/workflow/status/dipeshbabu/bareplane/ci.yml?branch=main&amp;label=build&amp;style=flat-square&amp;color=2563EB"></a>
  <a href="./go.mod"><img alt="Go 1.23+" src="https://img.shields.io/badge/go-1.23%2B-2563EB?style=flat-square"></a>
  <a href="./LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-2563EB?style=flat-square"></a>
</p>

---

Bareplane brings Proxmox provisioning, Kubernetes bootstrap, and GitOps handoff to your terminal. Define your cluster in `bareplane.yaml`, review infrastructure changes, bootstrap Kubernetes with kube-vip and Cilium, and hand platform configuration over to Argo CD.

## Installation

Build from source with Go 1.23 or newer:

```sh
git clone https://github.com/dipeshbabu/bareplane.git
cd bareplane
go build -o bin/bareplane ./cmd/bareplane
./bin/bareplane version
```

Add `bin` to your `PATH` to use `bareplane` from your project directory. From this checkout, you can also use `go run ./cmd/bareplane`.

Bareplane is in early development. Packaged releases are planned. Infrastructure and bootstrap execution require a supported Linux or WSL controller; check the [toolchain requirements](docs/compatibility.json) and [known limitations](docs/known-limitations.md).

## Create A Project

Create a workspace and validate its starter configuration:

```sh
mkdir my-cluster
cd my-cluster
bareplane init
bareplane validate
bareplane status
```

`init` creates `bareplane.yaml` without overwriting existing files. Configure your Proxmox endpoint, targets, image, and SSH settings before provisioning. Use the [configuration guide](docs/configuration.md) and [complete example](examples/acceptance.yaml) for Kubernetes settings, explicit machine addresses, and an unused API VIP.

The examples below use the starter cluster name, `bareplane`. Replace it with your configured `metadata.name` when approving changes.

## Provision Infrastructure

Set the documented [Proxmox credentials](docs/proxmox.md), inspect existing guests, and review a saved Terraform plan before applying it:

```sh
bareplane doctor
bareplane plan
bareplane render
bareplane terraform plan
bareplane terraform apply --approve bareplane
```

Apply accepts only the current attested saved plan and requires exact cluster-name approval. It invalidates the attestation before mutation; run a new plan before every further apply. See [planning](docs/plan.md) and [guarded apply](docs/terraform-apply.md).

## Bootstrap Kubernetes

Provision your machines, configure their SSH addresses, then bootstrap the cluster:

```sh
bareplane bootstrap render
bareplane bootstrap doctor
bareplane bootstrap check
bareplane bootstrap trust
bareplane bootstrap preflight
bareplane bootstrap apply --approve bareplane
```

Review SSH fingerprints against the machine consoles before trusting them. Bootstrap manages host preparation, Kubernetes, kube-vip, Cilium, node joins, private kubeconfig, and health checks. See the [bootstrap guide](docs/bootstrap.md) and [recovery procedures](docs/bootstrap-recovery.md).

## Hand Over To GitOps

Configure your public HTTPS repository, revision, and root path in `spec.gitops`, then generate its payload:

```sh
bareplane gitops render
```

Review `gitops/`, copy it to your configured repository root, and commit and publish it yourself. Once it is available remotely:

```sh
bareplane gitops install --approve bareplane
bareplane gitops handoff --approve bareplane
bareplane status
```

Argo CD takes steady-state ownership of platform configuration after verified handoff. Bareplane retains infrastructure and Kubernetes bootstrap ownership. Keep private `.bareplane/` state out of Git. See [GitOps setup](docs/gitops.md) and [handoff](docs/gitops-handoff.md).

## Add Platform Components

The `minimal` profile starts with Argo CD. Use [explicit component selections](docs/component-selection.md) to add implemented integrations:

- [cert-manager](docs/certificates.md), [Metrics Server](docs/metrics-server.md), and [External DNS](docs/dns.md)
- [SOPS](docs/sops.md) and [external Vault](docs/vault.md) secret delivery
- [Observability](docs/observability.md) and [retained local storage](docs/storage.md)

Each integration has configuration and dependency requirements. AI/GPU and data profiles, broad storage systems, and upgrades remain future work.

The [core convergence guide](docs/core-profile.md) covers the tested combination of certificates, metrics, observability and retained local storage.

## Reference

Explore the available commands:

```sh
bareplane --help
bareplane terraform --help
bareplane bootstrap --help
bareplane gitops --help
```

Commands accept an optional configuration path and otherwise use `bareplane.yaml`. `status` reports local lifecycle records; use the [health checks](docs/bootstrap-health.md) for live readiness.

- [Configuration](docs/configuration.md)
- [Project status](docs/status.md)
- [Ownership model](docs/ownership.md)
- [Architecture](docs/architecture.md)

## Developing

Run the repository checks and build the CLI:

```sh
make check
make build
```

Useful source entry points:

| Path | Purpose |
| --- | --- |
| `cmd/bareplane` | CLI entry point |
| `internal/cli` | Commands and lifecycle orchestration |
| `internal/provider` | Infrastructure provider boundaries |
| `internal/render` | Terraform, Ansible, and GitOps generation |

CI checks Go, generated configuration, and disposable Kubernetes VMs. The [acceptance baseline](docs/acceptance.md) describes the separate Proxmox procedure required for release certification.

## Contributing

Keep pull requests focused and include regression coverage for bug fixes. Run `make check` before opening a PR. See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow.

## License

[MIT](LICENSE)
