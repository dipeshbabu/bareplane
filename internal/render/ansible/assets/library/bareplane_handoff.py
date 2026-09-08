#!/usr/bin/python
"""Verify a public snapshot, then hand root reconciliation to Argo."""

import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import sys
import tempfile

import yaml


def main():
    from ansible.module_utils.basic import AnsibleModule
    from ansible.module_utils import bareplane_kubeconfig as credentials
    from ansible.module_utils.bareplane_git_repository import Commands, GitOpsError, inspect_repository, require, validate_contract
    from ansible.module_utils.bareplane_argocd_state import Installer, Receipt
    from ansible.module_utils.bareplane_handoff_state import (
        HandoffInterrupted, Plan, RootClient, RootRecord, RootWriter, repository_probe, wait_reconciliation, verify_new_component_absence,
    )
    module = AnsibleModule(argument_spec=dict(
        kubeconfig=dict(type='path', required=True), cluster=dict(type='str', required=True), vip=dict(type='str', required=True),
        version=dict(type='str', required=True), approved=dict(type='bool', default=False),
        repository=dict(type='str', required=True), revision=dict(type='str', required=True), root_path=dict(type='str', required=True),
        input_dir=dict(type='path', required=True), contract=dict(type='str', required=True), argo_contract=dict(type='str', required=True),
    ), supports_check_mode=True)

    def interrupted(signum, frame):
        raise HandoffInterrupted('Handoff was interrupted; the owned root may already reconcile; inspect private intent before retry')

    signal.signal(signal.SIGTERM, interrupted)
    try:
        p = module.params
        require(sys.platform == 'linux' and not module.check_mode and p['approved'], 'Root handoff requires explicit approval on Linux/WSL and does not support check mode')
        require(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', p['cluster'])
                and all(re.fullmatch('[0-9a-f]{64}', p[key]) for key in ['contract', 'argo_contract']), 'Invalid handoff identity')
        validate_contract(p['repository'], p['revision'], p['root_path'])
        kubeconfig = credentials.check_destination(p['kubeconfig'])
        with kubeconfig.open('rb') as stream:
            content = stream.read(credentials.LIMIT + len(credentials.HEADER) + 66)
        document = credentials.decode(content.split(b'\n', 1)[1])
        ca = credentials.binary(document['clusters'][0]['cluster']['certificate-authority-data'])
        ca_hash = hashlib.sha256(ca).hexdigest()
        require(credentials.existing(kubeconfig, p['cluster'], ca_hash, credentials.endpoint(p['vip'], 6443)), 'Private kubeconfig identity is unavailable')
        state, inputs = kubeconfig.parent, Path(p['input_dir'])
        require(str(inputs) == str(state / 'handoff-input'), 'Handoff input must use its canonical private location')
        info = inputs.lstat()
        require(stat.S_ISDIR(info.st_mode) and not info.st_mode & 0o077, 'Handoff input must be private and not redirected')
        files, total = {}, 0
        for directory, directories, names in os.walk(inputs, followlinks=False):
            for name in directories:
                info = (Path(directory) / name).lstat()
                require(stat.S_ISDIR(info.st_mode) and not info.st_mode & 0o022, 'Handoff input has a redirected or writable directory')
            for name in names:
                path = Path(directory) / name
                relative = path.relative_to(inputs).as_posix()
                info = path.lstat()
                require(stat.S_ISREG(info.st_mode) and not info.st_mode & 0o022 and info.st_size <= 4 * 1024 * 1024,
                        'Handoff input files must be bounded owned regular files')
                with path.open('rb') as stream:
                    data = stream.read(4 * 1024 * 1024 + 1)
                require(len(data) <= 4 * 1024 * 1024, 'Handoff input file exceeds its limit')
                files[relative] = data
                total += len(data)
                require(len(files) <= 4097 and total <= 32 * 1024 * 1024, 'Handoff input exceeds the bounded payload limit')
        marker = json.loads(files.pop('.bareplane-generated.json'))
        require(marker == dict(managedBy='bareplane', kind='handoff-input', version=1), 'Handoff input is unmanaged')
        digest = hashlib.sha256(json.dumps([p['argo_contract']], separators=(',', ':')).encode())
        for name, data in sorted(files.items()):
            require(all(re.fullmatch(r'[a-z0-9][a-z0-9_.-]*', part) and part not in {'.', '..'} for part in name.split('/')),
                    'Handoff input path is invalid')
            digest.update(f'{len(name)}:{name}:{len(data)}:'.encode())
            digest.update(data)
        require(digest.hexdigest() == p['contract'], 'Handoff input differs from the approved local render')
        plan = Plan(files, p['cluster'], p['repository'], p['revision'], p['root_path'])
        work = state / 'handoff-work'
        try:
            work.mkdir(mode=0o700)
        except FileExistsError:
            pass
        info = work.lstat()
        require(stat.S_ISDIR(info.st_mode) and not info.st_mode & 0o077, 'Handoff working directory must be private and not redirected')
        commands = Commands(work, timeout=1500)
        client = RootClient(commands, kubeconfig, plan.root['metadata']['name'], p['cluster'])
        require(client.json('version', '--client=true', '-o', 'json')['clientVersion']['gitVersion'] == 'v' + p['version'],
                'Controller kubectl must match the pinned Kubernetes version')
        argo = Receipt(state, p['cluster'], ca_hash, p['argo_contract'], '0' * 40)
        require(argo.original is not None and argo.data['stage'] == 'ready', 'Owned Argo readiness is required')
        record = RootRecord(state, p['cluster'], ca_hash, argo.data['installation'], p['contract'], plan.root['metadata']['name'], '0' * 40)
        writer = RootWriter(client, record, plan)
        if record.original is None:
            require(not client.get(plan.root), 'An existing unrelated root Application blocks handoff')
        namespace = client.json('get', 'namespace', 'argocd', '-o', 'json')
        require(namespace['metadata']['uid'] == argo.data['resources']['Namespace/argocd']['uid']
                and not namespace['metadata'].get('deletionTimestamp'), 'Argo namespace identity changed')
        changed = False
        if record.data['mode'] == 'pinned':
            commit = inspect_repository(commands, p['repository'], p['revision'], p['root_path'], expected_files=files)
            with tempfile.TemporaryDirectory(prefix='handoff-snapshot-', dir=work) as temporary:
                for name, data in files.items():
                    target = Path(temporary) / name
                    target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                    descriptor = os.open(target, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
                    with os.fdopen(descriptor, 'wb') as stream:
                        stream.write(data)
                rendered = commands.run(['kubectl', 'kustomize', str(Path(temporary) / 'components/argocd')])
                if record.original is None:
                    for child in plan.children.values():
                        source = child['spec']['source']['path']
                        if source != 'components/argocd':
                            component = commands.run(['kubectl', 'kustomize', str(Path(temporary) / source)])
                            verify_new_component_absence(client, list(yaml.safe_load_all(component)))
            baseline = Installer(client, argo, list(yaml.safe_load_all(rendered)), p['cluster'])
            for obj in baseline.resources:
                current = client.get(obj)
                if record.original is None:
                    baseline.verify(obj, current, argo.data['resources'][obj['kind'] + '/' + obj['metadata']['name']])
                else:
                    require(current and current['metadata']['uid'] == argo.data['resources'][obj['kind'] + '/' + obj['metadata']['name']]['uid']
                            and current['metadata'].get('annotations', {}).get('bareplane.io/installation') == argo.data['installation'],
                            'Argo continuity changed during initial handoff')
            baseline.wait_ready()
            if record.original is None:
                baseline.verify_no_handoff()
            repository_probe(client, p['repository'], argo)
            previous_uid = record.data['uid']
            writer.write('pinned', commit)
            changed = not previous_uid or record.data['commit'] != commit
            wait_reconciliation(client, writer, plan, pinned=True)
            latest = inspect_repository(commands, p['repository'], p['revision'], p['root_path'], expected_files=files)
            require(latest == commit, 'Configured revision advanced during initial verification; the root remains pinned; retry the new reviewed snapshot')
            record.verified()
            writer.write('following', commit)
            changed = True
        elif record.data['pending']:
            writer.write('following', record.data['commit'])
            changed = True
        wait_reconciliation(client, writer, plan, pinned=False)
        if not record.data['complete']:
            record.complete()
        module.exit_json(changed=changed, handed_off=True, initial_commit=record.data['commit'],
                         msg='Root and child reconciliation verified; Argo owns long-lived platform state')
    except GitOpsError as error:
        module.fail_json(msg=str(error))
    except (OSError, ValueError, KeyError, IndexError, TypeError, AttributeError, RecursionError, yaml.YAMLError):
        module.fail_json(msg='Cannot safely verify root ownership, published snapshot, or Argo reconciliation; no completion was recorded')


if __name__ == '__main__':
    main()
