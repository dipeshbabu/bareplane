# Architecture

## GitOps source of truth

The [component graph](platform-graph.md) centralizes profile sets, dependencies, ownership exclusions, availability gates, and deterministic Argo ordering. Planned capabilities remain explicitly unavailable until their focused implementations and health contracts exist.

The [user-owned GitOps contract](gitops.md) separates bootstrap from platform reconciliation. Bareplane keeps ownership of Kubernetes bootstrap, kube-vip, and Cilium; it may directly install only minimal Argo CD bootstrap resources and the root Application for handoff. The published user repository owns subsequent platform desired state through Argo. Repository rendering, Argo bootstrap, handoff verification, and dependency-driven profiles remain separate components, and none may silently take over bootstrap-owned Cilium.

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
