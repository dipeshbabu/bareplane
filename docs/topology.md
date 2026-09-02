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

## Identity constraints

Generated names are limited to 63 characters so they remain usable as common infrastructure and Kubernetes identifiers. Bareplane also caps a topology at 10,000 machines to reject accidental or malicious configurations before allocating unbounded generated state.

The topology layer carries desired role, CPU, memory, disk, and GPU intent but contains no Proxmox, Terraform, Ansible, or Kubernetes behavior.
