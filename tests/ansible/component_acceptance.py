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
        if component not in {'cert-manager', 'metrics-server', 'external-dns'}:
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

    def create(self):
        if self.api('get', 'namespace', self.namespace, '--ignore-not-found', '-o', 'json'):
            raise RuntimeError('Disposable component namespace unexpectedly exists')
        self.api('create', '-f', '-', '-o', 'json', data=self.app)

    def install(self):
        self.create()
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
                resources = [dict(kind=resource.get('kind'), name=resource.get('name'), namespace=resource.get('namespace'),
                                  sync=resource.get('status'), health=resource.get('health', {}).get('status'))
                             for resource in status.get('resources', [])[:100]]
                summary = dict(sync=status.get('sync', {}).get('status'), health=status.get('health', {}).get('status'),
                               phase=status.get('operationState', {}).get('phase'),
                               revisionMatches=status.get('sync', {}).get('revision') == self.revision,
                               sourceMatches=status.get('sync', {}).get('comparedTo', {}).get('source') == self.app['spec']['source'],
                               resources=resources)
                print('Disposable component status (no error bodies): ' + json.dumps(summary), flush=True)
                self.readiness_diagnostics()
                raise RuntimeError(self.namespace + ' Argo convergence timed out; condition types: ' + str(kinds))
            time.sleep(2)

    def readiness_diagnostics(self):
        # Only public fixture identities and reason codes. Never dump Secret
        # objects, pod specs, environment values, controller logs or messages.
        try:
            pods = self.api('get', 'pods', '-n', self.namespace, '-o', 'json').get('items', [])
            public = []
            for pod in pods[:20]:
                containers = []
                for container in pod.get('status', {}).get('containerStatuses', []):
                    state = container.get('state', {})
                    containers.append(dict(name=container.get('name'), ready=container.get('ready'), restarts=container.get('restartCount'),
                                           waiting=state.get('waiting', {}).get('reason'), terminated=state.get('terminated', {}).get('reason'),
                                           previousExit=container.get('lastState', {}).get('terminated', {}).get('exitCode')))
                public.append(dict(name=pod['metadata']['name'], phase=pod.get('status', {}).get('phase'), containers=containers,
                                   conditions=[{key: condition.get(key) for key in ['type', 'status', 'reason']}
                                               for condition in pod.get('status', {}).get('conditions', [])]))
            print('Disposable component pod readiness: ' + json.dumps(public), flush=True)
            for kind in (['issuers.cert-manager.io', 'certificates.cert-manager.io'] if self.namespace in {'cert-manager', 'metrics-server'} else []):
                items = self.api('get', kind, '-n', self.namespace, '-o', 'json').get('items', [])
                print('Disposable ' + kind + ': ' + json.dumps([dict(name=item['metadata']['name'], conditions=[
                    {key: condition.get(key) for key in ['type', 'status', 'reason']} for condition in item.get('status', {}).get('conditions', [])]) for item in items[:20]]), flush=True)
            if self.namespace == 'metrics-server':
                service = self.api('get', 'apiservice', 'v1beta1.metrics.k8s.io', '--ignore-not-found', '-o', 'json')
                print('Disposable aggregation readiness: ' + json.dumps(dict(caPresent=bool(service.get('spec', {}).get('caBundle')),
                    insecure=service.get('spec', {}).get('insecureSkipTLSVerify', False), conditions=[
                        {key: condition.get(key) for key in ['type', 'status', 'reason']} for condition in service.get('status', {}).get('conditions', [])])), flush=True)
                log = self.command('logs', 'deployment/metrics-server', '-n', self.namespace, '--tail=30').decode(errors='replace').lower()
                patterns = {'untrusted-ca': 'unknown authority', 'missing-ip-san': "doesn't contain any ip sans", 'permission-denied': 'permission denied',
                            'authentication-refused': 'unauthorized', 'authorization-refused': 'forbidden', 'scrape-failure': 'failed to scrape',
                            'empty-metrics-cache': 'no metrics', 'authentication-config': 'extension-apiserver-authentication',
                            'missing-file': 'no such file', 'invalid-flag': 'unknown flag'}
                print('Disposable Metrics Server diagnostic categories: ' + json.dumps([name for name, pattern in patterns.items() if pattern in log]), flush=True)
            if self.namespace == 'external-dns':
                deployment = self.api('get', 'deployment', 'external-dns', '-n', self.namespace, '-o', 'json')
                args = deployment['spec']['template']['spec']['containers'][0].get('args', [])
                print('Disposable DNS apply mode: ' + json.dumps(dict(dryRun='--dry-run' in args)), flush=True)
                log = self.command('logs', 'deployment/external-dns', '-n', self.namespace, '--tail=40').decode(errors='replace').lower()
                patterns = {'invalid-flag': 'unknown long flag', 'unexpected-argument': 'unexpected', 'permission-denied': 'permission denied',
                            'kubernetes-forbidden': 'forbidden', 'request-timeout': 'timeout', 'missing-credentials': 'credentials are not configured',
                            'cloudflare-refused': 'refused', 'missing-kubeconfig': 'kubeconfig', 'no-zone': 'no hosted zone',
                            'cache-not-synced': 'cache to sync', 'read-only-filesystem': 'read-only file system',
                            'no-changes': 'all records are already up to date', 'planned-changes': 'changing record',
                            'failed-batch': 'batch dns operation failed', 'failed-write': 'failed to submit',
                            'record-conflict': 'conflict', 'endpoint-generation': 'endpoints generated from service'}
                print('Disposable DNS diagnostic categories: ' + json.dumps([name for name, pattern in patterns.items() if pattern in log]), flush=True)
        except (RuntimeError, KeyError, TypeError, subprocess.SubprocessError):
            print('Disposable readiness diagnostics unavailable; no raw response was exposed.', flush=True)

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
