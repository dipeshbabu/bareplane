#!/usr/bin/python
"""Shared controller-only private kubeconfig validation and publication."""
import base64
import binascii
import hashlib
import ipaddress
import os
from pathlib import Path
import stat
import subprocess
import tempfile
from urllib.parse import urlsplit

import yaml


LIMIT = 65536
HEADER = b'# bareplane-managed kubeconfig v1 sha256='
EXTENSION = 'bareplane.io/identity'


class StrictLoader(yaml.SafeLoader):
    pass


def unique_mapping(loader, node):
    result = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node)
        if not isinstance(key, str) or key in result:
            raise ValueError('Kubeconfig has duplicate or non-string mapping keys')
        result[key] = loader.construct_object(value_node)
    return result


StrictLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, unique_mapping)


def decode(data):
    if not data or len(data) > LIMIT:
        raise ValueError('Kubeconfig is empty or oversized')
    try:
        for count, token in enumerate(yaml.scan(data)):
            if count > 4096 or isinstance(token, (yaml.AliasToken, yaml.AnchorToken, yaml.TagToken)):
                raise ValueError('Kubeconfig aliases, anchors, tags, or excessive structure are unsupported')
        result = yaml.load(data, Loader=StrictLoader)
    except (yaml.YAMLError, UnicodeError, RecursionError):
        raise ValueError('Kubeconfig YAML is malformed') from None
    if not isinstance(result, dict):
        raise ValueError('Kubeconfig must be a mapping')
    return result


def keys(mapping, allowed, required=()):
    if not isinstance(mapping, dict) or set(mapping) - set(allowed) or set(required) - set(mapping):
        raise ValueError('Kubeconfig contains missing or unsupported fields')


def binary(value):
    if not isinstance(value, str) or len(value) > 4 * ((LIMIT + 2) // 3):
        raise ValueError('Kubeconfig embedded credential is malformed')
    try:
        decoded = base64.b64decode(value, validate=True)
        if len(decoded) > LIMIT:
            raise ValueError('Oversized embedded credential')
        return decoded
    except (binascii.Error, ValueError):
        raise ValueError('Kubeconfig embedded credential is malformed') from None


def openssl(args, data):
    try:
        output = subprocess.run(['openssl'] + args, input=data, capture_output=True, timeout=5, check=True).stdout
    except (OSError, subprocess.SubprocessError):
        raise ValueError('Cannot validate the embedded certificate/key; OpenSSL and valid non-expired credentials are required') from None
    if len(output) > LIMIT:
        raise ValueError('Credential validation exceeded its output bound')
    return output


def endpoint(vip, port):
    address = ipaddress.ip_address(vip)
    if port != 6443:
        raise ValueError('Unsupported Kubernetes API port')
    return 'https://' + ('[' + str(address) + ']' if address.version == 6 else str(address)) + ':' + str(port)


def validate(doc, cluster, ca_sha256, check_expiry=True):
    keys(doc, ['apiVersion', 'kind', 'preferences', 'clusters', 'users', 'contexts', 'current-context', 'extensions'],
         ['apiVersion', 'kind', 'clusters', 'users', 'contexts', 'current-context'])
    if doc['apiVersion'] != 'v1' or doc['kind'] != 'Config' or doc.get('preferences', {}) != {}:
        raise ValueError('Unsupported kubeconfig version or preferences')
    for field in ['clusters', 'users', 'contexts']:
        if not isinstance(doc[field], list) or len(doc[field]) != 1:
            raise ValueError('Exactly one cluster, user, and context are required')
    entry, user, context = doc['clusters'][0], doc['users'][0], doc['contexts'][0]
    keys(entry, ['name', 'cluster'], ['name', 'cluster'])
    keys(user, ['name', 'user'], ['name', 'user'])
    keys(context, ['name', 'context'], ['name', 'context'])
    if entry['name'] != cluster:
        raise ValueError('Kubeconfig belongs to a different cluster')
    for value in [user['name'], context['name'], doc['current-context']]:
        if not isinstance(value, str) or not value or len(value) > 253 or any(ord(c) < 33 or ord(c) > 126 for c in value):
            raise ValueError('Unsupported kubeconfig context or user name')
    keys(context['context'], ['cluster', 'user'], ['cluster', 'user'])
    if context['context'] != dict(cluster=cluster, user=user['name']) or doc['current-context'] != context['name']:
        raise ValueError('Kubeconfig context does not select the intended cluster and user')
    keys(entry['cluster'], ['server', 'certificate-authority-data'], ['server', 'certificate-authority-data'])
    keys(user['user'], ['client-certificate-data', 'client-key-data'], ['client-certificate-data', 'client-key-data'])
    server = entry['cluster']['server']
    if not isinstance(server, str):
        raise ValueError('Kubeconfig server is malformed')
    parsed = urlsplit(server)
    if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or parsed.path not in ['', '/'] or parsed.query or parsed.fragment:
        raise ValueError('Kubeconfig server must be a plain HTTPS endpoint')
    try:
        if parsed.port is not None and not 1 <= parsed.port <= 65535:
            raise ValueError('Invalid API port')
    except ValueError:
        raise ValueError('Kubeconfig server port is invalid') from None
    ca = binary(entry['cluster']['certificate-authority-data'])
    if hashlib.sha256(ca).hexdigest() != ca_sha256:
        raise ValueError('Kubeconfig CA differs from the authenticated primary')
    openssl(['x509', '-noout'] + (['-checkend', '0'] if check_expiry else []), ca)
    certificate = binary(user['user']['client-certificate-data'])
    private_key = binary(user['user']['client-key-data'])
    openssl(['x509', '-noout'] + (['-checkend', '0'] if check_expiry else []), certificate)
    # Only the public CA is staged temporarily. Client credentials stay in
    # memory; system trust stores must not validate an unrelated client issuer.
    with tempfile.NamedTemporaryFile(prefix='bareplane-public-ca-', mode='wb') as ca_file:
        ca_file.write(ca)
        ca_file.flush()
        openssl(['verify', '-CAfile', ca_file.name, '-no-CApath', '-no-CAstore', '-purpose', 'sslclient']
                + ([] if check_expiry else ['-no_check_time']), certificate)
    public_certificate = openssl(['x509', '-pubkey', '-noout'], certificate)
    public_key = openssl(['pkey', '-pubout', '-passin', 'pass:'], private_key)
    if public_certificate != public_key:
        raise ValueError('Kubeconfig client certificate and key do not match')


def prepare(source, cluster, ca_sha256, server):
    doc = decode(source)
    validate(doc, cluster, ca_sha256)
    if doc.get('extensions') not in (None, []):
        raise ValueError('Unmanaged kubeconfig extensions are unsupported')
    doc['clusters'][0]['cluster']['server'] = server
    doc['extensions'] = [dict(name=EXTENSION, extension=dict(cluster=cluster, caSHA256=ca_sha256, server=server))]
    body = yaml.safe_dump(doc, sort_keys=True).encode()
    if len(body) > LIMIT:
        raise ValueError('Canonical kubeconfig exceeds its size bound')
    return HEADER + hashlib.sha256(body).hexdigest().encode() + b'\n' + body


def inspect_path(path, directory=False, private=False):
    try:
        info = path.lstat()
    except FileNotFoundError:
        return False
    if not (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)):
        raise ValueError('Kubeconfig paths must not be symlinks or special files')
    if private and (info.st_mode & 0o077 or info.st_uid != os.geteuid()):
        raise ValueError('Kubeconfig state must be owner-only and owned by the current account')
    return True


def check_destination(destination):
    # Normalize '..' lexically, never follow symlinks to find the destination.
    path = Path(os.path.abspath(destination))
    if path.name != 'admin.conf' or [p.name for p in list(path.parents)[:3]] != ['bootstrap', 'state', '.bareplane']:
        raise ValueError('Kubeconfig destination must be the private Bareplane project state path')
    original = Path(destination)
    if not original.is_absolute():
        raise ValueError('Kubeconfig destination must be absolute')
    # Inspect the original path as well: a symlink before '..' cannot be hidden
    # by normalization. The final target uses the canonical lexical path.
    current = Path('/')
    for part in original.parts[1:-1]:
        current /= part
        inspect_path(current, directory=True)
    for parent in reversed(path.parents):
        inspect_path(parent, directory=True, private=parent in [path.parent, path.parent.parent])
    inspect_path(path, private=True)
    return path


def existing(path, cluster, ca_sha256, server, check_expiry=True):
    if not inspect_path(path, private=True):
        return False
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        contents = stream.read(LIMIT + len(HEADER) + 66)
    header, separator, body = contents.partition(b'\n')
    if not separator or not header.startswith(HEADER) or len(body) > LIMIT or header[len(HEADER):] != hashlib.sha256(body).hexdigest().encode():
        raise ValueError('Existing kubeconfig is unmanaged, modified, or oversized')
    doc = decode(body)
    validate(doc, cluster, ca_sha256, check_expiry=check_expiry)
    identity = [dict(name=EXTENSION, extension=dict(cluster=cluster, caSHA256=ca_sha256, server=server))]
    if doc.get('extensions') != identity or doc['clusters'][0]['cluster']['server'] != server:
        raise ValueError('Existing kubeconfig cluster identity or VIP differs; use explicit lifecycle recovery')
    return True


def publish(destination, source, cluster, ca_sha256, vip, port=6443, check=False):
    path = check_destination(destination)
    server = endpoint(vip, port)
    content = prepare(source, cluster, ca_sha256, server)
    if existing(path, cluster, ca_sha256, server):
        return False
    if check:
        return True
    for parent, mode in [(path.parents[2], 0o755), (path.parents[1], 0o700), (path.parent, 0o700)]:
        if not inspect_path(parent, directory=True, private=mode == 0o700):
            parent.mkdir(mode=mode)
    check_destination(str(path))
    fd, temporary = tempfile.mkstemp(prefix='.admin-stage-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        # Atomic first publication with no overwrite, even if another exporter
        # or a symlink appears after inspection. Existing credentials are never
        # silently rotated; lifecycle recovery owns certificate replacement.
        try:
            os.link(temporary, path)
        except FileExistsError:
            if existing(path, cluster, ca_sha256, server):
                return False
            raise
    finally:
        os.unlink(temporary)
    return True


def main():
    from ansible.module_utils.basic import AnsibleModule
    module = AnsibleModule(argument_spec=dict(
        operation=dict(type='str', choices=['publish', 'inspect', 'withdraw'], default='publish'),
        destination=dict(type='path', required=True), content=dict(type='str', no_log=True),
        cluster=dict(type='str', required=True), ca_sha256=dict(type='str', required=True),
        vip=dict(type='str', required=True), port=dict(type='int', default=6443),
    ), supports_check_mode=True)
    try:
        p = module.params
        if p['operation'] == 'publish':
            source = binary(p['content'])
            changed = publish(p['destination'], source, p['cluster'], p['ca_sha256'], p['vip'], p['port'], module.check_mode)
        else:
            path = check_destination(p['destination'])
            present = existing(path, p['cluster'], p['ca_sha256'], endpoint(p['vip'], p['port']), check_expiry=False)
            changed = present and p['operation'] == 'withdraw'
            if changed and not module.check_mode:
                os.unlink(path)
        module.exit_json(changed=changed)
    except ValueError as error:
        module.fail_json(msg=str(error))
    except (OSError, KeyError, TypeError, AttributeError):
        module.fail_json(msg='Cannot safely publish the private kubeconfig; inspect local state without printing credentials')


if __name__ == '__main__':
    main()
