# Plan

`bareplane plan` discovers current infrastructure and compares it with the deterministic machine topology generated from `bareplane.yaml`.

```bash
bareplane plan
bareplane plan clusters/dev/bareplane.yaml
```

Planning is read only. The current Proxmox path performs GET requests only and Bareplane has no apply or destroy command yet.

## Decisions

For each desired machine, the planner emits one of four actions:

- `CREATE` when no observed resource has the desired machine name.
- `NOOP` when exactly one observed resource has the name, carries both Bareplane ownership tags for this cluster, is not a template, and matches the observable CPU, memory, and disk capacity.
- `UPDATE` when an explicitly owned machine has observable CPU, memory, or disk drift.
- `CONFLICT` when the name is already used by an unowned resource, multiple observed resources share the name, or an owned resource with that name is a template.

A conflict exits with code `3`. Configuration, credential, discovery, or planning errors exit with code `1`. Invalid command usage exits with code `2`.

## No destructive planning yet

The initial planner deliberately emits no delete actions, including for explicitly owned resources that are no longer present in desired topology. Destructive lifecycle planning will require additional safeguards and an explicit lifecycle contract.

## Observable drift

The current planner compares CPU count, memory capacity, and disk capacity. Runtime power status and current Proxmox host placement are observational data, not desired-capacity drift.

GPU passthrough is not compared yet because the cluster resource endpoint does not establish PCI passthrough state. Bareplane will add GPU drift only after a dedicated hardware/config discovery path can observe it reliably.

## Ownership

Matching a generated Bareplane machine name is never enough for update planning. Existing resources must carry both explicit ownership tags described in [ownership.md](ownership.md). Assigning those tags manually is an explicit opt-in to Bareplane management for that cluster.
