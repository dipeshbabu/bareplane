import importlib.util
from pathlib import Path
import unittest


source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/module_utils/bareplane_kubelet_tls_config.py'
spec = importlib.util.spec_from_file_location('kubelet_tls_config', source)
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


VALID = b'''apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
authorization:
  mode: Webhook
clusterDNS: [10.96.0.10]
rotateCertificates: true
'''


class ServingConfigTests(unittest.TestCase):
    def test_changes_only_serving_flag_and_unchanged_rerun_is_byte_identical(self):
        for suffix in [b'', b'serverTLSBootstrap: false\n', b'serverTLSBootstrap: true\n']:
            target = config.enable_serving_bootstrap(VALID + suffix)
            self.assertEqual(target, VALID + b'serverTLSBootstrap: true\n')
            self.assertEqual(config.enable_serving_bootstrap(target), target)

    def test_ambiguous_yaml_and_unrelated_credential_overrides_are_refused(self):
        for suffix in [b'serverTLSBootstrap: true\nserverTLSBootstrap: false\n', b'serverTLSBootstrap: "false"\n',
                       b'tlsCertFile: /foreign/cert.pem\n', b'tlsPrivateKeyFile: /foreign/key.pem\n',
                       b'readOnlyPort: 10255\n', b'field: &alias {}\nother: *alias\n',
                       b'field: !!str unsafe\n', b'---\n{}\n', b'"serverTLSBootstrap": false\n']:
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                config.enable_serving_bootstrap(VALID + suffix)
        for data in [VALID.replace(b'enabled: false', b'enabled: true'), VALID.replace(b'mode: Webhook', b'mode: AlwaysAllow'),
                     VALID.replace(b'\n', b'\r\n'), VALID.rstrip(), b'A' * (config.LIMIT + 1)]:
            with self.assertRaises(ValueError):
                config.enable_serving_bootstrap(data)
