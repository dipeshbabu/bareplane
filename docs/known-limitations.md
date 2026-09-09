# v0.1 baseline limitations

- Proxmox is the only implemented infrastructure provider. Hosted CI uses fake provider responses plus real disposable guest clusters; release certification also requires operator-run Proxmox evidence.
- Execution requires the original supported Linux/WSL controller and strict private project state. Native Windows rendering/testing is not native Windows bootstrap support.
- The reference networking path is on-link IPv4 with explicit VM address mapping/reservations and a free API VIP. No inferred DHCP inventory, IPv6 operational certification, or arbitrary network migration is claimed.
- Kubernetes, kube-vip, Cilium, containerd, Ansible, and Argo use reviewed version boundaries/pins. Re-running installation is not an upgrade or node-replacement workflow.
- Initial GitOps supports anonymous HTTPS repositories and an exact reviewed payload. Private Git credentials, arbitrary plugins, and embedded plaintext secrets are unsupported. The explicit [SOPS integration](sops.md) supports its reviewed age/PGP CMP and restricted encrypted JSON Secret contract, not arbitrary plugin execution.
- Minimal currently means the implemented Argo control plane after bootstrap. Core platform, GPU/AI, and stateful data capabilities remain unavailable until their focused component issues, storage/credential contracts, and health checks land.
- The minimal Argo installation is non-HA with an ephemeral Redis cache. It does not install public ingress, SSO, optional controllers, or persistent storage.
- Initial handoff disables cascading prune/self-heal and verifies one snapshot before following configured Git. After transfer, user Git changes are authoritative; Bareplane does not compete with that reconciliation.
- `status` reports applicable local records, not a live cluster audit. Missing/changed ownership state requires inspection and explicit recovery.
- Bootstrap reset is limited to an owned bootstrap-only cluster and refuses application/storage-bearing state. Full lifecycle upgrades, application-aware recovery, backup/restore, and distribution remain separate milestones.
