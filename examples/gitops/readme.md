# Public GitOps acceptance fixture

This repository also hosts a generated, public **test fixture**, not an operator's live platform configuration. `examples/gitops-fixture.yaml` defines the fixed `lab` contract; `examples/gitops/root` contains its App-of-Apps source, `components/argocd` contains its reviewed minimal control plane, and `bootstrap/lab-root-application.yaml` is its separate root Application.

The files contain no runtime credentials. They are published here so disposable CI clusters can prove anonymous Git reachability and, in the separate handoff implementation, real Argo reconciliation. Bareplane's CLI still never commits or pushes user repositories. Do not apply this fixture to an existing cluster.

The fixture explicitly disables optional core defaults to keep M1/M2 regression coverage stable. Its generated payload still contains only the implemented Argo foundation; component-specific and full core-profile acceptance are separate tests.

`TestPublishedArgoFixtureMatchesRenderer` verifies every generated payload file against the actual renderer. Maintainers deliberately regenerate these documented fixture paths with:

```bash
BAREPLANE_UPDATE_GITOPS_FIXTURE=1 go test ./internal/render/gitops -run TestPublishedArgoFixtureMatchesRenderer
```

Regeneration is an explicit development operation; ordinary tests only compare bytes. Changes to this example must be reviewed and CI-verified like the embedded payload. Argo's Apache-2.0 license is included under `components/argocd/license.txt`.
