# Bootstrap health gate

Run the generated `health.yaml` after `kubeconfig.yaml`. It runs on the Linux/WSL controller using `.bareplane/state/bootstrap/admin.conf`; it does not SSH into machines or use the operator's global kubeconfig. The controller needs OpenSSL 3 and kubectl at the exact configured Kubernetes version.

```bash
cd .bareplane/bootstrap
export ANSIBLE_CONFIG="$PWD/ansible.cfg"
ansible-playbook health.yaml
```

The gate first validates the private kubeconfig's ownership, permissions, management digest, CA, VIP, and embedded certificate/key. It then reports deterministic PASS/FAIL results in this order:

1. API `/readyz` through the configured VIP.
2. Exact desired node names/count, Ready state, Kubernetes version, and control-plane labels/taints.
3. Healthy etcd mirror pods, exact non-learner stacked-etcd membership, and authenticated endpoint health for every member.
4. Healthy kube-vip mirror pods on their corresponding control-plane nodes.
5. No kube-proxy DaemonSet; complete Cilium DaemonSet/operator rollout; a healthy Cilium agent and Kubernetes connection on every desired node.
6. Fully available CoreDNS deployment.
7. Actual cluster DNS resolution, direct pod connectivity, ClusterIP connectivity, and Service DNS connectivity.

Checks stop at the first failure. Component API data, certificate keys, and full kubeconfigs are not printed. The gate has a ten-minute overall deadline and per-command/request bounds; cleanup has its own bounded budget even after the check deadline expires. A failed or incomplete check must block GitOps handoff.

## Temporary connectivity test

The last check creates a randomly named `bareplane-health-<id>` namespace with cluster/run ownership annotations. A small HTTP server runs on the primary; its client runs on a worker when available, otherwise another control plane, or the same node for a single-node cluster. This exercises cross-node networking whenever the topology supports it.

Both pods use the reviewed BusyBox 1.37.0 multi-architecture image pinned by digest, non-root users, a read-only root filesystem, dropped capabilities, RuntimeDefault seccomp, resource limits, a small emptyDir, and no mounted ServiceAccount token. An active deadline limits their lifetime. No long-lived workload or external network endpoint is installed for the smoke test.

Success and failure paths remove the namespace after checking its ownership and using a UID-preconditioned API delete, then wait for deletion. Cleanup failure is itself a failed gate and reports the exact owned namespace for inspection. An operator must clean up a remaining owned probe namespace before retrying; Bareplane will never delete a same-name namespace with changed ownership. Process termination or loss of API connectivity cannot guarantee immediate deletion, so active pod deadlines provide a second limit. `--check` is explicitly refused because a read-only preview cannot prove pod/service connectivity.

CI runs fake-client failure/refusal tests and the full gate against both the disposable single-node Debian cluster and a real three-control-plane/worker Ubuntu VM topology. The smoke image's digest is reviewed from the [official BusyBox image metadata](https://hub.docker.com/_/busybox); Cilium checks use the supported [agent status interface](https://docs.cilium.io/en/stable/cmdref/cilium-dbg_status/).
