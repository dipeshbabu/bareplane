#!/usr/bin/env python3
"""Curate a small pinned Prometheus + kube-state-metrics baseline."""

import hashlib
from pathlib import Path
import sys

import yaml


KSM_VERSION = '2.20.0'
PROMETHEUS_VERSION = '3.14.0'
KSM_COMMIT = '4ffeda2ef866b0fef372849825a803296483b336'
PROMETHEUS_COMMIT = 'd7598b7141418fa35be2b5ec5d0fefb634199610'
NAMESPACE = 'observability'
SOURCES = {
    'deployment': '08d57682c0117bda50b8e894be1669001e49ce0ff3e60c11fccc6f56dfbc9f2e',
    'cluster-role': '4447544443859be7f70deca48e4cb75d30b0b7da090cb9e914c9c08f97f7e62c',
    'cluster-role-binding': 'afd513a9dba5ffe1a4128e4f612788dcccc314fb58afaef7a627c506229570df',
    'service-account': '91f24588ac4f8944b9211fccefdce9ee70434676e8a4a55a14d69bc7f68ae0a5',
    'service': 'efa9813fb6a6a2d11b509cf4f2eb0ab044935b4153b425e697d4b31c7f6f42a4',
}
METRICS = ['kube_node_status_condition', 'kube_pod_status_phase', 'kube_pod_container_status_restarts_total',
           'kube_pod_container_status_waiting_reason', 'kube_deployment_spec_replicas',
           'kube_deployment_status_replicas_available', 'kube_namespace_status_phase']


class BlockDumper(yaml.SafeDumper):
    pass


BlockDumper.add_representer(str, lambda dumper, value: dumper.represent_scalar('tag:yaml.org,2002:str', value, style='|' if '\n' in value else None))


def verified(path, expected):
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != expected:
        raise ValueError('Observability source differs from its reviewed checksum')
    return data


def metadata(name):
    return dict(name=name, namespace=NAMESPACE)


def curate(directory):
    objects = [dict(apiVersion='v1', kind='Namespace', metadata=dict(name=NAMESPACE))]
    for name, expected in SOURCES.items():
        obj = yaml.safe_load(verified(directory / ('ksm-v2.20.0-' + name + '.yaml'), expected))
        if obj['kind'] not in {'ClusterRole', 'ClusterRoleBinding'}:
            obj['metadata']['namespace'] = NAMESPACE
        if obj['kind'] == 'ClusterRole':
            obj['metadata']['name'] = 'bareplane-kube-state-metrics'
            obj['rules'] = [dict(apiGroups=[''], resources=['nodes', 'pods', 'namespaces'], verbs=['get', 'list', 'watch']),
                            dict(apiGroups=['apps'], resources=['deployments'], verbs=['get', 'list', 'watch'])]
        if obj['kind'] == 'ClusterRoleBinding':
            obj['metadata']['name'] = 'bareplane-kube-state-metrics'
            obj['roleRef']['name'] = 'bareplane-kube-state-metrics'
            obj['subjects'] = [dict(kind='ServiceAccount', name='kube-state-metrics', namespace=NAMESPACE)]
        if obj['kind'] == 'Service':
            obj['spec'].pop('clusterIP', None)
            obj['spec']['type'] = 'ClusterIP'
            obj['spec']['ports'] = [dict(name='http-metrics', port=8080, targetPort='http-metrics')]
        if obj['kind'] == 'Deployment':
            pod = obj['spec']['template']['spec']
            pod['tolerations'] = [dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')]
            container = pod['containers'][0]
            container['args'] = ['--resources=nodes,pods,namespaces,deployments', '--metric-allowlist=' + ','.join(METRICS),
                                 '--host=0.0.0.0', '--telemetry-host=0.0.0.0', '--server-read-timeout=10s', '--server-write-timeout=10s']
            container['resources'] = dict(requests=dict(cpu='50m', memory='64Mi'), limits=dict(memory='256Mi'))
        objects.append(obj)
    labels = {'app.kubernetes.io/name': 'bareplane-prometheus'}
    security = dict(runAsNonRoot=True, runAsUser=65534, runAsGroup=65534, fsGroup=65534, seccompProfile=dict(type='RuntimeDefault'))
    prometheus = [dict(apiVersion='v1', kind='ServiceAccount', metadata=metadata('bareplane-prometheus'), automountServiceAccountToken=False),
        dict(apiVersion='v1', kind='Service', metadata=metadata('bareplane-prometheus'), spec=dict(type='ClusterIP', selector=labels,
            ports=[dict(name='http', port=9090, targetPort='http')])),
        dict(apiVersion='apps/v1', kind='Deployment', metadata=metadata('bareplane-prometheus'), spec=dict(replicas=1,
            strategy=dict(type='Recreate'), selector=dict(matchLabels=labels), template=dict(metadata=dict(labels=labels,
                annotations={'bareplane.io/config-sha256': 'BAREPLANE_OBSERVABILITY_CONFIG_SHA256'}), spec=dict(
                    serviceAccountName='bareplane-prometheus', automountServiceAccountToken=False, securityContext=security,
                    nodeSelector={'kubernetes.io/os': 'linux'},
                    tolerations=[dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')],
                    volumes=[dict(name='configuration', configMap=dict(name='bareplane-prometheus')),
                             dict(name='data', emptyDir=dict(sizeLimit='2Gi'))],
                    containers=[dict(name='prometheus', image='quay.io/prometheus/prometheus:v' + PROMETHEUS_VERSION,
                        imagePullPolicy='IfNotPresent', args=['--config.file=/etc/prometheus/prometheus.yml', '--storage.tsdb.path=/prometheus',
                            '--query.max-concurrency=4', '--query.max-samples=500000', '--query.timeout=30s', '--storage.tsdb.wal-compression'],
                        securityContext=dict(runAsNonRoot=True, allowPrivilegeEscalation=False, readOnlyRootFilesystem=True, capabilities=dict(drop=['ALL'])),
                        resources=dict(requests={'cpu': '100m', 'memory': '128Mi', 'ephemeral-storage': '1Gi'},
                                       limits={'memory': '512Mi', 'ephemeral-storage': '3Gi'}),
                        volumeMounts=[dict(name='configuration', mountPath='/etc/prometheus', readOnly=True), dict(name='data', mountPath='/prometheus')],
                        ports=[dict(name='http', containerPort=9090)],
                        readinessProbe=dict(httpGet=dict(path='/-/ready', port='http'), initialDelaySeconds=5, periodSeconds=10, timeoutSeconds=5),
                        livenessProbe=dict(httpGet=dict(path='/-/healthy', port='http'), initialDelaySeconds=30, periodSeconds=15, timeoutSeconds=5, failureThreshold=6))],
                )))),
    ]
    configuration = {
        'global': dict(scrape_interval='30s', scrape_timeout='10s', evaluation_interval='30s'),
        'storage': dict(tsdb=dict(retention=dict(time='BAREPLANE_OBSERVABILITY_RETENTION_HOURS' + 'h', size='1GB'))),
        'rule_files': ['/etc/prometheus/rules.yml'],
        'scrape_configs': [dict(job_name=name, static_configs=[dict(targets=[target])], sample_limit=10000, body_size_limit='10MB',
                                label_limit=30, label_name_length_limit=128, label_value_length_limit=256)
                           for name, target in [('prometheus', '127.0.0.1:9090'), ('kube-state-metrics', 'kube-state-metrics.observability.svc:8080')]],
    }
    rules = dict(groups=[dict(name='bareplane-health', rules=[
        dict(alert='BareplaneScrapeUnavailable', expr='up == 0', **{'for': '2m'}, labels=dict(severity='warning'),
             annotations=dict(summary='A baseline metrics target is unavailable')),
        dict(alert='BareplaneNodeNotReady', expr='kube_node_status_condition{condition="Ready",status="true"} == 0', **{'for': '5m'},
             labels=dict(severity='warning'), annotations=dict(summary='A Kubernetes node is not Ready')),
        dict(alert='BareplaneDeploymentUnavailable', expr='kube_deployment_spec_replicas > kube_deployment_status_replicas_available', **{'for': '5m'},
             labels=dict(severity='warning'), annotations=dict(summary='A Deployment has fewer available replicas than requested')),
    ])])
    cm = dict(apiVersion='v1', kind='ConfigMap', metadata=metadata('bareplane-prometheus'),
              data={'prometheus.yml': yaml.safe_dump(configuration, sort_keys=False), 'rules.yml': yaml.safe_dump(rules, sort_keys=False)})
    for obj in objects + prometheus + [cm]:
        obj['metadata'].setdefault('annotations', {}).update({'bareplane.io/cluster': 'BAREPLANE_CLUSTER_NAME',
            'bareplane.io/component': 'observability', 'argocd.argoproj.io/sync-wave': '-30' if obj['kind'] == 'Namespace' else '-10'})
    header = f'# Prometheus {PROMETHEUS_VERSION} ({PROMETHEUS_COMMIT}); kube-state-metrics {KSM_VERSION} ({KSM_COMMIT}).\n# Curated by hack/vendor_observability.py; Apache-2.0.\n'
    files = {name: (header + yaml.dump_all(values, Dumper=BlockDumper, sort_keys=False, width=120)).encode()
             for name, values in [('collector.yaml', objects), ('prometheus.yaml', prometheus), ('configuration.yaml', [cm])]}
    for name, source, expected in [
        ('kube-state-metrics-license.txt', 'ksm-v2.20.0-LICENSE', 'b40930bbcf80744c86c46a12bc9da056641d722716c378f5659b9e555ef833e1'),
        ('prometheus-license.txt', 'prometheus-v3.14.0-LICENSE', 'c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4'),
        ('prometheus-notice.txt', 'prometheus-v3.14.0-NOTICE', 'ac9e304462a58a4d71b6488423d4447e614c8b31d517fe24345016522c102a78'),
    ]:
        files[name] = verified(directory / source, expected)
    return files


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('usage: vendor_observability.py reviewed-source-directory')
    target = Path(__file__).resolve().parents[1] / 'internal/render/gitops/assets/observability'
    target.mkdir(parents=True, exist_ok=True)
    for name, data in curate(Path(sys.argv[1])).items():
        (target / name).write_bytes(data)
        print(f'{name}: {len(data)} bytes, SHA256 {hashlib.sha256(data).hexdigest()}')
