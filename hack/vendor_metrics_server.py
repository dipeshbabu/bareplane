#!/usr/bin/env python3
"""Curate the exact reviewed Metrics Server v0.9.0 release, never latest."""

import hashlib
from pathlib import Path
import sys

import yaml


SOURCE_SHA256 = '1cec29a5267809306a2c6ec74a3e449abbb705b4a8beed0c8a1963910f72c79b'
SOURCE_COMMIT = '2a7c4b2c7d46552ff47f4aeaa3a735c582587ecd'
LICENSE_SHA256 = 'b40930bbcf80744c86c46a12bc9da056641d722716c378f5659b9e555ef833e1'
NAMESPACE = 'metrics-server'


def curate(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise ValueError('Input is not the reviewed Metrics Server v0.9.0 release')
    objects = [dict(apiVersion='v1', kind='Namespace', metadata=dict(name=NAMESPACE))]
    for obj in yaml.safe_load_all(data):
        metadata = obj['metadata']
        if 'namespace' in metadata and obj['kind'] != 'RoleBinding':
            metadata['namespace'] = NAMESPACE
        for subject in obj.get('subjects', []):
            if subject.get('kind') == 'ServiceAccount':
                subject['namespace'] = NAMESPACE
        if obj['kind'] == 'Deployment':
            pod = obj['spec']['template']['spec']
            pod['tolerations'] = [dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')]
            pod['securityContext'] = dict(fsGroup=1000)
            container = pod['containers'][0]
            container['args'] = [
                '--cert-dir=/tmp', '--secure-port=10250', '--kubelet-preferred-address-types=InternalIP',
                '--kubelet-use-node-status-port', '--metric-resolution=15s',
                '--kubelet-certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt',
                '--tls-cert-file=/etc/metrics-server/tls/tls.crt', '--tls-private-key-file=/etc/metrics-server/tls/tls.key',
            ]
            container['resources'] = dict(requests=dict(cpu='BAREPLANE_METRICS_CPU', memory='BAREPLANE_METRICS_MEMORY'),
                                          limits=dict(memory='BAREPLANE_METRICS_MEMORY_LIMIT'))
            container['volumeMounts'].append(dict(name='serving-tls', mountPath='/etc/metrics-server/tls', readOnly=True))
            pod['volumes'].append(dict(name='serving-tls', secret=dict(secretName='metrics-server-serving', defaultMode=288)))
        if obj['kind'] == 'APIService':
            obj['spec']['insecureSkipTLSVerify'] = False
            obj['spec']['service']['namespace'] = NAMESPACE
            metadata.setdefault('annotations', {}).update({'cert-manager.io/inject-ca-from': NAMESPACE + '/metrics-server-serving',
                                                           'argocd.argoproj.io/sync-wave': '10'})
        objects.append(obj)
    objects += [
        dict(apiVersion='cert-manager.io/v1', kind='Issuer', metadata=dict(name='metrics-server-serving', namespace=NAMESPACE,
             annotations={'argocd.argoproj.io/sync-wave': '-20'}), spec=dict(selfSigned={})),
        dict(apiVersion='cert-manager.io/v1', kind='Certificate', metadata=dict(name='metrics-server-serving', namespace=NAMESPACE,
             annotations={'argocd.argoproj.io/sync-wave': '-10'}), spec=dict(
                 secretName='metrics-server-serving', commonName='metrics-server', duration='2160h', renewBefore='720h',
                 dnsNames=['metrics-server.metrics-server.svc'], usages=['digital signature', 'server auth'],
                 privateKey=dict(algorithm='ECDSA', size=256, rotationPolicy='Always'),
                 issuerRef=dict(name='metrics-server-serving', kind='Issuer', group='cert-manager.io'))),
    ]
    for obj in objects:
        annotations = obj['metadata'].setdefault('annotations', {})
        annotations.update({'bareplane.io/cluster': 'BAREPLANE_CLUSTER_NAME', 'bareplane.io/component': 'metrics-server'})
        if obj['kind'] == 'Namespace':
            annotations['argocd.argoproj.io/sync-wave'] = '-30'
    return (f'# Metrics Server v0.9.0, Apache-2.0; curated by hack/vendor_metrics_server.py.\n# Source commit: {SOURCE_COMMIT}\n# Source SHA256: {SOURCE_SHA256}\n'
            + yaml.safe_dump_all(objects, sort_keys=False, width=120)).encode()


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: vendor_metrics_server.py /path/to/components.yaml /path/to/LICENSE')
    target = Path(__file__).resolve().parents[1] / 'internal/render/gitops/assets/metrics-server'
    data = curate(Path(sys.argv[1]).read_bytes())
    license_data = Path(sys.argv[2]).read_bytes()
    if hashlib.sha256(license_data).hexdigest() != LICENSE_SHA256:
        raise ValueError('Expected upstream Apache-2.0 license')
    target.mkdir(parents=True, exist_ok=True)
    (target / 'upstream.yaml').write_bytes(data)
    (target / 'license.txt').write_bytes(license_data)
    print(f'Vendored {len(data)} bytes; SHA256 {hashlib.sha256(data).hexdigest()}')
