#!/usr/bin/env python3
"""Publish only a verified, approved, attested complete release; never overwrite."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

from release_artifacts import REPOSITORY, ReleaseError, require, verify_release

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / '.github/scripts'))
from release_policy import WORKFLOW, verify_release_policy
from verify_candidate import API


class ReleaseAPI:
    def __init__(self, token):
        self.token = token
        self.base = 'https://api.github.com/repos/' + REPOSITORY

    def request(self, method, path, value=None, missing=False):
        data = json.dumps(value).encode() if value is not None else None
        request = urllib.request.Request(self.base + path, data=data, method=method, headers={
            'Authorization': 'Bearer ' + self.token, 'Accept': 'application/vnd.github+json',
            'Content-Type': 'application/json', 'X-GitHub-Api-Version': '2026-03-10',
            'User-Agent': 'bareplane-release-publisher'})
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                payload = response.read(8 * 1024 * 1024 + 1)
            require(len(payload) <= 8 * 1024 * 1024, 'Release API response exceeded its bound')
            return json.loads(payload) if payload else {}
        except urllib.error.HTTPError as error:
            if missing and error.code == 404:
                return None
            raise ReleaseError('Release API refused an operation (HTTP ' + str(error.code) + '); no response body was exposed') from None
        except (urllib.error.URLError, OSError):
            raise ReleaseError('Release API could not complete the operation; inspect any draft/tag before retry') from None

    def get(self, path):
        return self.request('GET', path)

    def collection(self, path, key):
        result = []
        for page in range(1, 21):
            separator = '&' if '?' in path else '?'
            payload = self.get(path + separator + urllib.parse.urlencode(dict(per_page=100, page=page)))
            values = payload[key] if key is not None else payload
            require(isinstance(values, list), 'Release API collection is malformed')
            result.extend(values)
            if len(values) < 100:
                return result
        raise ReleaseError('Release API pagination exceeded its bound')


def gh(args):
    result = subprocess.run(['gh', *args], capture_output=True, timeout=300, check=False)
    require(result.returncode == 0, 'GitHub CLI verification/upload failed; no response body was exposed')


def check_publication(manifest, policy):
    require(policy.get('publish') is True and policy.get('operator_evidence_sha256'), 'Candidate artifacts cannot authorize publication')
    require(manifest['sourceCommit'] == policy['source_sha'] and manifest['version'] == policy['version'],
            'Release artifacts differ from the approved source or version')
    return 'v' + manifest['version']


def verify_assets(assets, files):
    require(isinstance(assets, list) and len(assets) == len(files) and {item['name'] for item in assets} == set(files),
            'Uploaded release asset inventory is incomplete or ambiguous')
    for asset in assets:
        expected = files[asset['name']]
        require(asset.get('state') == 'uploaded' and asset.get('size') == expected['size']
                and asset.get('digest') == 'sha256:' + expected['sha256'], 'Uploaded release asset differs from its verified local bytes')


def publish(directory, values, api, run_gh=gh, settings_api=None):
    directory = Path(directory).resolve()
    manifest = verify_release(directory)
    policy = verify_release_policy(api, values, settings_api=settings_api)
    tag = check_publication(manifest, policy)
    files = {path.name: dict(size=path.stat().st_size, sha256=hashlib.sha256(path.read_bytes()).hexdigest())
             for path in directory.iterdir()}
    for filename in sorted(files):
        arguments = ['attestation', 'verify', str(directory / filename), '--repo', REPOSITORY,
                     '--signer-workflow', REPOSITORY + '/' + WORKFLOW, '--source-digest', policy['source_sha'],
                     '--source-ref', 'refs/heads/' + policy['default_branch'], '--deny-self-hosted-runners']
        run_gh(arguments + ['--predicate-type', 'https://slsa.dev/provenance/v1'])
        if filename.endswith('.tar.gz'):
            run_gh(arguments + ['--predicate-type', 'https://spdx.dev/Document'])
    encoded = urllib.parse.quote(tag, safe='')
    require(api.request('GET', '/git/ref/tags/' + encoded, missing=True) is None, 'Version tag already exists; overwriting is refused')
    releases = api.collection('/releases', None)
    require(not any(item.get('tag_name') == tag for item in releases), 'Version release/draft already exists; inspect it before retrying')
    # Create-only ref publication is atomic. A collision never advances a tag.
    api.request('POST', '/git/refs', dict(ref='refs/tags/' + tag, sha=policy['source_sha']))
    notes = ('Source: ' + policy['source_sha'] + '\n\n'
             'Verified CI run: https://github.com/' + REPOSITORY + '/actions/runs/' + str(policy['ci_run_id']) + '\n\n'
             'Operator evidence digest: ' + policy['operator_evidence_sha256'] + '\n\n'
             'Archives include checksums, SPDX SBOM and full third-party notices. '
             'Verify build provenance with gh attestation verify --repo ' + REPOSITORY + '. '
             'Kubernetes bootstrap requires the documented Linux/WSL controller.')
    draft = api.request('POST', '/releases', dict(tag_name=tag, target_commitish=policy['source_sha'],
        name='Bareplane ' + manifest['version'], body=notes, draft=True, prerelease=policy['prerelease'], make_latest='false'))
    require(draft.get('draft') is True and draft.get('tag_name') == tag and type(draft.get('id')) is int,
            'GitHub did not create the expected private draft')
    run_gh(['release', 'upload', tag, *[str(directory / name) for name in sorted(files)], '--repo', REPOSITORY])
    release_id = draft['id']
    verify_assets(api.collection('/releases/' + str(release_id) + '/assets', None), files)
    final = verify_release_policy(api, values, settings_api=settings_api)
    require((final['ci_run_id'], final['ci_attempt'], final['operator_evidence_sha256'])
            == (policy['ci_run_id'], policy['ci_attempt'], policy['operator_evidence_sha256']),
            'Release evidence changed before publication; draft remains private')
    ref = api.get('/git/ref/tags/' + encoded)
    require(ref.get('object', {}).get('type') == 'commit' and ref['object']['sha'] == policy['source_sha'],
            'Version tag moved; draft remains private')
    published = api.request('PATCH', '/releases/' + str(release_id),
                            dict(draft=False, make_latest='false' if policy['prerelease'] else 'true'))
    require(published.get('draft') is False and published.get('immutable') is True,
            'Release publication did not report the required immutable state; operator inspection is required')
    return dict(tag=tag, source_sha=policy['source_sha'], release_id=release_id, immutable=True, publishing_performed=True)


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    args = parser.parse_args()
    try:
        token = os.environ.get('GH_TOKEN', '')
        require(bool(token), 'Release publication requires authorized GitHub access')
        settings_token = os.environ.get('RELEASE_SETTINGS_TOKEN', '')
        settings_api = API(REPOSITORY, settings_token) if settings_token else None
        print(json.dumps(publish(args.directory, os.environ, ReleaseAPI(token), settings_api=settings_api), sort_keys=True))
    except (ReleaseError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(str(error) if isinstance(error, (ReleaseError, ValueError)) else 'Malformed release state; refusing publication', file=sys.stderr)
        raise SystemExit(1)
