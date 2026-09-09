#!/usr/bin/env python3
"""Read-only exact-source CI prerequisite for release candidates; never publish."""

import datetime
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


REQUIRED = {'Quality', 'Host preparation VM', 'Bootstrap apply VMs', 'Multi-node join VMs',
            'Bootstrap recovery VM', 'Argo installation VM', 'GitOps handoff VM', 'Core cert-manager VM', 'Kubelet serving TLS VMs',
            'Core Metrics Server VM', 'Core DNS VM', 'Core SOPS VM', 'Core Vault VM', 'Core observability VM', 'v0.1 acceptance'}


def verify_runs(runs, commit, default_branch):
    matching = [run for run in runs if run.get('head_sha') == commit and run.get('head_branch') == default_branch and run.get('event') == 'push']
    if not matching:
        raise ValueError('No default-branch CI run exists for this exact source commit')
    latest = max(matching, key=lambda run: run['id'])
    if latest.get('status') != 'completed' or latest.get('conclusion') != 'success':
        raise ValueError('The latest CI run for this source is not a completed success')
    return latest


def verify_jobs(jobs):
    names = [job.get('name') for job in jobs]
    if not REQUIRED <= set(names) or len(names) != len(set(names)):
        raise ValueError('Required CI jobs are missing or ambiguous')
    if any(job.get('status') != 'completed' or job.get('conclusion') != 'success' for job in jobs):
        raise ValueError('Every CI job must succeed; pending, failed, cancelled, or skipped jobs block candidates')


def verify_same_attempt(before, after):
    if any(before.get(key) != after.get(key) for key in ['id', 'head_sha', 'run_attempt']):
        raise ValueError('Candidate CI changed while evidence was being verified')
    if after.get('status') != 'completed' or after.get('conclusion') != 'success':
        raise ValueError('Candidate CI no longer reports a completed success')


class API:
    def __init__(self, repository, token):
        self.base, self.token = 'https://api.github.com/repos/' + repository, token

    def get(self, path):
        request = urllib.request.Request(self.base + path, headers={
            'Authorization': 'Bearer ' + self.token, 'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'bareplane-candidate-ci-gate',
        })
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                data = response.read(8 * 1024 * 1024 + 1)
            if len(data) > 8 * 1024 * 1024:
                raise ValueError('Candidate evidence response exceeds its limit')
            return json.loads(data)
        except (urllib.error.URLError, OSError):
            raise ValueError('Cannot verify candidate CI from GitHub; no response bodies or credentials are logged') from None

    def collection(self, path, key):
        result = []
        for page in range(1, 21):
            separator = '&' if '?' in path else '?'
            payload = self.get(path + separator + urllib.parse.urlencode(dict(per_page=100, page=page)))
            values = payload.get(key)
            if not isinstance(values, list):
                raise ValueError('Candidate CI metadata has an unexpected shape')
            result.extend(values)
            if len(values) < 100:
                return result
        raise ValueError('Candidate evidence pagination exceeds its limit')


def main():
    repository, token, commit = (os.environ.get(key, '') for key in ['GITHUB_REPOSITORY', 'GH_TOKEN', 'SOURCE_SHA'])
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}', repository) or not token or not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('Candidate verification requires a repository, authentication, and an exact 40-character source SHA')
    api = API(repository, token)
    default = api.get('')['default_branch']
    comparison = api.get('/compare/' + commit + '...' + urllib.parse.quote(default, safe=''))
    if comparison.get('status') not in {'ahead', 'identical'} or comparison.get('behind_by') != 0:
        raise ValueError('Candidate source must belong to the current default branch history')
    runs = api.collection('/actions/workflows/ci.yml/runs?' + urllib.parse.urlencode(dict(head_sha=commit, branch=default, event='push')), 'workflow_runs')
    run = verify_runs(runs, commit, default)
    jobs = api.collection('/actions/runs/' + str(run['id']) + '/jobs?filter=latest', 'jobs')
    verify_jobs(jobs)
    verify_same_attempt(run, api.get('/actions/runs/' + str(run['id'])))
    print(json.dumps(dict(source_sha=commit, ci_run_id=run['id'], ci_attempt=run.get('run_attempt', 1),
                          checked_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                          automated_ci='passed', real_proxmox_acceptance='operator evidence still required',
                          publishing_performed=False), sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, TypeError) as error:
        print(str(error) if isinstance(error, ValueError) else 'Malformed candidate evidence; refusing approval', file=sys.stderr)
        raise SystemExit(1)
