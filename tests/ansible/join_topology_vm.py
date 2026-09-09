#!/usr/bin/env python3
"""Real three-control-plane + worker acceptance on disposable GitHub KVM guests.

Never invokes roles against the runner or an operator's configured machines.
Cloud-init host keys are generated here and pinned before the first SSH request.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time

import yaml

from cert_manager_acceptance import run_cert_manager_acceptance
from metrics_server_acceptance import run_metrics_server_acceptance
from dns_acceptance import run_dns_acceptance


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


def apply_diagnostics(state):
    # Only selected, bounded, redacted error lines from disposable CI guests.
    # Never upload raw phase logs or kubeadm's private node-local output.
    logs = sorted((state / 'logs').glob('*.log'), key=lambda path: path.stat().st_mtime)
    for path in logs[-2:]:
        with path.open('rb') as stream:
            data = stream.read(4 * 1024 * 1024).decode(errors='replace')
        lines = [line for line in data.splitlines() if re.search(r'^TASK |^\[ERROR\]|^fatal:|"msg":|"assertion":|"evaluated_to":', line)]
        for line in lines[-30:]:
            line = re.sub(r'[a-z0-9]{6}\.[a-z0-9]{16}|[A-Za-z0-9+/=_-]{32,}', '[redacted]', line)
            print(line[:600], flush=True)


def main():
    if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1':
        raise SystemExit('This mutating harness is restricted to disposable GitHub Actions runners')
    if not Path('/dev/kvm').exists():
        raise SystemExit('KVM is required; emulation is not an acceptance substitute')
    os.umask(0o077)
    work = Path(tempfile.mkdtemp(prefix='bareplane-join-vm-'))
    apply_mode = os.environ.get('BAREPLANE_TEST_BOOTSTRAP_APPLY') == '1'
    recovery_mode = os.environ.get('BAREPLANE_TEST_BOOTSTRAP_RECOVERY') == '1'
    kubelet_tls_mode = os.environ.get('BAREPLANE_TEST_KUBELET_TLS') == '1'
    metrics_mode = os.environ.get('BAREPLANE_TEST_METRICS_SERVER') == '1'
    dns_mode = os.environ.get('BAREPLANE_TEST_EXTERNAL_DNS') == '1'
    cert_manager_mode = os.environ.get('BAREPLANE_TEST_CERT_MANAGER') == '1' or metrics_mode
    handoff_mode = os.environ.get('BAREPLANE_TEST_GITOPS_HANDOFF') == '1' or cert_manager_mode or dns_mode
    argocd_mode = os.environ.get('BAREPLANE_TEST_ARGOCD_INSTALL') == '1' or handoff_mode
    single_mode = recovery_mode or argocd_mode
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
        names = ['lab-control-1'] if single_mode else ['lab-control-1', 'lab-control-2', 'lab-control-3', 'lab-worker-1']
        hosts = {name: '192.0.2.' + str(11 + index) for index, name in enumerate(names)}
        for index, (name, address) in enumerate(hosts.items()):
            mac = f'52:54:00:12:34:{index + 1:02x}'
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
                match=dict(macaddress=mac), **{'set-name': 'eth0'}, dhcp4=False,
                addresses=[address + '/24'], routes=[dict(to='default', via='192.0.2.1')],
                nameservers=dict(addresses=['1.1.1.1', '8.8.8.8']),
            ))))
            run(['cloud-localds', '--network-config=' + str(vm / 'network-config'), vm / 'seed.img', vm / 'user-data', vm / 'meta-data'])
            run(['qemu-img', 'create', '-f', 'qcow2', '-F', 'qcow2', '-b', image, vm / 'disk.qcow2', '24G'])
            if recovery_mode:
                run(['qemu-img', 'create', '-f', 'qcow2', vm / 'application-data.qcow2', '64M'])
            tap = 'bp-tap' + str(index)
            run(['sudo', 'ip', 'tuntap', 'add', 'dev', tap, 'mode', 'tap', 'user', os.environ['USER']])
            run(['sudo', 'ip', 'link', 'set', tap, 'master', 'bp-ci'])
            run(['sudo', 'ip', 'link', 'set', tap, 'up'])
            log = (vm / 'console.log').open('wb')
            logs.append(log)
            guests.append(subprocess.Popen([
                'qemu-system-x86_64', '-enable-kvm', '-cpu', 'host', '-smp', '2', '-m', '4096' if argocd_mode else '3072',
                '-nographic', *([] if recovery_mode else ['-no-reboot']), '-drive', f'file={vm / "disk.qcow2"},if=virtio,format=qcow2',
                '-drive', f'file={vm / "seed.img"},if=virtio,format=raw',
                '-netdev', f'tap,id=net0,ifname={tap},script=no,downscript=no',
                '-device', f'virtio-net-pci,netdev=net0,mac={mac}',
                *(['-drive', f'file={vm / "application-data.qcow2"},if=virtio,format=qcow2'] if recovery_mode else []),
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
            if apply_mode:
                ssh(name, 'timedatectl set-ntp true')
                deadline = time.monotonic() + 120
                while ssh(name, 'timedatectl show -p NTPSynchronized --value', capture_output=True, text=True).stdout.strip() != 'yes':
                    if time.monotonic() >= deadline:
                        raise RuntimeError('Disposable guest did not synchronize its clock: ' + name)
                    time.sleep(2)
            if recovery_mode:
                # This exact empty 64 MiB disk was created above for this guest.
                ssh(name, 'test "$(blockdev --getsize64 /dev/vdc)" = 67108864 && ! blkid /dev/vdc')
                ssh(name, 'mkfs.ext4 -q /dev/vdc && mkdir /bareplane-user-data && mount /dev/vdc /bareplane-user-data')
                disk_uuid = ssh(name, 'blkid -s UUID -o value /dev/vdc', capture_output=True, text=True).stdout.strip()
                if not re.fullmatch(r'[a-f0-9-]{36}', disk_uuid):
                    raise RuntimeError('Unexpected disposable application-disk identity')
                ssh(name, "printf '%s\\n' 'UUID=" + disk_uuid + " /bareplane-user-data ext4 defaults 0 2' >> /etc/fstab")
                ssh(name, "printf '%s\\n' 'preserve-application-data' > /bareplane-user-data/sentinel")

        config = yaml.safe_load((REPO / 'examples/bareplane.yaml').read_text())
        config['metadata']['name'] = 'lab'
        spec = config['spec']
        spec['nodes'] = [dict(name='control', role='control-plane', count=1 if single_mode else 3, cpu=2, memoryGB=4 if argocd_mode else 3, diskGB=24)]
        if not single_mode:
            spec['nodes'].append(dict(name='worker', role='worker', count=1, cpu=2, memoryGB=3, diskGB=24))
        spec['features']['gpu'] = False
        spec['profiles'] = ['minimal']
        if argocd_mode:
            spec['features']['observability'] = False
            # This is the explicit M1/M2 baseline, not future optional core defaults.
            spec['components'] = yaml.safe_load((REPO / 'examples/gitops-fixture.yaml').read_text())['spec']['components']
            spec['dns']['provider'] = 'manual'
            spec['secrets']['provider'] = 'sops'
            # Public immutable fixture proves Git reachability only. No remote
            # payload is applied in the installation issue; root handoff is separate.
            spec['gitops'] = dict(repoURL='https://github.com/dipeshbabu/bareplane.git',
                                  revision='main' if handoff_mode else '50561433e64dc3a0d395f0f1ea64d627eb2ff725', rootPath='missing/root')
        spec['bootstrap']['ssh'] = dict(user='root', privateKeyFile=str(key), hosts=hosts)
        spec['kubernetes']['apiVIP'] = '192.0.2.100'
        project = work / ("project 'quoted' \"double\" %h" if apply_mode else 'project')
        project.mkdir()
        if apply_mode:
            controller_key = project / 'private-key'
            controller_key.write_bytes(key.read_bytes())
            controller_key.chmod(0o600)
            spec['bootstrap']['ssh']['privateKeyFile'] = str(controller_key)
        config_path = project / 'bareplane.yaml'
        if handoff_mode:
            run([REPO / 'bin/bareplane', 'init', config_path])
        write_yaml(config_path, config)
        if handoff_mode:
            run([REPO / 'bin/bareplane', 'validate', config_path])
        run([REPO / 'bin/bareplane', 'bootstrap', 'render', config_path])
        bundle = project / '.bareplane/bootstrap'
        trust = project / '.bareplane/state/bootstrap'
        trust.mkdir(parents=True, exist_ok=True)
        if apply_mode:
            # SSH readiness above already pinned these generated VM host keys.
            # Exercise the real explicit trust workflow for the CLI test.
            run([REPO / 'bin/bareplane', 'bootstrap', 'trust', config_path], input=b'lab\n')
            if handoff_mode:
                run([REPO / 'bin/bareplane', 'bootstrap', 'doctor', config_path])
                run([REPO / 'bin/bareplane', 'bootstrap', 'check', config_path])
                run([REPO / 'bin/bareplane', 'bootstrap', 'preflight', config_path])
            command = [REPO / 'bin/bareplane', 'bootstrap', 'apply', '--approve', 'lab', config_path]
            try:
                run(command)
                progress = json.loads((trust / 'progress.json').read_text())
                if progress['completed'] != 8 or progress['active']:
                    raise RuntimeError('Successful apply did not record complete progress')
                repeat = run(command, capture_output=True, text=True).stdout
                print(repeat, flush=True)
                checked = re.findall(r'^CHECK\s+(\S+)', repeat, re.M)
                passed = re.findall(r'^PASS\s+(\S+)', repeat, re.M)
                if checked != ['health'] or passed != ['health']:
                    raise RuntimeError('A healthy CLI rerun reconfigured completed phases')
                if (trust / '.operation.lock').exists():
                    raise RuntimeError('Successful apply did not release its operation lock')
                if metrics_mode:
                    run([REPO / 'bin/bareplane', 'bootstrap', 'kubelet-tls', '--approve', 'lab', config_path])
                if kubelet_tls_mode:
                    # All four real nodes cover the primary, joined control
                    # planes and worker ownership contracts independently.
                    tls = [REPO / 'bin/bareplane', 'bootstrap', 'kubelet-tls', '--approve', 'lab', config_path]
                    run(tls)
                    identities = {}
                    receipts = {}
                    tls_ca = ssh(names[0], 'sha256sum /etc/kubernetes/pki/ca.crt', capture_output=True, text=True).stdout.split()[0]
                    for name in names:
                        receipt = trust / ('kubelet-tls-' + name + '-' + tls_ca + '.json')
                        receipts[name] = receipt.read_bytes()
                        record = json.loads(receipts[name])
                        if record['state']['stage'] != 'ready' or record['identity']['address'] != hosts[name]:
                            raise RuntimeError('Serving TLS did not record verified per-node identity')
                        identities[name] = ssh(name, 'systemctl show kubelet --property=MainPID --value', capture_output=True, text=True).stdout.strip()
                    run(tls)
                    for name in names:
                        if (trust / ('kubelet-tls-' + name + '-' + tls_ca + '.json')).read_bytes() != receipts[name]:
                            raise RuntimeError('Unchanged serving TLS rerun rewrote its approval receipt')
                        observed = ssh(name, 'systemctl show kubelet --property=MainPID --value', capture_output=True, text=True).stdout.strip()
                        if observed != identities[name]:
                            raise RuntimeError('Unchanged serving TLS rerun restarted a kubelet')
                    if (trust / '.operation.lock').exists():
                        raise RuntimeError('Serving TLS maintenance left an operation lock')
                    print('All four owned kubelets served CA-verified inventory-bound certificates; unchanged maintenance performed no approvals or kubelet restarts.', flush=True)
                if argocd_mode:
                    kubectl = ['kubectl', '--kubeconfig', str(trust / 'admin.conf'), '--request-timeout=10s']

                    def api(*args, data=None):
                        output = run(kubectl + list(args), input=json.dumps(data).encode() if data is not None else b'', capture_output=True).stdout
                        return json.loads(output) if output.strip() else {}

                    install = [REPO / 'bin/bareplane', 'gitops', 'install', '--approve', 'lab', config_path]

                    def refuse(reason):
                        try:
                            run(install, capture_output=True)
                        except subprocess.CalledProcessError:
                            phase = max((trust / 'logs').glob('argocd-*.log'), key=lambda p: p.stat().st_mtime)
                            if reason not in phase.read_text():
                                apply_diagnostics(trust)
                                raise RuntimeError('Argo installation failed outside the expected guard') from None
                        else:
                            raise RuntimeError('Unsafe Argo installation was accepted')
                        if (trust / 'argocd-ownership.json').exists():
                            raise RuntimeError('Read-only refusal recorded Kubernetes creation intent')

                    run([REPO / 'bin/bareplane', 'gitops', 'render', config_path])
                    refuse('Configured Git root is missing')
                    if api('get', 'namespace', 'argocd', '--ignore-not-found', '-o', 'json'):
                        raise RuntimeError('Missing repository path still created Argo state')
                    spec['gitops']['rootPath'] = 'examples/gitops/root' if handoff_mode else 'internal/render/gitops/assets/argocd'
                    write_yaml(config_path, config)
                    run([REPO / 'bin/bareplane', 'gitops', 'render', config_path])
                    foreign = api('create', 'namespace', 'argocd', '-o', 'json')
                    refuse('existing unmanaged Argo resource')
                    current = api('get', 'namespace', 'argocd', '-o', 'json')
                    if current['metadata']['uid'] != foreign['metadata']['uid'] or current['metadata'].get('annotations', {}).get('bareplane.io/cluster'):
                        raise RuntimeError('Unmanaged namespace was adopted or replaced')
                    api('delete', '--raw=/api/v1/namespaces/argocd', '-f', '-', data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=foreign['metadata']['uid'])))
                    run(kubectl + ['wait', '--for=delete', 'namespace/argocd', '--timeout=90s'])
                    run(install)
                    original = json.loads((trust / 'argocd-ownership.json').read_text())
                    if original['stage'] != 'ready' or len(original['resources']) != 38:
                        raise RuntimeError('Minimal Argo did not record full owned readiness')
                    run(install)
                    current = json.loads((trust / 'argocd-ownership.json').read_text())
                    if current != original:
                        raise RuntimeError('Argo rerun changed owned resource identities or content')
                    if json.loads((trust / 'gitops.json').read_text())['stage'] != 'argocd-ready':
                        raise RuntimeError('CLI did not record Argo readiness')
                    if api('get', 'applications,applicationsets', '-n', 'argocd', '-o', 'json')['items']:
                        raise RuntimeError('Installation performed an unrequested root handoff')
                    print('Pinned minimal Argo is Ready; public Git and unmanaged namespace guards passed; rerun preserved every resource UID.', flush=True)
                    if handoff_mode:
                        handoff = [REPO / 'bin/bareplane', 'gitops', 'handoff', '--approve', 'lab', config_path]
                        root_file = project / 'gitops/bootstrap/lab-root-application.yaml'
                        approved = root_file.read_bytes()
                        root_file.write_bytes(b'modified local export')
                        refused = subprocess.run([str(a) for a in handoff], capture_output=True, timeout=120)
                        root_file.write_bytes(approved)
                        if refused.returncode == 0 or api('get', 'application', 'lab-root', '-n', 'argocd', '--ignore-not-found', '-o', 'json'):
                            raise RuntimeError('Modified export reached root creation')
                        foreign_root = yaml.safe_load(approved)
                        foreign_root['metadata']['annotations'] = {'fixture': 'unmanaged'}
                        foreign_root['spec']['syncPolicy']['automated']['enabled'] = False
                        foreign_root = api('create', '-f', '-', '-o', 'json', data=foreign_root)
                        refused = subprocess.run([str(a) for a in handoff], capture_output=True, timeout=1800)
                        observed = api('get', 'application', 'lab-root', '-n', 'argocd', '-o', 'json')
                        if refused.returncode == 0 or observed['metadata']['uid'] != foreign_root['metadata']['uid'] or observed['metadata'].get('annotations', {}).get('bareplane.io/handoff'):
                            raise RuntimeError('Unrelated root was accepted or modified')
                        api('delete', '--raw=/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/lab-root', '-f', '-',
                            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=foreign_root['metadata']['uid'])))
                        run(kubectl + ['wait', '--for=delete', 'application/lab-root', '-n', 'argocd', '--timeout=90s'])
                        run(handoff)
                        root = api('get', 'application', 'lab-root', '-n', 'argocd', '-o', 'json')
                        receipt = json.loads((trust / 'handoff.json').read_text())
                        if not receipt['complete'] or receipt['mode'] != 'following' or root['spec']['source']['targetRevision'] != 'main' or root['spec']['source'].get('kustomize'):
                            raise RuntimeError('Verified snapshot was not transitioned to the configured revision')
                        child = api('get', 'application', 'lab-argocd', '-n', 'argocd', '-o', 'json')
                        if child['spec']['source']['targetRevision'] != 'main' or child['status']['sync']['status'] != 'Synced' or child['status']['health']['status'] != 'Healthy':
                            raise RuntimeError('Argo child did not reconcile the published source')
                        tracking = api('get', 'configmap', 'argocd-cm', '-n', 'argocd', '-o', 'json')['metadata'].get('annotations', {}).get('argocd.argoproj.io/tracking-id', '')
                        if not tracking.startswith('lab-argocd:'):
                            raise RuntimeError('Argo did not take steady-state ownership of its configuration')
                        for identity, entry in original['resources'].items():
                            kind, name = identity.split('/', 1)
                            args = ['get', kind, name, '-o', 'jsonpath={.metadata.uid}']
                            if kind not in {'Namespace', 'CustomResourceDefinition', 'ClusterRole', 'ClusterRoleBinding'}:
                                args += ['-n', 'argocd']
                            uid = run(kubectl + args, capture_output=True).stdout.decode()
                            if uid != entry['uid']:
                                raise RuntimeError('Argo self-management recreated an owned resource: ' + identity)
                        run(handoff)
                        repeat = api('get', 'application', 'lab-root', '-n', 'argocd', '-o', 'json')
                        if repeat['metadata']['uid'] != root['metadata']['uid'] or repeat['metadata']['generation'] != root['metadata']['generation']:
                            raise RuntimeError('Handoff rerun rewrote the root Application')
                        refused = subprocess.run([str(a) for a in install], capture_output=True, timeout=120)
                        if refused.returncode == 0 or json.loads((trust / 'gitops.json').read_text())['stage'] != 'gitops-handed-off':
                            raise RuntimeError('Installer reclaimed handed-off Argo state')
                        status = run([REPO / 'bin/bareplane', 'status', config_path], capture_output=True, text=True).stdout
                        if any(line not in status for line in ['kubernetes-ready: true', 'argocd-ready: true', 'gitops-handed-off: true']):
                            raise RuntimeError('Status did not distinguish verified lifecycle stages')
                        print('Root handoff verified pinned snapshot then following Git; Argo self-management and read-only rerun passed.', flush=True)
                        if cert_manager_mode:
                            run_cert_manager_acceptance(kubectl, REPO, work)
                        if metrics_mode:
                            run_metrics_server_acceptance(kubectl, REPO, work, names)
                        if dns_mode:
                            run_dns_acceptance(kubectl, REPO, work)
                if recovery_mode:
                    run([REPO / 'bin/bareplane', 'bootstrap', 'kubelet-tls', '--approve', 'lab', config_path])
                    run([REPO / 'bin/bareplane', 'bootstrap', 'diagnose', config_path])
                    (trust / 'admin.conf').unlink()
                    run([REPO / 'bin/bareplane', 'bootstrap', 'recover-kubeconfig', '--approve', 'lab', config_path])
                    if not (trust / 'admin.conf').is_file():
                        raise RuntimeError('Lost kubeconfig recovery did not restore the canonical file')
                    previous_ca = ssh(names[0], 'sha256sum /etc/kubernetes/pki/ca.crt', capture_output=True, text=True).stdout.split()[0]
                    # Image references are public fixture metadata, not runtime
                    # environment/configuration or credential-bearing logs.
                    runtime = json.loads(ssh(names[0], 'crictl ps -a -o json', capture_output=True, text=True).stdout)
                    print('Disposable reset image references: ' + json.dumps([container.get('image', {}) for container in runtime.get('containers', [])]), flush=True)
                    ssh(names[0], 'ls -1A /etc/kubernetes /var/lib/kubelet /var/lib/etcd')
                    run([REPO / 'bin/bareplane', 'bootstrap', 'reset', '--approve', 'lab', '--scope', 'cluster', '--confirm-destructive', config_path])
                    if (trust / 'admin.conf').exists() or (trust / 'reset.json').exists():
                        raise RuntimeError('Reset left canonical credentials or unfinished local intent')
                    preserved = ssh(names[0], 'cat /bareplane-user-data/sentinel', capture_output=True, text=True).stdout.strip()
                    current_uuid = ssh(names[0], 'blkid -s UUID -o value /dev/vdc', capture_output=True, text=True).stdout.strip()
                    if preserved != 'preserve-application-data' or current_uuid != disk_uuid:
                        raise RuntimeError('Reset changed unrelated application-disk data')
                    run(command)
                    current_ca = ssh(names[0], 'sha256sum /etc/kubernetes/pki/ca.crt', capture_output=True, text=True).stdout.split()[0]
                    if current_ca == previous_ca:
                        raise RuntimeError('Rebootstrap reused the destroyed cluster CA')
                    run([REPO / 'bin/bareplane', 'bootstrap', 'kubelet-tls', '--approve', 'lab', config_path])
                    if len(list(trust.glob('kubelet-tls-' + names[0] + '-*.json'))) != 2:
                        raise RuntimeError('Rebootstrap did not retain separate old/new CA-bound serving TLS audit receipts')
                    print('Reset/reboot/rebootstrap passed; unrelated application disk and sentinel were preserved.', flush=True)
            except subprocess.SubprocessError as error:
                for output in [getattr(error, 'stdout', None), getattr(error, 'stderr', None)]:
                    if output:
                        if isinstance(output, bytes):
                            output = output.decode(errors='replace')
                        print(re.sub(r'[a-z0-9]{6}\.[a-z0-9]{16}|[A-Za-z0-9+/=_-]{32,}', '[redacted]', output[-4000:]), flush=True)
                apply_diagnostics(trust)
                raise
            print('Guarded bootstrap apply formed the real ' + str(len(names)) + '-node cluster and repeated health only.', flush=True)
            return
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
        play('kubeconfig.yaml')
        exported = bundle.parent / 'state/bootstrap/admin.conf'
        if exported.stat().st_mode & 0o777 != 0o600:
            raise RuntimeError('Exported kubeconfig permissions are not private')
        # Verify this real multi-node cluster using the canonical controller
        # credential, without reading or displaying the certificate/key data.
        before = exported.stat().st_mtime_ns
        play('kubeconfig.yaml')
        if exported.stat().st_mtime_ns != before:
            raise RuntimeError('An unchanged kubeconfig was rewritten')
        play('health.yaml')
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
