# Merged-branch cleanup

The cleanup workflow runs only code checked out from the trusted default branch, including for `pull_request_target` events. Checkout does not persist credentials. The helper uses a fresh empty bare Git repository and injects the short-lived token into Git's process environment, never command arguments or repository configuration.

Candidates must be same-repository pull requests merged into the current default branch, with a current branch head equal to the recorded merged head. Default/conventional main branches, protected branches, active pull-request branches, forks, unmerged refs, and advanced heads are preserved. Metadata inspection must succeed before any deletion.

Deletion uses an explicit [expected-head Git lease](https://git-scm.com/docs/git-push), protecting the ref atomically if it advances after inspection. Overlapping workflow runs can safely race: a rejected deletion is considered complete only when a fresh remote read proves the ref disappeared or advanced. Authentication, network, or repository-rule failures are not silently ignored when the expected ref still exists. Each result is logged immediately, without credentials.

Tests exercise actual deletion and concurrent advance against disposable local bare repositories, plus fake GitHub metadata, protected/open/default branch filtering, and credential isolation:

```bash
python -m unittest discover -s tests/github -p 'test_*.py'
```

Do not run the production helper locally by forging a GitHub Actions environment. Live cleanup belongs to the repository's post-merge workflow.
