#!/usr/bin/python
"""Bootstrap-only full-cluster reset with explicit ownership boundaries."""
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import shutil
import stat
import subprocess
import tempfile


STATE = '/var/lib/bareplane/bootstrap'
RECEIPT = STATE + '/reset-receipt.json'
PROBE_DIGEST = 'sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
PROBE_IMAGES = {'docker.io/library/busybox:1.37.0@' + PROBE_DIGEST, 'docker.io/library/busybox@' + PROBE_DIGEST}
# Reviewed defaults from the checksum-pinned Cilium charts used by bootstrap.
CILIUM_IMAGES = {
    '1.20.0': ('383968cd5e8873f7976fa76aa6196045643558f4cc9518a207b9335cb24a0e93',
               '80744a8cc7c91c2f9e6347629406844eb35d79b30a732c6d41c15b17232a74f3'),
    '1.20.1': ('ae9ea21f7427fe24bc6ea7247eb552157a1b0a431744045d3f641545ca71d11b',
               '6c3885fc7b629099fdbe2a5c87869c86feb825fa18fae299eac0f61918d16ecf'),
}
OWNED_STATE = ['init-intent', 'init-ca-sha256', 'kubeadm.yaml', 'init-output', 'join-intent', 'join-complete',
               'join.yaml', 'join-output', 'cilium-intent', 'cilium-complete', 'cilium-values.yaml']
KUBE_FILES = {'admin.conf', 'super-admin.conf', 'kubelet.conf', 'bootstrap-kubelet.conf', 'controller-manager.conf',
              'scheduler.conf', '.bareplane-init-complete', 'manifests', 'pki'}
KUBELET_FILES = {'.kubelet-keep', 'config.yaml', 'instance-config.yaml', 'kubeadm-flags.env', 'pki', 'pods', 'plugins',
                 'plugins_registry', 'device-plugins', 'pod-resources', 'cpu_manager_state', 'memory_manager_state',
                 'checkpoints', 'allocated_pods_state', 'actuated_pods_state', 'dra_manager_state', 'image_manager'}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def command(args, timeout=20):
    result = subprocess.run(args, capture_output=True, timeout=timeout, check=True)
    require(len(result.stdout) <= 4 * 1024 * 1024, 'Reset inspection response exceeded its bound')
    return result.stdout


def approved_image_reference(image, images, prefixes):
    def named(reference):
        return reference in images or any(reference == prefix or re.fullmatch(re.escape(prefix) + r'@sha256:[a-f0-9]{64}', reference) for prefix in prefixes)

    if named(image):
        return True
    # containerd ListContainers reports image config IDs, not display tags.
    # Resolve that immutable ID through CRI and require a pinned named alias.
    if re.fullmatch(r'sha256:[a-f0-9]{64}', image) is None:
        return False
    status = json.loads(command(['crictl', 'inspecti', image]))['status']
    if status.get('id') != image:
        return False
    return any(named(alias) for alias in status.get('repoTags', []) + status.get('repoDigests', []))


def owned_probe_sandbox(sandbox, cluster):
    metadata, annotations = sandbox.get('metadata', {}), sandbox.get('annotations', {})
    nonce = annotations.get('bareplane.io/health-run', '')
    return (re.fullmatch(r'[a-f0-9]{16}', nonce) is not None and annotations.get('bareplane.io/cluster') == cluster
            and metadata.get('namespace') == 'bareplane-health-' + nonce and metadata.get('name') in ['server', 'client'])


def sandbox_cleanup_order(sandbox):
    name = sandbox.get('metadata', {}).get('name', '')
    if name.startswith(('kube-apiserver-', 'kube-controller-manager-', 'kube-scheduler-', 'etcd-', 'kube-vip-')):
        return 3, sandbox['id']
    if name.startswith('cilium-') and not name.startswith('cilium-operator-'):
        return 2, sandbox['id']
    if name.startswith('cilium-operator-'):
        return 1, sandbox['id']
    return 0, sandbox['id']


def remove_owned_sandboxes(sandboxes):
    # Pod-network cleanup requires the Cilium agent. Remove workloads before
    # Cilium, and retain the API/static control plane until those are gone.
    for sandbox in sorted(sandboxes, key=sandbox_cleanup_order):
        identifier = sandbox['id']
        require(re.fullmatch(r'[a-f0-9]{64}', identifier), 'Invalid CRI sandbox identity blocks reset')
        command(['crictl', 'stopp', identifier], timeout=45)
        command(['crictl', 'rmp', identifier], timeout=45)


def snapshot(path, helpers):
    data = helpers.read_file(path)
    if data is None:
        return None
    require(len(data) <= 4096, 'Reset receipt is oversized')
    result = json.loads(data)
    require(isinstance(result, dict), 'Reset receipt is malformed')
    return result


def write_private(path, value):
    fd, temporary = tempfile.mkstemp(prefix='.reset-stage-', dir=str(Path(path).parent))
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, sort_keys=True)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def inspect_tree(path, helpers):
    if not helpers.checked_path(path, directory=True):
        return
    require(not os.path.ismount(path), 'Dedicated or redirected Kubernetes data mounts are unsupported for reset')
    for directory, dirs, files in os.walk(path, followlinks=False):
        for name in dirs + files:
            item = Path(directory) / name
            info = item.lstat()
            require(not stat.S_ISLNK(info.st_mode) and (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)),
                    'Redirected or special Kubernetes state blocks reset')
            require(not os.path.ismount(item), 'Nested data mounts block bootstrap reset')


def inspect(p, helpers):
    require(re.fullmatch(r'[a-f0-9]{32}', p['reset_id']) is not None, 'Invalid reset identifier')
    require(command(['kubeadm', 'version', '-o', 'short']).decode().strip() == 'v' + p['kubernetes_version'], 'Reset requires the pinned kubeadm version')
    runtime_versions = re.findall(r'(?m)^\s*Version:\s*v?([^\s]+)', command(['ctr', '--timeout', '10s', 'version']).decode())
    require(runtime_versions == ['2.2.6', '2.2.6'], 'Reset requires the pinned managed containerd client and server')
    require(command(['kubelet', '--version']).decode().strip() == 'Kubernetes v' + p['kubernetes_version'], 'Reset requires the pinned kubelet version')
    running = subprocess.run(['pgrep', '-x', 'kubeadm'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
    require(running.returncode == 1, 'Another kubeadm process is active or cannot be ruled out; wait before reset')
    namespaces = command(['ctr', 'namespaces', 'list', '-q']).decode().splitlines()
    require(set(namespaces) <= {'k8s.io'}, 'Unmanaged containerd namespaces block bootstrap reset')
    sandboxes = json.loads(command(['crictl', 'pods', '-o', 'json'])).get('items', [])
    probe_ids = {sandbox.get('id') for sandbox in sandboxes if owned_probe_sandbox(sandbox, p['cluster'])}
    for sandbox in sandboxes:
        metadata = sandbox.get('metadata', {})
        require(sandbox.get('id') in probe_ids or (metadata.get('namespace') == 'kube-system' and metadata.get('name', '').startswith(
                ('kube-apiserver-', 'kube-controller-manager-', 'kube-scheduler-', 'etcd-', 'kube-vip-', 'cilium-', 'coredns-'))),
                'Unmanaged CRI pod sandboxes block bootstrap reset')
    images = set(command(['kubeadm', 'config', 'images', 'list', '--kubernetes-version', 'v' + p['kubernetes_version']]).decode().splitlines())
    require(p['cilium_version'] in CILIUM_IMAGES, 'Reset requires a reviewed Cilium image pin')
    for repository, digest in zip(['quay.io/cilium/cilium', 'quay.io/cilium/operator-generic'], CILIUM_IMAGES[p['cilium_version']]):
        images.add(repository + '@sha256:' + digest)
        images.add(repository + ':v' + p['cilium_version'] + '@sha256:' + digest)
    prefixes = ['ghcr.io/kube-vip/kube-vip:v' + p['kube_vip_version']]
    checked_images = {}
    for container in json.loads(command(['crictl', 'ps', '-a', '-o', 'json'])).get('containers', []):
        specification = container.get('image', {})
        # kubelet preserves the requested pinned reference separately from the
        # resolved config ID in current CRI ImageSpec messages.
        image = specification.get('userSpecifiedImage') or specification.get('image', '')
        probe = container.get('podSandboxId') in probe_ids
        key = (image, probe)
        if key not in checked_images:
            checked_images[key] = approved_image_reference(image, PROBE_IMAGES if probe else images, [] if probe else prefixes)
        require(checked_images[key], 'Unrecognized container images block bootstrap reset')
    for path in ['/etc/kubernetes', '/etc/kubernetes/manifests', '/etc/kubernetes/pki', '/var/lib/kubelet',
                 '/var/lib/etcd', '/etc/cni/net.d', '/var/lib/bareplane', STATE]:
        helpers.checked_path(path, directory=True)
    for path in ['/etc/kubernetes/manifests', '/etc/kubernetes/pki', '/var/lib/etcd']:
        inspect_tree(path, helpers)
    require(not os.path.ismount('/var/lib/kubelet'), 'A mounted kubelet data root blocks reset')
    for root, allowed in [('/etc/kubernetes', KUBE_FILES), ('/var/lib/kubelet', KUBELET_FILES), ('/var/lib/etcd', {'member'})]:
        if Path(root).exists():
            require(set(os.listdir(root)) <= allowed, 'Unrecognized data in the Kubernetes-owned root ' + root + ' blocks reset')
    if Path('/etc/kubernetes/manifests').exists():
        allowed = {'.kubelet-keep', 'kube-vip.yaml', 'kube-apiserver.yaml', 'kube-controller-manager.yaml', 'kube-scheduler.yaml', 'etcd.yaml'}
        require(set(os.listdir('/etc/kubernetes/manifests')) <= allowed, 'Unmanaged static pod manifests block reset')
    if Path('/etc/cni/net.d').exists():
        require(set(os.listdir('/etc/cni/net.d')) <= {'.kubernetes-cni-keep', '05-cilium.conflist'}, 'Unmanaged CNI configuration blocks reset')
        cni = helpers.read_file('/etc/cni/net.d/05-cilium.conflist')
        if cni:
            parsed = json.loads(cni)
            require(parsed.get('name') == 'cilium' and [v.get('type') for v in parsed.get('plugins', [])] == ['cilium-cni'],
                    'Unmanaged CNI configuration blocks reset')
    for name in OWNED_STATE + ['reset-receipt.json']:
        helpers.private_path(STATE + '/' + name)
    ca = helpers.read_file('/etc/kubernetes/pki/ca.crt')
    ca_hash = hashlib.sha256(ca).hexdigest() if ca else ''
    receipt = snapshot(RECEIPT, helpers)
    if receipt and receipt.get('reset_id') == p['reset_id']:
        require(receipt.get('cluster') == p['cluster'] and receipt.get('node') == p['name'] and receipt.get('version') == 1,
                'Reset receipt belongs to a different node or cluster')
        require(not ca_hash or ca_hash == receipt.get('ca_sha256'), 'A different cluster appeared during reset')
        if receipt.get('stage') in ['clean', 'complete']:
            require(not ca_hash and not Path('/etc/kubernetes/kubelet.conf').exists() and not Path('/var/lib/etcd/member').exists(),
                    'Kubernetes state reappeared after reset; refusing to reset it again')
        return dict(state='resetting', ca_sha256=receipt.get('ca_sha256', ''), needs_reset=receipt.get('stage') not in ['clean', 'complete'], sandboxes=sandboxes)
    if receipt:
        require(receipt.get('stage') == 'complete', 'Another incomplete reset requires explicit recovery')
    primary = p['name'] == p['primary']
    intent_path = STATE + ('/init-intent' if primary else '/join-intent')
    intent = helpers.read_file(intent_path)
    if intent is None:
        require(not ca_hash and not helpers.checked_path('/etc/kubernetes/kubelet.conf') and not Path('/var/lib/etcd').exists(),
                'Unmanaged or foreign Kubernetes state blocks reset')
        for path in ['/etc/kubernetes/manifests/kube-apiserver.yaml', '/var/lib/kubelet/config.yaml']:
            require(not Path(path).exists(), 'Unmanaged partial Kubernetes state blocks reset')
        require(not sandboxes and not any(helpers.read_file(STATE + '/' + name) is not None for name in OWNED_STATE),
                'Unmanaged bootstrap records block reset of a prepared host')
        if Path('/etc/kubernetes').exists():
            require(set(os.listdir('/etc/kubernetes')) <= {'manifests'}, 'Unmanaged Kubernetes configuration blocks reset')
        if Path('/var/lib/kubelet').exists():
            require(set(os.listdir('/var/lib/kubelet')) <= {'.kubelet-keep'}, 'Unmanaged kubelet data blocks reset')
        require(not Path('/etc/cni/net.d/05-cilium.conflist').exists(), 'Unmanaged Cilium state blocks reset')
        state = 'prepared'
    else:
        expected = p['init_digest'] if primary else p['join_digest']
        require(expected and intent == (expected + '\n').encode(), 'Kubeadm intent differs from the requested cluster or node')
        if primary:
            stored = helpers.read_file(STATE + '/kubeadm.yaml')
            require(stored and hashlib.sha256(stored).hexdigest() == expected, 'Primary configuration ownership cannot be established')
        if ca_hash and p['ca_sha256']:
            require(ca_hash == p['ca_sha256'], 'Node CA belongs to a different cluster')
        marker = helpers.read_file('/etc/kubernetes/.bareplane-init-complete')
        if marker is not None:
            require(marker == (p['cluster'] + '\n').encode(), 'Control-plane ownership marker is foreign')
        if primary and marker is not None:
            require(helpers.read_file(STATE + '/init-ca-sha256') == (ca_hash + '\n').encode(), 'Primary CA ownership record is missing or changed')
        state = 'owned'
    vip = helpers.read_file('/etc/kubernetes/manifests/kube-vip.yaml')
    if vip is not None:
        require(hashlib.sha256(vip).hexdigest() in p['vip_hashes'], 'Modified or foreign kube-vip manifest blocks reset')
    return dict(state=state, ca_sha256=ca_hash, needs_reset=state == 'owned', sandboxes=sandboxes)


def bootstrap_pods_only(pods, names):
    allowed = ('kube-apiserver-', 'kube-controller-manager-', 'kube-scheduler-', 'etcd-', 'kube-vip-', 'cilium-', 'coredns-')
    for pod in pods:
        metadata = pod.get('metadata', {})
        require(metadata.get('namespace') == 'kube-system' and metadata.get('name', '').startswith(allowed),
                'Application or unrecognized pods block bootstrap-only reset')
        require(pod.get('spec', {}).get('nodeName') in names, 'Pods on unmanaged nodes block reset')


def guard_api(p, helpers):
    ca = helpers.read_file('/etc/kubernetes/pki/ca.crt')
    if not ca or not helpers.checked_path('/etc/kubernetes/admin.conf'):
        require(p['allow_unavailable_api'], 'API credentials are required to exclude application storage before reset')
        return
    try:
        admin = json.loads(command(['kubectl', '--kubeconfig', '/etc/kubernetes/admin.conf', 'config', 'view', '--raw', '-o', 'json']))
        helpers.validate_admin(admin, p['cluster'], hashlib.sha256(ca).hexdigest(), p['vip'])
        base = ['kubectl', '--kubeconfig', '/etc/kubernetes/admin.conf', '--request-timeout=5s']
        command(base + ['get', '--raw=/readyz'])
    except (OSError, subprocess.SubprocessError):
        require(p['allow_unavailable_api'], 'API access is required to exclude application storage before reset')
        return
    for resource in ['persistentvolumes', 'persistentvolumeclaims', 'storageclasses', 'statefulsets', 'jobs', 'cronjobs']:
        require(not json.loads(command(base + ['get', resource, '-A', '-o', 'json']))['items'], 'Persistent application storage blocks bootstrap reset')
    namespaces = json.loads(command(base + ['get', 'namespaces', '-o', 'json']))['items']
    for namespace in namespaces:
        metadata = namespace['metadata']
        annotations = metadata.get('annotations', {})
        owned_cilium = (metadata['name'] == 'cilium-secrets' and annotations.get('meta.helm.sh/release-name') == 'bareplane-cilium'
                        and annotations.get('meta.helm.sh/release-namespace') == 'kube-system'
                        and metadata.get('labels', {}).get('app.kubernetes.io/managed-by') == 'Helm')
        require(metadata['name'] in ['default', 'kube-system', 'kube-public', 'kube-node-lease'] or owned_cilium,
                'Application or leftover verification namespace blocks bootstrap reset: ' + metadata['name'])
    bootstrap_pods_only(json.loads(command(base + ['get', 'pods', '-A', '-o', 'json']))['items'], p['nodes'])
    for deployment in json.loads(command(base + ['get', 'deployments', '-A', '-o', 'json']))['items']:
        require(deployment['metadata'].get('namespace') == 'kube-system' and deployment['metadata']['name'] in ['coredns', 'cilium-operator'],
                'Application deployments block bootstrap reset')
    for crd in json.loads(command(base + ['get', 'customresourcedefinitions', '-o', 'json']))['items']:
        require(crd['metadata']['name'].endswith('.cilium.io'), 'Non-bootstrap extensions block reset')
    nodes = json.loads(command(base + ['get', 'nodes', '-o', 'json']))['items']
    require({node['metadata']['name'] for node in nodes} <= set(p['nodes']), 'Unmanaged registered nodes block reset')


def reset_node(p, helpers):
    require(p.get('approved') is True, 'Destructive reset requires explicit approval')
    result = inspect(p, helpers)
    for path in ['/var/lib/bareplane', STATE, STATE + '/recovery']:
        helpers.private_path(path, directory=True)
        Path(path).mkdir(mode=0o700, exist_ok=True)
    archive = Path(STATE) / 'recovery' / p['reset_id']
    helpers.private_path(str(archive), directory=True)
    archive.mkdir(mode=0o700, exist_ok=True)
    receipt = snapshot(RECEIPT, helpers)
    if not receipt or receipt.get('reset_id') != p['reset_id']:
        receipt = dict(version=1, reset_id=p['reset_id'], cluster=p['cluster'], node=p['name'], ca_sha256=result['ca_sha256'],
                       stage='started', boot_id=Path('/proc/sys/kernel/random/boot_id').read_text().strip())
        write_private(RECEIPT, receipt)
    if receipt['stage'] in ['clean', 'complete']:
        return
    for name in OWNED_STATE:
        source, destination = Path(STATE) / name, archive / name
        data = helpers.read_file(str(source))
        if data is not None:
            previous = helpers.read_file(str(destination))
            require(previous is None or previous == data, 'A diagnostic archive differs from current state; inspect it before reset')
            if previous is None:
                with destination.open('xb') as stream:
                    destination.chmod(0o600)
                    stream.write(data)
                    stream.flush()
                    os.fsync(stream.fileno())
    command(['systemctl', 'stop', 'kubelet'])
    if result['needs_reset']:
        current_ids = {item['id'] for item in json.loads(command(['crictl', 'pods', '-o', 'json'])).get('items', [])}
        require(current_ids <= {item['id'] for item in result['sandboxes']}, 'New unvalidated CRI pods appeared during reset')
        remove_owned_sandboxes([item for item in result['sandboxes'] if item['id'] in current_ids])
        descriptor, output = tempfile.mkstemp(prefix='reset-output-', dir=archive)
        with os.fdopen(descriptor, 'wb') as stream:
            subprocess.run(['kubeadm', 'reset', '--force', '--cri-socket=unix:///run/containerd/containerd.sock',
                            '--cert-dir=/etc/kubernetes/pki', '--skip-phases=remove-etcd-member'], stdout=stream, stderr=subprocess.STDOUT,
                           timeout=300, check=True, preexec_fn=lambda: resource.setrlimit(resource.RLIMIT_FSIZE, (1048576, 1048576)))
        require(not json.loads(command(['crictl', 'pods', '-o', 'json'])).get('items'), 'CRI pods remain; reset is incomplete')
    for path in ['/etc/kubernetes/pki', '/etc/kubernetes/manifests', '/var/lib/kubelet']:
        if result['needs_reset']:
            require(not Path(path).exists() or not os.listdir(path), 'Kubeadm cleanup left Kubernetes state; inspect private reset output')
    if Path('/etc/kubernetes/pki').exists():
        Path('/etc/kubernetes/pki').rmdir()
    member = Path('/var/lib/etcd/member')
    if member.exists():
        inspect_tree('/var/lib/etcd', helpers)
        shutil.rmtree(member)
    if Path('/var/lib/etcd').exists():
        Path('/var/lib/etcd').rmdir()
    for path in ['/etc/cni/net.d/05-cilium.conflist', '/etc/kubernetes/manifests/kube-vip.yaml', '/etc/kubernetes/.bareplane-init-complete'] + [STATE + '/' + name for name in OWNED_STATE]:
        if helpers.checked_path(path):
            os.unlink(path)
    receipt['stage'] = 'clean'
    write_private(RECEIPT, receipt)


def main():
    from ansible.module_utils.basic import AnsibleModule
    from ansible.module_utils import bareplane_join_state as helpers
    module = AnsibleModule(argument_spec=dict(
        operation=dict(type='str', choices=['facts', 'inspect', 'guard', 'reset', 'finish'], required=True),
        reset_id=dict(type='str', required=True), cluster=dict(type='str', required=True), name=dict(type='str', required=True),
        primary=dict(type='str', required=True), nodes=dict(type='list', elements='str', default=[]), vip=dict(type='str', required=True),
        kubernetes_version=dict(type='str', required=True), cilium_version=dict(type='str', required=True), kube_vip_version=dict(type='str', required=True),
        ca_sha256=dict(type='str', default=''), init_digest=dict(type='str', default=''), join_digest=dict(type='str', default=''),
        vip_hashes=dict(type='list', elements='str', default=[]), allow_unavailable_api=dict(type='bool', default=False),
        approved=dict(type='bool', default=False),
    ), supports_check_mode=True)
    try:
        p = module.params
        require(os.geteuid() == 0, 'Reset inspection requires non-interactive root access')
        if p['operation'] == 'facts':
            ca = helpers.read_file('/etc/kubernetes/pki/ca.crt')
            receipt = snapshot(RECEIPT, helpers)
            digest = hashlib.sha256(ca).hexdigest() if ca else ''
            complete = False
            if receipt and receipt.get('reset_id') == p['reset_id']:
                require(receipt.get('cluster') == p['cluster'] and receipt.get('node') == p['name'], 'Foreign reset receipt')
                digest = digest or receipt.get('ca_sha256', '')
                complete = receipt.get('stage') == 'complete'
            require(not digest or re.fullmatch(r'[a-f0-9]{64}', digest), 'Invalid reset CA identity')
            module.exit_json(changed=False, ca_sha256=digest, reset_complete=complete)
        elif p['operation'] == 'inspect':
            summary = inspect(p, helpers)
            summary.pop('sandboxes', None)
            module.exit_json(changed=False, **summary)
        elif p['operation'] == 'guard':
            guard_api(p, helpers)
            module.exit_json(changed=False)
        else:
            require(p['approved'] and not module.check_mode, 'Destructive reset requires explicit approval and cannot run in check mode')
            if p['operation'] == 'reset':
                reset_node(p, helpers)
            else:
                receipt = snapshot(RECEIPT, helpers)
                require(receipt and receipt.get('reset_id') == p['reset_id'] and receipt.get('stage') in ['clean', 'complete'], 'Reset cleanup has not completed')
                require(receipt['boot_id'] != Path('/proc/sys/kernel/random/boot_id').read_text().strip(), 'A reboot is required to clear bootstrap network state')
                receipt['stage'] = 'complete'
                write_private(RECEIPT, receipt)
            module.exit_json(changed=True)
    except ValueError as error:
        module.fail_json(msg=str(error))
    except (OSError, subprocess.SubprocessError, KeyError, TypeError):
        module.fail_json(msg='Bootstrap reset could not complete safely; inspect private reset state and diagnostics')


if __name__ == '__main__':
    main()
