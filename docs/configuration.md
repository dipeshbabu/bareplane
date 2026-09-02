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

Targets are optional in the current schema because validation, doctor, discovery, and read-only planning can be useful before provisioning configuration is complete. Rendering or applying provider infrastructure may require targets later.

## Validate

```bash
bareplane validate examples/bareplane.yaml
```

Without a path, Bareplane validates `bareplane.yaml` in the current directory.

See [`examples/bareplane.yaml`](../examples/bareplane.yaml) for a complete example.
