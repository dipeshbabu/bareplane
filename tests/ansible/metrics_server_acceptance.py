"""Real Argo deployment, verified aggregation, metrics and TLS rotation checks."""

import base64
import datetime
import hashlib
import subprocess
import time

from component_acceptance import ComponentAcceptance


def run_metrics_server_acceptance(kubectl, repository, work, nodes):
    component = ComponentAcceptance(kubectl, repository, 'metrics-server')
    command, api = component.command, component.api
    component.install()
    command('wait', '--for=condition=Available', 'apiservice/v1beta1.metrics.k8s.io', '--timeout=180s', timeout=190)
    identity = component.deployment_identity('metrics-server')

    def public_certificate():
        return base64.b64decode(command('get', 'secret', 'metrics-server-serving', '-n', 'metrics-server', '-o', r'jsonpath={.data.tls\.crt}'), validate=True)

    def verify_metrics():
        deadline = time.monotonic() + 180
        while True:
            try:
                service = api('get', 'apiservice', 'v1beta1.metrics.k8s.io', '-o', 'json')
                if service['spec'].get('insecureSkipTLSVerify', False) is not False or not service['spec'].get('caBundle'):
                    raise RuntimeError('Metrics APIService does not require verified TLS')
                if base64.b64decode(service['spec']['caBundle'], validate=True) != public_certificate():
                    raise RuntimeError('Metrics APIService trust has not followed the serving certificate')
                metrics = api('get', '--raw=/apis/metrics.k8s.io/v1beta1/nodes')['items']
                if {item['metadata']['name'] for item in metrics} != set(nodes):
                    raise RuntimeError('Resource metrics do not cover the expected nodes')
                for item in metrics:
                    timestamp = datetime.datetime.fromisoformat(item['timestamp'].replace('Z', '+00:00'))
                    age = (datetime.datetime.now(datetime.timezone.utc) - timestamp).total_seconds()
                    if not -10 <= age <= 120 or not all(item['usage'].get(key) for key in ['cpu', 'memory']):
                        raise RuntimeError('Node resource metrics are missing or stale')
                pods = api('get', '--raw=/apis/metrics.k8s.io/v1beta1/namespaces/metrics-server/pods')['items']
                if not pods or any(not pod.get('containers') or not all(container['usage'].get('cpu') and container['usage'].get('memory')
                                                                      for container in pod['containers']) for pod in pods):
                    raise RuntimeError('Pod resource metrics are missing')
                command('top', 'nodes')
                command('top', 'pods', '-n', 'metrics-server')
                return
            except RuntimeError:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(2)

    verify_metrics()
    certificate = public_certificate()
    (work / 'metrics-server-public.crt').write_bytes(certificate)
    verified = subprocess.run(['openssl', 'verify', '-no-CApath', '-no-CAstore', '-check_ss_sig', '-purpose', 'sslserver', '-CAfile', str(work / 'metrics-server-public.crt'),
                               '-verify_hostname', 'metrics-server.metrics-server.svc', str(work / 'metrics-server-public.crt')],
                              capture_output=True, timeout=20)
    if verified.returncode:
        raise RuntimeError('Metrics serving certificate DNS identity or explicit trust failed verification')
    original_hash = hashlib.sha256(certificate).digest()
    current = api('get', 'certificate', 'metrics-server-serving', '-n', 'metrics-server', '-o', 'json')
    revision = current['status']['revision']
    # Same status contract as cmctl v2.4.0 renew: preserve existing conditions and
    # add Issuing=True for this current generation using an exact version update.
    timestamp = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00', 'Z')
    conditions = [condition for condition in current['status'].get('conditions', []) if condition['type'] != 'Issuing']
    conditions.append(dict(type='Issuing', status='True', reason='ManuallyTriggered', message='Disposable acceptance certificate rotation',
                           observedGeneration=current['metadata']['generation'], lastTransitionTime=timestamp))
    current['status']['conditions'] = conditions
    api('replace', '--raw=/apis/cert-manager.io/v1/namespaces/metrics-server/certificates/metrics-server-serving/status', '-f', '-', data=current)
    deadline = time.monotonic() + 180
    while True:
        current = api('get', 'certificate', 'metrics-server-serving', '-n', 'metrics-server', '-o', 'json')
        if current.get('status', {}).get('revision', 0) > revision and hashlib.sha256(public_certificate()).digest() != original_hash:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError('Metrics serving certificate did not rotate')
        time.sleep(2)
    verify_metrics()
    component.refresh()
    if component.deployment_identity('metrics-server') != identity:
        raise RuntimeError('Certificate rotation or unchanged reconciliation replaced the Metrics Server workload')
    print('Argo-owned Metrics Server provided fresh node/pod metrics with verified TLS; certificate rotation and unchanged workload identity passed.', flush=True)
