"""Numeric-only disposable VM failure diagnostics; no raw logs or secrets."""

import json
from pathlib import Path
import re


def parse_counters(data):
    result = {}
    for line in data.splitlines():
        match = re.fullmatch(r'(MemTotal|MemAvailable|SwapTotal|SwapFree|oom_kill):?\s+([0-9]+)(?:\s+kB)?', line)
        if match:
            result[match[1]] = int(match[2])
    return result


def host_counters(proc=Path('/proc')):
    result = {}
    for name in ['meminfo', 'vmstat']:
        try:
            with (proc / name).open('rb') as stream:
                result.update(parse_counters(stream.read(128 * 1024).decode(errors='replace')))
        except OSError:
            pass
    return result


def summarize(guests, before, current):
    codes = [guest.poll() for guest in guests]
    return dict(qemuReturnCodes=codes, host=current,
                hostOomKillsDuringTest=max(0, current.get('oom_kill', 0) - before.get('oom_kill', 0)))


def failure_diagnostics(guests, before, names, probe=None):
    summary = summarize(guests, before, host_counters())
    summary['guests'] = {}
    for name in names:
        try:
            result = probe(name, 'timeout 10s cat /proc/meminfo /proc/vmstat', capture_output=True, text=True) if probe else None
            summary['guests'][name] = dict(reachable=True, counters=parse_counters(result.stdout)) if result else dict(reachable=False)
        except Exception:
            summary['guests'][name] = dict(reachable=False)
    print('Disposable VM failure counters: ' + json.dumps(summary, sort_keys=True), flush=True)
