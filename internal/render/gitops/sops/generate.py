#!/usr/bin/python3
"""Restricted SOPS CMP: one authenticated Secret, no repository commands.

Run with Python isolated mode. Plaintext is emitted only after the complete
document passes authentication and validation; all failures use fixed messages.
"""

import base64
import json
import os
from pathlib import Path
import re
import resource
import signal
import stat
import subprocess
import sys
import tempfile


INPUT_LIMIT = 1024 * 1024
OUTPUT_LIMIT = 4 * INPUT_LIMIT
CONFIG_DIR = Path('/home/argocd/cmp-server/config')
KEY_DIR = Path('/var/run/bareplane-sops')
NAME = re.compile(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\Z')
DATA_KEY = re.compile(r'[A-Za-z0-9._-]{1,253}\Z')
ENCRYPTED = re.compile(r'ENC\[AES256_GCM,data:[A-Za-z0-9+/=]+,iv:[A-Za-z0-9+/=]+,tag:[A-Za-z0-9+/=]+,type:str\]\Z')
AGE_RECIPIENT = re.compile(r'age1[023456789acdefghjklmnpqrstuvwxyz]{58}\Z')
PGP_FINGERPRINT = re.compile(r'[0-9A-Fa-f]{40}\Z')


class Refusal(Exception):
    pass


def require(condition):
    if not condition:
        raise Refusal('SOPS input or key contract was refused')


def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result)
        result[key] = value
    return result


def decode(data):
    return json.loads(data, object_pairs_hook=pairs,
                      parse_constant=lambda _: require(False))


def read_regular(path, limit, projected=False):
    # Only trusted Kubernetes projected key/config mounts may contain symlinks.
    # Repository inputs are opened atomically without following a final symlink.
    descriptor = os.open(path, os.O_RDONLY | os.O_NONBLOCK | (0 if projected else os.O_NOFOLLOW))
    with os.fdopen(descriptor, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_size <= limit)
        value = stream.read(limit + 1)
    require(len(value) <= limit)
    return value


def validate_secret(obj, namespaces, encrypted):
    require(isinstance(obj, dict) and set(obj) <= {'apiVersion', 'kind', 'metadata', 'type', 'data', 'stringData', 'sops'})
    require(obj.get('apiVersion') == 'v1' and obj.get('kind') == 'Secret')
    metadata = obj.get('metadata')
    require(isinstance(metadata, dict) and set(metadata) == {'name', 'namespace'})
    require(isinstance(metadata['name'], str) and NAME.fullmatch(metadata['name']))
    require(isinstance(metadata['namespace'], str) and metadata['namespace'] in namespaces
            and metadata['namespace'] != 'argocd' and not metadata['namespace'].startswith('kube-'))
    require(obj.get('type') in {'Opaque', 'kubernetes.io/tls', 'kubernetes.io/dockerconfigjson'})
    require(('data' in obj) != ('stringData' in obj))
    values = obj.get('data', obj.get('stringData'))
    require(isinstance(values, dict) and 1 <= len(values) <= 256)
    require(all(isinstance(key, str) and DATA_KEY.fullmatch(key) and isinstance(value, str)
                for key, value in values.items()))
    if encrypted:
        require(all(ENCRYPTED.fullmatch(value) for value in values.values()))
    else:
        require('sops' not in obj)
        if 'data' in obj:
            for value in values.values():
                base64.b64decode(value, validate=True)
    if obj['type'] == 'kubernetes.io/tls':
        require(set(values) == {'tls.crt', 'tls.key'})
    if obj['type'] == 'kubernetes.io/dockerconfigjson':
        require(set(values) == {'.dockerconfigjson'})


def validate_ciphertext(obj, policy):
    validate_secret(obj, policy['namespaces'], encrypted=True)
    metadata = obj.get('sops')
    require(isinstance(metadata, dict) and set(metadata) <= {
        'age', 'pgp', 'kms', 'gcp_kms', 'azure_kv', 'hc_vault',
        'lastmodified', 'mac', 'encrypted_regex', 'version'})
    require(metadata.get('version') == '3.13.3' and metadata.get('encrypted_regex') == '^(data|stringData)$')
    require(isinstance(metadata.get('mac'), str) and ENCRYPTED.fullmatch(metadata['mac']))
    require(isinstance(metadata.get('lastmodified'), str) and len(metadata['lastmodified']) <= 64)
    require(not any(metadata.get(name) for name in ['kms', 'gcp_kms', 'azure_kv', 'hc_vault']))
    recipients = 0
    for key_type, pattern, field, allowed in [('age', AGE_RECIPIENT, 'recipient', {'recipient', 'enc'}),
                                             ('pgp', PGP_FINGERPRINT, 'fp', {'fp', 'enc', 'created_at'})]:
        entries = metadata.get(key_type) or []
        require(isinstance(entries, list) and len(entries) <= 32)
        # A file can retain both kinds for recovery, but at least one locally
        # configured key must be usable. No KMS, remote keyservice or age plugin.
        for entry in entries:
            require(isinstance(entry, dict) and set(entry) == allowed)
            require(isinstance(entry[field], str) and pattern.fullmatch(entry[field]))
            require(isinstance(entry['enc'], str) and 1 <= len(entry['enc']) <= 16384)
        if policy[key_type]:
            recipients += len(entries)
    require(recipients > 0)


def limits():
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_FSIZE, (OUTPUT_LIMIT, OUTPUT_LIMIT))
    resource.setrlimit(resource.RLIMIT_CPU, (30, 30))


def run_private(args, environment, cwd, output=None, timeout=20):
    child = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=output or subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL, env=environment, cwd=cwd,
                             start_new_session=True, preexec_fn=limits)
    try:
        require(child.wait(timeout=timeout) == 0)
    finally:
        # Includes descendants on timeout/interruption, without exposing argv or
        # captured stderr (both SOPS and GPG can include sensitive error data).
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait()


def generate(source, policy, key_dir=KEY_DIR, config_dir=CONFIG_DIR, temporary='/tmp', sops='/usr/local/bin/sops', destination=None):
    ciphertext = read_regular(source, INPUT_LIMIT)
    encrypted = decode(ciphertext)
    validate_ciphertext(encrypted, policy)
    if destination is not None:
        require(encrypted['metadata']['namespace'] == destination)
    with tempfile.TemporaryDirectory(prefix='bareplane-sops-', dir=temporary) as directory:
        private = Path(directory)
        environment = {'PATH': '/usr/local/bin:/usr/bin:/bin', 'HOME': directory, 'GNUPGHOME': str(private / 'gnupg'),
                       'SOPS_AGE_KEY_FILE': str(private / 'age-key'), 'SOPS_GPG_EXEC': str(config_dir / 'gpg'),
                       'BAREPLANE_SOPS_PASSPHRASE_FILE': str(private / 'passphrase'), 'TMPDIR': directory,
                       'LANG': 'C.UTF-8'}
        (private / 'gnupg').mkdir(mode=0o700)
        (private / 'age-key').write_bytes(read_regular(key_dir / 'age/key', 65536, projected=True) if policy['age'] else b'')
        (private / 'passphrase').write_bytes(read_regular(key_dir / 'passphrase/key', 16384, projected=True) if policy['passphrase'] else b'')
        input_path, output_path = private / 'input.json', private / 'output.json'
        input_path.write_bytes(ciphertext)
        try:
            if policy['pgp']:
                key = private / 'pgp-key'
                key.write_bytes(read_regular(key_dir / 'pgp/key', INPUT_LIMIT, projected=True))
                run_private(['/usr/bin/gpg', '--no-options', '--batch', '--no-tty', '--import', str(key)], environment, directory)
            run_private([sops, 'decrypt', '--input-type', 'json', '--output-type', 'json', '--output', str(output_path), str(input_path)],
                        environment, directory)
            plaintext = decode(read_regular(output_path, OUTPUT_LIMIT))
            validate_secret(plaintext, policy['namespaces'], encrypted=False)
            require(plaintext['metadata'] == encrypted['metadata'] and plaintext['type'] == encrypted['type'])
            # Kubernetes server-side apply does not reliably track stringData.
            # Emit only data, after full-file SOPS MAC verification has finished.
            if 'stringData' in plaintext:
                plaintext['data'] = {key: base64.b64encode(value.encode()).decode()
                                     for key, value in plaintext.pop('stringData').items()}
            plaintext['metadata']['labels'] = {'bareplane.io/sops-managed': 'true'}
            result = json.dumps(plaintext, separators=(',', ':'), ensure_ascii=True).encode() + b'\n'
            require(len(result) <= OUTPUT_LIMIT)
            return result
        finally:
            if policy['pgp']:
                # GPG may detach its agent. Terminate only this render's private
                # keyring agent before removing its memory-backed directory.
                try:
                    run_private(['/usr/bin/gpgconf', '--kill', 'all'], environment, directory, timeout=5)
                except (OSError, Refusal, subprocess.SubprocessError):
                    pass


def main():
    os.umask(0o077)
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))

    def interrupted(signum, frame):
        raise Refusal('SOPS generation was interrupted')

    for signum in [signal.SIGTERM, signal.SIGALRM, signal.SIGINT]:
        signal.signal(signum, interrupted)
    signal.alarm(60)
    try:
        policy = decode(read_regular(CONFIG_DIR / 'policy.json', 16384, projected=True))
        destination = os.environ.get('ARGOCD_APP_NAMESPACE', '')
        require(destination in policy['namespaces'])
        result = generate(Path.cwd() / 'secret.sops.json', policy, destination=destination)
        sys.stdout.buffer.write(result)
        sys.stdout.buffer.flush()
    except Exception:
        # Never echo user-controlled exceptions, decrypted values or a traceback
        # to Argo logs/UI. Failed generation leaves the live Secret untouched.
        sys.stderr.write('SOPS generation refused; verify encrypted input, namespace and delivered key references.\n')
        return 1
    finally:
        signal.alarm(0)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
