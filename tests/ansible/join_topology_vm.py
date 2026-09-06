#!/usr/bin/env python3
"""Real three-control-plane + worker acceptance on disposable GitHub KVM guests.

Never invokes roles against the runner or an operator's configured machines.
Cloud-init host keys are generated here and pinned before the first SSH request.
"""
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time

import yaml


IMAGE_URL = 'https://cloud-images.ubuntu.com/noble/20260826/noble-server-cloudimg-amd64.img'
IMAGE_SHA256 = 'd0fe84bb5f80853425fa6be28e2c106f30104c3cfe8611933f2e65c9b63f0e30'
REPO = Path(__file__).resolve().parents[2]


def run(args, **kwargs):
    return subprocess.run([str(arg) for arg in args], check=True, timeout=1800, **kwargs)


def write_yaml(path, data, prefix=''):
    path.write_text(prefix + yaml.safe_dump(data, sort_keys=False))
    path.chmod(0o600)


def boot_diagnostics(work, name):
    # Only bounded boot/network errors from throwaway guests, never user-data
    # or unrestricted console output (which can contain generated credentials).
    console = work / name / 'console.log'
    with console.open('rb') as stream:
        data = stream.read(2 * 1024 * 1024).decode(errors='replace')
    lines = [line for line in data.splitlines() if re.search(r'qemu:|error|warn|failed|netplan|network|cloud-init|sshd', line, re.I)]
    for line in lines[-40:]:
        print(re.sub(r'[A-Za-z0-9+/=_-]{48,}', '[redacted]', line)[:500], flush=True)


def main():
    if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1':
        raise SystemExit('This mutating harness is restricted to disposable GitHub Actions runners')
    if not Path('/dev/kvm').exists():
        raise SystemExit('KVM is required; emulation is not an acceptance substitute')
    os.umask(0o077)
    work = Path(tempfile.mkdtemp(prefix='bareplane-join-vm-'))
    guests = []
    logs = []
    try:
        image = work / 'ubuntu.img'
        run(['curl', '-fsSL', '--retry', '3', '--max-time', '300', IMAGE_URL, '-o', image])
        with image.open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != IMAGE_SHA256:
                raise RuntimeError('Ubuntu cloud image checksum mismatch')
        key = work / 'id_ed25519'
        run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', key])
        public_key = key.with_suffix('.pub').read_text().strip()
        known_hosts = work / 'known_hosts'
        known_hosts.write_text('')
        run(['sudo', 'chmod', 'a+rw', '/dev/kvm'])
        run(['sudo', 'ip', 'link', 'add', 'bp-ci', 'type', 'bridge'])
        run(['sudo', 'ip', 'address', 'add', '192.0.2.1/24', 'dev', 'bp-ci'])
        run(['sudo', 'ip', 'link', 'set', 'bp-ci', 'up'])
        run(['sudo', 'sysctl', '-w', 'net.ipv4.ip_forward=1'])
        run(['sudo', 'iptables', '-t', 'nat', '-A', 'POSTROUTING', '-s', '192.0.2.0/24', '!', '-o', 'bp-ci', '-j', 'MASQUERADE'])
        run(['sudo', 'iptables', '-I', 'FORWARD', '-i', 'bp-ci', '-j', 'ACCEPT'])
        run(['sudo', 'iptables', '-I', 'FORWARD', '-o', 'bp-ci', '-j', 'ACCEPT'])
        names = ['lab-control-1', 'lab-control-2', 'lab-control-3', 'lab-worker-1']
        hosts = {name: '192.0.2.' + str(11 + index) for index, name in enumerate(names)}
        for index, (name, address) in enumerate(hosts.items()):
            vm = work / name
            vm.mkdir()
            host_key = vm / 'host_ed25519'
            run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', host_key])
            host_public = host_key.with_suffix('.pub').read_text().strip()
            with known_hosts.open('a') as stream:
                stream.write(address + ' ' + host_public + '\n')
            write_yaml(vm / 'user-data', dict(
                hostname=name, manage_etc_hosts=True, disable_root=False, ssh_pwauth=False,
                users=[dict(name='root', lock_passwd=True, ssh_authorized_keys=[public_key])],
                ssh_keys=dict(ed25519_private=host_key.read_text(), ed25519_public=host_public),
            ), '#cloud-config\n')
            write_yaml(vm / 'meta-data', {'instance-id': name, 'local-hostname': name})
            write_yaml(vm / 'network-config', dict(version=2, ethernets=dict(eth0=dict(
                match=dict(name='en*'), **{'set-name': 'eth0'}, dhcp4=False,
                addresses=[address + '/24'], routes=[dict(to='default', via='192.0.2.1')],
                nameservers=dict(addresses=['1.1.1.1', '8.8.8.8']),
            ))))
            run(['cloud-localds', '--network-config=' + str(vm / 'network-config'), vm / 'seed.img', vm / 'user-data', vm / 'meta-data'])
            run(['qemu-img', 'create', '-f', 'qcow2', '-F', 'qcow2', '-b', image, vm / 'disk.qcow2', '24G'])
            tap = 'bp-tap' + str(index)
            run(['sudo', 'ip', 'tuntap', 'add', 'dev', tap, 'mode', 'tap', 'user', os.environ['USER']])
            run(['sudo', 'ip', 'link', 'set', tap, 'master', 'bp-ci'])
            run(['sudo', 'ip', 'link', 'set', tap, 'up'])
            log = (vm / 'console.log').open('wb')
            logs.append(log)
            guests.append(subprocess.Popen([
                'qemu-system-x86_64', '-enable-kvm', '-cpu', 'host', '-smp', '2', '-m', '3072',
                '-nographic', '-no-reboot', '-drive', f'file={vm / "disk.qcow2"},if=virtio,format=qcow2',
                '-drive', f'file={vm / "seed.img"},if=virtio,format=raw',
                '-netdev', f'tap,id=net0,ifname={tap},script=no,downscript=no',
                '-device', f'virtio-net-pci,netdev=net0,mac=52:54:00:12:34:{index + 1:02x}',
            ], stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT))

        def ssh(name, command, **kwargs):
            return run(['ssh', '-F', '/dev/null', '-i', key, '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5',
                        '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + str(known_hosts),
                        'root@' + hosts[name], command], **kwargs)

        for index, name in enumerate(names):
            deadline = time.monotonic() + 240
            while True:
                if guests[index].poll() is not None:
                    boot_diagnostics(work, name)
                    raise RuntimeError('QEMU exited before SSH readiness: ' + name)
                try:
                    ssh(name, 'timeout 120s cloud-init status --wait', capture_output=True, text=True)
                    break
                except subprocess.CalledProcessError as error:
                    if time.monotonic() >= deadline:
                        print((error.stderr or '')[-1000:], flush=True)
                        boot_diagnostics(work, name)
                        raise RuntimeError('Guest did not become SSH-ready: ' + name) from None
                    time.sleep(3)
            ssh(name, 'apt-get update -qq && apt-get install -y -qq python3-apt sudo')

        config = yaml.safe_load((REPO / 'examples/bareplane.yaml').read_text())
        config['metadata']['name'] = 'lab'
        spec = config['spec']
        spec['nodes'] = [dict(name='control', role='control-plane', count=3, cpu=2, memoryGB=3, diskGB=24),
                         dict(name='worker', role='worker', count=1, cpu=2, memoryGB=3, diskGB=24)]
        spec['features']['gpu'] = False
        spec['profiles'] = ['minimal']
        spec['bootstrap']['ssh'] = dict(user='root', privateKeyFile=str(key), hosts=hosts)
        spec['kubernetes']['apiVIP'] = '192.0.2.100'
        write_yaml(work / 'bareplane.yaml', config)
        run([REPO / 'bin/bareplane', 'bootstrap', 'render', work / 'bareplane.yaml'])
        bundle = work / '.bareplane/bootstrap'
        trust = work / '.bareplane/state/bootstrap'
        trust.mkdir(parents=True)
        (trust / 'known_hosts').write_bytes(known_hosts.read_bytes())
        env = dict(os.environ, ANSIBLE_CONFIG=str(bundle / 'ansible.cfg'), ANSIBLE_NOCOLOR='1')

        def play(name, capture=False, expect_failure=False):
            result = subprocess.run(['ansible-playbook', '--private-key', str(key), str(name)], cwd=bundle, env=env,
                                    text=True, capture_output=capture, timeout=1500)
            if (result.returncode != 0) != expect_failure:
                if capture:
                    print(result.stdout, result.stderr, flush=True)
                raise RuntimeError('Unexpected Ansible result: ' + str(name))
            return result.stdout or ''

        for phase in ['host_prepare', 'kubernetes_install', 'api_vip', 'control_plane_init', 'cilium']:
            play(phase + '.yaml')

        # A stale token cannot poison subsequent joins: every enrollment creates
        # a fresh token and never reads token material from local state.
        ssh(names[0], 'kubeadm token create --ttl=1s', stdout=subprocess.DEVNULL)
        time.sleep(2)

        # Deliberately foreign local state must stop after the control planes
        # join, before mutating the worker. The next run exercises partial resume.
        ssh(names[-1], 'install -m 0600 /dev/null /etc/kubernetes/kubelet.conf')
        refused = play('join.yaml', capture=True, expect_failure=True)
        if 'Unmanaged or foreign cluster state blocks joining' not in refused:
            print(refused, flush=True)
            raise RuntimeError('Foreign worker was not refused at the ownership boundary')
        ssh(names[-1], 'test -f /etc/kubernetes/kubelet.conf && test ! -s /etc/kubernetes/kubelet.conf && rm /etc/kubernetes/kubelet.conf')
        play('join.yaml')
        repeat = play('join.yaml', capture=True)
        print(repeat, flush=True)
        recaps = re.findall(r'changed=(\d+)\s+unreachable=(\d+)\s+failed=(\d+)', repeat)
        if len(recaps) != 4 or any(row != ('0', '0', '0') for row in recaps):
            raise RuntimeError('The complete topology did not reapply without changes')
        for name in names[1:]:
            ssh(name, 'test ! -e /var/lib/bareplane/bootstrap/join.yaml')
        certs = ssh(names[0], 'kubectl --kubeconfig /etc/kubernetes/admin.conf -n kube-system get secret kubeadm-certs --ignore-not-found -o name', capture_output=True, text=True)
        if certs.stdout.strip():
            raise RuntimeError('Uploaded join certificates were not cleaned up')
        descriptions = ssh(names[0], 'kubectl --kubeconfig /etc/kubernetes/admin.conf -n kube-system get secrets -o jsonpath="{.items[*].data.description}"', capture_output=True, text=True)
        if 'YmFyZXBsYW5lLW5vZGUtam9pbg==' in descriptions.stdout:
            raise RuntimeError('A Bareplane node join token was not revoked')
        print('Three stacked-etcd control planes and one worker: joined, Ready, credentials cleaned, unchanged rerun.', flush=True)
    finally:
        for guest in guests:
            guest.terminate()
        for guest in guests:
            try:
                guest.wait(timeout=15)
            except subprocess.TimeoutExpired:
                guest.kill()
                guest.wait()
        for log in logs:
            log.close()


if __name__ == '__main__':
    main()
