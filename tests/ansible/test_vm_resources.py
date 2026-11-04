"""Disposable diagnostics expose fixed numeric counters, never private output."""

import contextlib
import io
import json
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from vm_resources import failure_diagnostics, parse_counters, summarize


class ResourceDiagnosticsTests(unittest.TestCase):
    def test_only_closed_numeric_kernel_fields_are_reported(self):
        result = parse_counters('MemTotal: 123 kB\nMemAvailable: 45 kB\noom_kill 2\n'
                                'PRIVATE-SENTINEL secret\nMemFree: 999 kB\nSwapFree: not-a-number\n')
        self.assertEqual(result, {'MemTotal': 123, 'MemAvailable': 45, 'oom_kill': 2})
        self.assertNotIn('PRIVATE-SENTINEL', json.dumps(result))

    def test_process_exit_codes_and_oom_delta_survive_guest_connection_failure(self):
        guests = [SimpleNamespace(poll=lambda: -9), SimpleNamespace(poll=lambda: None)]
        self.assertEqual(summarize(guests, {'oom_kill': 4}, {'oom_kill': 5})['hostOomKillsDuringTest'], 1)
        output = io.StringIO()
        def probe(*_args, **_kwargs):
            raise RuntimeError('PRIVATE-SENTINEL connection failure')
        with contextlib.redirect_stdout(output), patch('vm_resources.host_counters', return_value={'oom_kill': 5}):
            failure_diagnostics(guests, {'oom_kill': 4}, ['lab-control-1'], probe)
        self.assertNotIn('PRIVATE-SENTINEL', output.getvalue())
        self.assertIn('"qemuReturnCodes": [-9, null]', output.getvalue())
        self.assertIn('"reachable": false', output.getvalue())


if __name__ == '__main__':
    unittest.main()
