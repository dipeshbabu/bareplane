#!/usr/bin/env python3
"""Curate the checksum-pinned cert-manager v1.21.1 release; never fetch latest."""

import hashlib
from pathlib import Path
import sys

import yaml


SOURCE_SHA256 = '5f6a499b8c1857d57f560f536e0dcc830914b45c420899fe7ad0692c8624e408'
SOURCE_COMMIT = '24e33194fb39488eff2bbf10c6dc640f407cad44'


def curate(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise ValueError('Input is not the reviewed cert-manager v1.21.1 release')
    objects = []
    for obj in yaml.safe_load_all(data):
        if not obj:
            continue
        metadata = obj['metadata']
        metadata.setdefault('annotations', {}).update({'bareplane.io/cluster': 'BAREPLANE_CLUSTER_NAME', 'bareplane.io/component': 'cert-manager'})
        if obj['kind'] in {'Namespace', 'CustomResourceDefinition'}:
            metadata['annotations']['argocd.argoproj.io/sync-wave'] = '-30'
        if metadata.get('namespace') == 'kube-system':
            metadata['namespace'] = 'cert-manager'
        if obj['kind'] == 'Deployment':
            pod = obj['spec']['template']['spec']
            pod['tolerations'] = [{'key': 'node-role.kubernetes.io/control-plane', 'operator': 'Exists', 'effect': 'NoSchedule'}]
            for container in pod['containers']:
                container['args'] = [arg.replace('--leader-election-namespace=kube-system', '--leader-election-namespace=cert-manager') for arg in container.get('args', [])]
                container['resources'] = dict(requests=dict(cpu='100m', memory='64Mi'), limits=dict(memory='512Mi'))
        objects.append(obj)
    return (f'# cert-manager v1.21.1, Apache-2.0; curated by hack/vendor_cert_manager.py.\n# Source commit: {SOURCE_COMMIT}\n# Source SHA256: {SOURCE_SHA256}\n'
            + yaml.safe_dump_all(objects, sort_keys=False, width=120)).encode()


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('usage: vendor_cert_manager.py /path/to/cert-manager.yaml')
    target = Path(__file__).resolve().parents[1] / 'internal/render/gitops/assets/cert-manager/upstream.yaml'
    target.parent.mkdir(parents=True, exist_ok=True)
    result = curate(Path(sys.argv[1]).read_bytes())
    target.write_bytes(result)
    print(f'Vendored {len(result)} bytes; SHA256 {hashlib.sha256(result).hexdigest()}')
