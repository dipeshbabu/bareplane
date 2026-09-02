# Configuration

Bareplane uses a single versioned `bareplane.yaml` file as the user-facing source of intent.

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

## Validate

```bash
bareplane validate examples/bareplane.yaml
```

Without a path, Bareplane validates `bareplane.yaml` in the current directory.

See [`examples/bareplane.yaml`](../examples/bareplane.yaml) for a complete example.
