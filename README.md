# Bareplane

Build and operate a private Kubernetes platform on hardware you own — without ever mutating infrastructure you did not explicitly approve.

## The guarantee

Bareplane does not change anything on trust. Every stage that can mutate infrastructure is gated, and every gate fails closed:

- **Apply accepts only an attested plan.** `terraform plan` writes a saved plan alongside a SHA-256 attestation. `terraform apply` verifies that attestation immediately before mutation, accepts nothing else, and invalidates it before the first change — so a partial or failed apply can never be silently replayed.
- **Approval is a name, not a flag.** Mutating commands require `--approve <cluster-name>` matching the cluster in your configuration. There is no `--yes` and no auto-approve.
- **It refuses what it does not own.** Unexpected Proxmox guests, unmanaged generated Terraform, and unmanaged Argo resources are errors. Bareplane will not adopt them, and it will not compete with a reconciler that already owns them.
- **Host identities are trusted explicitly.** `bootstrap trust` records only SSH host keys you reviewed out of band against the machine consoles. Nothing pipes automatic approval into host trust.
- **`status` reports records, not guesses.** It reads local lifecycle state and says so. It never claims a live cluster audit it did not perform, and it never asserts that an attested plan is still current — `apply` remains the authoritative verifier.

When a command cannot prove it is safe to proceed, it stops and names what is missing. Read [known limitations](docs/known-limitations.md) before you decide whether that trade is the one you want.

## How it works

One user-owned configuration file drives the whole lifecycle, with a clear ownership boundary at each stage:

```text
bareplane.yaml
      |
      v
Bareplane CLI
      |
      +--> provider layer     --> infrastructure      (Bareplane owns)
      +--> deterministic Terraform
      +--> bootstrap layer    --> Kubernetes          (Bareplane owns)
      +--> GitOps renderer    --> desired state       (you own, via Argo)
```

Bareplane keeps ownership of infrastructure, Kubernetes bootstrap, kube-vip, and Cilium. It installs only the minimal Argo CD resources needed to hand over. After handoff, your Git repository is authoritative and Bareplane stops competing for that state. See [docs/architecture.md](docs/architecture.md) and the [user-owned GitOps contract](docs/gitops.md).

Provider-specific behavior stays behind provider boundaries, so additional environments can be added without rewriting the CLI or the configuration model.

## Scope

Bareplane is in early development. The v0.1 path is deliberately narrow, and the [component registry](docs/platform-graph.md) gates everything else as unavailable rather than shipping it half-built.

**Implemented and covered by the [acceptance baseline](docs/acceptance.md):** Proxmox infrastructure, Linux/kubeadm/Cilium bootstrap, private cluster access and health, minimal Argo CD, verified GitOps handoff, and the opt-in cert-manager, kubelet serving TLS, metrics-server, external-dns, and SOPS secret delivery components.

**Not implemented.** GPU and AI capabilities, stateful data services, and persistent storage are present in the registry as dependency-ordered entries with an `unavailable` status. They are a plan, not a feature list. Proxmox is the only provider; re-running installation is not an upgrade or node-replacement workflow; initial GitOps supports anonymous HTTPS repositories only. The [full list](docs/known-limitations.md) is maintained deliberately and is not a disclaimer.

## Lifecycle

Build the binary with `make build`, or substitute `go run ./cmd/bareplane` for `bareplane` below.

**Set up and inspect** — nothing here mutates anything.

```bash
bareplane init                  # starter config; never overwrites existing files
bareplane validate              # strict configuration contract
bareplane doctor                # local tooling, credentials, Proxmox reachability
bareplane status                # local lifecycle state and the safest next command
bareplane plan                  # ownership-aware desired-vs-observed, no Terraform
```

**Provision infrastructure** — the attestation gate.

```bash
bareplane render                          # deterministic Terraform in .bareplane/terraform
bareplane terraform plan                  # provider-backed saved plan + SHA-256 attestation
bareplane terraform apply --approve <cluster-name>
```

**Bootstrap Kubernetes** — the host trust gate.

```bash
bareplane bootstrap render                # deterministic Ansible inventory
bareplane bootstrap doctor                # local readiness
bareplane bootstrap check                 # remote reachability
bareplane bootstrap trust                 # record reviewed SSH host identities
bareplane bootstrap preflight             # authenticated fresh-host verification
bareplane bootstrap apply --approve <cluster-name>
```

Recovery and diagnostics: `bootstrap diagnose`, `bootstrap recover-kubeconfig`, `bootstrap kubelet-tls`, and `bootstrap reset --approve <cluster-name>`, which is limited to an owned bootstrap-only cluster and refuses application- or storage-bearing state. See [bootstrap recovery](docs/bootstrap-recovery.md).

**Hand over to GitOps** — the ownership transfer gate.

```bash
bareplane gitops render                   # public repository export under gitops/
bareplane gitops install --approve <cluster-name>
bareplane gitops handoff --approve <cluster-name>
```

`gitops render` produces a reviewable export with no cluster mutation, Git commit, or push, and preserves edited or unmanaged output. `gitops install` requires completed bootstrap, fresh health, a reviewed export, and exact approval. `gitops handoff` verifies a published snapshot, pins initial root and child reconciliation, then transfers steady-state ownership to Git.

## Contracts

Each command's behavior is specified rather than inferred:

| Area | Documents |
| --- | --- |
| Configuration | [configuration](docs/configuration.md) · [topology](docs/topology.md) · [component selection](docs/component-selection.md) |
| Ownership | [ownership](docs/ownership.md) · [architecture](docs/architecture.md) · [platform graph](docs/platform-graph.md) |
| Provider | [providers](docs/providers.md) · [proxmox](docs/proxmox.md) · [doctor](docs/doctor.md) · [plan](docs/plan.md) |
| Terraform | [render](docs/render.md) · [workspace](docs/terraform-workspace.md) · [plan](docs/terraform-plan.md) · [apply](docs/terraform-apply.md) |
| Bootstrap | [bootstrap](docs/bootstrap.md) · [preflight](docs/bootstrap-preflight.md) · [apply](docs/bootstrap-apply.md) · [health](docs/bootstrap-health.md) · [recovery](docs/bootstrap-recovery.md) · [kubernetes](docs/kubernetes.md) |
| GitOps | [contract](docs/gitops.md) · [render](docs/gitops-render.md) · [install](docs/gitops-install.md) · [handoff](docs/gitops-handoff.md) · [status](docs/status.md) |
| Components | [certificates](docs/certificates.md) · [kubelet TLS](docs/kubelet-tls.md) · [metrics server](docs/metrics-server.md) · [DNS](docs/dns.md) · [SOPS](docs/sops.md) |

## Development

Requires Go 1.23 or newer. One direct dependency.

```bash
make check
make build
```

The [v0.1 acceptance baseline](docs/acceptance.md) combines a fast mocked full lifecycle, real disposable Kubernetes and Argo jobs, and a separate operator-run Proxmox release-candidate procedure. Hosted CI is not evidence that a physical Proxmox environment passed. Check the [release checklist](docs/release-checklist.md), [compatibility record](docs/compatibility.json), and [known limitations](docs/known-limitations.md) before calling a candidate release-ready.

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance.

## License

MIT
