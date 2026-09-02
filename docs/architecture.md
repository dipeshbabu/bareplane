# Architecture

Bareplane separates user intent, infrastructure provisioning, cluster bootstrap, and continuous reconciliation.

```text
bareplane.yaml
      |
      v
Bareplane CLI
      |
      +--> provider layer --> infrastructure
      |
      +--> bootstrap layer --> Kubernetes
      |
      +--> GitOps renderer --> user-owned desired state --> Argo CD
```

## Ownership boundaries

- Providers create or discover infrastructure. Provider packages must not own workload reconciliation.
- Bootstrap prepares Kubernetes and installs the minimum components required to hand control to GitOps.
- GitOps owns long-lived in-cluster state after bootstrap.
- Secrets are referenced by configuration and materialized through a dedicated secrets integration rather than embedded in generated manifests.

## Initial scope

The first supported provider will target Proxmox. The generic packages must remain provider-neutral so future bare-metal or alternate virtualization providers can implement the same interfaces without changing the configuration model.

Profiles such as `minimal`, `ai`, and `data` will select composable platform capabilities rather than fork the core installation path.
