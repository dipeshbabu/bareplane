#!/usr/bin/python
"""Guarded node-local configuration transition; no keys or CSRs are exported."""

import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile

from ansible.module_utils import bareplane_join_state as owned
from ansible.module_utils.bareplane_kubelet_tls_state import Record, private_read, publish


CONFIG = '/var/lib/kubelet/config.yaml'
STATE = Path(owned.STATE)
LIMIT = 65536


def require(condition, message):
    if not condition:
        raise ValueError(message)


def fingerprint(data):
    return hashlib.sha256(data).hexdigest()


def configuration():
    require(owned.checked_path(CONFIG), 'Owned kubelet configuration is missing')
    info = os.lstat(CONFIG)
    # kubeadm v1.36.4 writes config.yaml as root-owned 0644. It has no inline
    # private keys. Permit that upstream mode or a hardened 0600, never writable
    # by another account, and preserve it when changing the serving flag.
    require(info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) in {0o600, 0o644}
            and info.st_nlink == 1 and info.st_size <= LIMIT, 'Kubelet configuration has unsafe ownership, permissions or size')
    data = owned.read_file(CONFIG)
    require(data and len(data) <= LIMIT, 'Kubelet configuration is missing or oversized')
    return data


def process_contract(identity):
    pid = owned.run(['systemctl', 'show', 'kubelet', '--property=MainPID', '--value']).decode().strip()
    require(re.fullmatch(r'[1-9][0-9]{0,9}', pid), 'Kubelet must be running before serving-TLS maintenance')
    command = owned.read_file('/proc/' + pid + '/cmdline').split(b'\0')
    require(command[0] == b'/usr/bin/kubelet', 'Kubelet is not running the reviewed binary path')
    config = [arg for arg in command if arg == b'--config' or arg.startswith(b'--config=')]
    require(config == [b'--config=' + CONFIG.encode()], 'Kubelet configuration path is overridden or ambiguous')
    for arg in command[1:]:
        name, _, value = arg.partition(b'=')
        require(name not in {b'--tls-cert-file', b'--tls-private-key-file', b'--server-tls-bootstrap', b'--read-only-port',
                             b'--anonymous-auth', b'--authorization-mode'}, 'A command-line override blocks reviewed serving-TLS configuration')
        if name in {b'--node-ip', b'--hostname-override'}:
            require(value == identity['address' if name == b'--node-ip' else 'name'].encode(), 'Kubelet command-line identity differs from inventory')


def inspect(identity, node, primary, vip):
    require(os.geteuid() == 0 and re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', identity['name'])
            and re.fullmatch('[0-9a-f]{64}', identity['ca_sha256']), 'Invalid root-owned serving-TLS identity')
    if primary:
        proof = owned.inspect_primary(identity['cluster'], vip=vip)
        require(proof['ca_sha256'] == identity['ca_sha256'], 'Primary bootstrap CA changed')
    else:
        require(owned.inspect_node(identity, node)['state'] == 'complete', 'A completed owned node join is required')
    owned.verify_node(node, identity, ready=True)
    local = json.loads(owned.run(['kubectl', '--kubeconfig', '/etc/kubernetes/kubelet.conf', '--request-timeout=5s',
                                 'get', 'node', identity['name'], '-o', 'json']))
    require(local.get('metadata', {}).get('uid') == node['metadata']['uid'], 'Node-local authentication reaches a different cluster identity')
    who = json.loads(owned.run(['kubectl', '--kubeconfig', '/etc/kubernetes/kubelet.conf', '--request-timeout=5s',
                               'auth', 'whoami', '-o', 'json']))['status']['userInfo']
    require(who.get('username') == 'system:node:' + identity['name'] and not who.get('uid')
            and sorted(who.get('groups', [])) == ['system:authenticated', 'system:nodes'], 'Node-local credential is not the expected kubelet identity')
    extra = who.get('extra', {})
    require(set(extra) == {'authentication.kubernetes.io/credential-id'} and len(extra['authentication.kubernetes.io/credential-id']) == 1,
            'Node-local authentication did not identify its X509 credential')
    credential_id = extra['authentication.kubernetes.io/credential-id'][0]
    require(re.fullmatch('X509SHA256=[0-9a-f]{64}', credential_id), 'Invalid authenticated node client certificate fingerprint')
    hostname = owned.run(['hostname']).decode().strip()
    require(hostname == identity['name'], 'Authenticated host name differs from the approved node')
    interfaces = json.loads(owned.run(['ip', '-j', '-4', 'address', 'show']))
    addresses = {entry['local'] for interface in interfaces for entry in interface.get('addr_info', []) if entry.get('family') == 'inet'}
    require(identity['address'] in addresses, 'Inventory address is not present on the authenticated host')
    machine = owned.read_file('/etc/machine-id').decode().strip()
    require(re.fullmatch('[0-9a-f]{32}', machine), 'Authenticated machine identity is unavailable')
    owned.private_path(str(STATE), directory=True)
    process_contract(identity)
    config = configuration()
    binding = dict(cluster=identity['cluster'], node=identity['name'], nodeUID=node['metadata']['uid'],
                   address=identity['address'], caSHA256=identity['ca_sha256'], machineID=machine)
    record = Record(STATE / 'kubelet-tls.json', binding)
    # Client certificates rotate independently. Keep their freshly authenticated
    # fingerprint out of the stable machine/CA ownership identity.
    record.credential_id = credential_id
    if record.original is not None:
        state = record.data['state']
        require(set(state) == {'stage', 'originalSHA256', 'targetSHA256'} and state['stage'] in {'prepared', 'configured', 'complete'}
                and all(re.fullmatch('[0-9a-f]{64}', state[key]) for key in ['originalSHA256', 'targetSHA256']),
                'Serving-TLS node receipt is incomplete or invalid')
        require(fingerprint(config) in {state['originalSHA256'], state['targetSHA256']}, 'Kubelet configuration drifted after TLS intent')
        require(state['stage'] == 'prepared' or fingerprint(config) == state['targetSHA256'], 'Configured kubelet was rolled back outside owned recovery')
        backup = private_read(STATE / 'kubelet-config-before-tls.yaml')
        require(backup is not None and fingerprint(backup) == state['originalSHA256'], 'Private kubelet configuration backup changed')
    return config, record


def configure(original, target, record, identity):
    # The controller validates full YAML. Independently enforce that the remote
    # write can change only the one plain root-level setting, preserving bytes.
    require(isinstance(original, bytes) and isinstance(target, bytes) and 0 < len(target) <= LIMIT,
            'Invalid serving-TLS configuration payload')
    candidates = {original + b'serverTLSBootstrap: true\n'}
    lines = original.splitlines(keepends=True)
    if lines.count(b'serverTLSBootstrap: false\n') == 1:
        candidates.add(b''.join(b'serverTLSBootstrap: true\n' if line == b'serverTLSBootstrap: false\n' else line for line in lines))
    if lines.count(b'serverTLSBootstrap: true\n') == 1:
        candidates.add(original)
    require(target in candidates, 'Serving-TLS transition would change unrelated kubelet bytes')
    current = configuration()
    if record.original is None:
        require(current == original and current != target, 'Existing serving-TLS configuration cannot be implicitly adopted')
        backup = STATE / 'kubelet-config-before-tls.yaml'
        previous = private_read(backup)
        require(previous is None or previous == original, 'An unrelated kubelet backup blocks transition')
        if previous is None:
            publish(backup, original)
        record.save(dict(stage='prepared', originalSHA256=fingerprint(original), targetSHA256=fingerprint(target)))
    else:
        require(fingerprint(target) == record.data['state']['targetSHA256'], 'TLS retry differs from the recorded target configuration')
    state = record.data['state']
    require(fingerprint(current) in {state['originalSHA256'], state['targetSHA256']}, 'Kubelet changed before its guarded transition')
    if state['stage'] == 'complete':
        require(current == target, 'Completed kubelet TLS configuration drifted')
        return False
    if current != target:
        descriptor, temporary = tempfile.mkstemp(prefix='.bareplane-kubelet-tls-', dir=Path(CONFIG).parent)
        try:
            with os.fdopen(descriptor, 'wb') as stream:
                os.fchmod(stream.fileno(), stat.S_IMODE(os.stat(CONFIG, follow_symlinks=False).st_mode))
                stream.write(target)
                stream.flush()
                os.fsync(stream.fileno())
            require(configuration() == current, 'Kubelet configuration changed during transition')
            os.replace(temporary, CONFIG)
            directory = os.open(Path(CONFIG).parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
    record.save(dict(state, stage='configured'))
    owned.run(['systemctl', 'restart', 'kubelet'])
    process_contract(identity)
    require(configuration() == target, 'Kubelet configuration changed after restart')
    record.save(dict(state, stage='complete'))
    return True


def main():
    from ansible.module_utils.basic import AnsibleModule
    module = AnsibleModule(argument_spec=dict(
        operation=dict(type='str', choices=['inspect', 'configure'], required=True),
        approved=dict(type='bool', default=False), identity=dict(type='dict', required=True), node=dict(type='dict', required=True),
        primary=dict(type='bool', default=False), vip=dict(type='str', required=True),
        original=dict(type='str', no_log=True), target=dict(type='str', no_log=True),
    ), supports_check_mode=True)

    def interrupted(signum, frame):
        raise ValueError('Serving TLS transition interrupted; retain private backup and intent')

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        p = module.params
        if p['operation'] == 'inspect':
            config, record = inspect(p['identity'], p['node'], p['primary'], p['vip'])
            module.exit_json(changed=False, configuration=base64.b64encode(config).decode(), identity=dict(record.data['identity'], credentialID=record.credential_id),
                             configured=record.original is not None and record.data['state']['stage'] == 'complete')
        require(p['approved'] and not module.check_mode, 'Serving-TLS configuration requires exact approved maintenance, not check mode')
        owned.private_path(str(STATE), directory=True)
        lock_path = STATE / '.kubelet-tls.lock'
        owned.private_path(str(lock_path))
        descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
        with os.fdopen(descriptor, 'rb') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            _, record = inspect(p['identity'], p['node'], p['primary'], p['vip'])
            original, target = base64.b64decode(p['original'], validate=True), base64.b64decode(p['target'], validate=True)
            changed = configure(original, target, record, p['identity'])
        module.exit_json(changed=changed, configured=True)
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.SubprocessError):
        module.fail_json(msg='Cannot safely prove or transition owned kubelet serving TLS; private backup and intent were retained for approved retry')


if __name__ == '__main__':
    main()
