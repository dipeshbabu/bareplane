# Providers

Bareplane isolates infrastructure-specific behavior behind provider contracts so core orchestration does not depend on Proxmox or any future backend.

## Contract boundaries

The base `Provider` capability is intentionally small: it identifies the backend and validates provider configuration. Optional infrastructure capabilities are separate interfaces:

- `Discoverer` inspects existing infrastructure and returns provider-neutral inventory.
- `Planner` compares desired configuration with inventory and returns provider-neutral changes.

A backend should implement only capabilities it can support correctly. Core code must feature-detect optional capabilities rather than assume every provider implements everything.

## Registry

Providers are resolved through `provider.Registry`. Builtin provider wiring lives under `internal/provider/builtin`; core packages should import that registry rather than importing a concrete provider package directly.

The initial builtin provider is Proxmox. Its current implementation validates and normalizes the endpoint without making network calls. API authentication, discovery, and infrastructure planning will be added separately.

## Generic models

`provider.Inventory`, `provider.Node`, `provider.Plan`, and `provider.Change` contain only fields that Bareplane core can reason about. Backend-specific API objects must remain inside the provider implementation.
