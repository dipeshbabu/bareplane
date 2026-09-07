"""Minimal Argo creation with UID-bound resume and no unmanaged adoption."""

import copy
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import stat
import tempfile
import time

from ansible.module_utils.bareplane_git_repository import GitOpsError, require


VERSION = '3.5.2'
INSTALLATION = 'bareplane.io/installation'
CLUSTER_KINDS = {'Namespace', 'CustomResourceDefinition', 'ClusterRole', 'ClusterRoleBinding'}
COMPONENTS = ['application-controller', 'redis', 'repo-server', 'server']
EXPECTED = {'Namespace/argocd', 'Secret/argocd-secret'}
for kind, names in {
    'CustomResourceDefinition': ['applications.argoproj.io', 'applicationsets.argoproj.io', 'appprojects.argoproj.io'],
    'ServiceAccount': ['argocd-' + name for name in COMPONENTS],
    'Role': ['argocd-application-controller', 'argocd-redis', 'argocd-server'],
    'RoleBinding': ['argocd-application-controller', 'argocd-redis', 'argocd-server'],
    'ClusterRole': ['argocd-application-controller', 'argocd-server'],
    'ClusterRoleBinding': ['argocd-application-controller', 'argocd-server'],
    'ConfigMap': ['argocd-' + name for name in ['cm', 'cmd-params-cm', 'gpg-keys-cm', 'rbac-cm', 'ssh-known-hosts-cm', 'tls-certs-cm']],
    'Service': ['argocd-' + name for name in ['metrics', 'redis', 'repo-server', 'server', 'server-metrics']],
    'Deployment': ['argocd-redis', 'argocd-repo-server', 'argocd-server'],
    'StatefulSet': ['argocd-application-controller'],
    'NetworkPolicy': ['argocd-' + name + '-network-policy' for name in COMPONENTS],
}.items():
    EXPECTED.update(kind + '/' + name for name in names)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode() + b'\n'


def identity(obj):
    return obj['kind'] + '/' + obj['metadata']['name']


def private_file(path, limit):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not info.st_mode & 0o077 and info.st_size <= limit,
            'Argo ownership records must be bounded private regular files')
    with path.open('rb') as stream:
        data = stream.read(limit + 1)
    require(len(data) <= limit, 'Argo ownership record exceeds the size limit')
    return data


def normalized(obj):
    """Hash desired API-defaulted state, not status or allocated identities."""
    value = copy.deepcopy(obj)
    value.pop('status', None)
    metadata = value['metadata']
    require(not metadata.get('ownerReferences') and not metadata.get('deletionTimestamp'),
            'An Argo resource is terminating or has an unexpected controlling owner')
    finalizers = metadata.get('finalizers', [])
    allowed_finalizers = ['customresourcecleanup.apiextensions.k8s.io'] if value['kind'] == 'CustomResourceDefinition' else []
    require(not finalizers or finalizers == allowed_finalizers, 'An Argo resource has an unexpected finalizer')
    value['metadata'] = {k: v for k, v in metadata.items() if k in {'name', 'namespace', 'labels', 'annotations'}}
    annotations = value['metadata'].get('annotations', {})
    if value['kind'] == 'Deployment':
        annotations.pop('deployment.kubernetes.io/revision', None)
    if value['kind'] == 'Service':
        service = value['spec']
        require(service.get('type', 'ClusterIP') == 'ClusterIP', 'Argo services must not be publicly exposed')
        address = service.pop('clusterIP', None)
        addresses = service.pop('clusterIPs', None)
        if address:
            require(address != 'None' and addresses == [address], 'Argo Service has an unexpected allocated address')
            ipaddress.ip_address(address)
    if value['kind'] == 'Secret':
        require(value['metadata']['name'] == 'argocd-secret' and not value.get('stringData'), 'Unexpected Argo Secret declaration')
        require(set(value.get('data', {})) <= {'admin.password', 'admin.passwordMtime', 'server.secretkey', 'tls.crt', 'tls.key'},
                'Argo Secret has unexpected credential keys; refusing ownership changes')
        value.pop('data', None)
    return hashlib.sha256(canonical(value)).hexdigest()


class Client:
    def __init__(self, commands, kubeconfig):
        self.commands, self.kubeconfig = commands, str(kubeconfig)

    def json(self, *args, data=None):
        raw = self.commands.run(['kubectl', '--kubeconfig', self.kubeconfig, '--request-timeout=10s', *args],
                                data=json.dumps(data).encode() if data is not None else b'')
        return json.loads(raw) if raw.strip() else {}

    def get(self, obj):
        args = ['get', obj['kind'], obj['metadata']['name'], '--ignore-not-found', '-o', 'json']
        if obj['kind'] not in CLUSTER_KINDS:
            args += ['--namespace', 'argocd']
        return self.json(*args)

    def create(self, obj, dry_run=False):
        args = ['create', '--field-manager=bareplane-argocd', '-f', '-', '-o', 'json']
        if dry_run:
            args += ['--dry-run=server']
        return self.json(*args, data=obj)


class Receipt:
    def __init__(self, directory, cluster, ca, contract, commit):
        self.path = Path(directory) / 'argocd-ownership.json'
        self.original = None
        if self.path.exists() or self.path.is_symlink():
            self.original = private_file(self.path, 65536)
            self.data = json.loads(self.original)
            require(canonical(self.data) == self.original, 'Argo ownership record is not canonical')
            require(set(self.data) == {'version', 'cluster', 'caSHA256', 'contractSHA256', 'installation', 'stage', 'pending', 'resources', 'repositoryCommit'},
                    'Argo ownership record has unexpected fields')
            require(self.data['version'] == 1 and self.data['cluster'] == cluster and self.data['caSHA256'] == ca
                    and self.data['contractSHA256'] == contract, 'Argo installation belongs to another cluster, CA, or payload; implicit upgrades are refused')
            require(re.fullmatch(r'[0-9a-f]{32}', self.data['installation']) and self.data['stage'] in {'installing', 'ready'}
                    and re.fullmatch(r'[0-9a-f]{40}', self.data['repositoryCommit']), 'Invalid Argo ownership identity')
            entries = self.data['resources']
            require(isinstance(entries, dict) and set(entries) <= EXPECTED, 'Argo ownership resource inventory is invalid')
            for entry in entries.values():
                require(set(entry) == {'uid', 'sha256'} and isinstance(entry['uid'], str) and len(entry['uid']) <= 128
                        and re.fullmatch(r'[0-9a-f]{64}', entry['sha256']), 'Invalid Argo resource ownership record')
            pending = self.data['pending']
            require(pending is None or (pending in entries and not entries[pending]['uid']), 'Argo pending resource is invalid')
            require(all(entry['uid'] or key == pending for key, entry in entries.items()), 'Ambiguous Argo creation record')
            if self.data['stage'] == 'ready':
                require(set(entries) == EXPECTED and pending is None and all(e['uid'] for e in entries.values()), 'Incomplete Argo installation was marked ready')
        else:
            self.data = dict(version=1, cluster=cluster, caSHA256=ca, contractSHA256=contract,
                             installation=secrets.token_hex(16), stage='installing', pending=None, resources={}, repositoryCommit=commit)

    def save(self):
        if self.original is None:
            require(not self.path.exists() and not self.path.is_symlink(), 'Argo ownership appeared during installation')
        else:
            require(private_file(self.path, 65536) == self.original, 'Argo ownership changed during installation')
        data = canonical(self.data)
        descriptor, name = tempfile.mkstemp(prefix='.argocd-state-', dir=self.path.parent)
        try:
            with os.fdopen(descriptor, 'wb') as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            if self.original is None:
                os.link(name, self.path)  # First publication must not overwrite.
            else:
                os.replace(name, self.path)
            self.original = data
        finally:
            if os.path.exists(name):
                os.unlink(name)


def validate_payload(resources, cluster):
    require(len(resources) == len(EXPECTED) and {identity(obj) for obj in resources} == EXPECTED,
            'Only the reviewed minimal Argo resource set may be installed')
    for obj in resources:
        metadata = obj['metadata']
        annotations = metadata.get('annotations', {})
        require(annotations.get('bareplane.io/cluster') == cluster and annotations.get('bareplane.io/component') == 'argocd'
                and INSTALLATION not in annotations, 'Argo payload ownership differs from the reviewed contract')
        require((obj['kind'] in CLUSTER_KINDS and not metadata.get('namespace'))
                or (obj['kind'] not in CLUSTER_KINDS and metadata.get('namespace') == 'argocd'), 'Unexpected Argo resource namespace')
        if obj['kind'] == 'Secret':
            require(not obj.get('data') and not obj.get('stringData'), 'Bootstrap must not provide runtime credential values')


class Installer:
    def __init__(self, client, receipt, resources, cluster):
        validate_payload(resources, cluster)
        self.client, self.receipt, self.cluster = client, receipt, cluster
        # Namespace first; otherwise creation follows a stable dependency order.
        order = ['Namespace', 'CustomResourceDefinition', 'ServiceAccount', 'ClusterRole', 'Role', 'ClusterRoleBinding',
                 'RoleBinding', 'ConfigMap', 'Secret', 'Service', 'NetworkPolicy', 'Deployment', 'StatefulSet']
        self.resources = sorted(copy.deepcopy(resources), key=lambda obj: (order.index(obj['kind']), obj['metadata']['name']))
        for obj in self.resources:
            obj['metadata']['annotations'][INSTALLATION] = receipt.data['installation']

    def verify(self, expected, observed, entry):
        require(observed and observed['metadata'].get('annotations', {}).get(INSTALLATION) == self.receipt.data['installation']
                and observed['metadata'].get('annotations', {}).get('bareplane.io/cluster') == self.cluster,
                'An Argo resource is missing or belongs to another installation')
        uid = observed['metadata'].get('uid')
        require(isinstance(uid, str) and uid and (not entry['uid'] or uid == entry['uid']), 'An Argo resource UID changed')
        require(identity(expected) == identity(observed) and normalized(observed) == entry['sha256'],
                'An owned Argo resource drifted from its approved API-defaulted state; implicit repair is refused: ' + identity(expected))
        return uid

    def install(self, commit):
        changed = False
        state = self.receipt.data
        if self.receipt.original is None:
            # Refuse all pre-existing names before creating the namespace. Labels
            # alone are never sufficient to take ownership of an installation.
            for obj in self.resources:
                require(not self.client.get(obj), 'An existing unmanaged Argo resource blocks installation')
            self.receipt.save()
        for obj in self.resources:
            key = identity(obj)
            observed = self.client.get(obj)
            entry = state['resources'].get(key)
            if entry:
                if observed:
                    uid = self.verify(obj, observed, entry)
                    if not entry['uid']:
                        entry['uid'], state['pending'] = uid, None
                        self.receipt.save()
                    continue
                require(not entry['uid'] and state['pending'] == key and state['stage'] == 'installing',
                        'An owned Argo resource disappeared; explicit recovery is required')
            else:
                require(not observed and state['stage'] == 'installing' and state['pending'] is None,
                        'Unexpected Argo state appeared during installation')
                expected = self.client.create(obj, dry_run=True)
                entry = dict(uid='', sha256=normalized(expected))
                state['resources'][key], state['pending'] = entry, key
                self.receipt.save()
            observed = self.client.create(obj)
            entry['uid'] = self.verify(obj, observed, entry)
            state['pending'] = None
            self.receipt.save()
            changed = True
        self.wait_ready()
        for obj in self.resources:
            self.verify(obj, self.client.get(obj), state['resources'][identity(obj)])
        self.verify_no_handoff()
        state['stage'], state['repositoryCommit'] = 'ready', commit
        self.receipt.save()
        return changed

    def wait_ready(self):
        deadline = time.monotonic() + 600
        while True:
            pending = []
            for obj in self.resources:
                kind = obj['kind']
                if kind not in {'CustomResourceDefinition', 'Deployment', 'StatefulSet'}:
                    continue
                observed = self.client.get(obj)
                require(observed, 'An Argo readiness resource disappeared')
                status = observed.get('status', {})
                if kind == 'CustomResourceDefinition':
                    complete = any(c.get('type') == 'Established' and c.get('status') == 'True' for c in status.get('conditions', []))
                else:
                    replicas = observed['spec'].get('replicas', 1)
                    complete = replicas > 0 and status.get('observedGeneration', 0) >= observed['metadata']['generation']
                    complete = complete and all(status.get(field, 0) == replicas for field in ['replicas', 'updatedReplicas', 'readyReplicas', 'availableReplicas'])
                    if kind == 'StatefulSet':
                        complete = complete and status.get('currentRevision') == status.get('updateRevision') and status.get('currentReplicas', 0) == replicas
                if not complete:
                    pending.append(identity(obj))
            if not pending:
                return
            require(time.monotonic() < deadline, 'Argo readiness timed out for: ' + ', '.join(pending))
            time.sleep(2)

    def verify_no_handoff(self):
        for kind in ['applications.argoproj.io', 'applicationsets.argoproj.io']:
            require(not self.client.json('get', kind, '--namespace', 'argocd', '-o', 'json').get('items'),
                    'GitOps Applications already exist; installation cannot reclaim a handed-off control plane')
        cilium = self.client.json('get', 'daemonset', 'cilium', '--namespace', 'kube-system', '-o', 'json')
        metadata = cilium['metadata']
        require('argocd.argoproj.io/tracking-id' not in metadata.get('annotations', {})
                and 'argocd.argoproj.io/instance' not in metadata.get('labels', {}), 'Cilium must remain outside Argo ownership')
