import subprocess
import unittest
from unittest.mock import patch

from observability_acceptance import check_prometheus_rules


class ObservabilityRuleRunnerTests(unittest.TestCase):
    def test_rule_runner_uses_writable_disposable_volume_without_changing_container_security(self):
        result = subprocess.CompletedProcess([], 0, b'SUCCESS', b'')
        with patch('observability_acceptance.subprocess.run', return_value=result) as run:
            check_prometheus_rules(['kubectl', '--kubeconfig', '/private/config'], b'public-vectors')
        args = run.call_args.args[0]
        self.assertEqual(args[-6:], ['/bin/env', 'TMPDIR=/prometheus', '/bin/promtool', 'test', 'rules', '/dev/stdin'])
        self.assertEqual(run.call_args.kwargs['input'], b'public-vectors')
        self.assertEqual(run.call_args.kwargs['timeout'], 90)

    def test_failed_rule_assertions_fail_acceptance(self):
        result = subprocess.CompletedProcess([], 1, b'', b'public rule failure')
        with patch('observability_acceptance.subprocess.run', return_value=result):
            with self.assertRaises(RuntimeError):
                check_prometheus_rules(['kubectl'], b'public-vectors')
