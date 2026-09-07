#!/usr/bin/env python3
"""Mechanically curate the reviewed upstream Argo manifest; never fetch latest.

Usage: python hack/vendor_argocd.py /path/to/downloaded/install.yaml
Requires PyYAML 6.0.3. Output is deterministic and checked into the renderer.
"""

import hashlib
from pathlib import Path
import sys

import yaml


SOURCE_COMMIT = "e258ee23c3e52266d407572f4bcdfe7d9ed36cb5"
SOURCE_SHA256 = "9a87f2b3e14c278f12501eb0ef5c3955b27cf05370ca425381c6a908cf85a5c5"
OMITTED = {"applicationset-controller", "dex-server", "notifications-controller"}
CLUSTER_SCOPED = {"CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding", "Namespace"}


def curate(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise ValueError("input is not the reviewed Argo CD v3.5.2 manifest")
    output = []
    for obj in yaml.safe_load_all(data):
        labels = obj["metadata"].get("labels", {})
        if labels.get("app.kubernetes.io/component") in OMITTED:
            continue
        metadata = obj["metadata"]
        if obj["kind"] not in CLUSTER_SCOPED:
            metadata["namespace"] = "argocd"
        metadata.setdefault("annotations", {}).update({
            "bareplane.io/cluster": "BAREPLANE_CLUSTER_NAME",
            "bareplane.io/component": "argocd",
        })
        if obj["kind"] in {"RoleBinding", "ClusterRoleBinding"}:
            for subject in obj.get("subjects", []):
                if subject["kind"] == "ServiceAccount":
                    subject["namespace"] = "argocd"
        if obj["kind"] == "Secret" and (obj.get("data") or obj.get("stringData")):
            raise ValueError("upstream must not supply credential values")
        output.append(obj)
    return ("# Argo CD v3.5.2, Apache-2.0; curated by hack/vendor_argocd.py.\n"
            f"# Source commit: {SOURCE_COMMIT}\n"
            f"# Source SHA256: {SOURCE_SHA256}\n"
            + yaml.safe_dump_all(output, sort_keys=False, width=120)).encode()


def main():
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    target = Path(__file__).resolve().parents[1] / "internal/render/gitops/assets/argocd/upstream.yaml"
    rendered = curate(Path(sys.argv[1]).read_bytes())
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(rendered)
    print(f"Vendored {len(rendered)} bytes; SHA256 {hashlib.sha256(rendered).hexdigest()}")


if __name__ == "__main__":
    main()
