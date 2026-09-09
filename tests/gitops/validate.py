#!/usr/bin/env python3
"""Offline render plus real Kustomize, Kubernetes and Argo schema validation.

This test never contacts a Kubernetes API or Git repository. Kubeconform fetches
only the reviewed, commit-pinned public JSON schemas. No credentials are used.
"""

import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import urllib.request

import jsonschema
from lupa.lua51 import LuaRuntime
import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'ansible'))
from cert_manager_acceptance import certificate_resources


SCHEMAS = "https://raw.githubusercontent.com/yannh/kubernetes-json-schema/07b64c5376535fbbd6fb9910621e1a41f7613c14/{{.NormalizedKubernetesVersion}}-standalone{{.StrictSuffix}}/{{.ResourceKind}}{{.KindSuffix}}.json"
CRD_OPENAPI = "https://raw.githubusercontent.com/kubernetes/kubernetes/v1.36.4/api/openapi-spec/v3/apis__apiextensions.k8s.io__v1_openapi.json"
CRD_OPENAPI_SHA256 = "1c7dd621bece6661867bcc29471f774a46b36c02567d59815c7c72f6d08aa512"
CONFIG = """apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: gitops-ci
spec:
  domain: ci.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
  nodes:
    - name: control
      role: control-plane
      count: 1
      cpu: 4
      memoryGB: 8
      diskGB: 64
  gitops:
    repoURL: https://github.com/example/gitops-ci.git
    revision: main
    rootPath: clusters/gitops-ci
  features:
    observability: false
    gpu: false
  profiles: [minimal]
  components:
    disabled: [metrics-server, cert-manager, observability, external-dns, storage, secrets-sops, vault]
  dns:
    provider: manual
  secrets:
    provider: sops
"""


def run(args, **kwargs):
    result = subprocess.run(args, check=True, capture_output=True, timeout=180, **kwargs)
    if len(result.stdout) > 8 * 1024 * 1024:
        raise RuntimeError("validator output exceeded test limit")
    return result.stdout


def strict_schema(value):
    if isinstance(value, dict):
        if value.get("type") == "object" and "properties" in value and "additionalProperties" not in value and not value.get("x-kubernetes-preserve-unknown-fields"):
            value["additionalProperties"] = False
        for child in value.values():
            strict_schema(child)
    elif isinstance(value, list):
        for child in value:
            strict_schema(child)
    return value


def validate_child_health(script):
    def assess(obj):
        lua = LuaRuntime(register_eval=False, register_builtins=False)
        # API JSON is a tree: remove Python fixture aliases before conversion.
        lua.globals().obj = lua.table_from(json.loads(json.dumps(obj)), recursive=True)
        return lua.execute(script)["status"]

    source = {"repoURL": "https://github.com/example/gitops.git", "targetRevision": "main", "path": "components/argocd"}
    healthy = {"spec": {"source": source}, "status": {"health": {"status": "Healthy"}, "sync": {"status": "Synced", "comparedTo": {"source": source}}}}
    assert assess({}) == "Progressing"
    assert assess(healthy) == "Healthy"
    for phase, expected in [("Running", "Progressing"), ("Terminating", "Progressing"), ("Failed", "Degraded"), ("Error", "Degraded"), ("Succeeded", "Healthy")]:
        obj = copy.deepcopy(healthy)
        obj["status"]["operationState"] = {"phase": phase}
        assert assess(obj) == expected
    for condition in ["ComparisonError", "InvalidSpecError", "SyncError"]:
        obj = copy.deepcopy(healthy)
        obj["status"]["conditions"] = [{"type": condition}]
        assert assess(obj) == "Degraded"
    obj = copy.deepcopy(healthy)
    obj["status"]["sync"]["status"] = "OutOfSync"
    assert assess(obj) == "Progressing"
    obj = copy.deepcopy(healthy)
    obj["status"]["health"]["status"] = "Degraded"
    assert assess(obj) == "Degraded"
    for field in ["repoURL", "targetRevision", "path"]:
        obj = copy.deepcopy(healthy)
        # Break the deepcopy-preserved source alias before changing desired state.
        obj["spec"]["source"] = dict(source, **{field: "changed"})
        assert assess(obj) == "Progressing"
    obj = copy.deepcopy(healthy)
    del obj["status"]["sync"]["comparedTo"]
    assert assess(obj) == "Progressing"


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: validate.py /path/to/bareplane /path/to/kubectl")
    bareplane, kubectl = (str(Path(arg).resolve()) for arg in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="bareplane-gitops-schema-") as temporary:
        root = Path(temporary)
        config = root / "bareplane.yaml"
        config.write_text(CONFIG, encoding="utf-8", newline="\n")
        run([bareplane, "gitops", "render", str(config)])
        export = root / "gitops"
        before = {p.relative_to(export).as_posix(): p.read_bytes() for p in export.rglob("*") if p.is_file()}
        run([bareplane, "gitops", "render", str(config)])
        assert before == {p.relative_to(export).as_posix(): p.read_bytes() for p in export.rglob("*") if p.is_file()}
        argo = run([kubectl, "kustomize", str(export / "components/argocd")])
        children = run([kubectl, "kustomize", str(export / "clusters/gitops-ci")])
        documents = list(yaml.safe_load_all(argo))
        applications = list(yaml.safe_load_all(children)) + [yaml.safe_load((export / "bootstrap/gitops-ci-root-application.yaml").read_bytes())]
        crd = next(d for d in documents if d["kind"] == "CustomResourceDefinition" and d["metadata"]["name"] == "applications.argoproj.io")
        schema = next(v["schema"]["openAPIV3Schema"] for v in crd["spec"]["versions"] if v["name"] == "v1alpha1")
        validator = jsonschema.Draft4Validator(strict_schema(copy.deepcopy(schema)))
        for app in applications:
            validator.validate(app)
            invalid = copy.deepcopy(app)
            invalid["spec"]["source"]["unexpectedField"] = "reject"
            assert not validator.is_valid(invalid), "Argo schema check must reject unknown fields"
        configmap = next(d for d in documents if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "argocd-cm")
        assert "resource.customizations.health.argoproj.io_Application" in configmap["data"]
        validate_child_health(configmap["data"]["resource.customizations.health.argoproj.io_Application"])
        assert configmap["data"]["application.resourceTrackingMethod"] == "annotation"
        # Exercise the opt-in component through the public CLI, including the
        # exact issuer/Certificate schemas used by disposable VM acceptance.
        component_root = root / 'cert-manager'
        component_root.mkdir()
        component_config = yaml.safe_load(CONFIG)
        component_config['spec']['components']['disabled'].remove('cert-manager')
        component_config['spec']['certificates'] = {'issuers': [
            {'name': 'lab-selfsigned', 'type': 'self-signed'},
            {'name': 'lab-ca', 'type': 'ca', 'secretName': 'operator-ca'},
        ]}
        component_path = component_root / 'bareplane.yaml'
        component_path.write_text(yaml.safe_dump(component_config), encoding='utf-8')
        run([bareplane, 'gitops', 'render', str(component_path)])
        component_export = component_root / 'gitops'
        component_docs = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(component_export / 'components/cert-manager')])))
        component_apps = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(component_export / 'clusters/gitops-ci')])))
        for app in component_apps:
            validator.validate(app)
        applications += component_apps
        custom_validators = {}
        for definition in component_docs:
            if definition['kind'] == 'CustomResourceDefinition':
                for version in definition['spec']['versions']:
                    key = (definition['spec']['group'] + '/' + version['name'], definition['spec']['names']['kind'])
                    custom_validators[key] = jsonschema.Draft4Validator(strict_schema(copy.deepcopy(version['schema']['openAPIV3Schema'])))
        custom_resources = [obj for obj in component_docs if obj['apiVersion'] == 'cert-manager.io/v1'] + certificate_resources()
        metrics_root = root / 'metrics-server'
        metrics_root.mkdir()
        metrics_config = copy.deepcopy(component_config)
        # A valid DNS label can be a YAML boolean. Exercise the public renderer
        # and real Kustomize/schema path with an ambiguous cluster identifier.
        metrics_config['metadata']['name'] = 'false'
        metrics_config['spec']['components']['disabled'].remove('metrics-server')
        metrics_config['spec']['components']['enabled'] = ['metrics-server']
        metrics_path = metrics_root / 'bareplane.yaml'
        metrics_path.write_text(yaml.safe_dump(metrics_config), encoding='utf-8')
        run([bareplane, 'gitops', 'render', str(metrics_path)])
        metrics_export = metrics_root / 'gitops'
        metrics_docs = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(metrics_export / 'components/metrics-server')])))
        metrics_apps = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(metrics_export / 'clusters/gitops-ci')])))
        for app in metrics_apps:
            validator.validate(app)
        applications += metrics_apps
        custom_resources += [obj for obj in metrics_docs if obj['apiVersion'] == 'cert-manager.io/v1']
        documents += [obj for obj in metrics_docs if obj['apiVersion'] != 'cert-manager.io/v1']
        dns_root = root / 'external-dns'
        dns_root.mkdir()
        dns_config = yaml.safe_load(CONFIG)
        dns_config['metadata']['name'] = 'false'
        dns_config['spec']['components']['disabled'].remove('external-dns')
        dns_config['spec']['dns'] = dict(provider='cloudflare', automation=dict(
            zoneID='a' * 32, zoneName='example.test', domain='apps.example.test', sourceNamespace='on', ownerID='b' * 32,
            tokenSecret=dict(name='123', key='false', revision='2026-01-01')))
        dns_path = dns_root / 'bareplane.yaml'
        dns_path.write_text(yaml.safe_dump(dns_config), encoding='utf-8')
        run([bareplane, 'gitops', 'render', str(dns_path)])
        dns_export = dns_root / 'gitops'
        documents += list(yaml.safe_load_all(run([kubectl, 'kustomize', str(dns_export / 'components/external-dns')])))
        dns_apps = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(dns_export / 'clusters/gitops-ci')])))
        for app in dns_apps:
            validator.validate(app)
        applications += dns_apps
        sops_root = root / 'sops'
        sops_root.mkdir()
        sops_config = yaml.safe_load(CONFIG)
        sops_config['metadata']['name'] = 'false'
        sops_config['spec']['components']['disabled'].remove('secrets-sops')
        sops_config['spec']['secrets']['sops'] = dict(ageKey=dict(name='bareplane-sops-age', key='false'),
            pgpKey=dict(name='bareplane-sops-pgp', key='private.asc'),
            pgpPassphrase=dict(name='bareplane-sops-pass', key='passphrase'), namespaces=['on', 'workloads'], revision='123')
        sops_path = sops_root / 'bareplane.yaml'
        sops_path.write_text(yaml.safe_dump(sops_config), encoding='utf-8')
        run([bareplane, 'gitops', 'render', str(sops_path)])
        sops_export = sops_root / 'gitops'
        sops_argo = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(sops_export / 'components/argocd')])))
        assert len(sops_argo) == 38, 'SOPS changed the cold Argo resource inventory'
        repo_server = next(obj for obj in sops_argo if obj['kind'] == 'Deployment' and obj['metadata']['name'] == 'argocd-repo-server')
        pod = repo_server['spec']['template']['spec']
        assert pod['automountServiceAccountToken'] is False
        assert len(pod['containers']) == 2
        sidecar = next(obj for obj in pod['containers'] if obj['name'] == 'bareplane-sops')
        assert sidecar['command'] == ['/var/run/argocd/argocd-cmp-server']
        assert next(mount for mount in sidecar['volumeMounts'] if mount['mountPath'] == '/tmp')['name'] == 'sops-tmp'
        documents += sops_argo
        documents += list(yaml.safe_load_all(run([kubectl, 'kustomize', str(sops_export / 'components/secrets-sops')])))
        sops_apps = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(sops_export / 'clusters/gitops-ci')])))
        for app in sops_apps:
            validator.validate(app)
        applications += sops_apps
        observability_root = root / 'observability'
        observability_root.mkdir()
        observability_config = yaml.safe_load((Path(__file__).resolve().parents[2] / 'examples/observability-fixture.yaml').read_bytes())
        observability_config['metadata']['name'] = 'false'
        observability_config['spec']['observability']['retentionHours'] = 48
        observability_path = observability_root / 'bareplane.yaml'
        observability_path.write_text(yaml.safe_dump(observability_config), encoding='utf-8')
        run([bareplane, 'gitops', 'render', str(observability_path)])
        observability_export = observability_root / 'gitops'
        observability_docs = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(observability_export / 'components/observability')])))
        documents += observability_docs
        observability_apps = list(yaml.safe_load_all(run([kubectl, 'kustomize', str(observability_export / observability_config['spec']['gitops']['rootPath'])])))
        for app in observability_apps:
            validator.validate(app)
        applications += observability_apps
        prometheus_cm = next(obj for obj in observability_docs if obj['kind'] == 'ConfigMap')
        prometheus_config = yaml.safe_load(prometheus_cm['data']['prometheus.yml'])
        assert prometheus_config['storage']['tsdb']['retention'] == dict(time='48h', size='1GB')
        assert len(prometheus_config['scrape_configs']) == 2 and 'remote_write' not in prometheus_config
        for obj in custom_resources:
            custom = custom_validators[obj['apiVersion'], obj['kind']]
            custom.validate(obj)
            invalid = copy.deepcopy(obj)
            invalid['spec']['unexpectedField'] = 'reject'
            assert not custom.is_valid(invalid), 'Certificate schema must reject unknown fields'
        documents += [obj for obj in component_docs if obj['apiVersion'] != 'cert-manager.io/v1']
        for obj in documents:
            if obj["kind"] in {"Deployment", "StatefulSet"}:
                assert obj["spec"]["template"]["spec"]["tolerations"] == [{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}]
        # The standard standalone registry omits recursive CRD schemas. Validate
        # these against Kubernetes' own checksum-pinned API extension OpenAPI;
        # do not silently skip them or use ignore-missing-schemas.
        with urllib.request.urlopen(CRD_OPENAPI, timeout=30) as response:
            raw = response.read(1 << 20)
        assert hashlib.sha256(raw).hexdigest() == CRD_OPENAPI_SHA256
        crd_schema = {"$ref": "#/components/schemas/io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinition", "components": json.loads(raw)["components"]}
        crd_validator = jsonschema.Draft4Validator(strict_schema(crd_schema))
        definitions = [d for d in documents if d["kind"] == "CustomResourceDefinition"]
        for definition in definitions:
            crd_validator.validate(definition)
            invalid = copy.deepcopy(definition)
            invalid["spec"]["scope"] = 123
            assert not crd_validator.is_valid(invalid)
        validated = root / "kubernetes.yaml"
        validated.write_text(yaml.safe_dump_all([d for d in documents if d["kind"] != "CustomResourceDefinition"]), encoding="utf-8", newline="\n")
        output = run(["go", "run", "github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0", "-strict", "-summary", "-output", "json", "-kubernetes-version", "1.36.0", "-schema-location", SCHEMAS, str(validated)])
        summary = json.loads(output)["summary"]
        assert summary["invalid"] == 0 and summary["errors"] == 0 and summary["skipped"] == 0
        assert summary["valid"] + len(definitions) == len(documents)
        run([sys.executable, '-m', 'unittest', 'discover', '-s', 'tests/ansible', '-p', 'test_handoff_state.py'],
            env=dict(os.environ, BAREPLANE_TEST_KUBECTL=kubectl))
        print(f"Validated {len(documents)} Kubernetes resources, {len(applications)} strict Argo Applications, and {len(custom_resources)} certificate/issuer resources; deterministic export, root commit pinning, and Kustomize patches passed")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as exc:
        # These commands inspect generated public fixtures only, never live state.
        print(exc.stdout.decode(errors="replace")[-12000:], file=sys.stderr)
        print(exc.stderr.decode(errors="replace")[-12000:], file=sys.stderr)
        raise SystemExit(exc.returncode)
