#!/usr/bin/env python3
"""Curate pinned ESO into a single-namespace, pull-only Vault integration."""

import hashlib
from pathlib import Path
import subprocess
import sys
import tempfile

import yaml


VERSION = '2.10.0'
COMMIT = '8488600898e856d74a7e0f53ed5e3cc79d89f4e8'
CHART_SHA = 'b96e948fff3674638b5d3f9e43886f3796e04739c4b4127929aed2ddac7d1418'
LICENSE_SHA = '58d1e17ffe5109a7ae296caafcadfdbe6a7d176f0bc4ab01e12a689b0499d8bd'
HELM_SHA = 'cd27ec335b9c961a0a098cce870fded88429210edc898fd213da0b16e67333bd'
WORKLOAD = 'BAREPLANE_VAULT_WORKLOAD_NAMESPACE'
NAMESPACE = 'vault-secrets'
NAME = 'bareplane-vault'


def verified(path, digest):
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != digest:
        raise ValueError('Vault integration input differs from its reviewed checksum')
    return data


def scoped_role(name, namespace, rules):
    return [dict(apiVersion='rbac.authorization.k8s.io/v1', kind='Role', metadata=dict(name=name, namespace=namespace), rules=rules),
            dict(apiVersion='rbac.authorization.k8s.io/v1', kind='RoleBinding', metadata=dict(name=name, namespace=namespace),
                 roleRef=dict(apiGroup='rbac.authorization.k8s.io', kind='Role', name=name),
                 subjects=[dict(kind='ServiceAccount', name=NAME, namespace=NAMESPACE)])]


def curate(directory, helm):
    chart = directory / 'external-secrets-2.10.0.tgz'
    verified(chart, CHART_SHA)
    license_data = verified(directory / 'external-secrets-2.10.0-LICENSE', LICENSE_SHA)
    verified(helm, HELM_SHA)
    values = dict(fullnameOverride=NAME, scopedNamespace=WORKLOAD, scopedRBAC=True,
                  controllerClass=NAME, processClusterStore=False, processClusterExternalSecret=False,
                  processClusterPushSecret=False, processClusterGenerator=False, processPushSecret=False,
                  webhook=dict(create=False), certController=dict(create=False),
                  crds=dict(createClusterExternalSecret=False, createClusterSecretStore=False,
                            createClusterGenerator=False, createPushSecret=False, createClusterPushSecret=False))
    with tempfile.TemporaryDirectory(prefix='bareplane-vendor-vault-') as temporary:
        path = Path(temporary) / 'values.yaml'
        path.write_text(yaml.safe_dump(values))
        rendered = subprocess.run([str(helm), 'template', NAME, str(chart), '--namespace', NAMESPACE,
                                   '--kube-version', '1.36.4', '--include-crds', '--values', str(path)],
                                  check=True, capture_output=True, timeout=60).stdout
    if len(rendered) > 8 * 1024 * 1024:
        raise ValueError('Unbounded Vault integration chart output')
    resources = list(yaml.safe_load_all(rendered))
    keep = {'externalsecrets.external-secrets.io', 'secretstores.external-secrets.io', 'generatorstates.generators.external-secrets.io'}
    crds = []
    for obj in resources:
        if obj['kind'] != 'CustomResourceDefinition' or obj['metadata']['name'] not in keep:
            continue
        # Fresh installations serve only the operator's native version. No
        # conversion webhook/cert controller or unrelated generator CRDs.
        native = 'v1alpha1' if obj['metadata']['name'].startswith('generatorstates.') else 'v1'
        versions = [version for version in obj['spec']['versions'] if version['name'] == native]
        if len(versions) != 1 or obj['spec']['scope'] != 'Namespaced':
            raise ValueError('Unexpected native ESO schema')
        versions[0].update(served=True, storage=True)
        obj['spec']['versions'] = versions
        obj['spec']['conversion'] = dict(strategy='None')
        crds.append(obj)
    if {obj['metadata']['name'] for obj in crds} != keep:
        raise ValueError('Missing reviewed ESO schema')
    deployment = next(obj for obj in resources if obj['kind'] == 'Deployment' and obj['metadata']['name'] == NAME)
    account = next(obj for obj in resources if obj['kind'] == 'ServiceAccount' and obj['metadata']['name'] == NAME)
    deployment['spec']['replicas'] = 1
    deployment['spec']['strategy'] = dict(type='Recreate')
    pod = deployment['spec']['template']['spec']
    pod['securityContext'] = dict(runAsNonRoot=True, runAsUser=65532, runAsGroup=65532, seccompProfile=dict(type='RuntimeDefault'))
    pod['tolerations'] = [dict(key='node-role.kubernetes.io/control-plane', operator='Exists', effect='NoSchedule')]
    pod['nodeSelector'] = {'kubernetes.io/os': 'linux'}
    container = pod['containers'][0]
    container['image'] = 'ghcr.io/external-secrets/external-secrets:v' + VERSION
    container['securityContext'] = dict(runAsNonRoot=True, readOnlyRootFilesystem=True, allowPrivilegeEscalation=False,
                                        capabilities=dict(drop=['ALL']))
    container['resources'] = dict(requests=dict(cpu='100m', memory='128Mi'), limits=dict(memory='512Mi'))
    container['args'] = [
        '--namespace=' + WORKLOAD, '--controller-class=BAREPLANE_VAULT_CONTROLLER_CLASS', '--enable-leader-election=true', '--leader-election-id=' + NAME,
        '--enable-cluster-store-reconciler=false', '--enable-cluster-external-secret-reconciler=false',
        '--enable-cluster-push-secret-reconciler=false', '--enable-push-secret-reconciler=false',
        '--enable-secret-store-reconciler=true', '--enable-generator-state=false', '--unsafe-allow-generic-targets=false',
        '--enable-secrets-caching=false', '--enable-managed-secrets-caching=false', '--enable-configmaps-caching=false',
        '--enable-vault-token-cache=false', '--store-requeue-interval=30s', '--concurrent=1', '--client-qps=10', '--client-burst=20',
        '--metrics-addr=127.0.0.1:8080', '--live-addr=:8082', '--loglevel=info', '--zap-time-encoding=iso8601',
    ]
    container['ports'] = [dict(name='live', containerPort=8082, protocol='TCP')]
    for kind, path in [('livenessProbe', '/healthz'), ('readinessProbe', '/readyz')]:
        container[kind] = dict(httpGet=dict(path=path, port='live'), initialDelaySeconds=5, periodSeconds=10, timeoutSeconds=5, failureThreshold=6)
    objects = [dict(apiVersion='v1', kind='Namespace', metadata=dict(name=NAMESPACE)), account,
               dict(apiVersion='v1', kind='ServiceAccount', metadata=dict(name='bareplane-vault-reader', namespace=WORKLOAD), automountServiceAccountToken=False)]
    objects += scoped_role(NAME + '-leader', NAMESPACE, [
        dict(apiGroups=['coordination.k8s.io'], resources=['leases'], verbs=['create']),
        dict(apiGroups=['coordination.k8s.io'], resources=['leases'], resourceNames=[NAME], verbs=['get', 'update', 'patch']),
    ])
    objects += scoped_role(NAME + '-controller', WORKLOAD, [
        dict(apiGroups=['external-secrets.io'], resources=['externalsecrets', 'secretstores'], verbs=['get', 'list', 'watch', 'update', 'patch']),
        dict(apiGroups=['external-secrets.io'], resources=['externalsecrets/status', 'secretstores/status'], verbs=['get', 'update', 'patch']),
        # ESO 2.10 starts this informer even with generation disabled. No state
        # creation/deletion privileges or provider generator CRDs are granted.
        dict(apiGroups=['generators.external-secrets.io'], resources=['generatorstates'], verbs=['get', 'list', 'watch']),
        dict(apiGroups=[''], resources=['secrets'], verbs=['get', 'list', 'watch', 'create', 'update', 'patch']),
        # The legacy-token fallback uses a metadata informer; grant only public
        # ServiceAccount reads here, while TokenRequest stays name-constrained.
        dict(apiGroups=[''], resources=['serviceaccounts'], verbs=['get', 'list', 'watch']),
        dict(apiGroups=[''], resources=['serviceaccounts/token'], resourceNames=['bareplane-vault-reader'], verbs=['create']),
        dict(apiGroups=[''], resources=['events'], verbs=['create', 'patch']),
    ])
    objects.append(deployment)
    for obj in crds + objects:
        metadata = obj['metadata']
        annotations = metadata.setdefault('annotations', {})
        annotations.update({'bareplane.io/cluster': 'BAREPLANE_CLUSTER_NAME', 'bareplane.io/component': 'vault'})
        if metadata.get('namespace') == WORKLOAD:
            annotations['bareplane.io/existing-namespace'] = WORKLOAD
        annotations['argocd.argoproj.io/sync-wave'] = '-30' if obj['kind'] == 'Namespace' else '-20' if obj['kind'] == 'CustomResourceDefinition' else '-10'
    header = f'# External Secrets Operator {VERSION}, Apache-2.0; curated by hack/vendor_vault.py.\n# Source commit: {COMMIT}\n'
    return {name: (header + yaml.safe_dump_all(values, sort_keys=False, width=120)).encode()
            for name, values in [('crds.yaml', crds), ('controller.yaml', objects)]} | {'license.txt': license_data}


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: vendor_vault.py reviewed-source-directory pinned-linux-amd64-helm')
    files = curate(Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve())
    target = Path(__file__).resolve().parents[1] / 'internal/render/gitops/assets/vault'
    target.mkdir(parents=True, exist_ok=True)
    for name, data in files.items():
        (target / name).write_bytes(data)
        print(f'{name}: {len(data)} bytes, SHA256 {hashlib.sha256(data).hexdigest()}')
