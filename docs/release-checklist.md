# v0.1 release-candidate checklist

This is an evidence checklist, not an automatic release command. Distribution/signing/publication is a separate milestone. Do not mark a source release-ready from mocked tests alone.

## Source and automated gate

- [ ] Exact source SHA is in the default branch history; all implementation PRs were squash-merged only after their latest heads passed CI.
- [ ] The latest default-branch CI run for that exact SHA is completed successfully, including every individual VM job and `v0.1 acceptance`.
- [ ] Candidate verifier records the exact run ID and attempt. No pending, failed, cancelled, skipped, missing, or ambiguous required checks are accepted; no fallback to an older passing run.
- [ ] Current compatibility record, public fixture, embedded payload checksums, license notices, and reference configuration match the tested source.

## Real Proxmox evidence (operator supplied)

- [ ] Authorized disposable lab identifier, operator, date, and exact Proxmox version are recorded.
- [ ] Controller/tool versions, resolved bpg/proxmox lock version/hash, cloud image checksum, and sanitized configuration hash are recorded.
- [ ] Fresh infrastructure → healthy Kubernetes → verified GitOps handoff procedure completed without unmanaged adoption.
- [ ] Four-node topology, VIP/etcd/Cilium/DNS/network health, root/child Argo convergence, and ownership boundaries were verified live.
- [ ] Stage-aware rerun preserved VM/resource identities and produced no destructive drift.
- [ ] Failure/recovery evidence shows no false state advancement, no saved-plan replay, safe explicit recovery, and unrelated data preservation.
- [ ] Evidence excludes credentials, private logs, kubeconfig data, join material, and Terraform state. Private diagnostics remain operator-controlled.

## Release decision

- [ ] Known limitations are accepted and documented for the candidate.
- [ ] Failed or changed prerequisites invalidate approval for the affected source; fixes require new exact-source evidence.
- [ ] The distribution milestone's signing, checksums, SBOM/provenance, install verification, and publication controls are complete before publishing binaries/tags.

Suggested evidence fields: `source_sha`, `ci_run_id`, `ci_attempt`, `operator`, `lab_id`, `tested_at`, `proxmox_version`, `tool_versions`, `provider_lock_sha256`, `guest_image_sha256`, `sanitized_config_sha256`, `first_run_result`, `rerun_result`, `failure_recovery_result`, `limitations`, and `release_decision`. Do not invent successful real-environment evidence when only CI or mocked tests ran.
