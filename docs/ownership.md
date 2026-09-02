# Infrastructure ownership

Bareplane requires explicit metadata before it considers an existing machine resource safe to manage.

## Ownership tags

A resource belongs to a Bareplane cluster only when both of these exact tags are present:

```text
bareplane
bareplane-cluster-<cluster-name>
```

For a cluster named `lab`, the ownership tags are:

```text
bareplane
bareplane-cluster-lab
```

The manager tag alone is not sufficient. A cluster tag alone is not sufficient. Similar names and different capitalization do not match.

## Safety rule

A VM named `lab-workers-1` is **not** owned by Bareplane unless the explicit ownership tags are present. This prevents a naming collision with existing infrastructure from becoming an accidental adoption or future destructive action.

Manually applying both ownership tags is an explicit opt-in to Bareplane management for that cluster. Do not add them to infrastructure that Bareplane should only observe.

The ownership package is provider-neutral. Providers preserve their observed tags as generic inventory metadata, while planning and lifecycle code applies this ownership contract before proposing managed changes.
