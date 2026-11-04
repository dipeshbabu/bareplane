"""Exact-source release policy. Candidate creation never authorizes publication."""

import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import urllib.parse

from verify_candidate import API, verify_jobs, verify_runs, verify_same_attempt

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'hack'))
from release_artifacts import REPOSITORY, SHA, VERSION

WORKFLOW = '.github/workflows/release.yml'
EVIDENCE_FIELDS = {'source_sha', 'operator', 'lab_id', 'tested_at', 'proxmox_version',
                   'first_run', 'rerun', 'failure_recovery', 'decision'}


def validate_context(values, default_branch, now=None):
    now = dt.datetime.now(dt.timezone.utc) if now is None else now
    source, version = values.get('SOURCE_SHA', ''), values.get('RELEASE_VERSION', '')
    expected_ref = 'refs/heads/' + default_branch
    if (values.get('GITHUB_ACTIONS') != 'true' or values.get('GITHUB_EVENT_NAME') != 'workflow_dispatch'
            or values.get('GITHUB_REPOSITORY') != REPOSITORY or values.get('GITHUB_REF') != expected_ref
            or values.get('GITHUB_WORKFLOW_REF') != REPOSITORY + '/' + WORKFLOW + '@' + expected_ref
            or not SHA.fullmatch(source) or source != values.get('GITHUB_SHA')
            or not VERSION.fullmatch(version) or values.get('RELEASE_PUBLISH') not in {'true', 'false'}):
        raise ValueError('Release requests require the trusted default-branch workflow, exact dispatch source and a closed version')
    publish = values['RELEASE_PUBLISH'] == 'true'
    result = dict(source_sha=source, version=version, tag='v' + version, publish=publish,
                  prerelease='-' in version, operator_evidence_sha256='', publishing_performed=False)
    if not publish:
        return result
    raw = values.get('RELEASE_OPERATOR_EVIDENCE', '')
    if not raw or len(raw.encode()) > 16384:
        raise ValueError('Publication requires bounded genuine operator Proxmox evidence')
    evidence = json.loads(raw)
    if (not isinstance(evidence, dict) or set(evidence) != EVIDENCE_FIELDS
            or evidence['source_sha'] != source or evidence['operator'] != values.get('GITHUB_ACTOR')
            or not isinstance(evidence['operator'], str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,38}', evidence['operator'])
            or not isinstance(evidence['lab_id'], str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', evidence['lab_id'])
            or any(evidence[key] != 'passed' for key in ['first_run', 'rerun', 'failure_recovery'])
            or evidence['decision'] != 'approved'):
        raise ValueError('Operator evidence does not approve this exact source and lifecycle')
    reference = json.loads((ROOT / 'docs/compatibility.json').read_bytes())['proxmox_reference']
    if not isinstance(evidence['proxmox_version'], str) or not re.fullmatch(re.escape(reference) + r'(?:\.[0-9]+)?', evidence['proxmox_version']):
        raise ValueError('Operator evidence must identify the reviewed Proxmox reference version')
    tested = dt.datetime.strptime(evidence['tested_at'], '%Y-%m-%dT%H:%M:%SZ').replace(tzinfo=dt.timezone.utc)
    age = now - tested
    if age < dt.timedelta(minutes=-5) or age > dt.timedelta(days=7):
        raise ValueError('Operator evidence is future-dated or older than seven days')
    result['operator_evidence_sha256'] = hashlib.sha256(json.dumps(evidence, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return result


def verify_release_policy(api, values, now=None, settings_api=None):
    default = api.get('')['default_branch']
    request = validate_context(values, default, now)
    source = request['source_sha']
    comparison = api.get('/compare/' + source + '...' + urllib.parse.quote(default, safe=''))
    if comparison.get('status') not in {'ahead', 'identical'} or comparison.get('behind_by') != 0:
        raise ValueError('Release source is outside default-branch history')
    runs = api.collection('/actions/workflows/ci.yml/runs?' + urllib.parse.urlencode(dict(head_sha=source, branch=default, event='push')), 'workflow_runs')
    run = verify_runs(runs, source, default)
    verify_jobs(api.collection('/actions/runs/' + str(run['id']) + '/jobs?filter=latest', 'jobs'))
    verify_same_attempt(run, api.get('/actions/runs/' + str(run['id'])))
    if request['publish']:
        if settings_api is None:
            raise ValueError('Publication requires a separate read-only release-settings token with Administration read access')
        if settings_api.get('/immutable-releases').get('enabled') is not True:
            raise ValueError('Publication requires enabled immutable releases')
        environment = settings_api.get('/environments/release')
        if not any(rule.get('type') == 'required_reviewers' and rule.get('reviewers')
                   for rule in environment.get('protection_rules', [])):
            raise ValueError('Publication requires a release environment with an explicit reviewer')
    return dict(request, ci_run_id=run['id'], ci_attempt=run.get('run_attempt', 1), default_branch=default)


def main():
    token = os.environ.get('GH_TOKEN', '')
    if not token:
        raise ValueError('Release verification requires GitHub read access')
    settings_token = os.environ.get('RELEASE_SETTINGS_TOKEN', '')
    settings_api = API(REPOSITORY, settings_token) if settings_token else None
    result = verify_release_policy(API(REPOSITORY, token), os.environ, settings_api=settings_api)
    output = os.environ.get('GITHUB_OUTPUT')
    if output:
        with open(output, 'a', encoding='utf-8') as stream:
            for name in ['source_sha', 'version', 'tag', 'publish', 'prerelease', 'operator_evidence_sha256', 'ci_run_id', 'ci_attempt']:
                value = str(result[name]).lower() if isinstance(result[name], bool) else str(result[name])
                stream.write(name + '=' + value + '\n')
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, TypeError) as error:
        print(str(error) if isinstance(error, ValueError) else 'Malformed release evidence; refusing publication', file=sys.stderr)
        raise SystemExit(1)
