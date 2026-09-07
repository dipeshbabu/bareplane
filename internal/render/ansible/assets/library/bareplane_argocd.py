#!/usr/bin/python
"""Controller-only minimal Argo installation using private bootstrap access."""

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


INPUTS = {'configuration.yaml', 'kustomization.yaml', 'license.txt', 'namespace.yaml', 'parameters.yaml', 'upstream.yaml'}


def main():
    from ansible.module_utils.basic import AnsibleModule
    from ansible.module_utils import bareplane_kubeconfig as credentials
    from ansible.module_utils.bareplane_git_repository import Commands, GitOpsError, inspect_repository, require
    from ansible.module_utils.bareplane_argocd_state import Client, Installer, Receipt, VERSION

    module = AnsibleModule(argument_spec=dict(
        kubeconfig=dict(type='path', required=True), cluster=dict(type='str', required=True), vip=dict(type='str', required=True),
        version=dict(type='str', required=True), approved=dict(type='bool', default=False),
        repository=dict(type='str', required=True), revision=dict(type='str', required=True), root_path=dict(type='str', required=True),
        input_dir=dict(type='path', required=True), contract=dict(type='str', required=True),
    ), supports_check_mode=True)

    def interrupted(signum, frame):
        raise GitOpsError('Argo installation was interrupted; inspect private ownership state before retry')

    signal.signal(signal.SIGTERM, interrupted)
    try:
        p = module.params
        require(sys.platform == 'linux' and not module.check_mode and p['approved'],
                'Argo installation requires explicit approval on a Linux/WSL controller and cannot run in check mode')
        require(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', p['cluster'])
                and re.fullmatch(r'[0-9a-f]{64}', p['contract']), 'Invalid Argo installation identity')
        kubeconfig = credentials.check_destination(p['kubeconfig'])
        with kubeconfig.open('rb') as stream:
            content = stream.read(credentials.LIMIT + len(credentials.HEADER) + 66)
        document = credentials.decode(content.split(b'\n', 1)[1])
        ca = credentials.binary(document['clusters'][0]['cluster']['certificate-authority-data'])
        ca_hash = hashlib.sha256(ca).hexdigest()
        require(credentials.existing(kubeconfig, p['cluster'], ca_hash, credentials.endpoint(p['vip'], 6443)),
                'Private bootstrap kubeconfig is unavailable')
        state = kubeconfig.parent
        inputs = Path(p['input_dir'])
        require(str(inputs) == str(state / 'argocd-input'), 'Argo payload must be in the canonical private bootstrap directory')
        info = inputs.lstat()
        require(stat.S_ISDIR(info.st_mode) and not info.st_mode & 0o077, 'Argo payload directory must be private and must not be a symlink')
        require({entry.name for entry in inputs.iterdir()} == INPUTS | {'.bareplane-generated.json'}, 'Argo payload contains unexpected files')
        files = {}
        for name in sorted(INPUTS | {'.bareplane-generated.json'}):
            path = inputs / name
            info = path.lstat()
            require(stat.S_ISREG(info.st_mode) and not info.st_mode & 0o022 and info.st_size <= 4 * 1024 * 1024,
                    'Argo input files must be bounded owned regular files')
            with path.open('rb') as stream:
                files[name] = stream.read(4 * 1024 * 1024 + 1)
            require(len(files[name]) <= 4 * 1024 * 1024, 'Argo input exceeded its size limit')
        require(json.loads(files.pop('.bareplane-generated.json')) == dict(managedBy='bareplane', kind='argocd-input', version=1),
                'Argo payload is unmanaged')
        digest = hashlib.sha256(json.dumps([p['cluster'], p['repository'], p['revision'], p['root_path'], VERSION], separators=(',', ':')).encode())
        for name, data in sorted(files.items()):
            digest.update(f'{len(name)}:{name}:{len(data)}:'.encode())
            digest.update(data)
        require(digest.hexdigest() == p['contract'], 'Argo payload or repository contract changed after approval')
        work = state / 'argocd-work'
        try:
            work.mkdir(mode=0o700)
        except FileExistsError:
            pass
        info = work.lstat()
        require(stat.S_ISDIR(info.st_mode) and not info.st_mode & 0o077, 'Argo working directory must be private and not redirected')
        commands = Commands(work)
        client = Client(commands, kubeconfig)
        version = client.json('version', '--client=true', '-o', 'json')
        require(version['clientVersion']['gitVersion'] == 'v' + p['version'], 'Controller kubectl must match the pinned Kubernetes version')
        commit = inspect_repository(commands, p['repository'], p['revision'], p['root_path'])
        # Build from a fresh private snapshot of the already-verified bytes,
        # never from the user export or an untrusted remote Kustomize tree.
        with tempfile.TemporaryDirectory(prefix='argocd-payload-', dir=work) as temporary:
            for name, data in files.items():
                descriptor = os.open(str(Path(temporary) / name), os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
                with os.fdopen(descriptor, 'wb') as stream:
                    stream.write(data)
            rendered = commands.run(['kubectl', 'kustomize', temporary])
        resources = list(yaml.safe_load_all(rendered))
        receipt = Receipt(state, p['cluster'], ca_hash, p['contract'], commit)
        changed = Installer(client, receipt, resources, p['cluster']).install(commit)
        module.exit_json(changed=changed, ready=True, argo_version=VERSION, repository_commit=commit,
                         msg='Minimal Argo is ready; no root Application or platform payload was applied')
    except GitOpsError as error:
        module.fail_json(msg=str(error))
    except (OSError, ValueError, KeyError, IndexError, TypeError, AttributeError, RecursionError, yaml.YAMLError):
        module.fail_json(msg='Cannot safely verify private Argo ownership, repository, input, or Kubernetes response; readiness was not recorded')


if __name__ == '__main__':
    main()
