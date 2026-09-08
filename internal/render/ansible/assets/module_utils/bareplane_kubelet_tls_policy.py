"""Strict public serving-CSR policy; inventory and node UID come from SSH proof.

This module never signs, approves, reads private keys, or changes cluster state.
Callers must recheck the same node UID and CSR resourceVersion before an exact
approval-subresource update, and keep a private intent receipt across retries.
"""

import base64
import copy
import datetime
import hashlib
import ipaddress
import json
import re

from cryptography import x509
from cryptography.exceptions import InvalidSignature, UnsupportedAlgorithm
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec, padding, rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, ExtensionOID, NameOID


SIGNER = 'kubernetes.io/kubelet-serving'
CREDENTIAL_ID = 'authentication.kubernetes.io/credential-id'
LIMIT = 16384


def require(condition, message):
    if not condition:
        raise ValueError(message)


def public_key_digest(key):
    return hashlib.sha256(key.public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)).hexdigest()


def certificate_time(certificate, field):
    # Ansible supports older cryptography; prefer its newer timezone-aware API
    # without evaluating deprecated properties on current installations.
    if hasattr(certificate, field + '_utc'):
        return getattr(certificate, field + '_utc')
    return getattr(certificate, field).replace(tzinfo=datetime.timezone.utc)


def validate_subject(subject, node):
    values = [(attribute.oid, attribute.value) for attribute in subject]
    require(len(values) == 2 and set(values) == {(NameOID.ORGANIZATION_NAME, 'system:nodes'),
                                               (NameOID.COMMON_NAME, 'system:node:' + node)},
            'Serving subject differs from the authenticated inventory node')


def validate_sans(extension, node, address):
    names = list(extension.value)
    require(len(names) == 2 and set(names) == {x509.DNSName(node), x509.IPAddress(ipaddress.ip_address(address))},
            'Serving SANs must be exactly the inventory hostname and verified node address')


def validate_request(resource, node, node_uid, address, credential_id=None):
    """Return a bounded public identity proof, never a boolean approval shortcut."""
    require(isinstance(node, str) and re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', node)
            and isinstance(node_uid, str) and re.fullmatch(r'[a-zA-Z0-9-]{1,128}', node_uid), 'Invalid trusted node identity')
    parsed_ip = ipaddress.ip_address(address)
    require(parsed_ip.version == 4 and not parsed_ip.is_loopback and not parsed_ip.is_unspecified and not parsed_ip.is_multicast,
            'Serving address must be the verified IPv4 node address')
    require(resource.get('apiVersion') == 'certificates.k8s.io/v1' and resource.get('kind') == 'CertificateSigningRequest',
            'Unexpected serving CSR API type')
    metadata, spec = resource['metadata'], resource['spec']
    require(set(spec) <= {'request', 'signerName', 'username', 'groups', 'uid', 'extra', 'usages', 'expirationSeconds'},
            'Serving CSR has unsupported specification fields')
    require(re.fullmatch(r'[a-z0-9][a-z0-9.-]{0,252}', metadata['name'])
            and re.fullmatch(r'[a-zA-Z0-9-]{1,128}', metadata['uid'])
            and re.fullmatch(r'[0-9]{1,32}', metadata['resourceVersion'])
            and not metadata.get('deletionTimestamp') and not metadata.get('ownerReferences') and not metadata.get('finalizers'),
            'Serving CSR identity is missing, redirected, or terminating')
    require(spec.get('signerName') == SIGNER and spec.get('username') == 'system:node:' + node,
            'Serving CSR signer or authenticated requestor differs')
    require(credential_id is None or re.fullmatch('X509SHA256=[0-9a-f]{64}', credential_id), 'Invalid authenticated client certificate identity')
    expected_extra = {} if credential_id is None else {CREDENTIAL_ID: [credential_id]}
    require(sorted(spec.get('groups', [])) == ['system:authenticated', 'system:nodes'] and spec.get('extra', {}) == expected_extra and not spec.get('uid'),
            'Serving CSR has unexpected authenticated groups or identity extensions')
    usages = spec.get('usages', [])
    require(sorted(usages) in [['digital signature', 'server auth'], ['digital signature', 'key encipherment', 'server auth']],
            'Serving CSR usages must not grant client authentication or signing authority')
    lifetime = spec.get('expirationSeconds', 31536000)
    require(type(lifetime) is int and 600 <= lifetime <= 31536000, 'Serving CSR lifetime exceeds the reviewed contract')
    encoded = spec.get('request', '')
    require(isinstance(encoded, str) and 0 < len(encoded) <= LIMIT, 'Serving CSR exceeds the bounded request limit')
    try:
        pem = base64.b64decode(encoded, validate=True)
        csr = x509.load_pem_x509_csr(pem)
        require(csr.public_bytes(serialization.Encoding.PEM) == pem, 'Serving CSR must contain exactly one canonical PEM request')
        require(csr.is_signature_valid and csr.signature_hash_algorithm.name in {'sha256', 'sha384', 'sha512'},
                'Serving CSR signature is invalid or weak')
        key = csr.public_key()
        strong_rsa = isinstance(key, rsa.RSAPublicKey) and 2048 <= key.key_size <= 8192 and key.public_numbers().e == 65537
        strong_ec = isinstance(key, ec.EllipticCurvePublicKey) and key.curve.name in {'secp256r1', 'secp384r1', 'secp521r1'}
        require(strong_rsa or strong_ec, 'Serving CSR public key does not meet the reviewed security policy')
        require(strong_rsa or 'key encipherment' not in usages, 'EC serving CSR requests an unrelated RSA key usage')
        validate_subject(csr.subject, node)
        extensions = list(csr.extensions)
        require(len(extensions) == 1 and extensions[0].oid == ExtensionOID.SUBJECT_ALTERNATIVE_NAME,
                'Serving CSR contains unsupported extensions or certificate authority requests')
        validate_sans(extensions[0], node, address)
        # The sole supported attribute carries the reviewed SAN extension.
        require([attribute.oid.dotted_string for attribute in csr.attributes] == ['1.2.840.113549.1.9.14'],
                'Serving CSR contains unsupported attributes')
    except (ValueError, TypeError, AttributeError, UnsupportedAlgorithm, x509.DuplicateExtension):
        # Crypto parser errors may echo untrusted values; expose no body data.
        raise ValueError('Serving CSR cryptographic identity failed the strict inventory policy') from None
    conditions = resource.get('status', {}).get('conditions', [])
    require(not conditions and not resource.get('status', {}).get('certificate'),
            'Only a pending unapproved serving CSR can begin a new approval intent')
    return dict(node=node, nodeUID=node_uid, address=str(parsed_ip), credentialID=credential_id, csrName=metadata['name'], csrUID=metadata['uid'],
                resourceVersion=metadata['resourceVersion'], requestSHA256=hashlib.sha256(pem).hexdigest(),
                specSHA256=hashlib.sha256(json.dumps(spec, sort_keys=True, separators=(',', ':')).encode()).hexdigest(),
                publicKeySHA256=public_key_digest(key))


def validate_node(node, proof):
    metadata = node['metadata']
    require(metadata.get('name') == proof['node'] and metadata.get('uid') == proof['nodeUID'] and not metadata.get('deletionTimestamp'),
            'Node identity changed before serving CSR approval')
    expected_addresses = {('Hostname', proof['node']), ('InternalIP', proof['address'])}
    addresses = [(item['type'], item['address']) for item in node['status']['addresses']]
    require(len(addresses) == len(expected_addresses) and set(addresses) == expected_addresses,
            'Node addresses changed from the out-of-band verified inventory')


def approval_document(resource, proof, node):
    """Construct an exact resourceVersion-conditional update after saved intent."""
    validate_node(node, proof)
    require(validate_request(resource, proof['node'], proof['nodeUID'], proof['address'], proof['credentialID']) == proof,
            'Serving CSR changed after the approval intent was recorded')
    result = copy.deepcopy(resource)
    timestamp = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds').replace('+00:00', 'Z')
    result['status'] = {'conditions': [dict(type='Approved', status='True', reason='BareplaneInventoryVerified',
                                          message='Exact inventory, node UID, requestor and serving key policy verified',
                                          lastUpdateTime=timestamp, lastTransitionTime=timestamp)]}
    return result


def validate_certificate(pem, ca_pem, ca_sha256, proof, now=None):
    """Verify a single issued public leaf against the pinned bootstrap CA."""
    require(isinstance(pem, bytes) and 0 < len(pem) <= LIMIT and isinstance(ca_pem, bytes) and 0 < len(ca_pem) <= LIMIT,
            'Serving certificate or CA exceeds its bounded size')
    require(hashlib.sha256(ca_pem).hexdigest() == ca_sha256, 'Bootstrap CA changed during serving certificate verification')
    try:
        certificate, ca = x509.load_pem_x509_certificate(pem), x509.load_pem_x509_certificate(ca_pem)
        require(certificate.public_bytes(serialization.Encoding.PEM) == pem and ca.public_bytes(serialization.Encoding.PEM) == ca_pem,
                'Serving identity must use canonical single certificates')
        require(ca.extensions.get_extension_for_oid(ExtensionOID.BASIC_CONSTRAINTS).value.ca and certificate.issuer == ca.subject,
                'Serving certificate was not issued by the bootstrap CA')
        algorithm = certificate.signature_hash_algorithm
        require(algorithm.name in {'sha256', 'sha384', 'sha512'}, 'Serving certificate signature is weak')
        ca_key = ca.public_key()
        if isinstance(ca_key, rsa.RSAPublicKey):
            ca_key.verify(certificate.signature, certificate.tbs_certificate_bytes, padding.PKCS1v15(), algorithm)
        elif isinstance(ca_key, ec.EllipticCurvePublicKey):
            ca_key.verify(certificate.signature, certificate.tbs_certificate_bytes, ec.ECDSA(algorithm))
        else:
            raise ValueError('Unsupported bootstrap CA public key')
        require(public_key_digest(certificate.public_key()) == proof['publicKeySHA256'], 'Issued serving key differs from the approved request')
        validate_subject(certificate.subject, proof['node'])
        validate_sans(certificate.extensions.get_extension_for_oid(ExtensionOID.SUBJECT_ALTERNATIVE_NAME), proof['node'], proof['address'])
        constraints = certificate.extensions.get_extension_for_oid(ExtensionOID.BASIC_CONSTRAINTS).value
        require(not constraints.ca, 'Serving certificate must not have CA authority')
        usage = certificate.extensions.get_extension_for_oid(ExtensionOID.KEY_USAGE).value
        require(usage.digital_signature and not any([usage.content_commitment, usage.data_encipherment, usage.key_agreement,
                                                     usage.key_cert_sign, usage.crl_sign]), 'Serving certificate has unrelated key privileges')
        extended = certificate.extensions.get_extension_for_oid(ExtensionOID.EXTENDED_KEY_USAGE).value
        require(list(extended) == [ExtendedKeyUsageOID.SERVER_AUTH], 'Serving certificate must not grant client authentication')
        allowed = {ExtensionOID.BASIC_CONSTRAINTS, ExtensionOID.KEY_USAGE, ExtensionOID.EXTENDED_KEY_USAGE,
                   ExtensionOID.SUBJECT_ALTERNATIVE_NAME, ExtensionOID.SUBJECT_KEY_IDENTIFIER, ExtensionOID.AUTHORITY_KEY_IDENTIFIER}
        require(all(extension.oid in allowed for extension in certificate.extensions), 'Serving certificate has unsupported extensions')
        now = now or datetime.datetime.now(datetime.timezone.utc)
        starts = certificate_time(certificate, 'not_valid_before')
        ends = certificate_time(certificate, 'not_valid_after')
        ca_starts = certificate_time(ca, 'not_valid_before')
        ca_ends = certificate_time(ca, 'not_valid_after')
        require(ca_starts <= now < ca_ends and starts <= now and ends - now >= datetime.timedelta(hours=1)
                and ends - starts <= datetime.timedelta(days=366) and ends <= ca_ends,
                'Serving certificate or CA is expired, premature, overlong, or requires immediate renewal')
    except (ValueError, TypeError, AttributeError, InvalidSignature, UnsupportedAlgorithm, x509.ExtensionNotFound, x509.DuplicateExtension):
        raise ValueError('Issued serving certificate failed strict bootstrap-CA verification') from None
    return dict(certificateSHA256=certificate.fingerprint(hashes.SHA256()).hex(),
                notAfter=ends.isoformat(), publicKeySHA256=proof['publicKeySHA256'])
