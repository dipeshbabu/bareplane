#!/usr/local/bin/python3
"""Read one external StorageClass contract; never mutate storage or expose tokens."""

import ipaddress
import json
import os
from pathlib import Path
import re
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

MAX_RESPONSE = 128 * 1024
LABEL = re.compile(r'[a-z0-9](?:[-a-z0-9]*[a-z0-9])?\Z')


class Refusal(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, url):
        raise Refusal('API redirect was refused')


def require(condition, reason):
    if not condition:
        raise Refusal(reason)


def class_name(value):
    return (isinstance(value, str) and 1 <= len(value) <= 253
            and all(1 <= len(part) <= 63 and LABEL.fullmatch(part) for part in value.split('.')))


def inspect(value, expected):
    require(class_name(expected), 'class selector is invalid')
    require(isinstance(value, dict) and value.get('kind') == 'StorageClass'
            and value.get('apiVersion') == 'storage.k8s.io/v1', 'class response has an unexpected identity')
    metadata = value.get('metadata', {})
    require(isinstance(metadata, dict) and metadata.get('name') == expected
            and isinstance(metadata.get('uid'), str) and 1 <= len(metadata['uid']) <= 128
            and not metadata.get('deletionTimestamp'),
            'class is missing, deleting or has changed identity')
    provisioner = value.get('provisioner')
    require(isinstance(provisioner, str) and 1 <= len(provisioner) <= 253
            and provisioner.strip() == provisioner and provisioner != 'kubernetes.io/no-provisioner',
            'class must use a dynamic provisioner')
    require(value.get('reclaimPolicy') == 'Retain', 'class must retain volumes after claim deletion')
    require(value.get('allowVolumeExpansion') is True, 'class must explicitly allow volume expansion')
    require(value.get('volumeBindingMode') in {'Immediate', 'WaitForFirstConsumer'}, 'class binding mode is unsupported')
    return dict(contract='verified', dynamic=True, retained=True, expansion=True,
                backend_health_verified=False, provisioning_performed=False)


def fetch(expected, environment=None, account=Path('/var/run/secrets/kubernetes.io/serviceaccount'), opener=None):
    require(class_name(expected), 'class selector is invalid')
    environment = os.environ if environment is None else environment
    address = ipaddress.ip_address(environment.get('KUBERNETES_SERVICE_HOST', ''))
    port = int(environment.get('KUBERNETES_SERVICE_PORT_HTTPS', '443'))
    require(1 <= port <= 65535, 'API port is invalid')
    host = '[' + str(address) + ']' if address.version == 6 else str(address)
    with (account / 'token').open('rb') as stream:
        token = stream.read(16385)
    require(0 < len(token) <= 16384, 'projected API credential exceeded its bound')
    credential = token.decode('ascii').strip()
    require(credential and '\n' not in credential and '\r' not in credential, 'projected API credential is malformed')
    context = ssl.create_default_context(cafile=str(account / 'ca.crt'))
    request = urllib.request.Request('https://' + host + ':' + str(port)
        + '/apis/storage.k8s.io/v1/storageclasses/' + urllib.parse.quote(expected, safe=''),
        headers={'Authorization': 'Bearer ' + credential, 'Accept': 'application/json'}, method='GET')
    if opener is None:
        transport = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=context), NoRedirect())
        operation = transport.open
    else:
        operation = opener
    with operation(request, timeout=10) as response:
        payload = response.read(MAX_RESPONSE + 1)
    require(len(payload) <= MAX_RESPONSE, 'API response exceeded its bound')
    return inspect(json.loads(payload), expected)


def main():
    try:
        require(len(sys.argv) == 2, 'class arguments are invalid')
        print(json.dumps(fetch(sys.argv[1]), sort_keys=True))
        return 0
    except Refusal as error:
        print('External storage contract refused: ' + str(error), file=sys.stderr)
    except (OSError, ValueError, TypeError, KeyError, AttributeError, urllib.error.URLError):
        print('External storage contract refused: cannot verify the selected class; no API bodies or credentials were exposed.', file=sys.stderr)
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
