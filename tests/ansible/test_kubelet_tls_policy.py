"""Serving CSR adversarial tests with locally generated throwaway public keys."""

import base64
import copy
import datetime
import hashlib
import importlib.util
import ipaddress
from pathlib import Path
import unittest

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec, rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID, ObjectIdentifier


source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/module_utils/bareplane_kubelet_tls_policy.py'
spec = importlib.util.spec_from_file_location('kubelet_tls_policy', source)
policy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(policy)


class ServingPolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.key = ec.generate_private_key(ec.SECP256R1())

    def request(self, key=None, subject=None, sans=None, extension=None, attribute=False):
        key = key or self.key
        subject = subject or x509.Name([x509.NameAttribute(NameOID.ORGANIZATION_NAME, 'system:nodes'),
                                       x509.NameAttribute(NameOID.COMMON_NAME, 'system:node:lab-control-1')])
        if sans is None:
            sans = [x509.DNSName('lab-control-1'), x509.IPAddress(ipaddress.ip_address('192.0.2.11'))]
        builder = x509.CertificateSigningRequestBuilder().subject_name(subject).add_extension(x509.SubjectAlternativeName(sans), critical=False)
        if extension:
            builder = builder.add_extension(extension, critical=True)
        if attribute:
            builder = builder.add_attribute(ObjectIdentifier('1.2.840.113549.1.9.7'), b'PRIVATE-SENTINEL')
        csr = builder.sign(key, hashes.SHA256()).public_bytes(serialization.Encoding.PEM)
        return dict(apiVersion='certificates.k8s.io/v1', kind='CertificateSigningRequest',
                    metadata=dict(name='csr-fixture', uid='csr-uid', resourceVersion='123'),
                    spec=dict(request=base64.b64encode(csr).decode(), signerName=policy.SIGNER,
                              username='system:node:lab-control-1', groups=['system:nodes', 'system:authenticated'],
                              usages=['digital signature', 'server auth']))

    def validate(self, resource):
        return policy.validate_request(resource, 'lab-control-1', 'node-uid', '192.0.2.11')

    def test_valid_rsa_and_ec_requests_bind_uid_inventory_and_public_key(self):
        for key in [self.key, rsa.generate_private_key(public_exponent=65537, key_size=2048)]:
            resource = self.request(key=key)
            if isinstance(key, rsa.RSAPrivateKey):
                resource['spec']['usages'].append('key encipherment')
            proof = self.validate(resource)
            self.assertEqual(proof['nodeUID'], 'node-uid')
            self.assertEqual(proof['csrUID'], 'csr-uid')
            self.assertEqual(proof['publicKeySHA256'], policy.public_key_digest(key.public_key()))
            self.assertNotIn('request', proof)

    def test_untrusted_users_signers_groups_usages_and_lifetimes_are_refused(self):
        changes = [('username', 'system:node:other'), ('username', 'system:serviceaccount:default:app'),
                   ('signerName', 'kubernetes.io/kube-apiserver-client'), ('groups', ['system:masters']),
                   ('groups', ['system:nodes', 'system:authenticated', 'system:nodes']), ('extra', {'scope': ['admin']}),
                   ('uid', 'foreign-user'), ('usages', ['digital signature', 'client auth']),
                   ('usages', ['digital signature', 'server auth', 'cert sign']),
                   ('expirationSeconds', 31536001), ('expirationSeconds', 599), ('expirationSeconds', True)]
        for field, value in changes:
            resource = self.request()
            resource['spec'][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                self.validate(resource)

    def test_authenticated_kubernetes_credential_fingerprint_must_match_ssh_proof(self):
        resource = self.request()
        credential = 'X509SHA256=' + 'a' * 64
        resource['spec']['extra'] = {policy.CREDENTIAL_ID: [credential]}
        proof = policy.validate_request(resource, 'lab-control-1', 'node-uid', '192.0.2.11', credential)
        self.assertEqual(proof['credentialID'], credential)
        for expected in [None, 'X509SHA256=' + 'b' * 64]:
            with self.assertRaises(ValueError):
                policy.validate_request(resource, 'lab-control-1', 'node-uid', '192.0.2.11', expected)

    def test_sans_cannot_claim_other_nodes_services_wildcards_or_uris(self):
        for extra in [x509.DNSName('other'), x509.DNSName('*.example.com'), x509.IPAddress(ipaddress.ip_address('192.0.2.100')),
                      x509.UniformResourceIdentifier('spiffe://foreign'), x509.RFC822Name('foreign@example.com')]:
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                self.validate(self.request(sans=[x509.DNSName('lab-control-1'), extra]))
        for sans in [[], [x509.DNSName('lab-control-1')], [x509.IPAddress(ipaddress.ip_address('192.0.2.11'))]]:
            with self.subTest(sans=sans), self.assertRaises(ValueError):
                self.validate(self.request(sans=sans))

    def test_weak_keys_subject_escalation_ca_extensions_and_attributes_are_refused(self):
        cases = [dict(key=rsa.generate_private_key(public_exponent=65537, key_size=1024)),
                 dict(subject=x509.Name([x509.NameAttribute(NameOID.ORGANIZATION_NAME, 'system:masters'),
                                        x509.NameAttribute(NameOID.COMMON_NAME, 'system:node:lab-control-1')])),
                 dict(extension=x509.BasicConstraints(ca=True, path_length=None)), dict(attribute=True)]
        for kwargs in cases:
            with self.subTest(case=list(kwargs)), self.assertRaises(ValueError) as caught:
                self.validate(self.request(**kwargs))
            self.assertNotIn('PRIVATE-SENTINEL', str(caught.exception))

    def test_modified_approved_terminated_or_oversized_requests_are_refused(self):
        original = self.request()
        mutations = [lambda r: r['metadata'].update(deletionTimestamp='now'),
                     lambda r: r['metadata'].update(ownerReferences=[{'uid': 'foreign'}]),
                     lambda r: r['metadata'].pop('resourceVersion'),
                     lambda r: r.update(status={'conditions': [{'type': 'Approved', 'status': 'True'}]}),
                     lambda r: r.update(status={'certificate': 'unexpected'}),
                     lambda r: r['spec'].update(request='A' * (policy.LIMIT + 1)),
                     lambda r: r['spec'].update(request='not base64'),
                     lambda r: r['spec'].update(request=base64.b64encode(base64.b64decode(r['spec']['request']) * 2).decode())]
        for mutation in mutations:
            resource = copy.deepcopy(original)
            mutation(resource)
            with self.assertRaises((ValueError, KeyError)):
                self.validate(resource)

    def test_approval_is_bound_to_node_uid_addresses_and_exact_csr_version(self):
        resource = self.request()
        proof = self.validate(resource)
        node = dict(metadata=dict(name='lab-control-1', uid='node-uid'), status=dict(addresses=[
            dict(type='Hostname', address='lab-control-1'), dict(type='InternalIP', address='192.0.2.11')]))
        approved = policy.approval_document(resource, proof, node)
        self.assertEqual(approved['metadata'], resource['metadata'])
        self.assertNotIn('status', resource)
        self.assertEqual(approved['status']['conditions'][0]['reason'], 'BareplaneInventoryVerified')
        for mutation in [lambda r, n: n['metadata'].update(uid='replaced'),
                         lambda r, n: n['status']['addresses'][1].update(address='192.0.2.12'),
                         lambda r, n: r['metadata'].update(resourceVersion='124'),
                         lambda r, n: r['spec'].update(expirationSeconds=86400),
                         lambda r, n: r['metadata'].update(uid='replaced')]:
            changed, changed_node = copy.deepcopy(resource), copy.deepcopy(node)
            mutation(changed, changed_node)
            with self.assertRaises(ValueError):
                policy.approval_document(changed, proof, changed_node)

    def certificates(self, client_auth=False, wrong_key=False, lifetime=30):
        now = datetime.datetime.now(datetime.timezone.utc)
        if not hasattr(self, 'ca_key'):
            self.ca_key = ec.generate_private_key(ec.SECP256R1())
        ca_key = self.ca_key
        ca_name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'throwaway-test-ca')])
        if not hasattr(self, 'ca_certificate'):
            self.ca_certificate = (x509.CertificateBuilder().subject_name(ca_name).issuer_name(ca_name).public_key(ca_key.public_key())
                                   .serial_number(x509.random_serial_number()).not_valid_before(now - datetime.timedelta(minutes=1))
                                   .not_valid_after(now + datetime.timedelta(days=365)).add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
                                   .sign(ca_key, hashes.SHA256()))
        ca = self.ca_certificate
        key = ec.generate_private_key(ec.SECP256R1()) if wrong_key else self.key
        csr = x509.load_pem_x509_csr(base64.b64decode(self.request()['spec']['request']))
        leaf = (x509.CertificateBuilder().subject_name(csr.subject).issuer_name(ca_name).public_key(key.public_key())
                .serial_number(x509.random_serial_number()).not_valid_before(now - datetime.timedelta(minutes=1))
                .not_valid_after(now + datetime.timedelta(days=lifetime))
                .add_extension(csr.extensions[0].value, critical=False)
                .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
                .add_extension(x509.KeyUsage(True, False, False, False, False, False, False, False, False), critical=True)
                .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.CLIENT_AUTH if client_auth else ExtendedKeyUsageOID.SERVER_AUTH]), critical=False)
                .sign(ca_key, hashes.SHA256()))
        return leaf.public_bytes(serialization.Encoding.PEM), ca.public_bytes(serialization.Encoding.PEM)

    def test_signed_certificate_must_match_key_ca_identity_expiry_and_server_only_usage(self):
        proof = self.validate(self.request())
        pem, ca = self.certificates()
        result = policy.validate_certificate(pem, ca, hashlib.sha256(ca).hexdigest(), proof)
        self.assertEqual(result['publicKeySHA256'], proof['publicKeySHA256'])
        for kwargs in [dict(client_auth=True), dict(wrong_key=True), dict(lifetime=0), dict(lifetime=367)]:
            pem, ca = self.certificates(**kwargs)
            with self.subTest(kwargs=kwargs), self.assertRaises(ValueError):
                policy.validate_certificate(pem, ca, hashlib.sha256(ca).hexdigest(), proof)
        with self.assertRaises(ValueError):
            policy.validate_certificate(pem, ca, '0' * 64, proof)
