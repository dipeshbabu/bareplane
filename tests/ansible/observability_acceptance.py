"""Real Argo-owned collection/query checks with deliberate ephemeral history."""

import json
import subprocess
import time
from urllib.parse import urlencode

from component_acceptance import ComponentAcceptance


def wait_for(predicate, message, timeout=240):
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() >= deadline:
            raise RuntimeError(message)
        time.sleep(2)


def run_observability_acceptance(kubectl, repository, work):
    component = ComponentAcceptance(kubectl, repository, 'observability')
    command, api = component.command, component.api
    component.install()
    identities = {name: component.deployment_identity(name) for name in ['bareplane-prometheus', 'kube-state-metrics']}
    command('exec', 'deployment/bareplane-prometheus', '-n', 'observability', '--', '/bin/promtool', 'check', 'config', '/etc/prometheus/prometheus.yml')
    rules = (repository / 'tests/ansible/observability_rules.yaml').read_bytes()
    result = subprocess.run(kubectl + ['exec', '-i', 'deployment/bareplane-prometheus', '-n', 'observability', '--',
                            '/bin/promtool', 'test', 'rules', '/dev/stdin'], input=rules, capture_output=True, timeout=90)
    if result.returncode:
        raise RuntimeError('Pinned Prometheus rule behavior differs from its reviewed contract')

    def query(expression):
        path = '/api/v1/namespaces/observability/services/http:bareplane-prometheus:9090/proxy/api/v1/query?' + urlencode({'query': expression})
        try:
            response = api('get', '--raw=' + path)
        except RuntimeError:
            return None
        if response.get('status') != 'success' or response.get('data', {}).get('resultType') != 'vector':
            return None
        return response['data'].get('result')

    def equals(expression, value):
        result = query(expression)
        return result is not None and len(result) == 1 and result[0].get('value', [None, None])[1] == value

    wait_for(lambda: equals('sum(up{job=~"prometheus|kube-state-metrics"})', '2'), 'Baseline scrape targets did not become healthy')
    wait_for(lambda: equals('kube_node_status_condition{node="lab-control-1",condition="Ready",status="true"}', '1'),
             'Prometheus did not collect the real Kubernetes node readiness state')
    for name in ['bareplane-prometheus', 'kube-state-metrics']:
        service = api('get', 'service', name, '-n', 'observability', '-o', 'json')
        if service['spec'].get('type') != 'ClusterIP' or service['spec'].get('externalIPs'):
            raise RuntimeError('Observability acquired external exposure')
    namespace = 'observability-probe'
    command('create', '-f', '-', data=dict(apiVersion='v1', kind='Namespace', metadata=dict(name=namespace)))
    deployment = api('create', '-f', '-', '-o', 'json', data=dict(apiVersion='apps/v1', kind='Deployment',
        metadata=dict(name='deliberately-unscheduled', namespace=namespace), spec=dict(replicas=1, selector=dict(matchLabels={'app': 'observability-probe'}),
            template=dict(metadata=dict(labels={'app': 'observability-probe'}), spec=dict(nodeSelector={'bareplane.io/disposable-never-schedule': 'true'},
                containers=[dict(name='pause', image='registry.k8s.io/pause:3.10.2')])))))
    selector = '{namespace="observability-probe",deployment="deliberately-unscheduled"}'
    metric = 'kube_deployment_spec_replicas' + selector
    wait_for(lambda: equals(metric + ' - kube_deployment_status_replicas_available' + selector, '1'),
             'Controlled unavailable workload was not reflected in the collected metrics')
    command('delete', '--raw=/apis/apps/v1/namespaces/' + namespace + '/deployments/deliberately-unscheduled', '-f', '-',
            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=deployment['metadata']['uid'])))
    wait_for(lambda: query(metric) == [], 'Deleted probe remained in current Kubernetes state metrics')
    history = 'max_over_time(' + metric + '[10m])'
    if not equals(history, '1'):
        raise RuntimeError('Prometheus did not retain the probe history before Pod replacement')
    pods = api('get', 'pods', '-n', 'observability', '-l', 'app.kubernetes.io/name=bareplane-prometheus', '-o', 'json')['items']
    if len(pods) != 1:
        raise RuntimeError('Unexpected Prometheus Pod count before disposable restart')
    previous = pods[0]['metadata']
    command('delete', '--raw=/api/v1/namespaces/observability/pods/' + previous['name'], '-f', '-',
            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=previous['uid'])))

    def replaced():
        observed = api('get', 'pods', '-n', 'observability', '-l', 'app.kubernetes.io/name=bareplane-prometheus', '-o', 'json')['items']
        return len(observed) == 1 and observed[0]['metadata']['uid'] != previous['uid'] and any(
            item.get('type') == 'Ready' and item.get('status') == 'True' for item in observed[0].get('status', {}).get('conditions', []))

    wait_for(replaced, 'Prometheus did not recover after its disposable Pod replacement')
    component.wait_application()
    wait_for(lambda: equals('sum(up{job=~"prometheus|kube-state-metrics"})', '2'), 'Scrapes did not resume after ephemeral recovery')
    if query(history) != []:
        raise RuntimeError('Ephemeral history contract did not match actual Pod replacement behavior')
    component.refresh()
    for name, identity in identities.items():
        if component.deployment_identity(name) != identity:
            raise RuntimeError('Read-only observability reconciliation replaced or reconfigured its Deployment')
    print('Argo-owned observability passed pinned configuration/rule checks, real readiness and workload metrics, bounded cluster-local exposure, and explicit ephemeral-history recovery.', flush=True)
