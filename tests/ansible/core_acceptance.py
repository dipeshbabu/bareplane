"""One exact-source core graph, reconciled together after GitOps handoff."""

import json
import re
import time
from urllib.parse import urlencode

import yaml

from component_acceptance import ComponentAcceptance


COMPONENTS = ('argocd', 'cert-manager', 'metrics-server', 'observability', 'storage')
ROOT_PATH = 'examples/gitops/core-root'


def core_source(repository, revision):
    if (not re.fullmatch(r'https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git', repository)
            or not re.fullmatch(r'[0-9a-f]{40}', revision)):
        raise RuntimeError('Core acceptance requires one exact public source commit')
    return dict(repoURL=repository, targetRevision=revision, path=ROOT_PATH,
                kustomize=dict(patches=[dict(target=dict(group='argoproj.io', version='v1alpha1', kind='Application'),
                    patch=json.dumps([dict(op='replace', path='/spec/source/repoURL', value=repository),
                                      dict(op='replace', path='/spec/source/targetRevision', value=revision)]))]))


def root_source_patch(root, desired, ownership):
    metadata = root.get('metadata', {})
    nonce = ownership.get('nonce')
    if (root.get('kind') != 'Application' or metadata.get('name') != 'lab-root'
            or metadata.get('namespace') != 'argocd' or not metadata.get('uid')
            or metadata.get('annotations', {}).get('bareplane.io/cluster') != 'lab'
            or not isinstance(nonce, str) or not re.fullmatch(r'[0-9a-f]{32}', nonce)
            or metadata.get('annotations', {}).get('bareplane.io/handoff') != nonce
            or ownership.get('uid') != metadata['uid'] or ownership.get('rootName') != 'lab-root'
            or ownership.get('cluster') != 'lab' or ownership.get('mode') != 'following'
            or ownership.get('complete') is not True):
        raise RuntimeError('Core acceptance cannot replace unrelated root ownership')
    return [dict(op='test', path='/metadata/uid', value=metadata['uid']),
            dict(op='test', path='/spec/source', value=root['spec']['source']),
            dict(op='replace', path='/spec/source', value=desired)]


def converged(application, source, revision):
    status = application.get('status', {})
    sync = status.get('sync', {})
    return (sync.get('status') == 'Synced' and sync.get('revision') == revision
            and sync.get('comparedTo', {}).get('source') == source
            and status.get('health', {}).get('status') == 'Healthy'
            and not application.get('operation') and not status.get('conditions')
            and status.get('operationState', {}).get('phase') not in {'Running', 'Failed', 'Error', 'Terminating'})


def wait_for(predicate, reason, timeout=300):
    deadline = time.monotonic() + timeout
    while True:
        if predicate():
            return
        if time.monotonic() >= deadline:
            raise RuntimeError(reason)
        time.sleep(2)


def sync_to_source(api, name, source, revision, uid):
    operation = dict(sync=dict(revision=revision, prune=False))
    for attempt in range(3):
        current = api('get', 'application', name, '-n', 'argocd', '-o', 'json')
        if current['metadata']['uid'] != uid or current['spec']['source'] != source:
            raise RuntimeError('Core recovery ownership or source changed')
        active = current.get('operation', {})
        active_sync = active.get('sync', {})
        if converged(current, source, revision):
            return
        if (active_sync.get('revision') == revision and not active_sync.get('prune', False)
                and set(active_sync) <= {'revision', 'prune', 'syncOptions', 'syncStrategy'}):
            return  # A matching automated or ambiguously accepted operation is already progressing.
        if active:
            raise RuntimeError('An unrelated core synchronization operation is active')
        try:
            api('patch', 'application', name, '-n', 'argocd', '--type=json', '-p', json.dumps([
                dict(op='test', path='/metadata/uid', value=uid),
                dict(op='test', path='/metadata/resourceVersion', value=current['metadata']['resourceVersion']),
                dict(op='test', path='/spec/source', value=source),
                dict(op='add', path='/operation', value=operation)]), '-o', 'json')
            return
        except RuntimeError:
            if attempt == 2:
                raise
            time.sleep(0.5)


def application_diagnostics(name, current, source, revision):
    status = current.get('status', {})
    sync = status.get('sync', {})
    compared = sync.get('comparedTo', {}).get('source', {})
    messages = ' '.join(str(condition.get('message', '')) for condition in status.get('conditions', []))
    messages += ' ' + str(status.get('operationState', {}).get('message', ''))
    patterns = {'shared-resource': 'shared', 'manifest-generation': 'manifest', 'patch': 'patch',
                'git-reference': 'revision', 'permission': 'forbidden', 'namespace': 'namespace',
                'conflict': 'conflict', 'invalid': 'invalid'}
    return dict(application=name, sync=sync.get('status'), health=status.get('health', {}).get('status'),
                revisionMatches=sync.get('revision') == revision, specSourceMatches=current.get('spec', {}).get('source') == source,
                comparedSourceMatches=compared == source,
                comparedSourceDifferentFields=sorted(key for key in set(compared) | set(source) if compared.get(key) != source.get(key)),
                operationActive=bool(current.get('operation')), operationPhase=status.get('operationState', {}).get('phase'),
                conditionCount=len(status.get('conditions', [])),
                errorCategories=[name for name, pattern in patterns.items() if pattern in messages.lower()],
                resources=[dict(kind=item.get('kind'), name=item.get('name'), sync=item.get('status'),
                                health=item.get('health', {}).get('status')) for item in status.get('resources', [])[:80]])


def run_core_acceptance(kubectl, repository, work, ssh, nodes, ownership):
    # Reuse the bounded, credential-free API adapter. Its constructor enforces
    # disposable GitHub execution and a public exact source commit.
    adapter = ComponentAcceptance(kubectl, repository, 'cert-manager')
    api, command, revision = adapter.api, adapter.command, adapter.revision
    source_repo = adapter.app['spec']['source']['repoURL']
    root = api('get', 'application', 'lab-root', '-n', 'argocd', '-o', 'json')
    root_uid = root['metadata']['uid']
    argo = api('get', 'application', 'lab-argocd', '-n', 'argocd', '-o', 'json')
    argo_uid = argo['metadata']['uid']
    cilium = api('get', 'daemonset', 'cilium', '-n', 'kube-system', '-o', 'json')
    cilium_uid = cilium['metadata']['uid']
    node_uids = {node['metadata']['name']: node['metadata']['uid']
                 for node in api('get', 'nodes', '-o', 'json')['items']}
    if set(node_uids) != set(nodes):
        raise RuntimeError('Core fixture topology differs from the approved bootstrap')
    directory = '/var/lib/bareplane/local-volumes/lab/first/data'
    ssh('lab-control-1', 'test ! -e ' + directory + ' && test ! -L ' + directory
        + ' && install -d -m 0700 ' + directory)
    desired = core_source(source_repo, revision)
    api('patch', 'application', 'lab-root', '-n', 'argocd', '--type=json', '-p',
        json.dumps(root_source_patch(root, desired, ownership)), '-o', 'json')
    sync_to_source(api, 'lab-root', desired, revision, root_uid)

    expected = {}
    for component in COMPONENTS:
        application = yaml.safe_load((repository / ROOT_PATH / 'applications' / (component + '.yaml')).read_bytes())
        application['spec']['source'].update(repoURL=source_repo, targetRevision=revision)
        expected[component] = application

    def application(name):
        return api('get', 'application', name, '-n', 'argocd', '-o', 'json')

    def wait_graph():
        try:
            wait_for(lambda: converged(application('lab-root'), desired, revision),
                     'The composed core root did not converge to its exact reviewed source', timeout=900)
            for component, definition in expected.items():
                name = definition['metadata']['name']
                wait_for(lambda: converged(application(name), definition['spec']['source'], revision),
                         'Core component did not converge: ' + component)
                current = application(name)
                annotations = current['metadata'].get('annotations', {})
                if (annotations.get('bareplane.io/cluster') != 'lab'
                        or not annotations.get('argocd.argoproj.io/tracking-id', '').startswith('lab-root:')):
                    raise RuntimeError('Core child Application has unrelated ownership: ' + component)
        except RuntimeError:
            sources = {'lab-root': desired, **{item['metadata']['name']: item['spec']['source'] for item in expected.values()}}
            for name, source in sources.items():
                try:
                    print('Core application diagnostics: ' + json.dumps(application_diagnostics(name, application(name), source, revision)), flush=True)
                except (RuntimeError, KeyError, TypeError):
                    print('Core application diagnostics unavailable; response bodies were not exposed.', flush=True)
            for component in ['cert-manager', 'metrics-server', 'observability', 'storage']:
                ComponentAcceptance(kubectl, repository, component).readiness_diagnostics()
            raise

    wait_graph()
    if application('lab-root')['metadata']['uid'] != root_uid or application('lab-argocd')['metadata']['uid'] != argo_uid:
        raise RuntimeError('Core activation replaced the existing GitOps foundation')
    command('wait', '--for=condition=Ready', 'clusterissuer/lab-selfsigned', '--timeout=120s', timeout=130)

    def metrics_ready():
        try:
            service = api('get', 'apiservice', 'v1beta1.metrics.k8s.io', '-o', 'json')
        except RuntimeError:
            return False
        if service['spec'].get('insecureSkipTLSVerify', False) or not service['spec'].get('caBundle'):
            raise RuntimeError('The composed core profile disabled metrics serving TLS verification')
        try:
            values = api('get', '--raw=/apis/metrics.k8s.io/v1beta1/nodes').get('items', [])
        except RuntimeError:
            return False
        return {node['metadata']['name'] for node in values if node.get('usage', {}).get('cpu') and node.get('usage', {}).get('memory')} == set(nodes)

    wait_for(metrics_ready, 'Composed core node metrics did not become available')
    query = '/api/v1/namespaces/observability/services/http:bareplane-prometheus:9090/proxy/api/v1/query?'

    def scrapes_ready():
        try:
            response = api('get', '--raw=' + query + urlencode({'query': 'sum(up{job=~"prometheus|kube-state-metrics"})'}))
        except RuntimeError:
            return False
        result = response.get('data', {}).get('result', [])
        return response.get('status') == 'success' and len(result) == 1 and result[0].get('value', [None, None])[1] == '2'

    wait_for(scrapes_ready, 'Composed core metrics collection did not become healthy')
    volumes = list(yaml.safe_load_all((repository / 'components/storage/resources.yaml').read_bytes()))
    pv_name = next(obj['metadata']['name'] for obj in volumes if obj['kind'] == 'PersistentVolume')
    pv = api('get', 'pv', pv_name, '-o', 'json')
    if pv['spec']['persistentVolumeReclaimPolicy'] != 'Retain':
        raise RuntimeError('Core profile changed retained local-volume policy')
    pv_uid = pv['metadata']['uid']
    namespace = 'core-workload'
    api('create', '-f', '-', '-o', 'json', data=dict(apiVersion='v1', kind='Namespace', metadata=dict(name=namespace)))
    claim = api('create', '-f', '-', '-o', 'json', data=dict(apiVersion='v1', kind='PersistentVolumeClaim',
        metadata=dict(name='local-data', namespace=namespace), spec=dict(storageClassName='bareplane-local', accessModes=['ReadWriteOnce'],
            selector=dict(matchLabels={'bareplane.io/local-volume': 'first'}), resources=dict(requests=dict(storage='1Gi')))))

    def pod(name, read=False):
        code = ("assert open('/data/sentinel').read() == 'core-profile-persistence'" if read else
                "from pathlib import Path; Path('/data/sentinel').write_text('core-profile-persistence')")
        return dict(apiVersion='v1', kind='Pod', metadata=dict(name=name, namespace=namespace), spec=dict(restartPolicy='Never',
            automountServiceAccountToken=False, securityContext=dict(runAsNonRoot=True, runAsUser=1000, runAsGroup=1000, fsGroup=1000,
                fsGroupChangePolicy='OnRootMismatch', seccompProfile=dict(type='RuntimeDefault')),
            tolerations=[dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')],
            volumes=[dict(name='data', persistentVolumeClaim=dict(claimName='local-data', readOnly=read))],
            containers=[dict(name='check', image='python:3.12.14-slim-bookworm', command=['/usr/local/bin/python3', '-I', '-B', '-c', code],
                volumeMounts=[dict(name='data', mountPath='/data', readOnly=read)],
                securityContext=dict(readOnlyRootFilesystem=True, allowPrivilegeEscalation=False, capabilities=dict(drop=['ALL'])),
                resources=dict(requests=dict(cpu='10m', memory='32Mi'), limits=dict(memory='64Mi')))]))

    for name, read in [('writer', False), ('reader', True)]:
        created = api('create', '-f', '-', '-o', 'json', data=pod(name, read))

        def completed():
            current = api('get', 'pod', name, '-n', namespace, '-o', 'json')
            if current.get('status', {}).get('phase') == 'Failed':
                raise RuntimeError('Core persistence workload failed')
            return current.get('status', {}).get('phase') == 'Succeeded' and current['spec'].get('nodeName') == 'lab-control-1'

        wait_for(completed, 'Core persistence workload did not complete')
        command('delete', '--raw=/api/v1/namespaces/' + namespace + '/pods/' + name, '-f', '-',
                data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=created['metadata']['uid'])))

    # With self-heal deliberately disabled, a controlled workload failure must
    # be visible, then recover only after an explicit synchronization to Git.
    deployment = api('get', 'deployment', 'bareplane-prometheus', '-n', 'observability', '-o', 'json')
    deployment_uid = deployment['metadata']['uid']
    api('patch', 'deployment', 'bareplane-prometheus', '-n', 'observability', '--type=json', '-p', json.dumps([
        dict(op='test', path='/metadata/uid', value=deployment_uid), dict(op='test', path='/spec/replicas', value=1),
        dict(op='replace', path='/spec/replicas', value=0)]), '-o', 'json')
    wait_for(lambda: application('lab-observability').get('status', {}).get('sync', {}).get('status') == 'OutOfSync',
             'Core workload failure was not reflected by Argo')
    observed = application('lab-observability')
    sync_to_source(api, 'lab-observability', expected['observability']['spec']['source'], revision, observed['metadata']['uid'])
    wait_graph()
    wait_for(scrapes_ready, 'Core metrics collection did not recover after explicit Git synchronization')
    if api('get', 'deployment', 'bareplane-prometheus', '-n', 'observability', '-o', 'json')['metadata']['uid'] != deployment_uid:
        raise RuntimeError('Core recovery recreated the owned observability workload')
    workload_ids = {}
    for namespace in ['argocd', 'cert-manager', 'metrics-server', 'observability']:
        for obj in api('get', 'deployments,statefulsets', '-n', namespace, '-o', 'json')['items']:
            workload_ids[(namespace, obj['kind'], obj['metadata']['name'])] = (obj['metadata']['uid'], obj['metadata']['generation'])
    child_ids = {name: application('lab-' + name)['metadata']['uid'] for name in COMPONENTS}
    for name in ['lab-root', *('lab-' + component for component in COMPONENTS)]:
        command('annotate', 'application', name, '-n', 'argocd', 'argocd.argoproj.io/refresh=hard', '--overwrite')
    time.sleep(2)
    wait_graph()
    for (namespace, kind, name), identity in workload_ids.items():
        current = api('get', kind, name, '-n', namespace, '-o', 'json')
        if (current['metadata']['uid'], current['metadata']['generation']) != identity:
            raise RuntimeError('An unchanged composed core reconciliation rewrote a workload')
    if any(application('lab-' + name)['metadata']['uid'] != uid for name, uid in child_ids.items()):
        raise RuntimeError('An unchanged core reconciliation replaced an Application')
    retained = api('get', 'pv', pv_name, '-o', 'json')
    if retained['metadata']['uid'] != pv_uid or retained['spec'].get('claimRef', {}).get('uid') != claim['metadata']['uid']:
        raise RuntimeError('Core reconciliation changed a retained volume or its claim binding')
    cilium = api('get', 'daemonset', 'cilium', '-n', 'kube-system', '-o', 'json')
    if (cilium['metadata']['uid'] != cilium_uid
            or cilium['metadata'].get('annotations', {}).get('argocd.argoproj.io/tracking-id')
            or cilium['metadata'].get('labels', {}).get('argocd.argoproj.io/instance')):
        raise RuntimeError('Core convergence replaced bootstrap networking')
    if {node['metadata']['name']: node['metadata']['uid'] for node in api('get', 'nodes', '-o', 'json')['items']} != node_uids:
        raise RuntimeError('Core convergence changed the approved node identities')
    wait_for(metrics_ready, 'Core node metrics did not survive reconciliation')
    print('The exact-source core graph converged with verified metrics TLS, observability, retained PVC data, explicit failure recovery, stable workload identities, and bootstrap ownership.', flush=True)
