# Authenticated bootstrap preflight

`bareplane bootstrap preflight [path]` is the final read-only gate before Bareplane is allowed to prepare a host. Passing means Bareplane authenticated with the configured SSH key, verified the server through the project-specific known_hosts file, confirmed non-interactive sudo, and found every desired machine suitable for the v0.1 kubeadm and Cilium path.

Run the bootstrap sequence in order:

```bash
bareplane bootstrap doctor
bareplane bootstrap check
bareplane bootstrap trust
bareplane bootstrap preflight
```

The command requires `Config.ValidateKubernetesBootstrap()` to pass. It refuses to run without `.bareplane/state/bootstrap/known_hosts` or if that managed file fails its integrity, type, size, symlink, or permission checks. The configured private key must exist as a regular non-symlink file and be owner-only on POSIX systems; Bareplane never reads or prints its contents.

## SSH boundary

Bareplane invokes OpenSSH with:

- public-key-only, batch authentication;
- the configured identity as the only identity;
- `StrictHostKeyChecking=yes`;
- the Bareplane project known_hosts file as the trust source;
- global/user SSH configuration and global known_hosts fallback disabled;
- agent, X11, local-command, connection-sharing, and forwarding behavior disabled;
- one bounded connection attempt and an overall per-machine deadline;
- stderr suppression and a 32 KiB stdout ceiling.

The remote input is a fixed Bareplane-owned POSIX `sh` script. It performs only fact reads and `sudo -n true`; it does not install packages, write files, disable swap, restart services, initialize kubeadm, join a cluster, or change Kubernetes state. Server-supplied errors and raw remote output are not printed.

## Supported v0.1 hosts

| Property | Supported |
| --- | --- |
| Distribution | Debian 12 or 13; Ubuntu 22.04, 24.04, or 26.04 LTS |
| Architecture | amd64/x86_64 or arm64/aarch64 |
| Linux kernel | 5.10 or newer |
| Memory | At least 2 GiB |
| Free root disk | At least 10 GiB |
| CPU | At least 2 for control planes; at least 1 for workers |

The architecture and kernel floor follows [Cilium's system requirements](https://docs.cilium.io/en/stable/operations/system_requirements/). The control-plane CPU, memory, swap, and host uniqueness checks follow [kubeadm's installation prerequisites](https://kubernetes.io/docs/setup/production-environment/tools/kubeadm/install-kubeadm/). Bareplane intentionally supports a narrower distribution matrix so later host-preparation roles have one testable package and service contract.

## Collected facts

For each machine, Bareplane collects and validates:

- distribution ID and version;
- architecture and kernel release;
- hostname and default-route interface;
- online CPU count, total memory, and available root disk;
- configured swap and time-synchronization state;
- non-interactive sudo capability;
- existing `containerd`, `kubelet`, `kubeadm`, and `kubectl` commands;
- existing CNI state, Kubernetes PKI, `/etc/kubernetes`, kubelet kubeadm state, or etcd membership;
- PCI display-controller presence for GPU intent.

Facts use a versioned, fixed-field protocol. Unknown, duplicate, missing, malformed, unsafe, or oversized fields fail the machine rather than being ignored.

## Result policy

A machine fails when authentication or host verification fails, sudo is unavailable, its OS/architecture/kernel/resources are unsupported, swap is enabled, time synchronization is not confirmed, the default interface is missing, its hostname duplicates another desired machine, existing Kubernetes/CNI state is present, or requested GPU hardware is absent.

An already installed containerd or Kubernetes command is a warning at this stage because later ownership checks must inspect its exact version and configuration before mutation. Hardware capacity below the desired VM shape is also reported as a warning when the hard Kubernetes minimum still passes. Unexpected GPU hardware without GPU intent is a warning.

Results are emitted in stable machine-name order. PASS and WARN do not hide facts from another machine; any FAIL makes the command exit nonzero. Preflight never records lifecycle progress or mutates remote/local cluster state.
