"""External managed Vault contract against an isolated TLS API and real JWTs."""

import base64
import copy
import json
import subprocess
import time
from urllib.parse import urlsplit

import yaml
from cryptography.hazmat.primitives import serialization

from component_acceptance import ComponentAcceptance
from fake_vault import FakeVault, FakeVaultServer, certificate_authority


def wait_for(predicate, message, timeout=300):
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() >= deadline:
            raise RuntimeError(message)
        time.sleep(2)


def run_vault_acceptance(kubectl, repository, work):
    component = ComponentAcceptance(kubectl, repository, 'vault')
    command, api = component.command, component.api
    namespace = 'vault-workloads'

    def review(token):
        obj = api('create', '--raw=/apis/authentication.k8s.io/v1/tokenreviews', '-f', '-', data=dict(
            apiVersion='authentication.k8s.io/v1', kind='TokenReview', spec=dict(token=token, audiences=['vault'])))
        status = obj.get('status', {})
        return (status.get('authenticated') is True and status.get('user', {}).get('username') ==
                'system:serviceaccount:vault-workloads:bareplane-vault-reader' and 'vault' in status.get('audiences', []))

    model = FakeVault(review)
    documents = list(yaml.safe_load_all((repository / 'components/vault/integration.yaml').read_bytes()))
    store = next(obj for obj in documents if obj['kind'] == 'SecretStore')
    secret_resource = next(obj for obj in documents if obj['kind'] == 'ExternalSecret')
    store_name = store['metadata']['name']
    command('create', '-f', '-', data=dict(apiVersion='v1', kind='Namespace', metadata=dict(name=namespace)))
    unmanaged = api('create', '-f', '-', '-o', 'json', data=dict(apiVersion='v1', kind='Secret',
        metadata=dict(name='database', namespace=namespace), type='Opaque', stringData=dict(password='unmanaged-disposable-value')))

    # Extend the same Argo-owned ConfigMap with reviewed ESO health scripts.
    argo = copy.copy(component)
    argo.namespace, argo.name = 'argocd', 'lab-argocd'
    argo.app = api('get', 'application', argo.name, '-n', 'argocd', '-o', 'json')
    argo_uid = argo.app['metadata']['uid']
    argo.app['spec']['source'] = dict(repoURL=component.app['spec']['source']['repoURL'], targetRevision=component.revision,
                                     path='examples/gitops/vault-argocd')
    api('replace', '-f', '-', '-o', 'json', data=argo.app)
    argo.wait_application()
    if api('get', 'application', argo.name, '-n', 'argocd', '-o', 'json')['metadata']['uid'] != argo_uid:
        raise RuntimeError('Vault health configuration replaced its Argo owner')

    with FakeVaultServer(model, work / 'fake-vault-tls', '192.0.2.1') as server:
        command('create', '-f', '-', data=dict(apiVersion='v1', kind='Secret', metadata=dict(name='vault-ca', namespace=namespace),
                                              type='Opaque', data={'ca.crt': base64.b64encode(server.ca).decode()}))

        def source(ca='vault-ca', resync='initial'):
            desired = dict(repoURL=component.app['spec']['source']['repoURL'], targetRevision=component.revision, path='components/vault')
            desired['kustomize'] = dict(patches=[
                dict(target=dict(group='external-secrets.io', version='v1', kind='SecretStore', name=store_name), patch=json.dumps(dict(
                    apiVersion='external-secrets.io/v1', kind='SecretStore', metadata=dict(name=store_name),
                    spec=dict(provider=dict(vault=dict(server=server.url, caProvider=dict(type='Secret', name=ca, key='ca.crt'))))))),
                dict(target=dict(group='external-secrets.io', version='v1', kind='ExternalSecret', name=secret_resource['metadata']['name']),
                    patch=json.dumps(dict(apiVersion='external-secrets.io/v1', kind='ExternalSecret',
                        metadata=dict(name=secret_resource['metadata']['name'], annotations={'bareplane.io/test-resync': resync}), spec=dict(refreshInterval='30s')))),
            ])
            return desired

        def update(ca='vault-ca', resync='updated'):
            obj = api('get', 'application', component.name, '-n', 'argocd', '-o', 'json')
            component.app['spec']['source'] = source(ca, resync)
            obj['spec']['source'] = component.app['spec']['source']
            api('replace', '-f', '-', '-o', 'json', data=obj)

        def target():
            return api('get', 'secret', 'database', '-n', namespace, '-o', 'json')

        def ready(kind, name, expected):
            obj = api('get', kind, name, '-n', namespace, '--ignore-not-found', '-o', 'json')
            return any(condition.get('type') == 'Ready' and condition.get('status') == expected
                       for condition in obj.get('status', {}).get('conditions', []))

        def desired_value():
            return {key: base64.b64encode(value.encode()).decode() for key, value in model.values.items()}

        def assert_retained(previous):
            current = target()
            if current['metadata']['uid'] != previous['metadata']['uid'] or current['data'] != previous['data']:
                raise RuntimeError('Vault failure changed retained Secret identity or data')

        def logs():
            return command('logs', 'deployment/bareplane-vault', '-n', 'vault-secrets', '--tail=-1', '--limit-bytes=2097152')

        def assert_public_logs():
            data = logs()
            if len(data) >= 2097152:
                raise RuntimeError('Vault log audit exceeded its completeness bound')
            with model.lock:
                for value in model.sensitive:
                    if value in data or base64.b64encode(value) in data:
                        raise RuntimeError('Vault integration exposed a private fixture value in logs')

        component.app['spec']['source'] = source()
        component.create()
        wait_for(lambda: bool(api('get', 'namespace', 'vault-secrets', '--ignore-not-found', '-o', 'json')),
                 'Argo did not create its owned Vault operator namespace')
        command('create', '-f', '-', data=dict(apiVersion='cilium.io/v2', kind='CiliumNetworkPolicy',
            metadata=dict(name='disposable-vault-api-only', namespace='vault-secrets'), spec=dict(
                endpointSelector=dict(matchLabels={'app.kubernetes.io/name': 'external-secrets', 'app.kubernetes.io/instance': 'bareplane-vault'}),
                egress=[
                    dict(toCIDR=['192.0.2.1/32'], toPorts=[dict(ports=[dict(protocol='TCP', port=str(urlsplit(server.url).port))])]),
                    dict(toEntities=['kube-apiserver'], toPorts=[dict(ports=[dict(protocol='TCP', port='443'), dict(protocol='TCP', port='6443')])]),
                    dict(toEndpoints=[dict(matchLabels={'k8s:io.kubernetes.pod.namespace': 'kube-system', 'k8s:k8s-app': 'kube-dns'})],
                         toPorts=[dict(ports=[dict(protocol='UDP', port='53'), dict(protocol='TCP', port='53')])]),
                ])))
        try:
            for definition in ['secretstores.external-secrets.io', 'externalsecrets.external-secrets.io']:
                wait_for(lambda: bool(api('get', 'customresourcedefinition', definition, '--ignore-not-found', '-o', 'json')),
                         'Argo did not create the reviewed Vault integration schema')
                command('wait', '--for=condition=Established', 'customresourcedefinition/' + definition, '--timeout=90s', timeout=100)
            wait_for(lambda: ready('secretstore', store_name, 'True'), 'Vault Store did not authenticate using the scoped Kubernetes JWT')
            wait_for(lambda: b'Vault refuses to adopt an existing unmanaged Secret' in logs(), 'Vault did not hit the expected unmanaged Secret guard')
            observed = target()
            if observed['metadata']['resourceVersion'] != unmanaged['metadata']['resourceVersion'] or observed['data'] != unmanaged['data']:
                raise RuntimeError('Vault modified an unmanaged Secret before ownership refusal')
            command('delete', '--raw=/api/v1/namespaces/vault-workloads/secrets/database', '-f', '-', data=dict(
                apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=unmanaged['metadata']['uid'])))
            update(resync='release-disposable-collision')
            component.wait_application()
            wait_for(lambda: target().get('data') == desired_value(), 'Vault did not deliver the selected secret properties')
            original = target()
            if original['metadata'].get('ownerReferences') or original['metadata'].get('labels', {}).get('bareplane.io/vault-managed') != 'true':
                raise RuntimeError('Retained Vault Secret has unexpected ownership')
            deployment_uid = component.deployment_identity('bareplane-vault')[0]
            model.rotate()
            wait_for(lambda: target().get('data') == desired_value(), 'Vault value rotation did not synchronize')
            rotated = target()
            if rotated['metadata']['uid'] != original['metadata']['uid']:
                raise RuntimeError('Vault rotation replaced the Secret identity')
            with model.lock:
                model.authorized = False
                model.tokens.clear()
                before_denials = model.denials
            wait_for(lambda: model.denials > before_denials, 'Vault revocation was not observed')
            wait_for(lambda: ready('secretstore', store_name, 'False'), 'Vault revocation was not reflected in status')
            assert_retained(rotated)
            with model.lock:
                model.authorized = True
            component.wait_application()
            with model.lock:
                model.available = False
            wait_for(lambda: ready('secretstore', store_name, 'False'), 'Vault unavailability was not reflected in status')
            assert_retained(rotated)
            with model.lock:
                model.available = True
            component.wait_application()
            with model.lock:
                model.deleted = True
            wait_for(lambda: ready('externalsecret', secret_resource['metadata']['name'], 'False'), 'Missing Vault source was not reported')
            assert_retained(rotated)
            with model.lock:
                model.deleted = False
            component.wait_application()
            with model.lock:
                missing = model.values.pop('password')
            wait_for(lambda: ready('externalsecret', secret_resource['metadata']['name'], 'False'), 'Missing Vault property was not reported')
            assert_retained(rotated)
            with model.lock:
                model.values['password'] = missing
            component.wait_application()
            trust = api('get', 'secret', 'vault-ca', '-n', namespace, '-o', 'json')
            _, wrong_ca = certificate_authority()
            trust['data']['ca.crt'] = base64.b64encode(wrong_ca.public_bytes(serialization.Encoding.PEM)).decode()
            command('replace', '-f', '-', data=trust)
            wait_for(lambda: ready('secretstore', store_name, 'False'), 'Vault invalid trust was not refused')
            wait_for(lambda: b'certificate signed by unknown authority' in logs(), 'Vault trust refusal did not exercise certificate verification')
            assert_retained(rotated)
            trust = api('get', 'secret', 'vault-ca', '-n', namespace, '-o', 'json')
            trust['data']['ca.crt'] = base64.b64encode(server.ca).decode()
            command('replace', '-f', '-', data=trust)
            component.wait_application()
            wait_for(lambda: target().get('data') == desired_value(), 'Vault did not recover after provider trust restoration')
            assert_retained(rotated)
            if not model.revocations or model.unexpected or component.deployment_identity('bareplane-vault')[0] != deployment_uid:
                raise RuntimeError('Vault credential cleanup, API scope or Deployment identity contract failed')
            assert_public_logs()
            component.refresh()
            declared = api('get', 'externalsecret', secret_resource['metadata']['name'], '-n', namespace, '-o', 'json')
            command('delete', '--raw=/apis/external-secrets.io/v1/namespaces/' + namespace + '/externalsecrets/' + declared['metadata']['name'],
                    '-f', '-', data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=declared['metadata']['uid'])))
            wait_for(lambda: not api('get', 'externalsecret', declared['metadata']['name'], '-n', namespace, '--ignore-not-found', '-o', 'json'),
                     'Disposable Vault declaration did not finish deletion')
            assert_retained(rotated)
            # Restore the declaration through its existing Argo owner, not by
            # adopting or manually writing the retained runtime Secret.
            app = api('get', 'application', component.name, '-n', 'argocd', '-o', 'json')
            app['operation'] = dict(sync=dict(revision=component.revision))
            api('replace', '-f', '-', '-o', 'json', data=app)
            component.wait_application()
            assert_retained(rotated)
            print('Argo-owned Vault integration passed real audience-bound JWT authentication, TLS trust, ownership refusal, rotation, revocation, unavailability and retained-data checks.', flush=True)
        except Exception:
            with model.lock:
                print('Disposable Vault audit counts: ' + json.dumps(dict(logins=model.logins, reads=model.reads,
                    denials=model.denials, revocations=model.revocations, unexpected=model.unexpected)), flush=True)
            component.readiness_diagnostics()
            raise
