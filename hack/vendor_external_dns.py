#!/usr/bin/env python3
"""Curate exact ExternalDNS v0.22.0 source into a service-only scoped payload."""

import hashlib
from pathlib import Path
import sys

import yaml


COMMIT = '994f908d4abdfe5fbf38f2f61613ed570432e43a'
SOURCES = {
    'deployment': 'ad9f94d059478cf9b3ca945737323b14fb5288195062ab866d1ff8a9f394898a',
    'clusterrole': '66bdbcb9e55501891b20633cb9c01d930e32e3df3823a98601a63bb4232224bb',
    'clusterrolebinding': '9741c2cbb929c2048848493ae985dba064a78eeef92e7e6b9b033faa2c8361e7',
    'serviceaccount': 'a0daf3d8a90132d50ad8151c98a9edb5811677481adf3dd28916aaf9581f0af7',
}
LICENSE_SHA256 = 'b40930bbcf80744c86c46a12bc9da056641d722716c378f5659b9e555ef833e1'


def curate(directory):
    objects = [dict(apiVersion='v1', kind='Namespace', metadata=dict(name='external-dns'))]
    for name, expected in SOURCES.items():
        data = (directory / ('external-dns-v0.22.0-' + name + '.yaml')).read_bytes()
        if hashlib.sha256(data).hexdigest() != expected:
            raise ValueError('ExternalDNS source differs from its reviewed checksum')
        obj = yaml.safe_load(data)
        metadata = obj['metadata']
        metadata['namespace'] = 'external-dns'
        if obj['kind'] == 'ClusterRole':
            obj['kind'] = 'Role'
            metadata.update(name='bareplane-external-dns-reader', namespace='BAREPLANE_DNS_SOURCE_NAMESPACE')
            # With a LoadBalancer-only service filter v0.22.0 does not create
            # Pod, Node or EndpointSlice informers. No other resource is read.
            obj['rules'] = [dict(apiGroups=[''], resources=['services'], verbs=['get', 'list', 'watch'])]
        if obj['kind'] == 'ClusterRoleBinding':
            obj['kind'] = 'RoleBinding'
            metadata.update(name='bareplane-external-dns-reader', namespace='BAREPLANE_DNS_SOURCE_NAMESPACE')
            obj['roleRef'] = dict(apiGroup='rbac.authorization.k8s.io', kind='Role', name='bareplane-external-dns-reader')
            obj['subjects'] = [dict(kind='ServiceAccount', name='external-dns', namespace='external-dns')]
        if obj['kind'] in {'Role', 'RoleBinding'}:
            metadata['annotations'] = {'bareplane.io/existing-namespace': 'BAREPLANE_DNS_SOURCE_NAMESPACE'}
        if obj['kind'] == 'Deployment':
            obj['spec']['replicas'] = 1
            pod = obj['spec']['template']['spec']
            obj['spec']['template']['metadata']['annotations'] = {'bareplane.io/credentials-revision': 'BAREPLANE_DNS_CREDENTIAL_REVISION'}
            pod['securityContext'] = dict(runAsNonRoot=True, fsGroup=65534, seccompProfile=dict(type='RuntimeDefault'))
            pod['tolerations'] = [dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')]
            pod['nodeSelector'] = {'kubernetes.io/os': 'linux'}
            container = pod['containers'][0]
            container['image'] = 'registry.k8s.io/external-dns/external-dns:v0.22.0'
            container['imagePullPolicy'] = 'IfNotPresent'
            container['args'] = [
                '--source=service', '--service-type-filter=LoadBalancer', '--namespace=BAREPLANE_DNS_SOURCE_NAMESPACE',
                '--annotation-filter=bareplane.io/dns-managed=true', '--provider=cloudflare',
                '--zone-id-filter=BAREPLANE_DNS_ZONE_ID', '--domain-filter=BAREPLANE_DNS_DOMAIN',
                '--registry=txt', '--txt-owner-id=BAREPLANE_DNS_OWNER_ID', '--txt-prefix=bareplane-',
                '--policy=upsert-only', '--interval=1m', '--dry-run=BAREPLANE_DNS_DRY_RUN',
                '--cloudflare-dns-records-per-page=5000', '--batch-change-size=200', '--batch-change-interval=1s',
            ]
            container['env'] = [dict(name='CF_API_TOKEN', valueFrom=dict(secretKeyRef=dict(name='BAREPLANE_DNS_SECRET_NAME', key='BAREPLANE_DNS_SECRET_KEY')))]
            container['securityContext'] = dict(allowPrivilegeEscalation=False, readOnlyRootFilesystem=True, runAsNonRoot=True,
                                                runAsUser=65532, runAsGroup=65532, capabilities=dict(drop=['ALL']))
            container['resources'] = dict(requests=dict(cpu='100m', memory='128Mi'), limits=dict(memory='512Mi'))
            container['ports'] = [dict(name='http', containerPort=7979, protocol='TCP')]
            for kind, delay, threshold in [('livenessProbe', 10, 2), ('readinessProbe', 5, 6)]:
                container[kind] = dict(httpGet=dict(path='/healthz', port='http'), initialDelaySeconds=delay,
                                       periodSeconds=10, timeoutSeconds=5, failureThreshold=threshold)
        objects.append(obj)
    for obj in objects:
        annotations = obj['metadata'].setdefault('annotations', {})
        annotations.update({'bareplane.io/cluster': 'BAREPLANE_CLUSTER_NAME', 'bareplane.io/component': 'external-dns'})
        if obj['kind'] == 'Namespace':
            annotations['argocd.argoproj.io/sync-wave'] = '-30'
    return (f'# ExternalDNS v0.22.0, Apache-2.0; curated by hack/vendor_external_dns.py.\n# Source commit: {COMMIT}\n'
            + yaml.safe_dump_all(objects, sort_keys=False, width=120)).encode()


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('usage: vendor_external_dns.py /path/to/reviewed/source/directory')
    directory = Path(sys.argv[1])
    data = curate(directory)
    license_data = (directory / 'external-dns-v0.22.0-license.txt').read_bytes()
    if hashlib.sha256(license_data).hexdigest() != LICENSE_SHA256:
        raise ValueError('Unreviewed ExternalDNS license')
    target = Path(__file__).resolve().parents[1] / 'internal/render/gitops/assets/external-dns'
    target.mkdir(parents=True, exist_ok=True)
    (target / 'upstream.yaml').write_bytes(data)
    (target / 'license.txt').write_bytes(license_data)
    print(f'Vendored {len(data)} bytes; SHA256 {hashlib.sha256(data).hexdigest()}')
