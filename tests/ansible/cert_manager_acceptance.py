"""Real certificate issuance in a disposable Argo-managed component fixture."""

import base64
import json
import os
import re
import subprocess
import time

import yaml


def certificate_resources():
    return [
        dict(apiVersion='cert-manager.io/v1', kind='Certificate', metadata=dict(name='acceptance-ca', namespace='cert-manager'),
             spec=dict(isCA=True, commonName='Bareplane disposable acceptance CA', secretName='acceptance-ca', duration='24h', renewBefore='8h',
                       privateKey=dict(algorithm='ECDSA', size=256, rotationPolicy='Always'),
                       issuerRef=dict(name='lab-selfsigned', kind='ClusterIssuer', group='cert-manager.io'))),
        dict(apiVersion='cert-manager.io/v1', kind='Issuer', metadata=dict(name='acceptance-ca', namespace='cert-manager'), spec=dict(ca=dict(secretName='acceptance-ca'))),
        dict(apiVersion='cert-manager.io/v1', kind='Certificate', metadata=dict(name='acceptance-leaf', namespace='cert-manager'),
             spec=dict(secretName='acceptance-leaf', dnsNames=['acceptance.cert-manager.svc'], duration='12h', renewBefore='4h',
                       privateKey=dict(algorithm='ECDSA', size=256, rotationPolicy='Always'),
                       issuerRef=dict(name='acceptance-ca', kind='Issuer', group='cert-manager.io'))),
    ]


def run_cert_manager_acceptance(kubectl, repository, work):
    if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1':
        raise RuntimeError('Component acceptance is restricted to disposable GitHub runners')
    revision = os.environ.get('BAREPLANE_TEST_GIT_REF', '')
    source_repo = os.environ.get('BAREPLANE_TEST_GIT_REPO', '')
    if not re.fullmatch('[0-9a-f]{40}', revision) or not re.fullmatch(r'https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git', source_repo):
        raise RuntimeError('Component acceptance requires an exact public source revision')

    def command(*args, data=None, timeout=60):
        result = subprocess.run(kubectl + list(args), input=json.dumps(data).encode() if data is not None else b'',
                                capture_output=True, timeout=timeout, check=False)
        if result.returncode:
            raise RuntimeError('Disposable component API operation failed; no response bodies are published')
        return result.stdout

    def api(*args, **kwargs):
        data = command(*args, **kwargs)
        return json.loads(data) if data.strip() else {}

    if api('get', 'namespace', 'cert-manager', '--ignore-not-found', '-o', 'json'):
        raise RuntimeError('Disposable component namespace unexpectedly exists')
    app = yaml.safe_load((repository / 'examples/gitops/cert-manager-root/applications/cert-manager.yaml').read_bytes())
    app['spec']['source']['repoURL'] = source_repo
    app['spec']['source']['targetRevision'] = revision
    api('create', '-f', '-', '-o', 'json', data=app)

    def wait_application(previous_reconciliation=None):
        deadline = time.monotonic() + 600
        while True:
            current = api('get', 'application', 'lab-cert-manager', '-n', 'argocd', '-o', 'json')
            status = current.get('status', {})
            if (status.get('sync', {}).get('status') == 'Synced' and status['sync'].get('revision') == revision
                    and status['sync'].get('comparedTo', {}).get('source') == app['spec']['source']
                    and status.get('health', {}).get('status') == 'Healthy' and not current.get('operation')
                    and not status.get('conditions')
                    and not current['metadata'].get('annotations', {}).get('argocd.argoproj.io/refresh')
                    and status.get('reconciledAt') != previous_reconciliation
                    and status.get('operationState', {}).get('phase') not in {'Running', 'Failed', 'Error', 'Terminating'}):
                return current
            if time.monotonic() > deadline:
                kinds = [condition.get('type') for condition in status.get('conditions', [])]
                raise RuntimeError('Cert-manager Argo convergence timed out; condition types: ' + str(kinds))
            time.sleep(2)

    wait_application()
    command('wait', '--for=condition=Ready', 'clusterissuer/lab-selfsigned', '--timeout=120s', timeout=130)
    identities = {}
    for name in ['cert-manager', 'cert-manager-cainjector', 'cert-manager-webhook']:
        command('rollout', 'status', 'deployment/' + name, '-n', 'cert-manager', '--timeout=120s', timeout=130)
        deployment = api('get', 'deployment', name, '-n', 'cert-manager', '-o', 'json')
        if not deployment['metadata'].get('annotations', {}).get('argocd.argoproj.io/tracking-id', '').startswith('lab-cert-manager:'):
            raise RuntimeError('Cert-manager workload is not Argo-owned')
        identities[name] = (deployment['metadata']['uid'], deployment['metadata']['generation'])
    for resource in certificate_resources():
        api('create', '-f', '-', '-o', 'json', data=resource)
        command('wait', '--for=condition=Ready', resource['kind'].lower() + '/' + resource['metadata']['name'],
                '-n', 'cert-manager', '--timeout=180s', timeout=190)
    # Retrieve public certificates only. Private key data never leaves Kubernetes.
    for name in ['acceptance-ca', 'acceptance-leaf']:
        encoded = command('get', 'secret', name, '-n', 'cert-manager', '-o', r'jsonpath={.data.tls\.crt}')
        certificate = base64.b64decode(encoded, validate=True)
        (work / (name + '.crt')).write_bytes(certificate)
    verification = subprocess.run(['openssl', 'verify', '-no-CApath', '-no-CAstore', '-CAfile', str(work / 'acceptance-ca.crt'),
                                   '-verify_hostname', 'acceptance.cert-manager.svc', str(work / 'acceptance-leaf.crt')],
                                  capture_output=True, timeout=20)
    if verification.returncode:
        raise RuntimeError('Issued certificate chain or DNS identity failed validation')
    previous = api('get', 'application', 'lab-cert-manager', '-n', 'argocd', '-o', 'json')['status']['reconciledAt']
    # Argo timestamps have second resolution; ensure the requested refresh has
    # a distinguishable observation time, rather than accepting cached health.
    time.sleep(1.1)
    command('annotate', 'application', 'lab-cert-manager', '-n', 'argocd', 'argocd.argoproj.io/refresh=hard', '--overwrite')
    wait_application(previous)
    for name, identity in identities.items():
        deployment = api('get', 'deployment', name, '-n', 'cert-manager', '-o', 'json')
        if (deployment['metadata']['uid'], deployment['metadata']['generation']) != identity:
            raise RuntimeError('Unchanged cert-manager reconciliation replaced or modified a workload')
    print('Argo-owned cert-manager converged, issued a verified CA/leaf certificate chain, and preserved workload identities.', flush=True)
