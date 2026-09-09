"""Bounded TLS Vault API double; runtime values and credentials never enter Git."""

from collections import deque
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import json
import os
import secrets
import ssl
import threading
import time
from urllib.parse import urlsplit

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID


class FakeVault:
    def __init__(self, token_review):
        self.token_review = token_review
        self.lock = threading.RLock()
        self.tokens = {}
        self.requests = deque(maxlen=4096)
        self.sensitive = []
        self.values = dict(username='disposable-' + secrets.token_hex(12), password=secrets.token_hex(32))
        self.sensitive.extend(value.encode() for value in self.values.values())
        self.available, self.authorized, self.deleted = True, True, False
        self.version = 1
        self.logins = self.reads = self.revocations = self.denials = self.unexpected = 0

    def rotate(self):
        with self.lock:
            self.values['password'] = secrets.token_hex(32)
            self.sensitive.append(self.values['password'].encode())
            self.version += 1

    @staticmethod
    def reply(data=None, auth=None):
        return 200, dict(request_id='00000000-0000-4000-8000-000000000001', lease_id='', renewable=False,
                         lease_duration=0, data=data, auth=auth, warnings=None)

    @staticmethod
    def error(status=403):
        return status, {'errors': ['Disposable Vault refused the requested operation']}

    def handle(self, method, path, headers, payload=None):
        path = urlsplit(path).path
        with self.lock:
            self.requests.append(dict(method=method, path=path))
            if not self.available:
                return self.error(503)
            if headers.get('X-Vault-Namespace', ''):
                self.denials += 1
                return self.error()
            if method in {'POST', 'PUT'} and path == '/v1/auth/kubernetes/login':
                if not self.authorized or not isinstance(payload, dict) or set(payload) != {'role', 'jwt'} \
                        or payload['role'] != 'workloads-reader' or not isinstance(payload['jwt'], str) or len(payload['jwt']) > 16384:
                    self.denials += 1
                    return self.error()
                if not self.token_review(payload['jwt']):
                    self.denials += 1
                    return self.error()
                now = time.monotonic()
                self.tokens = {key: expiry for key, expiry in self.tokens.items() if expiry > now}
                if len(self.tokens) >= 256 or self.logins >= 4096:
                    return self.error(429)
                token = 'disposable-vault-' + secrets.token_hex(24)
                self.tokens[token] = now + 120
                self.sensitive.append(token.encode())
                self.logins += 1
                return self.reply(auth=dict(client_token=token, accessor='disposable-accessor', policies=['workloads-reader'],
                    token_policies=['workloads-reader'], metadata={}, lease_duration=120, renewable=False, token_type='service'))
            token = headers.get('X-Vault-Token')
            if not self.authorized or self.tokens.get(token, 0) <= time.monotonic():
                self.denials += 1
                return self.error()
            if method == 'GET' and path == '/v1/auth/token/lookup-self':
                return self.reply(dict(id=token, ttl=120, expire_time=(datetime.now(timezone.utc) + timedelta(seconds=120)).isoformat(),
                                       renewable=False, policies=['workloads-reader'], type='service'))
            if method in {'POST', 'PUT'} and path == '/v1/auth/token/revoke-self':
                self.tokens.pop(token, None)
                self.revocations += 1
                return 204, None
            if method == 'GET' and path == '/v1/kv/data/apps/database':
                self.reads += 1
                if self.deleted:
                    return self.error(404)
                return self.reply(dict(data=dict(self.values), metadata=dict(version=self.version, deletion_time='', destroyed=False,
                    created_time='2026-09-09T00:00:00Z')))
            self.unexpected += 1
            return self.error(404)


def certificate_authority():
    key = ec.generate_private_key(ec.SECP256R1())
    now = datetime.now(timezone.utc)
    issuer = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'Disposable Vault CA')])
    certificate = x509.CertificateBuilder().subject_name(issuer).issuer_name(issuer).public_key(key.public_key()) \
        .serial_number(x509.random_serial_number()).not_valid_before(now - timedelta(minutes=5)).not_valid_after(now + timedelta(days=1)) \
        .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True) \
        .add_extension(x509.KeyUsage(False, False, False, False, False, True, True, False, False), critical=True) \
        .add_extension(x509.SubjectKeyIdentifier.from_public_key(key.public_key()), critical=False) \
        .sign(key, hashes.SHA256())
    return key, certificate


class FakeVaultServer:
    def __init__(self, model, directory, address='127.0.0.1'):
        if address != '127.0.0.1' and (address != '192.0.2.1' or os.environ.get('GITHUB_ACTIONS') != 'true'
                                      or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1'):
            raise ValueError('Non-loopback fake Vault is restricted to the disposable GitHub VM bridge')
        directory.mkdir(mode=0o700)
        ca_key, ca = certificate_authority()
        leaf_key = ec.generate_private_key(ec.SECP256R1())
        now = datetime.now(timezone.utc)
        leaf = x509.CertificateBuilder().subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'Disposable Vault')])) \
            .issuer_name(ca.subject).public_key(leaf_key.public_key()).serial_number(x509.random_serial_number()) \
            .not_valid_before(now - timedelta(minutes=5)).not_valid_after(now + timedelta(days=1)) \
            .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True) \
            .add_extension(x509.KeyUsage(True, False, False, False, False, False, False, False, False), critical=True) \
            .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False) \
            .add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(ca_key.public_key()), critical=False) \
            .add_extension(x509.SubjectKeyIdentifier.from_public_key(leaf_key.public_key()), critical=False) \
            .add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address(address))]), critical=False) \
            .sign(ca_key, hashes.SHA256())
        self.ca = ca.public_bytes(serialization.Encoding.PEM)
        certificate, key = directory / 'server.crt', directory / 'server.key'
        certificate.write_bytes(leaf.public_bytes(serialization.Encoding.PEM))
        descriptor = os.open(key, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(descriptor, 'wb') as stream:
            stream.write(leaf_key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))

        class Handler(BaseHTTPRequestHandler):
            def setup(self):
                super().setup()
                self.connection.settimeout(5)

            def log_message(self, *args):
                pass

            def respond(self):
                try:
                    size = int(self.headers.get('Content-Length', '0'))
                    if not 0 <= size <= 65536:
                        raise ValueError('unbounded body')
                    payload = json.loads(self.rfile.read(size)) if size else None
                    status, response = model.handle(self.command, self.path, self.headers, payload)
                except (ValueError, UnicodeError):
                    status, response = model.error(400)
                data = json.dumps(response).encode() if response is not None else b''
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = do_POST = do_PUT = do_DELETE = respond

        self.server = ThreadingHTTPServer((address, 0), Handler)
        self.server.daemon_threads = True
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(certificate, key)
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.url = f'https://{address}:{self.server.server_port}'

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
