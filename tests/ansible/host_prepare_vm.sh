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
ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/primary_refuse_partial.yaml
if ! ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/primary_init.yaml; then
  # Use the fixture node address to diagnose the VIP independently of its route.
  kubectl --kubeconfig /etc/kubernetes/admin.conf --server=https://192.0.2.10:6443 --request-timeout=5s get pods -A -o wide || true
  for vip_container in $(crictl ps -a --name kube-vip -q); do
    crictl logs --tail=40 "$vip_container" 2>&1 \
      | grep -Ei 'error|warn|failed|fatal|panic' \
      | sed -E 's/[[:alnum:]+\/_=-]{32,}/[redacted]/g; s/[a-z0-9]{6}\.[a-z0-9]{16}/[redacted]/g' || true
  done
  # Only bounded error lines from this disposable fixture; never publish join output.
  if test -f /var/lib/bareplane/bootstrap/init-output; then
    grep -Ei '\[ERROR|error execution|timed out|failed' /var/lib/bareplane/bootstrap/init-output \
      | tail -20 | sed -E 's/[[:alnum:]+\/_=-]{32,}/[redacted]/g; s/[a-z0-9]{6}\.[a-z0-9]{16}/[redacted]/g' || true
  fi
  exit 1
fi
if ! ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/primary_init.yaml > "$workspace/primary-rerun.log" 2>&1; then
  cat "$workspace/primary-rerun.log"
  exit 1
fi
cat "$workspace/primary-rerun.log"
grep -Eq 'changed=0 .*unreachable=0 .*failed=0' "$workspace/primary-rerun.log"
ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/cilium.yaml
if ! ansible-playbook -i tests/ansible/control_plane.ini tests/ansible/cilium.yaml > "$workspace/cilium-rerun.log" 2>&1; then
  cat "$workspace/cilium-rerun.log"
  exit 1
fi
cat "$workspace/cilium-rerun.log"
grep -Eq 'changed=0 .*unreachable=0 .*failed=0' "$workspace/cilium-rerun.log"
