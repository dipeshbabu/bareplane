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
ansible-playbook -i localhost, tests/ansible/kubernetes_toolchain.yaml
if ! ansible-playbook -i localhost, tests/ansible/kubernetes_toolchain_rerun.yaml > "$workspace/kubernetes-rerun.log" 2>&1; then
  cat "$workspace/kubernetes-rerun.log"
  exit 1
fi
cat "$workspace/kubernetes-rerun.log"
grep -Eq 'changed=0 .*unreachable=0 .*failed=0' "$workspace/kubernetes-rerun.log"
# Create a LAN and an unrelated VIP claimant entirely inside the disposable VM.
ip netns add bareplane-vip-test
ip link add bareplane-vip type veth peer name bareplane-peer
ip link set bareplane-peer netns bareplane-vip-test
ip address add 192.0.2.10/24 dev bareplane-vip
ip link set bareplane-vip up
ip -n bareplane-vip-test address add 192.0.2.100/24 dev bareplane-peer
ip -n bareplane-vip-test link set bareplane-peer up
ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/api_vip_refuse.yaml
ip -n bareplane-vip-test address del 192.0.2.100/24 dev bareplane-peer
ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/api_vip.yaml
if ! ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/api_vip.yaml > "$workspace/vip-rerun.log" 2>&1; then
  cat "$workspace/vip-rerun.log"
  exit 1
fi
cat "$workspace/vip-rerun.log"
grep -Eq 'changed=0 .*unreachable=0 .*failed=0' "$workspace/vip-rerun.log"
