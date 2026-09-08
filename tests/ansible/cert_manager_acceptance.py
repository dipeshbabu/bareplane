"""Real certificate issuance in a disposable Argo-managed component fixture."""

import base64
import subprocess

from component_acceptance import ComponentAcceptance


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
    component = ComponentAcceptance(kubectl, repository, 'cert-manager')
    command, api = component.command, component.api
    component.install()
    command('wait', '--for=condition=Ready', 'clusterissuer/lab-selfsigned', '--timeout=120s', timeout=130)
    identities = {}
    for name in ['cert-manager', 'cert-manager-cainjector', 'cert-manager-webhook']:
        command('rollout', 'status', 'deployment/' + name, '-n', 'cert-manager', '--timeout=120s', timeout=130)
        identities[name] = component.deployment_identity(name)
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
    component.refresh()
    for name, identity in identities.items():
        if component.deployment_identity(name) != identity:
            raise RuntimeError('Unchanged cert-manager reconciliation replaced or modified a workload')
    print('Argo-owned cert-manager converged, issued a verified CA/leaf certificate chain, and preserved workload identities.', flush=True)
