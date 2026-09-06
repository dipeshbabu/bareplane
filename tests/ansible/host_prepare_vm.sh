#!/bin/sh
# Run only inside the disposable Debian CI guest. All host changes stay in it.
set -eu
test "${BAREPLANE_DISPOSABLE_VM:-}" = 1
test "$(id -u)" = 0
workspace=$(mktemp -d)
cp examples/bareplane.yaml "$workspace/bareplane.yaml"
bin/bareplane bootstrap render "$workspace/bareplane.yaml"
export ANSIBLE_CONFIG="$workspace/.bareplane/bootstrap/ansible.cfg"
export ANSIBLE_ROLES_PATH="$workspace/.bareplane/bootstrap/roles"
export ANSIBLE_NOCOLOR=1
export PATH="/opt/ansible/bin:$PATH"
mkdir -p /etc/containerd
ansible-playbook -i localhost, tests/ansible/host_prepare.yaml
if ! ansible-playbook -i localhost, tests/ansible/host_prepare_rerun.yaml > "$workspace/rerun.log" 2>&1; then
  cat "$workspace/rerun.log"
  exit 1
fi
cat "$workspace/rerun.log"
grep -Eq 'changed=0 .*unreachable=0 .*failed=0' "$workspace/rerun.log"
