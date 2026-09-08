"""Trust-bundle membership tests; the VM verifies real certificate signatures."""

import ssl
import unittest

from metrics_server_acceptance import valid_rotation_bundle


class MetricsTrustTests(unittest.TestCase):
    def pem(self, content):
        # These opaque DER-shaped test values exercise bundle membership only.
        # They cannot establish a TLS connection or authorize a real certificate.
        return ssl.DER_cert_to_PEM_cert(content).encode()

    def test_rotation_accepts_only_current_and_known_previous_certificates(self):
        previous, current, foreign = b'previous-public-cert', b'current-public-cert', b'foreign-public-cert'
        known = {previous, current}
        self.assertTrue(valid_rotation_bundle(self.pem(current), current, known))
        self.assertTrue(valid_rotation_bundle(self.pem(previous) + self.pem(current), current, known))
        self.assertFalse(valid_rotation_bundle(self.pem(previous), current, known))
        self.assertFalse(valid_rotation_bundle(self.pem(current) + self.pem(foreign), current, known))

    def test_empty_duplicate_garbage_and_oversized_bundles_are_refused(self):
        current = b'current-public-cert'
        pem = self.pem(current)
        for bundle in [b'', pem + pem, b'not a certificate\n' + pem, b'A' * 65537, pem.replace(b'CERTIFICATE', b'PRIVATE KEY')]:
            with self.subTest(bundle_length=len(bundle)):
                self.assertFalse(valid_rotation_bundle(bundle, current, {current}))
