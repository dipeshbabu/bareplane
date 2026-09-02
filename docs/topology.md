# Machine topology

Bareplane expands node groups into provider-neutral machine identities before any infrastructure backend renders or mutates resources.

## Naming

A machine name is:

```text
<cluster>-<node-group>-<ordinal>
```

For example, a cluster named `lab` with a `workers` group of count 3 produces:

```text
lab-workers-1
lab-workers-2
lab-workers-3
```

Ordinals are intentionally not zero padded. Existing names therefore remain unchanged when a group grows from 9 machines to 10, 100, or more.

## Ordering

Node groups are sorted by group name before expansion, and machines within a group are emitted by ascending ordinal. Reordering node groups in `bareplane.yaml` does not change the resulting topology.

## Provider targets

Provider targets describe the infrastructure hosts eligible to run generated machines. They are optional so read-only configurations remain valid.

```yaml
provider:
  type: proxmox
  endpoint: https://proxmox.example.com:8006
  targets:
    - pve1
    - pve2
```

A node group can restrict itself to a subset of those targets, which is useful for GPU or storage-specific hosts:

```yaml
nodes:
  - name: gpu-workers
    role: worker
    count: 2
    cpu: 16
    memoryGB: 64
    diskGB: 256
    gpu: true
    targets:
      - pve-gpu1
```

Targets are sorted before placement. A machine at ordinal `n` is assigned to `(n - 1) mod target-count`, so reordering targets in YAML does not change placement and growing a node group does not move existing ordinals. Changing the actual target set can change placement and should therefore be treated as an infrastructure decision.

Node-group targets must be present in the provider target set. If a group does not specify targets, it inherits the full provider target set.

## Identity constraints

Generated names are limited to 63 characters so they remain usable as common infrastructure and Kubernetes identifiers. Bareplane also caps a topology at 10,000 machines to reject accidental or malicious configurations before allocating unbounded generated state.

The topology layer carries desired target, role, CPU, memory, disk, and GPU intent but contains no Proxmox API, Terraform, Ansible, or Kubernetes behavior.
