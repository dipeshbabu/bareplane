"""Real Argo CMP acceptance; only encrypted data is served by local Git."""

import base64
import copy
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time

from component_acceptance import ComponentAcceptance


class EncryptedFixture:
    def __init__(self, directory):
        if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1':
            raise RuntimeError('SOPS fixture serving is restricted to disposable GitHub VMs')
        self.root = directory / 'sops-private'
        self.root.mkdir(mode=0o700)
        self.repo = self.root / 'repository'
        self.repo.mkdir()
        self.env = {'PATH': os.environ['PATH'], 'HOME': str(self.root), 'GNUPGHOME': str(self.root / 'gnupg'),
                    'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_CONFIG_SYSTEM': '/dev/null', 'GIT_TERMINAL_PROMPT': '0'}
        (self.root / 'gnupg').mkdir(mode=0o700)
        self.secret = 'disposable-sops-value-' + secrets.token_hex(24)
        self.passphrase = 'disposable-sops-passphrase-' + secrets.token_hex(24)
        self.sensitive = [self.secret.encode(), self.passphrase.encode(), b'AGE-SECRET-KEY-', b'BEGIN PGP PRIVATE KEY BLOCK']
        self.daemon = None
        self.command(['git', 'init', '-q'])
        self.command(['git', 'config', 'user.name', 'Disposable SOPS acceptance'])
        self.command(['git', 'config', 'user.email', 'sops@example.invalid'])
        self.command(['git', 'config', 'uploadpack.allowReachableSHA1InWant', 'true'])
        self.command(['git', 'config', 'daemon.receivepack', 'false'])
        self.recipient = self.age_key('age')
        (self.root / 'passphrase').write_text(self.passphrase)
        self.command(['gpg', '--no-options', '--batch', '--pinentry-mode', 'loopback', '--passphrase-file', str(self.root / 'passphrase'),
                      '--quick-generate-key', 'Disposable SOPS <sops@example.invalid>', 'rsa2048', 'encr', '1d'])
        listing = self.command(['gpg', '--no-options', '--batch', '--with-colons', '--list-secret-keys']).decode()
        self.fingerprint = next(line.split(':')[9] for line in listing.splitlines() if line.startswith('fpr:'))
        pgp = self.command(['gpg', '--no-options', '--batch', '--pinentry-mode', 'loopback', '--passphrase-file', str(self.root / 'passphrase'),
                            '--armor', '--export-secret-keys', self.fingerprint])
        (self.root / 'pgp').write_bytes(pgp)
        self.encrypt('age', ['--age', self.recipient])
        self.encrypt('pgp', ['--pgp', self.fingerprint])
        self.publish()

    def command(self, args, data=None):
        result = subprocess.run(args, input=data, capture_output=True, env=self.env, cwd=self.repo, timeout=90)
        if result.returncode or len(result.stdout) > 2 * 1024 * 1024:
            raise RuntimeError('Private encrypted-fixture command failed; output suppressed')
        return result.stdout

    def age_key(self, name):
        key = self.root / name
        self.command(['age-keygen', '-o', str(key)])
        return self.command(['age-keygen', '-y', str(key)]).decode().strip()

    def encrypt(self, name, recipients):
        payload = dict(apiVersion='v1', kind='Secret', metadata=dict(name='sample-' + name, namespace='sops-workloads'),
                       type='Opaque', stringData=dict(password=self.secret))
        data = self.command(['sops', 'encrypt', '--input-type', 'json', '--output-type', 'json', '--filename-override', 'secret.sops.json',
                             '--encrypted-regex', '^(data|stringData)$', *recipients], json.dumps(payload).encode())
        self.assert_public(data)
        target = self.repo / name
        target.mkdir(exist_ok=True)
        (target / 'secret.sops.json').write_bytes(data)

    def assert_public(self, data):
        for value in self.sensitive:
            if value in data or base64.b64encode(value) in data:
                raise RuntimeError('SOPS acceptance detected plaintext in a public artifact or log')

    def publish(self):
        self.command(['git', 'add', 'age/secret.sops.json', 'pgp/secret.sops.json'])
        self.command(['git', 'commit', '-qm', 'Update encrypted disposable fixture'])
        self.assert_public(self.command(['git', 'cat-file', '--batch-all-objects', '--batch']))
        self.revision = self.command(['git', 'rev-parse', 'HEAD']).decode().strip()
        return self.revision

    def serve(self):
        with socket.socket() as listener:
            listener.bind(('192.0.2.1', 0))
            port = listener.getsockname()[1]
        self.daemon = subprocess.Popen(['git', 'daemon', '--reuseaddr', '--export-all', '--max-connections=4',
            '--timeout=30', '--init-timeout=5', '--listen=192.0.2.1', '--port=' + str(port), '--base-path=' + str(self.root), str(self.repo / '.git')],
            env=self.env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.url = 'git://192.0.2.1:' + str(port) + '/repository/.git'
        deadline = time.monotonic() + 10
        while True:
            try:
                self.command(['git', 'ls-remote', self.url, 'HEAD'])
                return
            except RuntimeError:
                if self.daemon.poll() is not None or time.monotonic() >= deadline:
                    raise RuntimeError('Disposable read-only encrypted Git server did not start') from None
                time.sleep(0.2)

    def close(self):
        if self.daemon is not None:
            self.daemon.terminate()
            try:
                self.daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.daemon.kill()
                self.daemon.wait()
        self.command(['gpgconf', '--kill', 'all'])


def run_sops_acceptance(kubectl, repository, work):
    component = ComponentAcceptance(kubectl, repository, 'secrets-sops')
    fixture = EncryptedFixture(work)
    try:
        fixture.serve()
        exercise_sops(component, fixture)
    finally:
        fixture.close()


def exercise_sops(component, fixture):
    command, api = component.command, component.api
    component.install()
    policy = api('get', 'validatingadmissionpolicy', 'bareplane-sops-secret-ownership', '-o', 'json')
    if policy.get('status', {}).get('typeChecking', {}).get('expressionWarnings'):
        raise RuntimeError('SOPS ownership policy contains CEL type errors')
    command('create', '-f', '-', data=dict(apiVersion='v1', kind='Namespace', metadata=dict(name='sops-workloads')))
    original_deployment = api('get', 'deployment', 'argocd-repo-server', '-n', 'argocd', '-o', 'json')
    argo = api('get', 'application', 'lab-argocd', '-n', 'argocd', '-o', 'json')
    original_app_uid = argo['metadata']['uid']
    base_source = dict(repoURL=component.app['spec']['source']['repoURL'], targetRevision=component.revision,
                       path='examples/gitops/sops-argocd')

    def diagnostics():
        # Public identity/reason fields only; never publish logs or Pod specs.
        try:
            pods = api('get', 'pods', '-n', 'argocd', '-o', 'json').get('items', [])
            print('Disposable SOPS pod readiness: ' + json.dumps([dict(name=pod['metadata']['name'],
                phase=pod.get('status', {}).get('phase'), containers=[dict(name=container.get('name'),
                ready=container.get('ready'), restarts=container.get('restartCount'),
                waiting=container.get('state', {}).get('waiting', {}).get('reason'),
                previousExit=container.get('lastState', {}).get('terminated', {}).get('exitCode'))
                for container in pod.get('status', {}).get('containerStatuses', [])]) for pod in pods[:20]]), flush=True)
        except (RuntimeError, KeyError, TypeError, subprocess.SubprocessError):
            print('Disposable SOPS readiness diagnostics unavailable.', flush=True)

    def wait_application(name, source, healthy=True):
        deadline = time.monotonic() + 600
        while True:
            obj = api('get', 'application', name, '-n', 'argocd', '-o', 'json')
            status = obj.get('status', {})
            compared = status.get('sync', {}).get('comparedTo', {}).get('source') == source
            conditions = {entry.get('type') for entry in status.get('conditions', [])}
            phase = status.get('operationState', {}).get('phase')
            refreshing = obj['metadata'].get('annotations', {}).get('argocd.argoproj.io/refresh')
            if compared and not refreshing:
                if healthy and status.get('sync', {}).get('status') == 'Synced' and status.get('sync', {}).get('revision') == source['targetRevision'] \
                        and status.get('health', {}).get('status') == 'Healthy' and not conditions and not obj.get('operation') \
                        and phase not in {'Running', 'Failed', 'Error', 'Terminating'}:
                    return obj
                if not healthy and ('ComparisonError' in conditions or phase == 'Failed') and not obj.get('operation'):
                    return obj
            if time.monotonic() >= deadline:
                print('Disposable SOPS status: ' + json.dumps(dict(name=name, sync=status.get('sync', {}).get('status'),
                    health=status.get('health', {}).get('status'), conditions=sorted(conditions), phase=phase, compared=compared)), flush=True)
                diagnostics()
                raise RuntimeError('SOPS Argo acceptance did not reach its expected state')
            time.sleep(2)

    def assert_logs_public():
        for resource, container in [('deployment/argocd-repo-server', 'argocd-repo-server'),
                                     ('deployment/argocd-repo-server', 'bareplane-sops'),
                                     ('statefulset/argocd-application-controller', 'argocd-application-controller')]:
            data = command('logs', resource, '-n', 'argocd', '-c', container, '--tail=-1', '--limit-bytes=2097152')
            if len(data) >= 2097152:
                raise RuntimeError('SOPS log audit exceeded its completeness bound')
            fixture.assert_public(data)

    def configure_argo(revision):
        current = api('get', 'application', 'lab-argocd', '-n', 'argocd', '-o', 'json')
        if current['metadata']['uid'] != original_app_uid:
            raise RuntimeError('SOPS enablement replaced the Argo owner')
        source = copy.deepcopy(base_source)
        source['kustomize'] = dict(patches=[dict(target=dict(group='apps', version='v1', kind='Deployment', name='argocd-repo-server'),
            patch=json.dumps(dict(apiVersion='apps/v1', kind='Deployment', metadata=dict(name='argocd-repo-server'),
                spec=dict(template=dict(metadata=dict(annotations={'bareplane.io/sops-keys-revision': revision}))))))])
        current['spec']['source'] = source
        api('replace', '-f', '-', '-o', 'json', data=current)
        wait_application('lab-argocd', source)
        observed = api('get', 'deployment', 'argocd-repo-server', '-n', 'argocd', '-o', 'json')
        if observed['metadata']['uid'] != original_deployment['metadata']['uid']:
            raise RuntimeError('SOPS enablement replaced the repo-server Deployment')
        if not observed['metadata'].get('annotations', {}).get('argocd.argoproj.io/tracking-id', '').startswith('lab-argocd:'):
            raise RuntimeError('SOPS introduced competing repo-server ownership')

    def secret_source(name):
        return dict(repoURL=fixture.url, targetRevision=fixture.revision, path=name, plugin=dict(name='bareplane-sops-v1'))

    def application(name):
        return dict(apiVersion='argoproj.io/v1alpha1', kind='Application', metadata=dict(name='sops-' + name, namespace='argocd'), spec=dict(
            project='default', source=secret_source(name), destination=dict(server='https://kubernetes.default.svc', namespace='sops-workloads'),
            syncPolicy=dict(automated=dict(prune=False, selfHeal=False, allowEmpty=False),
                syncOptions=['ServerSideApply=true', 'FailOnSharedResource=true', 'DisableClientSideApplyMigration=true'], retry=dict(limit=1))))

    def reconcile(name, healthy=True):
        obj = api('get', 'application', 'sops-' + name, '-n', 'argocd', '-o', 'json')
        obj['spec']['source'] = secret_source(name)
        obj['metadata'].setdefault('annotations', {})['argocd.argoproj.io/refresh'] = 'hard'
        api('replace', '-f', '-', '-o', 'json', data=obj)
        return wait_application('sops-' + name, obj['spec']['source'], healthy)

    def read_secret(name):
        return api('get', 'secret', 'sample-' + name, '-n', 'sops-workloads', '--ignore-not-found', '-o', 'json')

    def verify_secret(name, previous=None):
        value = read_secret(name)
        expected = base64.b64encode(fixture.secret.encode()).decode()
        if value.get('data') != {'password': expected} or value['metadata'].get('labels', {}).get('bareplane.io/sops-managed') != 'true':
            raise RuntimeError('SOPS did not reconcile the expected private Secret value and ownership')
        if previous and (value['metadata']['uid'] != previous['metadata']['uid'] or value['data'] != previous['data']):
            raise RuntimeError('SOPS failure or rotation replaced the existing Secret')
        return value

    def deliver(name, key, value, update=False):
        obj = api('get', 'secret', name, '-n', 'argocd', '-o', 'json') if update else dict(
            apiVersion='v1', kind='Secret', metadata=dict(name=name, namespace='argocd'), type='Opaque')
        obj['data'] = {key: base64.b64encode(value).decode()}
        command('replace' if update else 'create', '-f', '-', data=obj)

    configure_argo('missing')
    command('create', '-f', '-', data=application('age'))
    wait_application('sops-age', secret_source('age'), healthy=False)
    if read_secret('age'):
        raise RuntimeError('Missing SOPS keys created a Secret')
    deliver('bareplane-sops-age', 'keys.txt', (fixture.root / 'age').read_bytes())
    deliver('bareplane-sops-pgp', 'private.asc', (fixture.root / 'pgp').read_bytes())
    deliver('bareplane-sops-passphrase', 'passphrase', (fixture.root / 'passphrase').read_bytes())
    assert_logs_public()
    configure_argo('delivered')
    reconcile('age')
    age_secret = verify_secret('age')

    unmanaged = api('create', '-f', '-', '-o', 'json', data=dict(apiVersion='v1', kind='Secret',
        metadata=dict(name='sample-pgp', namespace='sops-workloads'), type='Opaque', stringData=dict(password='unmanaged-disposable-value')))
    command('create', '-f', '-', data=application('pgp'))
    refused = wait_application('sops-pgp', secret_source('pgp'), healthy=False)
    if 'SOPS refuses to adopt an existing unmanaged Secret' not in json.dumps(refused.get('status', {}).get('operationState', {})):
        raise RuntimeError('Unmanaged SOPS Secret was not refused by the expected ownership policy')
    current = read_secret('pgp')
    if current['metadata']['uid'] != unmanaged['metadata']['uid'] or current['data'] != unmanaged['data']:
        raise RuntimeError('SOPS adopted an existing unmanaged Secret')
    command('delete', '--raw=/api/v1/namespaces/sops-workloads/secrets/sample-pgp', '-f', '-', data=dict(
        apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=unmanaged['metadata']['uid'])))
    fixture.encrypt('pgp', ['--pgp', fixture.fingerprint])
    fixture.publish()
    reconcile('pgp')
    verify_secret('pgp')

    original_ciphertext = (fixture.repo / 'age/secret.sops.json').read_bytes()
    tampered = json.loads(original_ciphertext)
    tampered['metadata']['name'] = 'tampered'
    (fixture.repo / 'age/secret.sops.json').write_text(json.dumps(tampered))
    fixture.publish()
    reconcile('age', healthy=False)
    verify_secret('age', age_secret)
    if api('get', 'secret', 'tampered', '-n', 'sops-workloads', '--ignore-not-found', '-o', 'json'):
        raise RuntimeError('SOPS accepted unauthenticated metadata')
    replacement_recipient = fixture.age_key('replacement-age')
    deliver('bareplane-sops-age', 'keys.txt', (fixture.root / 'replacement-age').read_bytes(), update=True)
    assert_logs_public()
    configure_argo('rotated')
    (fixture.repo / 'age/secret.sops.json').write_bytes(original_ciphertext)
    fixture.publish()
    reconcile('age', healthy=False)
    verify_secret('age', age_secret)
    fixture.encrypt('age', ['--age', replacement_recipient])
    fixture.publish()
    reconcile('age')
    verify_secret('age', age_secret)
    verify_secret('pgp')
    assert_logs_public()
    fixture.assert_public(fixture.command(['git', 'cat-file', '--batch-all-objects', '--batch']))
    component.refresh()
    print('Argo-owned SOPS passed age/PGP delivery, missing-key recovery, unmanaged Secret refusal, full MAC verification, key rotation and plaintext-exclusion checks.', flush=True)
