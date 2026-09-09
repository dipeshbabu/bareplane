"""Static local PV persistence and refusal checks on a disposable KVM node."""

import copy
import hashlib
import json
import time

import yaml

from component_acceptance import ComponentAcceptance


def wait_for(predicate, message, timeout=240):
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() >= deadline:
            raise RuntimeError(message)
        time.sleep(2)


def run_storage_acceptance(kubectl, repository, work, ssh):
    component = ComponentAcceptance(kubectl, repository, 'storage')
    command, api = component.command, component.api
    node = 'lab-control-1'
    directory = '/var/lib/bareplane/local-volumes/lab/first/data'
    resources = list(yaml.safe_load_all((repository / 'components/storage/resources.yaml').read_bytes()))
    job = next(obj for obj in resources if obj['kind'] == 'Job')
    volume = next(obj for obj in resources if obj['kind'] == 'PersistentVolume')
    job_name, pv_name = job['metadata']['name'], volume['metadata']['name']
    before_disk = ssh(node, 'blkid -s UUID -o value /dev/vdc && sha256sum /bareplane-user-data/sentinel', capture_output=True, text=True).stdout
    # This exact directory/symlink belongs to the disposable fixture. The target
    # is the separately created unrelated application disk and must not change.
    ssh(node, 'test ! -e ' + directory + ' && test ! -L ' + directory
        + ' && install -d -m 0700 /var/lib/bareplane/local-volumes/lab/first && ln -s /bareplane-user-data ' + directory)
    component.create()
    app = api('get', 'application', component.name, '-n', 'argocd', '-o', 'json')
    app_uid = app['metadata']['uid']

    def check_job():
        return api('get', 'job', job_name, '-n', 'local-storage', '--ignore-not-found', '-o', 'json')

    wait_for(lambda: check_job().get('status', {}).get('failed', 0) == 1, 'Unsafe local-volume path was not refused')
    pods = api('get', 'pods', '-n', 'local-storage', '-l', 'job-name=' + job_name, '-o', 'json')['items']
    if len(pods) != 1:
        raise RuntimeError('Unexpected local-volume inspection Pod count')
    log = command('logs', pods[0]['metadata']['name'], '-n', 'local-storage', '--tail=10')
    if b'Local storage refused:' not in log:
        raise RuntimeError('Local-volume refusal did not execute the path guard')
    mounts = pods[0].get('status', {}).get('containerStatuses', [{}])[0].get('volumeMounts', [])
    if not any(mount.get('name') == 'host' and mount.get('recursiveReadOnly') == 'Enabled' for mount in mounts):
        raise RuntimeError('Storage inspector did not receive a recursively read-only host view')
    if api('get', 'pv', pv_name, '--ignore-not-found', '-o', 'json'):
        raise RuntimeError('Unsafe path reached PersistentVolume creation')
    if ssh(node, 'blkid -s UUID -o value /dev/vdc && sha256sum /bareplane-user-data/sentinel', capture_output=True, text=True).stdout != before_disk:
        raise RuntimeError('Storage preflight changed unrelated disk data')
    ssh(node, 'test -L ' + directory + ' && test "$(readlink ' + directory + ')" = /bareplane-user-data && unlink '
        + directory + ' && mkdir -m 0700 ' + directory)
    failed = check_job()
    command('delete', '--raw=/apis/batch/v1/namespaces/local-storage/jobs/' + job_name, '-f', '-',
            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=failed['metadata']['uid'])))
    wait_for(lambda: not check_job(), 'Failed inspection Job did not finish deletion')
    desired = copy.deepcopy(component.app['spec']['source'])
    desired['kustomize'] = dict(patches=[dict(target=dict(group='batch', version='v1', kind='Job', name=job_name),
        patch=json.dumps(dict(apiVersion='batch/v1', kind='Job', metadata=dict(name=job_name,
            annotations={'bareplane.io/disposable-retry': 'prepared'}))))])
    operations = [dict(op='test', path='/metadata/uid', value=app_uid), dict(op='test', path='/spec/source', value=component.app['spec']['source']),
                  dict(op='replace', path='/spec/source', value=desired)]
    api('patch', 'application', component.name, '-n', 'argocd', '--type=json', '-p', json.dumps(operations), '-o', 'json')
    component.app['spec']['source'] = desired
    component.wait_application()
    if check_job().get('status', {}).get('succeeded') != 1:
        raise RuntimeError('Prepared local volume did not complete its read-only inspection')
    namespace = 'storage-workload'
    command('create', '-f', '-', data=dict(apiVersion='v1', kind='Namespace', metadata=dict(name=namespace)))
    claim = api('create', '-f', '-', '-o', 'json', data=dict(apiVersion='v1', kind='PersistentVolumeClaim',
        metadata=dict(name='local-data', namespace=namespace), spec=dict(storageClassName='bareplane-local', accessModes=['ReadWriteOnce'],
            selector=dict(matchLabels={'bareplane.io/local-volume': 'first'}), resources=dict(requests=dict(storage='1Gi')))))
    payload = 'persistent-disposable-storage-sentinel'
    expected_hash = hashlib.sha256(payload.encode()).hexdigest()

    def pod(name, read=False):
        code = ("import hashlib; assert hashlib.sha256(open('/data/sentinel','rb').read()).hexdigest() == '" + expected_hash + "'" if read else
                "from pathlib import Path; p=Path('/data/sentinel'); f=p.open('x'); f.write('" + payload + "'); f.close()")
        return dict(apiVersion='v1', kind='Pod', metadata=dict(name=name, namespace=namespace), spec=dict(restartPolicy='Never',
            automountServiceAccountToken=False, securityContext=dict(runAsNonRoot=True, runAsUser=1000, runAsGroup=1000, fsGroup=1000,
                fsGroupChangePolicy='OnRootMismatch', seccompProfile=dict(type='RuntimeDefault')),
            tolerations=[dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')],
            volumes=[dict(name='data', persistentVolumeClaim=dict(claimName='local-data', readOnly=read))],
            containers=[dict(name='check', image='python:3.12.14-slim-bookworm', command=['/usr/local/bin/python3', '-I', '-B', '-c', code],
                volumeMounts=[dict(name='data', mountPath='/data', readOnly=read)],
                securityContext=dict(readOnlyRootFilesystem=True, allowPrivilegeEscalation=False, capabilities=dict(drop=['ALL'])),
                resources=dict(requests=dict(cpu='10m', memory='32Mi'), limits=dict(memory='64Mi')))]))

    def complete(name):
        current = api('get', 'pod', name, '-n', namespace, '-o', 'json')
        if current.get('status', {}).get('phase') == 'Failed':
            raise RuntimeError('Disposable local-volume workload failed')
        return current.get('status', {}).get('phase') == 'Succeeded' and current['spec'].get('nodeName') == node

    writer = api('create', '-f', '-', '-o', 'json', data=pod('writer'))
    wait_for(lambda: complete('writer'), 'Local PVC writer did not complete on its declared node')
    bound = api('get', 'pv', pv_name, '-o', 'json')
    if bound['spec'].get('claimRef', {}).get('uid') != claim['metadata']['uid']:
        raise RuntimeError('Local volume did not bind to the disposable workload claim')
    command('delete', '--raw=/api/v1/namespaces/' + namespace + '/pods/writer', '-f', '-',
            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=writer['metadata']['uid'])))
    wait_for(lambda: not api('get', 'pod', 'writer', '-n', namespace, '--ignore-not-found', '-o', 'json'), 'Writer deletion did not finish')
    command('cordon', node)
    try:
        reader = api('create', '-f', '-', '-o', 'json', data=pod('reader', read=True))
        wait_for(lambda: any(condition.get('type') == 'PodScheduled' and condition.get('status') == 'False'
                 for condition in api('get', 'pod', 'reader', '-n', namespace, '-o', 'json').get('status', {}).get('conditions', [])),
                 'Node-local workload did not remain pending on node unavailability', timeout=60)
    finally:
        command('uncordon', node)
    wait_for(lambda: complete('reader'), 'Retained local data was not readable after workload recovery')
    command('delete', '--raw=/api/v1/namespaces/' + namespace + '/pods/reader', '-f', '-',
            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=reader['metadata']['uid'])))
    command('delete', '--raw=/api/v1/namespaces/' + namespace + '/persistentvolumeclaims/local-data', '-f', '-',
            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=claim['metadata']['uid'])))
    wait_for(lambda: api('get', 'pv', pv_name, '-o', 'json').get('status', {}).get('phase') == 'Released', 'Local PV did not retain its released data')
    released = api('get', 'pv', pv_name, '-o', 'json')
    if released['metadata']['uid'] != bound['metadata']['uid'] or released['spec'].get('claimRef', {}).get('uid') != claim['metadata']['uid']:
        raise RuntimeError('Retained PV identity or old claim binding was erased')
    component.refresh()
    if api('get', 'pv', pv_name, '-o', 'json')['spec'].get('claimRef', {}).get('uid') != claim['metadata']['uid']:
        raise RuntimeError('Argo rewrote the Kubernetes volume binder claim')
    current_hash = ssh(node, 'sha256sum ' + directory + '/sentinel', capture_output=True, text=True).stdout.split()[0]
    if current_hash != expected_hash or ssh(node, 'blkid -s UUID -o value /dev/vdc && sha256sum /bareplane-user-data/sentinel', capture_output=True, text=True).stdout != before_disk:
        raise RuntimeError('Storage retention changed local or unrelated application data')
    print('Argo-owned static storage passed recursive-read-only path refusal, prepared-volume validation, PVC binding, node-local persistence/recovery, Retain release and unrelated-disk preservation.', flush=True)
