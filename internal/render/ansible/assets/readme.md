# Generated bootstrap workspace

This directory is embedded in Bareplane and replaced by bootstrap render.
Review inventory.yaml, group_vars, playbooks, roles, and templates before use.
The persistent host trust file lives in ../state/bootstrap/known_hosts.

Run syntax validation from this directory with ANSIBLE_CONFIG set to its
ansible.cfg. Use ansible-playbook --syntax-check site.yaml to check all imports,
or ansible-playbook validate.yaml for a controller-only contract preview.
These checks need neither a private key nor a reachable host.

No private-key path is embedded. Future guarded execution must supply the
configured key through --private-key after verifying the project trust file,
set ANSIBLE_CONFIG explicitly, and control environment/extra-variable overrides.
Ansible controller execution targets Linux/WSL, with OpenSSH installed.

site.yaml defines the phase order. host_prepare installs pinned containerd and
prepares Linux; kubernetes_install adds pinned Kubernetes binaries; api_vip
checks LAN address conflicts and installs the static Pod manifest;
control_plane_init initializes the primary with private, configuration-bound state.
Cilium installs the reviewed CNI chart and verifies primary networking.
Join expands stacked-etcd control planes, then workers, with temporary credentials
and configuration/CA-bound ownership checks; completed nodes are not rejoined.
Kubeconfig retrieves a verified private admin credential only after topology
formation and stores it under ../state/bootstrap/admin.conf, never ~/.kube/config.
Health verifies API, node/etcd topology, VIP, Cilium, DNS, and pod/service traffic
using the private controller kubeconfig and cleaned-up ephemeral test resources.
The full health gate deliberately refuses check mode.
Guarded end-to-end CLI orchestration is still a separate phase. Do not interpret
a successful syntax check or contract preview as a successful bootstrap operation.
Only ansible.builtin is used; no external collections are required.
