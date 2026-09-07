#!/usr/bin/python
"""Shared read-only join identity and ownership checks."""
import base64
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import stat
import subprocess


STATE = '/var/lib/bareplane/bootstrap'
CA = '/etc/kubernetes/pki/ca.crt'
CONTROL_ROLE = 'node-role.kubernetes.io/control-plane'


def checked_path(path, directory=False):
    """Reject redirected ancestors even when the final path does not exist."""
    current = Path('/')
    parts = Path(path).parts[1:]
    for index, part in enumerate(parts):
        current /= part
        try:
            info = current.lstat()
        except FileNotFoundError:
            return False
        is_dir = index < len(parts) - 1 or directory
        if not (stat.S_ISDIR(info.st_mode) if is_dir else stat.S_ISREG(info.st_mode)):
            raise ValueError('Redirected or non-regular bootstrap path: ' + str(current))
    return True


def private_path(path, directory=False):
    if checked_path(path, directory=directory):
        info = os.lstat(path)
        if info.st_mode & 0o077 or info.st_uid != os.geteuid():
            raise ValueError('Bootstrap state must be owner-only and owned by the bootstrap account')


def read_file(path):
    if not checked_path(path):
        return None
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        data = stream.read(1048577)
    if len(data) > 1048576:
        raise ValueError('Oversized bootstrap state file')
    return data


def run(argv, data=None):
    result = subprocess.run(argv, input=data, capture_output=True, timeout=15, check=True)
    if len(result.stdout) > 1048576:
        raise ValueError('Oversized identity response')
    return result.stdout


def digest(identity):
    return hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def verify_node(node, identity, ready=False):
    if not node or node.get('metadata', {}).get('name') != identity['name'] or not node['metadata'].get('uid'):
        raise ValueError('Expected node identity is missing')
    control = CONTROL_ROLE in node['metadata'].get('labels', {})
    taints = node.get('spec', {}).get('taints', [])
    control_taint = any(t.get('key') == CONTROL_ROLE and t.get('effect') == 'NoSchedule' for t in taints)
    if control != identity['control_plane'] or control_taint != identity['control_plane']:
        raise ValueError('Node role or control-plane taint differs from the desired topology')
    status = node.get('status', {})
    if status.get('nodeInfo', {}).get('kubeletVersion') != 'v' + identity['version']:
        raise ValueError('Node kubelet version differs from the requested version')
    addresses = {a.get('address') for a in status.get('addresses', []) if a.get('type') == 'InternalIP'}
    if identity['address'] not in addresses:
        raise ValueError('Node address differs from the authenticated machine')
    if ready and not any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in status.get('conditions', [])):
        raise ValueError('Joined node is not Ready')


def decide(identity, node, intent, complete, local_ca, kubelet):
    """Pure transition logic: never retry kubeadm after an ambiguous attempt."""
    expected = (digest(identity) + '\n').encode()
    if intent is None and complete is None:
        if node or local_ca is not None or kubelet:
            raise ValueError('Unmanaged or foreign cluster state blocks joining; no automatic reset is allowed')
        return 'fresh'
    if intent != expected:
        raise ValueError('Join intent differs from this cluster, node, or configuration')
    if local_ca is None or hashlib.sha256(local_ca).hexdigest() != identity['ca_sha256'] or not kubelet:
        raise ValueError('Incomplete or foreign join state requires explicit recovery')
    verify_node(node, identity)
    if complete is not None and complete != expected + (node['metadata']['uid'] + '\n').encode():
        raise ValueError('Completed join has a different cluster configuration or node UID')
    # kubeadm may have succeeded just before the controller lost its connection.
    # Only verify readiness and finalize metadata in that case; never rejoin.
    return 'complete' if complete is not None else 'finalize'


def validate_admin(doc, cluster, ca_sha256, vip):
    if any(len(doc.get(field, [])) != 1 for field in ['clusters', 'users', 'contexts']):
        raise ValueError('Primary admin configuration has ambiguous cluster identity')
    entry, user, context = doc['clusters'][0], doc['users'][0], doc['contexts'][0]
    address = ipaddress.ip_address(vip)
    server = 'https://' + ('[' + str(address) + ']' if address.version == 6 else str(address)) + ':6443'
    if (entry['name'] != cluster or context['context'] != dict(cluster=cluster, user=user['name'])
            or doc.get('current-context') != context['name'] or entry['cluster'].get('server') != server
            or set(entry['cluster']) != {'server', 'certificate-authority-data'}
            or set(user['user']) != {'client-certificate-data', 'client-key-data'}
            or hashlib.sha256(base64.b64decode(entry['cluster']['certificate-authority-data'], validate=True)).hexdigest() != ca_sha256):
        raise ValueError('Primary admin configuration differs from the initialized cluster CA or VIP')


def inspect_primary(cluster, require_cilium=True, vip=None):
    if read_file('/etc/kubernetes/.bareplane-init-complete') != (cluster + '\n').encode():
        raise ValueError('The primary was not initialized by this Bareplane cluster')
    if require_cilium and read_file(STATE + '/cilium-complete') is None:
        raise ValueError('Bootstrap-owned Cilium must be complete before joining nodes')
    ca = read_file(CA)
    if not ca:
        raise ValueError('The primary cluster CA is missing')
    if read_file(STATE + '/init-ca-sha256') != (hashlib.sha256(ca).hexdigest() + '\n').encode():
        raise ValueError('Primary CA differs from initialized Bareplane state; explicit recovery is required')
    if not checked_path('/etc/kubernetes/admin.conf'):
        raise ValueError('The primary admin configuration is missing')
    private_path('/etc/kubernetes/admin.conf')
    admin = json.loads(run(['kubectl', '--kubeconfig', '/etc/kubernetes/admin.conf', 'config', 'view', '--raw', '-o', 'json']))
    validate_admin(admin, cluster, hashlib.sha256(ca).hexdigest(), vip)
    pubkey = run(['openssl', 'x509', '-pubkey', '-noout'], ca)
    der = run(['openssl', 'pkey', '-pubin', '-outform', 'DER'], pubkey)
    return dict(ca_sha256=hashlib.sha256(ca).hexdigest(), discovery_hash='sha256:' + hashlib.sha256(der).hexdigest())


def inspect_node(identity, node):
    for path in ['/etc/kubernetes', '/etc/kubernetes/manifests', '/etc/kubernetes/pki',
                 '/var/lib/bareplane', STATE, '/var/lib/kubelet', '/var/lib/etcd']:
        checked_path(path, directory=True)
    for name in ['join-intent', 'join-complete', 'join.yaml', 'join-output']:
        private_path(STATE + '/' + name)
    private_path('/var/lib/bareplane', directory=True)
    private_path(STATE, directory=True)
    initialized = read_file('/etc/kubernetes/.bareplane-init-complete')
    if initialized is not None and (not identity['control_plane'] or initialized != (identity['cluster'] + '\n').encode()):
        raise ValueError('Control-plane ownership marker differs from this cluster or role')
    kubelet = read_file('/etc/kubernetes/kubelet.conf') is not None
    state = decide(identity, node, read_file(STATE + '/join-intent'), read_file(STATE + '/join-complete'), read_file(CA), kubelet)
    if state == 'fresh':
        allowed = {'/etc/kubernetes/manifests', '/etc/kubernetes/manifests/.kubelet-keep',
                   '/var/lib/kubelet/.kubelet-keep'}
        if identity['control_plane']:
            allowed.add('/etc/kubernetes/manifests/kube-vip.yaml')
        for root in ['/etc/kubernetes', '/var/lib/kubelet']:
            if checked_path(root, directory=True):
                for directory, dirs, files in os.walk(root, followlinks=False):
                    for name in dirs + files:
                        path = str(Path(directory) / name)
                        if path not in allowed or Path(path).is_symlink():
                            raise ValueError('Unmanaged Kubernetes state blocks joining: ' + path)
                        if name.endswith('-keep') and read_file(path) != b'':
                            raise ValueError('Modified package marker blocks joining')
        if checked_path('/var/lib/etcd', directory=True):
            raise ValueError('Existing etcd data blocks joining')
        if any(read_file(STATE + '/' + name) is not None for name in ['join.yaml', 'join-output']):
            raise ValueError('Stale join material requires explicit recovery')
    else:
        if read_file(STATE + '/join.yaml') is not None:
            raise ValueError('Temporary join credentials remain; explicit cleanup is required before completion')
        # Authenticate using this machine's kubelet credential, not just the
        # primary's admin credential, to prove it belongs to the same cluster.
        local = json.loads(run(['kubectl', '--kubeconfig', '/etc/kubernetes/kubelet.conf',
                                '--request-timeout=5s', 'get', 'node', identity['name'], '-o', 'json']))
        if local.get('metadata', {}).get('uid') != node['metadata']['uid']:
            raise ValueError('Local kubelet authenticates to a different cluster')
    return dict(state=state, digest=digest(identity))


def main():
    from ansible.module_utils.basic import AnsibleModule
    module = AnsibleModule(argument_spec=dict(
        operation=dict(type='str', choices=['primary', 'initialized', 'node', 'verify'], required=True),
        cluster=dict(type='str'), identity=dict(type='dict'), node=dict(type='dict', default={}),
        vip=dict(type='str'),
    ), supports_check_mode=True)
    try:
        p = module.params
        if p['operation'] in ['primary', 'initialized']:
            result = inspect_primary(p['cluster'], require_cilium=p['operation'] == 'primary', vip=p['vip'])
        elif p['operation'] == 'verify':
            verify_node(p['node'], p['identity'], ready=True)
            result = dict(uid=p['node']['metadata']['uid'])
        else:
            result = inspect_node(p['identity'], p['node'])
        module.exit_json(changed=False, **result)
    except ValueError as error:
        module.fail_json(msg=str(error))
    except (OSError, subprocess.SubprocessError, KeyError, TypeError):
        module.fail_json(msg='Cannot verify join identity safely; inspect the machine privately before recovery')


if __name__ == '__main__':
    main()
