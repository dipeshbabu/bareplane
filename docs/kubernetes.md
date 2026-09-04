# Kubernetes bootstrap contract

Bareplane's v0.1 Kubernetes path is deliberately opinionated. A complete `spec.kubernetes` block gives later bootstrap phases one validated source for every version and network value they need:

```yaml
spec:
  kubernetes:
    version: 1.36.4
    apiVIP: 192.168.1.100
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12
    kubeVIPVersion: 1.2.3
    ciliumVersion: 1.20.1
    kubeProxyReplacement: true
```

This contract only validates user intent. It does not connect to a host, install software, render a kubeadm file, create a virtual IP, or mutate a Kubernetes cluster.

## v0.1 ownership and topology

Bareplane v0.1 uses:

- kubeadm to initialize and join Kubernetes nodes;
- stacked etcd on control-plane nodes;
- kube-vip as a static Pod for the stable control-plane endpoint;
- Cilium as the bootstrap-owned CNI;
- Cilium's kube-proxy replacement, with kubeadm configured not to install kube-proxy.

Cilium remains bootstrap-owned because cluster networking must work before the GitOps control plane can converge. Argo CD must not adopt or replace it during the v0.1 handoff.

At least one desired machine must have the `control-plane` role. A single control plane is supported for development and small non-HA environments. Three control-plane machines are recommended for an HA deployment with a useful stacked-etcd quorum. Two control-plane machines are accepted by the schema but do not provide failure tolerance for a two-member etcd quorum.

## Version pins and compatibility

Every component version is an exact, canonical `MAJOR.MINOR.PATCH` value. Prefixes such as `v`, floating values such as `latest`, shortened versions, prereleases, and build metadata are rejected. Later renderers may add an image-tag prefix when required, but the source configuration remains normalized.

The v0.1 compatibility window is:

| Component | Accepted versions | Example pin |
| --- | --- | --- |
| Kubernetes | `>= 1.35.0` and `< 1.37.0` | `1.36.4` |
| kube-vip | `>= 1.2.0` and `< 1.3.0` | `1.2.3` |
| Cilium | `>= 1.20.0` and `< 1.21.0` | `1.20.1` |

The Kubernetes window stays within currently maintained upstream minors that Cilium 1.20 lists as tested. Bareplane narrows kube-vip and Cilium to one minor line so later bootstrap rendering can rely on one reviewed interface. Expanding any window requires compatibility and acceptance coverage rather than only relaxing validation.

Reference material:

- [Kubernetes releases and support periods](https://kubernetes.io/releases/)
- [Cilium Kubernetes compatibility](https://docs.cilium.io/en/stable/network/kubernetes/requirements/)
- [Cilium kube-proxy replacement with kubeadm](https://docs.cilium.io/en/stable/network/kubernetes/kubeproxy-free/)
- [kube-vip static Pod bootstrap](https://kube-vip.io/docs/installation/static/)

## Network rules

`apiVIP` is the stable kube-apiserver address advertised by kube-vip. It must be an IPv4 or IPv6 global-unicast address. Private IPv4 and unique-local IPv6 addresses are valid; unspecified, loopback, link-local, multicast, broadcast, DNS, CIDR, and zone-scoped values are rejected.

`podCIDR` and `serviceCIDR` each contain one canonical IPv4 or IPv6 prefix. Host bits must be zero, both CIDRs must use the same address family, and neither range may contain the other or otherwise overlap. The API VIP must sit outside both ranges.

The v0.1 schema models a single-stack cluster. Dual-stack bootstrap needs a later explicit schema extension rather than comma-separated values hidden inside these fields.

## Validation levels

`Config.Validate()` treats `spec.kubernetes` as optional. Existing infrastructure validation, planning, rendering, and Terraform operations therefore continue to work when the block is absent. If the block is present, populated versions, addresses, and CIDRs must already be well formed and compatible.

`Config.ValidateKubernetesBootstrap()` is the mutation-gate contract for later Kubernetes phases. It first requires the complete SSH bootstrap mapping, then requires every Kubernetes field, at least one control-plane machine, and `kubeProxyReplacement: true`. Errors identify the exact field and are returned in deterministic order.
