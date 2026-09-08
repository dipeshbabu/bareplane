"""Bounded API and exact-source Argo checks for disposable component VMs."""

import json
import os
import re
import subprocess
import time

import yaml


class ComponentAcceptance:
    def __init__(self, kubectl, repository, component):
        if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1':
            raise RuntimeError('Component acceptance is restricted to disposable GitHub runners')
        if component not in {'cert-manager', 'metrics-server'}:
            raise RuntimeError('Unknown disposable component fixture')
        self.kubectl, self.namespace, self.name = kubectl, component, 'lab-' + component
        self.revision = os.environ.get('BAREPLANE_TEST_GIT_REF', '')
        source_repo = os.environ.get('BAREPLANE_TEST_GIT_REPO', '')
        if not re.fullmatch('[0-9a-f]{40}', self.revision) or not re.fullmatch(r'https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git', source_repo):
            raise RuntimeError('Component acceptance requires an exact public source revision')
        path = repository / ('examples/gitops/' + component + '-root/applications/' + component + '.yaml')
        self.app = yaml.safe_load(path.read_bytes())
        self.app['spec']['source'].update(repoURL=source_repo, targetRevision=self.revision)

    def command(self, *args, data=None, timeout=60):
        result = subprocess.run(self.kubectl + list(args), input=json.dumps(data).encode() if data is not None else b'',
                                capture_output=True, timeout=timeout, check=False)
        if result.returncode or len(result.stdout) > 2 * 1024 * 1024:
            raise RuntimeError('Disposable component API operation failed; no response bodies are published')
        return result.stdout

    def api(self, *args, **kwargs):
        data = self.command(*args, **kwargs)
        return json.loads(data) if data.strip() else {}

    def install(self):
        if self.api('get', 'namespace', self.namespace, '--ignore-not-found', '-o', 'json'):
            raise RuntimeError('Disposable component namespace unexpectedly exists')
        self.api('create', '-f', '-', '-o', 'json', data=self.app)
        self.wait_application()

    def wait_application(self, previous_reconciliation=None):
        deadline = time.monotonic() + 600
        while True:
            current = self.api('get', 'application', self.name, '-n', 'argocd', '-o', 'json')
            status = current.get('status', {})
            if (status.get('sync', {}).get('status') == 'Synced' and status['sync'].get('revision') == self.revision
                    and status['sync'].get('comparedTo', {}).get('source') == self.app['spec']['source']
                    and status.get('health', {}).get('status') == 'Healthy' and not current.get('operation')
                    and not status.get('conditions') and status.get('reconciledAt') != previous_reconciliation
                    and not current['metadata'].get('annotations', {}).get('argocd.argoproj.io/refresh')
                    and status.get('operationState', {}).get('phase') not in {'Running', 'Failed', 'Error', 'Terminating'}):
                return current
            if time.monotonic() > deadline:
                kinds = [condition.get('type') for condition in status.get('conditions', [])]
                raise RuntimeError(self.namespace + ' Argo convergence timed out; condition types: ' + str(kinds))
            time.sleep(2)

    def refresh(self):
        previous = self.api('get', 'application', self.name, '-n', 'argocd', '-o', 'json')['status']['reconciledAt']
        time.sleep(1.1)  # Argo timestamps have second resolution.
        self.command('annotate', 'application', self.name, '-n', 'argocd', 'argocd.argoproj.io/refresh=hard', '--overwrite')
        self.wait_application(previous)

    def deployment_identity(self, name):
        deployment = self.api('get', 'deployment', name, '-n', self.namespace, '-o', 'json')
        if not deployment['metadata'].get('annotations', {}).get('argocd.argoproj.io/tracking-id', '').startswith(self.name + ':'):
            raise RuntimeError('Component workload is not owned by its Argo Application')
        return deployment['metadata']['uid'], deployment['metadata']['generation']
