"""Read-only class-contract integration; no backend provisioning or durability claim."""

import copy
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


def run_existing_storage_acceptance(kubectl, repository):
    component = ComponentAcceptance(kubectl, repository, 'storage-existing')
    api = component.api
    fixture = yaml.safe_load((repository/'examples/existing-storage-fixture.yaml').read_bytes())
    name = fixture['spec']['storage']['className']
    resources = list(yaml.safe_load_all((repository/'components/storage-existing/resources.yaml').read_bytes()))
    job_name = next(value['metadata']['name'] for value in resources if value['kind'] == 'Job')
    account = 'system:serviceaccount:storage-contract:storage-class-check'
    # This synthetic class tests only metadata/permission contracts. It is not
    # backed by a CSI controller and cannot certify backend provisioning.
    definition = dict(apiVersion='storage.k8s.io/v1',kind='StorageClass',
        metadata=dict(name=name,annotations={'ci.bareplane.io/unmanaged':'preserve'}),
        provisioner='example.invalid/contract-test',reclaimPolicy='Delete',
        volumeBindingMode='WaitForFirstConsumer',allowVolumeExpansion=True)
    original = api('create','-f','-','-o','json',data=definition)
    component.create()
    application = api('get','application',component.name,'-n','argocd','-o','json')
    app_uid = application['metadata']['uid']

    def job():
        return api('get','job',job_name,'-n',component.namespace,'--ignore-not-found','-o','json')

    wait_for(lambda: job().get('status',{}).get('failed',0) == 1, 'External class with Delete policy was not refused')
    current = api('get','storageclass',name,'-o','json')
    if current != original:
        raise RuntimeError('Contract inspector changed an external class')

    def permitted(verb, resource='storageclasses', selected=name, group='storage.k8s.io'):
        result = api('create','-f','-','-o','json',data=dict(apiVersion='authorization.k8s.io/v1',kind='SubjectAccessReview',
            spec=dict(user=account,resourceAttributes=dict(group=group,resource=resource,name=selected,verb=verb))))
        return result.get('status',{}).get('allowed') is True

    if not permitted('get') or any(permitted(verb) for verb in ['list','watch','create','update','patch','delete']):
        raise RuntimeError('Class reader permissions exceed one named GET')
    if permitted('get',selected='unrelated-class') or permitted('get',resource='secrets',group='',selected='unrelated'):
        raise RuntimeError('Class reader can access unrelated classes or secrets')
    # Only the test fixture replaces this owned, unused class. Bareplane's
    # emitted role cannot perform this operation and never owns its metadata.
    api('delete','--raw=/apis/storage.k8s.io/v1/storageclasses/'+name,'-f','-',data=dict(
        apiVersion='v1',kind='DeleteOptions',preconditions=dict(uid=original['metadata']['uid'])))
    wait_for(lambda: not api('get','storageclass',name,'--ignore-not-found','-o','json'), 'Synthetic class deletion did not finish')
    definition['reclaimPolicy'] = 'Retain'
    prepared = api('create','-f','-','-o','json',data=definition)
    failed = job()
    api('delete','--raw=/apis/batch/v1/namespaces/'+component.namespace+'/jobs/'+job_name,'-f','-',data=dict(
        apiVersion='v1',kind='DeleteOptions',preconditions=dict(uid=failed['metadata']['uid'])))
    wait_for(lambda: not job(), 'Failed class contract Job did not finish deletion')
    source = copy.deepcopy(component.app['spec']['source'])
    source['kustomize'] = dict(patches=[dict(target=dict(group='batch',version='v1',kind='Job',name=job_name),
        patch=json.dumps(dict(apiVersion='batch/v1',kind='Job',metadata=dict(name=job_name,
            annotations={'ci.bareplane.io/explicit-retry':'prepared'}))))])
    current_app = api('get','application',component.name,'-n','argocd','-o','json')
    api('patch','application',component.name,'-n','argocd','--type=json','-p',json.dumps([
        dict(op='test',path='/metadata/uid',value=app_uid),
        dict(op='test',path='/metadata/resourceVersion',value=current_app['metadata']['resourceVersion']),
        dict(op='test',path='/spec/source',value=component.app['spec']['source']),
        dict(op='replace',path='/spec/source',value=source)]),'-o','json')
    component.app['spec']['source'] = source
    component.wait_application()
    if job().get('status',{}).get('succeeded') != 1:
        raise RuntimeError('Valid external class did not complete its contract inspection')
    if api('get','storageclass',name,'-o','json') != prepared:
        raise RuntimeError('Successful contract inspection adopted or changed external storage')
    before = job()['metadata']['uid']
    component.refresh()
    if job()['metadata']['uid'] != before or api('get','storageclass',name,'-o','json') != prepared:
        raise RuntimeError('Unchanged external contract refresh rewrote its Job or class')
    print('External class contract verified with one named GET, active TLS, restricted non-root inspection, safe refusal/retry and unchanged external identity. Backend provisioning/durability was not certified.',flush=True)
