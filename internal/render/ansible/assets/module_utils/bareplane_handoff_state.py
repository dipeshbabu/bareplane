"""Guarded root-Application planning and bounded GitOps acceptance."""

import copy
import hashlib
import json
from pathlib import Path
import re
import secrets
import time

import yaml

from ansible.module_utils.bareplane_git_repository import GitOpsError, require
from ansible.module_utils.bareplane_argocd_state import Client, Receipt, VERSION, canonical, private_file


HANDOFF = 'bareplane.io/handoff'
OWNED_ANNOTATIONS = {'bareplane.io/cluster', 'bareplane.io/component', 'argocd.argoproj.io/sync-wave', HANDOFF}
CONTROLLER_ANNOTATIONS = {'argocd.argoproj.io/refresh', 'argocd.argoproj.io/hydrate', 'notified.notifications.argoproj.io'}


class HandoffInterrupted(GitOpsError):
    pass


def root_hash(obj):
    metadata = obj['metadata']
    require(not metadata.get('ownerReferences') and not metadata.get('finalizers') and not metadata.get('deletionTimestamp'),
            'Root Application is terminating or has unexpected owners/finalizers')
    annotations = metadata.get('annotations', {})
    require(set(annotations) <= OWNED_ANNOTATIONS | CONTROLLER_ANNOTATIONS,
            'Root Application has unexpected ownership or reconciliation annotations')
    desired = dict(apiVersion=obj['apiVersion'], kind=obj['kind'], spec=obj['spec'], metadata=dict(
        name=metadata['name'], namespace=metadata['namespace'], labels=metadata.get('labels', {}),
        annotations={key: value for key, value in annotations.items() if key in OWNED_ANNOTATIONS},
    ))
    return hashlib.sha256(json.dumps(desired, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


class Plan:
    def __init__(self, files, cluster, repository, revision, root):
        self.cluster, self.repository, self.revision, self.path = cluster, repository, revision, root
        filename = 'bootstrap/' + cluster + '-root-application.yaml'
        self.root = yaml.safe_load(files[filename])
        self.validate_application(self.root, root, root_application=True)
        source = yaml.safe_load(files[root + '/kustomization.yaml'])
        require(set(source) == {'apiVersion', 'kind', 'resources'} and source['apiVersion'] == 'kustomize.config.k8s.io/v1beta1'
                and source['kind'] == 'Kustomization' and isinstance(source['resources'], list) and source['resources'],
                'Root source must be the reviewed closed App-of-Apps Kustomization')
        self.children = {}
        for name in source['resources']:
            require(isinstance(name, str) and re.fullmatch(r'applications/[a-z0-9][a-z0-9_.-]*\.yaml', name),
                    'Root source may include only reviewed child Application manifests')
            child = yaml.safe_load(files[root + '/' + name])
            path = child['spec']['source']['path']
            require(re.fullmatch(r'components/[a-z0-9][a-z0-9-]*', path) and path + '/kustomization.yaml' in files,
                    'Child Application source is outside the approved component graph')
            self.validate_application(child, path)
            key = child['metadata']['name']
            require(key not in self.children and key != self.root['metadata']['name'], 'Duplicate or recursive child Application')
            self.children[key] = child

    def validate_application(self, app, path, root_application=False):
        require(app['apiVersion'] == 'argoproj.io/v1alpha1' and app['kind'] == 'Application', 'Only Argo Applications are supported for root handoff')
        metadata, spec = app['metadata'], app['spec']
        require(metadata.get('namespace') == 'argocd' and re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', metadata['name'])
                and metadata.get('annotations', {}).get('bareplane.io/cluster') == self.cluster and not metadata.get('finalizers'),
                'Application metadata or ownership differs from the approved cluster')
        require(spec['source'] == dict(repoURL=self.repository, targetRevision=self.revision, path=path),
                'Application source differs from the approved repository/revision/path')
        require(spec.get('project') == 'default' and spec['destination'].get('server') == 'https://kubernetes.default.svc'
                and set(spec['destination']) == {'server', 'namespace'}, 'Application targets an unrelated project or cluster')
        if root_application:
            require(spec['destination']['namespace'] == 'argocd' and metadata['annotations'].get('bareplane.io/component') == 'root',
                    'Root Application destination or component differs')
        require(not spec['syncPolicy']['automated'].get('prune') and not spec['syncPolicy']['automated'].get('allowEmpty'),
                'Initial handoff must not enable cascading prune or empty synchronization')

    def pinned_root(self, commit, nonce):
        require(re.fullmatch(r'[0-9a-f]{40}', commit) and re.fullmatch(r'[0-9a-f]{32}', nonce), 'Invalid pinned handoff identity')
        root = copy.deepcopy(self.root)
        root['metadata']['annotations'][HANDOFF] = nonce
        source = root['spec']['source']
        source['targetRevision'] = commit
        source['kustomize'] = {'patches': [dict(
            target=dict(group='argoproj.io', version='v1alpha1', kind='Application', namespace='argocd', name=name),
            patch=json.dumps([dict(op='replace', path='/spec/source/targetRevision', value=commit)], separators=(',', ':')),
        ) for name in sorted(self.children)]}
        return root

    def following_root(self, nonce):
        require(re.fullmatch(r'[0-9a-f]{32}', nonce), 'Invalid handoff identity')
        root = copy.deepcopy(self.root)
        root['metadata']['annotations'][HANDOFF] = nonce
        return root


def application_health(app, expected_commit=None):
    """Return sanitized pending/failure state, never controller error bodies."""
    state = app.get('status', {})
    errors = [c.get('type') for c in state.get('conditions', []) if c.get('type') in
              {'ComparisonError', 'InvalidSpecError', 'SyncError', 'UnknownError', 'SharedResourceWarning'}]
    operation = state.get('operationState', {})
    health = state.get('health', {}).get('status')
    sync = state.get('sync', {})
    return (not app.get('operation') and not errors and operation.get('phase') not in {'Running', 'Terminating', 'Error', 'Failed'} and sync.get('status') == 'Synced' and health == 'Healthy'
            and (expected_commit is None or sync.get('revision') == expected_commit)
            and sync.get('comparedTo', {}).get('source') == app['spec']['source'])


def health_summary(app):
    state = app.get('status', {})
    sync = state.get('sync', {}).get('status')
    health = state.get('health', {}).get('status')
    phase = state.get('operationState', {}).get('phase')
    parts = [
        sync if sync in {'Synced', 'OutOfSync', 'Unknown'} else 'Unknown',
        health if health in {'Healthy', 'Progressing', 'Degraded', 'Suspended', 'Missing', 'Unknown'} else 'Unknown',
        phase if phase in {'Running', 'Terminating', 'Succeeded', 'Failed', 'Error'} else 'Unknown',
    ]
    if state.get('sync', {}).get('comparedTo', {}).get('source') != app.get('spec', {}).get('source'):
        parts.append('SourcePending')
    if app.get('operation'):
        parts.append('OperationPending')
    return '/'.join(parts)


class RootRecord(Receipt):
    """Reuse the private atomic receipt writer with a root-specific schema."""

    def __init__(self, directory, cluster, ca, installation, contract, name, commit):
        self.path = Path(directory) / 'handoff.json'
        self.original = None
        if self.path.exists() or self.path.is_symlink():
            self.original = private_file(self.path, 65536)
            self.data = json.loads(self.original)
            require(canonical(self.data) == self.original, 'Handoff receipt is not canonical')
            require(set(self.data) == {'version', 'cluster', 'caSHA256', 'argocdInstallation', 'contractSHA256', 'rootName',
                                      'nonce', 'uid', 'previousHash', 'targetHash', 'mode', 'pending', 'commit', 'verified', 'complete'},
                    'Handoff receipt has unexpected fields')
            require(self.data['version'] == 1 and self.data['cluster'] == cluster and self.data['caSHA256'] == ca
                    and self.data['argocdInstallation'] == installation and self.data['contractSHA256'] == contract
                    and self.data['rootName'] == name, 'Handoff receipt belongs to different cluster, Argo, or GitOps inputs')
            for field, length in [('nonce', 32), ('targetHash', 64), ('commit', 40)]:
                require(isinstance(self.data[field], str) and re.fullmatch('[0-9a-f]{' + str(length) + '}', self.data[field]),
                        'Handoff receipt identity is malformed')
            require(isinstance(self.data['uid'], str) and len(self.data['uid']) <= 128
                    and self.data['mode'] in {'pinned', 'following'}, 'Invalid root ownership state')
            require(all(isinstance(self.data[key], bool) for key in ['pending', 'verified', 'complete']), 'Invalid handoff stage flags')
            previous = self.data['previousHash']
            require((previous == '' and not self.data['uid'] and self.data['pending'])
                    or (isinstance(previous, str) and re.fullmatch('[0-9a-f]{64}', previous)), 'Invalid prior root identity')
            require(self.data['mode'] != 'following' or self.data['verified'], 'Following Git requires a verified initial snapshot')
            require(not self.data['complete'] or (self.data['mode'] == 'following' and not self.data['pending'] and self.data['uid']),
                    'Incomplete root ownership was marked handed off')
        else:
            self.data = dict(version=1, cluster=cluster, caSHA256=ca, argocdInstallation=installation,
                             contractSHA256=contract, rootName=name, nonce=secrets.token_hex(16), uid='', previousHash='',
                             targetHash='', mode='pinned', pending=False, commit=commit, verified=False, complete=False)

    def verified(self):
        require(self.data['uid'] and not self.data['pending'] and self.data['mode'] == 'pinned', 'Cannot verify an unacknowledged root')
        self.data['verified'] = True
        self.save()

    def complete(self):
        require(self.data['uid'] and not self.data['pending'] and self.data['mode'] == 'following' and self.data['verified'],
                'Cannot complete an unverified ownership transition')
        self.data['complete'] = True
        self.save()


class RootWriter:
    def __init__(self, client, record, plan):
        self.client, self.record, self.plan = client, record, plan

    def verify(self, observed, target_only=False):
        data = self.record.data
        require(observed and observed['metadata']['name'] == data['rootName'] and observed['metadata'].get('namespace') == 'argocd'
                and observed['metadata'].get('annotations', {}).get(HANDOFF) == data['nonce']
                and observed['metadata'].get('annotations', {}).get('bareplane.io/cluster') == data['cluster'],
                'Root Application is missing or belongs to unrelated ownership')
        uid = observed['metadata'].get('uid')
        require(isinstance(uid, str) and uid and (not data['uid'] or uid == data['uid']), 'Root Application UID changed')
        expected = {data['targetHash']}
        if data['pending'] and not target_only:
            expected.add(data['previousHash'])
        require(root_hash(observed) in expected, 'Root Application differs from its approved write intent; refusing replacement')
        return observed

    def acknowledge(self, observed):
        self.verify(observed, target_only=True)
        data = self.record.data
        data['uid'] = observed['metadata']['uid']
        data['previousHash'], data['pending'] = data['targetHash'], False
        self.record.save()
        return observed

    def write(self, mode, commit):
        data = self.record.data
        require(mode in {'pinned', 'following'} and re.fullmatch('[0-9a-f]{40}', commit), 'Invalid root transition')
        require(not data['complete'], 'Completed handoff is read-only')
        require(data['mode'] != 'following' or mode == 'following', 'GitOps ownership cannot return to bootstrap pinning')
        require(mode != 'following' or (data['verified'] and commit == data['commit']), 'Initial snapshot must be verified before following Git')
        observed = self.client.get(self.plan.root)
        if self.record.original is None:
            require(not observed, 'An existing unrelated root Application blocks handoff')
        elif observed:
            self.verify(observed)
            if data['pending'] and root_hash(observed) == data['targetHash']:
                self.acknowledge(observed)
        else:
            require(not data['uid'], 'The owned root Application disappeared; explicit recovery is required')
        if observed and not data['pending'] and data['mode'] == mode and data['commit'] == commit:
            return self.verify(observed, target_only=True)
        desired = self.plan.pinned_root(commit, data['nonce']) if mode == 'pinned' else self.plan.following_root(data['nonce'])

        def request(current):
            if not current:
                return copy.deepcopy(desired)
            value = copy.deepcopy(current)
            value.pop('status', None)
            value['spec'] = copy.deepcopy(desired['spec'])
            return value  # UID and resourceVersion are mandatory update preconditions.

        if not data['pending'] or data['mode'] != mode or data['commit'] != commit:
            require(mode != 'following' or not observed.get('operation'), 'Root synchronization became active; retry the guarded transition')
            preview = self.client.replace(request(observed), dry_run=True) if observed else self.client.create(desired, dry_run=True)
            data['previousHash'] = root_hash(observed) if observed else ''
            data['targetHash'] = root_hash(preview)
            data['uid'] = observed['metadata']['uid'] if observed else ''
            data['mode'], data['commit'], data['pending'] = mode, commit, True
            if mode == 'pinned':
                data['verified'] = False
            self.record.save()
        for attempt in range(3):
            try:
                result = self.client.replace(request(observed)) if observed else self.client.create(desired)
            except HandoffInterrupted:
                raise
            except GitOpsError:
                current = self.client.get(self.plan.root)
                if current:
                    self.verify(current)
                    if root_hash(current) == data['targetHash']:
                        return self.acknowledge(current)
                    observed = current
                else:
                    require(not data['uid'], 'Owned root disappeared during an update')
                if attempt == 2:
                    raise GitOpsError('Root write could not be acknowledged; inspect private handoff intent before retry') from None
            else:
                return self.acknowledge(result)


class RootClient(Client):
    def __init__(self, commands, kubeconfig, name, cluster):
        super().__init__(commands, kubeconfig)
        self.name, self.cluster = name, cluster

    def write(self, action, obj, dry_run):
        require(action in {'create', 'replace'}, 'Unsupported root write action')
        metadata = obj.get('metadata', {})
        require(obj.get('apiVersion') == 'argoproj.io/v1alpha1' and obj.get('kind') == 'Application'
                and metadata.get('name') == self.name and metadata.get('namespace') == 'argocd'
                and metadata.get('annotations', {}).get('bareplane.io/cluster') == self.cluster
                and re.fullmatch('[0-9a-f]{32}', metadata.get('annotations', {}).get(HANDOFF, '')),
                'Handoff may write only its approved root Application')
        if action == 'replace':
            require(metadata.get('uid') and metadata.get('resourceVersion'), 'Root updates require UID and resourceVersion preconditions')
        args = [action, '--field-manager=bareplane-gitops', '-f', '-', '-o', 'json']
        if dry_run:
            args.append('--dry-run=server')
        return self.json(*args, data=obj)

    def create(self, obj, dry_run=False):
        return self.write('create', obj, dry_run)

    def replace(self, obj, dry_run=False):
        return self.write('replace', obj, dry_run)


def repository_probe(client, repository, argo):
    """Prove anonymous TLS/Git reachability from the actual owned repo pod."""
    deployment = client.json('get', 'deployment', 'argocd-repo-server', '-n', 'argocd', '-o', 'json')
    require(deployment['metadata']['uid'] == argo.data['resources']['Deployment/argocd-repo-server']['uid'],
            'Argo repository deployment identity changed')
    pods = client.json('get', 'pods', '-n', 'argocd', '-l', 'app.kubernetes.io/name=argocd-repo-server', '-o', 'json')['items']
    ready = [pod for pod in pods if not pod['metadata'].get('deletionTimestamp') and
             any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in pod.get('status', {}).get('conditions', []))]
    require(len(ready) == 1, 'Exactly one Ready repository-server pod is required')
    pod = ready[0]
    owners = [owner for owner in pod['metadata'].get('ownerReferences', []) if owner.get('controller')]
    require(len(owners) == 1 and owners[0].get('kind') == 'ReplicaSet', 'Repository pod lacks its expected controller')
    owner = owners[0]
    replica = client.json('get', 'replicaset', owner['name'], '-n', 'argocd', '-o', 'json')
    parents = [parent for parent in replica['metadata'].get('ownerReferences', []) if parent.get('controller')]
    require(replica['metadata']['uid'] == owner['uid'] and len(parents) == 1 and parents[0].get('kind') == 'Deployment'
            and parents[0]['uid'] == deployment['metadata']['uid'], 'Repository pod does not belong to the owned Deployment')
    containers = pod['spec'].get('containers', [])
    require(len(containers) == 1 and containers[0]['name'] == 'argocd-repo-server'
            and containers[0]['image'] == 'quay.io/argoproj/argocd:v' + VERSION, 'Repository pod is not the reviewed pinned image')
    argv = ['kubectl', '--kubeconfig', client.kubeconfig, '--request-timeout=10s', 'exec', '-n', 'argocd', pod['metadata']['name'],
            '-c', 'argocd-repo-server', '--', '/usr/bin/env', '-i', 'PATH=/usr/local/bin:/usr/bin:/bin', 'HOME=/tmp',
            'GIT_CONFIG_NOSYSTEM=1', 'GIT_CONFIG_GLOBAL=/dev/null', 'GIT_CONFIG_SYSTEM=/dev/null', 'GIT_TERMINAL_PROMPT=0',
            'GIT_ASKPASS=/bin/false', '/usr/bin/git', '-c', 'credential.helper=', '-c', 'core.hooksPath=/dev/null',
            '-c', 'http.sslVerify=true', '-c', 'http.followRedirects=false', '-c', 'protocol.allow=never',
            '-c', 'protocol.https.allow=always', 'ls-remote', '--exit-code', repository]
    output = client.commands.run(argv, timeout=45, limit=4 * 1024 * 1024).decode()
    require(any(re.fullmatch('[0-9a-f]{40}\\t[^\\s]+', line) for line in output.splitlines()),
            'Argo repository server could not read the anonymous HTTPS repository')


def verify_cilium_excluded(client, applications):
    cilium = client.json('get', 'daemonset', 'cilium', '-n', 'kube-system', '-o', 'json')
    metadata = cilium['metadata']
    require('argocd.argoproj.io/tracking-id' not in metadata.get('annotations', {})
            and 'argocd.argoproj.io/instance' not in metadata.get('labels', {}), 'Cilium acquired Argo tracking; handoff is refused')
    for app in applications:
        for resource in app.get('status', {}).get('resources', []):
            group, kind, name = resource.get('group', ''), resource.get('kind', ''), resource.get('name', '')
            owned = group == 'cilium.io' or (kind == 'CustomResourceDefinition' and name.endswith('.cilium.io'))
            owned = owned or (group in {'', 'apps', 'rbac.authorization.k8s.io'} and
                              (name == 'cilium' or name.startswith('cilium-') or name.startswith('sh.helm.release.v1.bareplane-cilium.')))
            require(not owned, 'An Argo Application claims bootstrap-owned Cilium resources')


def verify_new_component_absence(client, resources):
    """Cold handoff cannot adopt existing platform namespaces or cluster objects."""
    namespaces = {obj['metadata']['name'] for obj in resources if obj['kind'] == 'Namespace'}
    cluster_kinds = {'Namespace', 'CustomResourceDefinition', 'ClusterRole', 'ClusterRoleBinding',
                     'MutatingWebhookConfiguration', 'ValidatingWebhookConfiguration', 'APIService'}
    declared_custom = {(obj['spec']['group'], version['name'], obj['spec']['names']['kind']): obj['spec']['scope']
                       for obj in resources if obj['kind'] == 'CustomResourceDefinition' for version in obj['spec']['versions']}
    for obj in resources:
        kind, metadata = obj['kind'], obj['metadata']
        if kind in cluster_kinds:
            require(not client.json('get', kind, metadata['name'], '--ignore-not-found', '-o', 'json'),
                    'An existing unmanaged platform resource blocks initial handoff: ' + kind + '/' + metadata['name'])
        else:
            namespace = metadata.get('namespace')
            group, _, version = obj['apiVersion'].rpartition('/')
            if not group:
                version = obj['apiVersion']
            custom_scope = declared_custom.get((group, version, kind))
            require(namespace in namespaces or (custom_scope == 'Cluster' and not namespace),
                    'Platform resource is outside its declared new namespace or CRD contract')


def wait_reconciliation(client, writer, plan, pinned):
    deadline = time.monotonic() + 600
    while True:
        root = writer.verify(client.get(plan.root), target_only=True)
        apps = client.json('get', 'applications.argoproj.io', '-n', 'argocd', '-o', 'json')['items']
        by_name = {app['metadata']['name']: app for app in apps}
        require(len(by_name) == len(apps), 'Argo returned duplicate Application identities')
        if pinned:
            require(set(by_name) <= set(plan.children) | {root['metadata']['name']}, 'Unrelated Applications appeared before initial handoff')
            require(not client.json('get', 'applicationsets.argoproj.io', '-n', 'argocd', '-o', 'json')['items'],
                    'ApplicationSets are not part of the initial reviewed handoff')
        verify_cilium_excluded(client, apps)
        commit = writer.record.data['commit'] if pinned else None
        pending = [] if application_health(root, commit) else ['root=' + health_summary(root)]
        for name, expected in sorted(plan.children.items()):
            child = by_name.get(name)
            if child is None:
                pending.append(name + '=Missing')
                continue
            tracking = root['metadata']['name'] + ':argoproj.io/Application:argocd/' + name
            require(child['metadata'].get('annotations', {}).get('argocd.argoproj.io/tracking-id') == tracking,
                    'A first-level child belongs to unrelated Argo ownership')
            if pinned:
                source = copy.deepcopy(expected['spec']['source'])
                source['targetRevision'] = commit
                actual = child['spec']['source']
                require(set(actual) == set(source) and actual.get('repoURL') == source['repoURL'] and actual.get('path') == source['path'],
                        'A child repository/path differs from the approved snapshot')
                if actual != source:
                    pending.append(name + '=AwaitingPinnedSource')
            if not application_health(child, commit):
                pending.append(name + '=' + health_summary(child))
        if not pending:
            return root
        require(time.monotonic() < deadline, 'GitOps reconciliation deadline exceeded: ' + ', '.join(pending))
        time.sleep(2)
