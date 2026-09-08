"""ExternalDNS convergence against an isolated, in-memory Cloudflare API."""

import base64
import copy
import json
import time
from urllib.parse import urlsplit

import yaml

from component_acceptance import ComponentAcceptance
from fake_cloudflare import FakeCloudflare, FakeCloudflareServer


def wait_for(predicate, message, timeout=180):
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() >= deadline:
            raise RuntimeError(message)
        time.sleep(1)


def run_dns_acceptance(kubectl, repository, work):
    component = ComponentAcceptance(kubectl, repository, 'external-dns')
    command, api = component.command, component.api
    source_namespace = 'dns-workloads'
    model = FakeCloudflare()
    model.seed('unowned.apps.example.test', 'A', '192.0.2.90')
    model.seed('foreign.apps.example.test', 'A', '192.0.2.90')
    documents = list(yaml.safe_load_all((repository / 'components/external-dns/upstream.yaml').read_bytes()))
    deployment = next(obj for obj in documents if obj['kind'] == 'Deployment')

    def service(name, hostname, address='192.0.2.80', annotated=True, kind='LoadBalancer'):
        annotations = {'external-dns.kubernetes.io/hostname': hostname, 'external-dns.kubernetes.io/ttl': '300'}
        if annotated:
            annotations['bareplane.io/dns-managed'] = 'true'
        return dict(apiVersion='v1', kind='Service', metadata=dict(name=name, namespace=source_namespace, annotations=annotations),
                    spec=dict(type=kind, externalIPs=[address], ports=[dict(port=80, targetPort=8080)]))

    def records(name, kind='A'):
        return [record for record in model.snapshot() if record['name'] == name and record['type'] == kind]

    def read_count():
        with model.lock:
            return sum(request['method'] == 'GET' and request['path'].endswith('/dns_records') for request in model.requests)

    def target_matches(name, target):
        found = records(name)
        return len(found) == 1 and found[0]['content'] == target

    def assert_unrelated_unchanged():
        for name in ['unowned', 'foreign']:
            values = records(name + '.apps.example.test')
            if len(values) != 1 or values[0]['content'] != '192.0.2.90':
                raise RuntimeError('ExternalDNS changed an unowned or foreign-owned record')
        for name in ['ignored.apps.example.test', 'wrongtype.apps.example.test', 'outside.other.example.test']:
            if records(name):
                raise RuntimeError('ExternalDNS exceeded its annotation, Service type or domain filter')
        with model.lock:
            if any('/zones/' + model.zone_id not in request['path'] for request in model.requests):
                raise RuntimeError('ExternalDNS queried outside the explicitly selected zone')
            owned_names = {'managed.apps.example.test'} | {record['name'] for record in model.records.values()
                                                          if record['type'] == 'TXT' and 'b' * 32 in record['content']}
            if any(operation['name'] not in owned_names for operation in model.operations):
                raise RuntimeError('ExternalDNS attempted to change a record outside its explicit ownership')

    # These objects are test workloads, not part of the controller's Git source.
    command('create', '-f', '-', data=dict(apiVersion='v1', kind='Namespace', metadata=dict(name=source_namespace)))
    for resource in [service('managed', 'managed.apps.example.test'), service('unowned', 'unowned.apps.example.test'),
                     service('foreign', 'foreign.apps.example.test'), service('ignored', 'ignored.apps.example.test', annotated=False),
                     service('wrongtype', 'wrongtype.apps.example.test', kind='ClusterIP'), service('outside', 'outside.other.example.test')]:
        command('create', '-f', '-', data=resource)

    with FakeCloudflareServer(model, '192.0.2.1') as server:
        def desired_source(mode, revision='initial'):
            args = [argument.replace('--interval=1m', '--interval=2s') for argument in deployment['spec']['template']['spec']['containers'][0]['args']]
            args = ['--dry-run=' + ('true' if mode == 'dry-run' else 'false') if argument.startswith('--dry-run=') else argument for argument in args]
            patch = dict(apiVersion='apps/v1', kind='Deployment', metadata=dict(name='external-dns'), spec=dict(template=dict(
                metadata=dict(annotations={'bareplane.io/credentials-revision': revision}),
                spec=dict(containers=[dict(name='external-dns', args=args, env=[dict(name='CLOUDFLARE_BASE_URL', value=server.url)])]))))
            source = copy.deepcopy(component.app['spec']['source'])
            source['kustomize'] = {'patches': [dict(target=dict(group='apps', version='v1', kind='Deployment', name='external-dns'),
                                                 patch=json.dumps(patch))]}
            return source

        component.app['spec']['source'] = desired_source('dry-run')
        component.create()
        wait_for(lambda: bool(api('get', 'namespace', 'external-dns', '--ignore-not-found', '-o', 'json')),
                 'Argo did not create the disposable DNS controller namespace')
        # Fail closed if the test-only SDK endpoint override is ever ineffective:
        # public Cloudflare addresses cannot be reached by this Pod.
        policy = dict(apiVersion='networking.k8s.io/v1', kind='NetworkPolicy',
                      metadata=dict(name='disposable-fake-api-only', namespace='external-dns'), spec=dict(
                          podSelector=dict(matchLabels={'app': 'external-dns'}), policyTypes=['Egress'], egress=[
                              {'to': [{'ipBlock': {'cidr': '192.0.2.1/32'}}], 'ports': [{'protocol': 'TCP', 'port': urlsplit(server.url).port}]},
                              {'to': [{'ipBlock': {'cidr': '192.0.2.0/24'}}, {'ipBlock': {'cidr': '10.96.0.1/32'}}],
                               'ports': [{'protocol': 'TCP', 'port': 443}, {'protocol': 'TCP', 'port': 6443}]},
                              {'to': [{'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'kube-system'}},
                                       'podSelector': {'matchLabels': {'k8s-app': 'kube-dns'}}}],
                               'ports': [{'protocol': 'UDP', 'port': 53}, {'protocol': 'TCP', 'port': 53}]},
                          ]))
        command('create', '-f', '-', data=policy)
        # Only a deliberately powerless fake token is created, after egress is
        # isolated. No token value is ever included in the published fixture.
        command('create', '-f', '-', data=dict(apiVersion='v1', kind='Secret', metadata=dict(name='cloudflare-api', namespace='external-dns'),
                                              type='Opaque', stringData={'apiToken': model.token}))
        component.wait_application()
        wait_for(lambda: read_count() >= 2, 'DNS dry-run did not read the fake provider')
        with model.lock:
            if model.mutations:
                raise RuntimeError('DNS dry-run attempted provider writes')
            model.authorized = False
        wait_for(lambda: model.denials >= 2, 'DNS provider authorization failure was not exercised')
        assert_unrelated_unchanged()
        with model.lock:
            if model.mutations:
                raise RuntimeError('Denied DNS access attempted mutations')
            model.authorized = True

        def update_application(mode, revision='initial'):
            current = api('get', 'application', component.name, '-n', 'argocd', '-o', 'json')
            current['spec']['source'] = desired_source(mode, revision)
            component.app['spec']['source'] = current['spec']['source']
            api('replace', '-f', '-', '-o', 'json', data=current)
            component.wait_application()

        update_application('apply')
        wait_for(lambda: len(records('managed.apps.example.test')) == 1, 'DNS apply did not create the explicitly selected record')
        managed = records('managed.apps.example.test')[0]
        if managed['content'] != '192.0.2.80':
            raise RuntimeError('DNS apply produced an unexpected target')
        ownership = [record for record in model.snapshot() if record['type'] == 'TXT' and 'b' * 32 in record['content']]
        if len(ownership) != 1 or 'managed.apps.example.test' not in ownership[0]['name']:
            raise RuntimeError('DNS apply did not establish the expected TXT ownership')
        foreign = ownership[0]
        model.seed(foreign['name'].replace('managed.apps.example.test', 'foreign.apps.example.test'), 'TXT',
                   foreign['content'].replace('b' * 32, 'c' * 32).replace('/managed', '/foreign'))
        previous_reads = read_count()
        command('patch', 'service', 'managed', '-n', source_namespace, '--type=merge', '-p', json.dumps({'spec': {'externalIPs': ['192.0.2.81']}}))
        wait_for(lambda: target_matches('managed.apps.example.test', '192.0.2.81'), 'Owned DNS record did not update')
        wait_for(lambda: read_count() >= previous_reads + 2, 'DNS ownership reconciliation did not repeat')
        assert_unrelated_unchanged()
        with model.lock:
            operation_count = len(model.operations)
        command('delete', 'service', 'managed', '-n', source_namespace, '--wait=true')
        previous_reads = read_count()
        wait_for(lambda: read_count() >= previous_reads + 2, 'DNS removal policy was not reconciled')
        if len(records('managed.apps.example.test')) != 1:
            raise RuntimeError('Upsert-only policy deleted a released DNS record')
        with model.lock:
            if any(operation['method'] == 'DELETE' for operation in list(model.operations)[operation_count:]):
                raise RuntimeError('Source removal attempted a logical DNS deletion despite upsert-only policy')

        deployment_uid = component.deployment_identity('external-dns')[0]
        with model.lock:
            model.token = 'rotated-disposable-token-with-no-external-authority'
        secret = api('get', 'secret', 'cloudflare-api', '-n', 'external-dns', '-o', 'json')
        secret['data']['apiToken'] = base64.b64encode(model.token.encode()).decode()
        command('replace', '-f', '-', data=secret)
        previous_reads = read_count()
        update_application('apply', 'rotated')
        wait_for(lambda: read_count() >= previous_reads + 2, 'Credential revision did not restore provider access')
        assert_unrelated_unchanged()
        component.refresh()
        if component.deployment_identity('external-dns')[0] != deployment_uid:
            raise RuntimeError('DNS credential rotation replaced the Deployment identity')
        print('Argo-owned ExternalDNS passed isolated fake-provider dry-run, denial, scoped apply/update, TXT ownership, no-delete and credential-rotation checks.', flush=True)
