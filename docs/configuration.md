# Configuration

Bareplane uses a single versioned `bareplane.yaml` file as the user-facing source of intent.

## Initialize

Create a starter configuration in the current directory:

```bash
bareplane init
```

Or choose an explicit path:

```bash
bareplane init clusters/dev/bareplane.yaml
```

Bareplane creates parent directories implied by the requested path, but it never overwrites an existing configuration. The generated file is a deterministic, minimal, valid configuration that can be checked immediately with `bareplane validate`.

## Contract

The current schema is `bareplane.io/v1alpha1` with kind `BareplaneCluster`.

Top-level fields are strict. Unknown fields fail validation instead of being ignored, so misspellings cannot silently change deployment behavior.

## Supported values

- Provider: `proxmox`
- Node roles: `control-plane`, `worker`
- Profiles: `minimal`, `ai`, `data`, `full`
- DNS: `cloudflare`, `manual`
- Secrets: `vault`, `sops`

Proxmox currently requires an endpoint. Node groups require positive counts, CPU and memory values, and at least 10 GB of disk. Node group names must be unique.

## Placement targets

`spec.provider.targets` optionally lists infrastructure hosts that Bareplane may use for generated machines. A node group can set its own `targets` to a subset of that provider list.

```yaml
spec:
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
    targets:
      - pve1
      - pve2
  nodes:
    - name: control-plane
      role: control-plane
      count: 3
      cpu: 4
      memoryGB: 8
      diskGB: 64
    - name: gpu-workers
      role: worker
      count: 2
      cpu: 16
      memoryGB: 64
      diskGB: 256
      gpu: true
      targets:
        - pve2
```

Target names use the same lowercase letters, numbers, and hyphen rules as Bareplane names. Duplicate targets are rejected. Every node-group target must exist in `spec.provider.targets`.

Targets are optional in the base schema because validation, doctor, discovery, and read-only planning can be useful before provisioning configuration is complete.

## Proxmox provisioning

The optional `spec.provider.proxmox` block contains non-secret settings required to render virtual machines:

```yaml
spec:
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
    targets:
      - pve1
      - pve2
    proxmox:
      bridge: vmbr0
      systemDatastore: local-lvm
      cloudImageFileID: local:import/debian-12-genericcloud-amd64.qcow2
      ssh:
        user: debian
        publicKeyFile: ~/.ssh/id_ed25519.pub
```

`bridge` is the Proxmox network bridge attached to generated VMs. `systemDatastore` is the datastore that receives their system disks and cloud-init disks. `cloudImageFileID` references an image that already exists in Proxmox using either `<datastore>:import/<file>` for an uncompressed import image or `<datastore>:iso/<file>` for a supported ISO-content image. The configured image must be available to every target that may receive a machine.

`ssh.user` is the cloud-init login user. `ssh.publicKeyFile` is a path on the operator machine to a public key; it is not the key contents and it is not a private key.

Bareplane deliberately does not model Proxmox API token values, SSH private keys, passwords, or other credentials in this block. API credentials remain environment-only.

The base `Validate()` path allows this block to be absent so read-only workflows remain usable. Infrastructure renderers call the stronger `ValidateProvisioning()` contract, which additionally requires at least one provider target and every Proxmox provisioning field.

## Kubernetes bootstrap settings

The optional `spec.kubernetes` block pins the Kubernetes control-plane, virtual-IP, and CNI inputs used by later bootstrap phases:

```yaml
spec:
  kubernetes:
    version: 1.36.4
    apiVIP: 192.168.1.100
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12
    kubeVIPVersion: 1.2.3
    ciliumVersion: 1.20.1
    kubeProxyReplacement: true
```

Versions are exact `MAJOR.MINOR.PATCH` values rather than floating channels or image tags. CIDRs must be canonical, non-overlapping, and use one address family. The API VIP must be a unicast IP address outside both cluster CIDRs.

The base `Validate()` path permits the entire block to be absent so infrastructure-only configurations remain valid. Kubernetes workflows use the stronger `ValidateKubernetesBootstrap()` contract, which requires complete SSH and Kubernetes settings, at least one control-plane machine, and Cilium kube-proxy replacement.

See [kubernetes.md](kubernetes.md) for the supported v0.1 compatibility matrix, ownership decisions, and topology contract.

## Validate

```bash
bareplane validate examples/bareplane.yaml
```

Without a path, Bareplane validates `bareplane.yaml` in the current directory.

See [`examples/bareplane.yaml`](../examples/bareplane.yaml) for a complete example.
