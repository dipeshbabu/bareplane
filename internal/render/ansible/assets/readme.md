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

site.yaml defines the phase order. Until the corresponding issues are
implemented, phase roles stop with a specific error (including in check mode).
This bundle alone does not yet create Kubernetes. Do not interpret a successful
syntax check or contract preview as a successful host/bootstrap operation.
Only ansible.builtin is used; no external collections are required.
