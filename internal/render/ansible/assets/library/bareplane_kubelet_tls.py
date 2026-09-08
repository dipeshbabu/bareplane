#!/usr/bin/python
"""Controller-only, inventory-bound serving CSR approval and verified TLS."""

import base64
import copy
import hashlib
import json
from pathlib import Path
import re
import signal
import socket
import ssl
import time

from ansible.module_utils import bareplane_kubeconfig as credentials
from ansible.module_utils import bareplane_kubelet_tls_policy as policy
from ansible.module_utils.bareplane_git_repository import Commands, GitOpsError
from ansible.module_utils.bareplane_kubelet_tls_config import enable_serving_bootstrap
from ansible.module_utils.bareplane_kubelet_tls_state import Record, private_read


def require(condition, message):
    if not condition:
        raise ValueError(message)


def plan_configuration(encoded):
    require(hasattr(policy.x509.CertificateSigningRequest, 'attributes'), 'Controller cryptography 36 or newer is required before node changes')
    require(encoded and len(encoded) <= 90000, 'Invalid bounded kubelet configuration')
    target = enable_serving_bootstrap(base64.b64decode(encoded, validate=True))
    # Ansible redacts occurrences of no_log argument values in module output.
    # When a YAML flag is appended, its base64 can contain the complete original
    # base64 value as a prefix. A distinct encoding preserves the opaque result
    # without weakening input redaction or publishing configuration bytes.
    return {'target_hex': target.hex()}


class Approval:
    def __init__(self, commands, kubeconfig, record, ca, credential_id=None):
        self.commands, self.kubeconfig, self.record, self.ca = commands, str(kubeconfig), record, ca
        self.identity = record.data['identity']
        self.credential_id = credential_id
        if record.original is not None:
            state = record.data['state']
            require(set(state) == {'stage', 'request', 'condition', 'certificate'} and state['stage'] in {'pending', 'approved', 'ready'}
                    and isinstance(state['request'], dict) and isinstance(state['condition'], dict), 'Invalid serving approval receipt')
            request = state['request']
            require(all(request[key] == self.identity[key] for key in ['node', 'nodeUID', 'address']), 'Serving approval belongs to another node')
            require(re.fullmatch(r'[a-z0-9][a-z0-9.-]{0,252}', request['csrName'])
                    and re.fullmatch('[0-9a-f]{64}', request['publicKeySHA256']) and re.fullmatch('[0-9a-f]{64}', request['requestSHA256']),
                    'Invalid serving approval identity')

    def api(self, *args, data=None):
        output = self.commands.run(['kubectl', '--kubeconfig', self.kubeconfig, '--request-timeout=10s', *args],
                                   data=json.dumps(data).encode() if data is not None else b'', limit=2 * 1024 * 1024)
        return json.loads(output) if output.strip() else {}

    def node(self):
        node = self.api('get', 'node', self.identity['node'], '-o', 'json')
        policy.validate_node(node, self.identity)
        return node

    def csr(self, name):
        return self.api('get', 'csr', name, '-o', 'json')

    def verify_approved(self, resource):
        state = self.record.data['state']
        clone = copy.deepcopy(resource)
        clone.pop('status', None)
        # The server changes resourceVersion on approval. All immutable public
        # request fields and UID still have to match the pre-write proof.
        clone['metadata']['resourceVersion'] = state['request']['resourceVersion']
        observed = policy.validate_request(clone, self.identity['node'], self.identity['nodeUID'], self.identity['address'], state['request']['credentialID'])
        require(observed == state['request'] and resource.get('status', {}).get('conditions') == [state['condition']],
                'Serving CSR approval differs from the saved exact intent')

    def live_certificate(self, request):
        context = ssl.create_default_context(cadata=self.ca.decode('ascii'))
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        with socket.create_connection((self.identity['address'], 10250), timeout=10) as connection:
            with context.wrap_socket(connection, server_hostname=self.identity['address']) as secure:
                pem = ssl.DER_cert_to_PEM_cert(secure.getpeercert(binary_form=True)).encode('ascii')
        return policy.validate_certificate(pem, self.ca, self.identity['caSHA256'], request)

    def reconcile(self):
        self.node()
        state = self.record.data['state']
        if not state or state['stage'] == 'ready':
            listed = self.api('get', 'csr', '--field-selector=spec.signerName=' + policy.SIGNER, '-o', 'json')['items']
            require(len(listed) <= 256, 'Serving CSR inventory exceeds the bounded review limit')
            pending = [resource for resource in listed if resource.get('spec', {}).get('username') == 'system:node:' + self.identity['node']
                       and not resource.get('status', {}).get('conditions') and not resource.get('status', {}).get('certificate')]
            require(len(pending) <= 1, 'Multiple pending requests for one node require explicit ownership review')
            if not pending and state:
                live = self.live_certificate(state['request'])
                require(live == state['certificate'], 'Live serving certificate changed outside the recorded renewal')
                self.node()
                return False
            if not pending:
                return None  # Kubelet may not have submitted its initial CSR yet.
            resource = pending[0]
            proof = policy.validate_request(resource, self.identity['node'], self.identity['nodeUID'], self.identity['address'], self.credential_id)
            update = policy.approval_document(resource, proof, self.node())
            self.record.save(dict(stage='pending', request=proof, condition=update['status']['conditions'][0], certificate=None))
            state = self.record.data['state']
        resource = self.csr(state['request']['csrName'])
        if state['stage'] == 'pending':
            if not resource.get('status', {}).get('conditions') and not resource.get('status', {}).get('certificate'):
                update = policy.approval_document(resource, state['request'], self.node())
                update['status']['conditions'] = [state['condition']]
                path = '/apis/certificates.k8s.io/v1/certificatesigningrequests/' + state['request']['csrName'] + '/approval'
                try:
                    self.api('replace', '--raw=' + path, '-f', '-', data=update)
                except GitOpsError:
                    # An interrupted acknowledgement may follow a successful
                    # update. Read back this exact UID/request/condition only.
                    pass
                resource = self.csr(state['request']['csrName'])
            self.verify_approved(resource)
            self.node()
            self.record.save(dict(state, stage='approved'))
            state = self.record.data['state']
        deadline = time.monotonic() + 180
        while True:
            resource = self.csr(state['request']['csrName'])
            self.verify_approved(resource)
            encoded = resource.get('status', {}).get('certificate')
            if encoded:
                require(len(encoded) <= policy.LIMIT, 'Issued serving certificate exceeds its size limit')
                issued = policy.validate_certificate(base64.b64decode(encoded, validate=True), self.ca, self.identity['caSHA256'], state['request'])
                try:
                    live = self.live_certificate(state['request'])
                except (OSError, ValueError):
                    live = None
                if live == issued:
                    self.node()
                    self.record.save(dict(state, stage='ready', certificate=issued))
                    return True
            require(time.monotonic() < deadline, 'Serving certificate issuance or verified kubelet TLS did not become ready')
            time.sleep(2)


def main():
    from ansible.module_utils.basic import AnsibleModule
    module = AnsibleModule(argument_spec=dict(
        operation=dict(type='str', choices=['plan', 'approve'], required=True), approved=dict(type='bool', default=False),
        configuration=dict(type='str', no_log=True), identity=dict(type='dict'),
        kubeconfig=dict(type='path'), vip=dict(type='str'),
    ), supports_check_mode=True)

    def interrupted(signum, frame):
        raise ValueError('Serving TLS maintenance interrupted; retain exact pending intent')

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        p = module.params
        if p['operation'] == 'plan':
            module.exit_json(changed=False, **plan_configuration(p['configuration']))
        require(p['approved'] and not module.check_mode, 'Serving CSR approval requires explicit maintenance approval')
        identity = p['identity']
        require(set(identity) == {'cluster', 'node', 'nodeUID', 'address', 'caSHA256', 'machineID', 'credentialID'}
                and re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', identity['node'])
                and re.fullmatch('[0-9a-f]{64}', identity['caSHA256']) and re.fullmatch('[0-9a-f]{32}', identity['machineID'])
                and re.fullmatch('X509SHA256=[0-9a-f]{64}', identity['credentialID']),
                'Invalid authenticated serving-TLS inventory proof')
        kubeconfig = credentials.check_destination(p['kubeconfig'])
        content = private_read(kubeconfig)
        document = credentials.decode(content.split(b'\n', 1)[1])
        ca = credentials.binary(document['clusters'][0]['cluster']['certificate-authority-data'])
        require(hashlib.sha256(ca).hexdigest() == identity['caSHA256'] and credentials.existing(
            kubeconfig, identity['cluster'], identity['caSHA256'], credentials.endpoint(p['vip'], 6443)), 'Private controller credential or bootstrap CA changed')
        # A destructive reset creates a new CA. Keep prior public audit receipts
        # without letting them authorize or block an unrelated new cluster.
        stable_identity = {key: value for key, value in identity.items() if key != 'credentialID'}
        record = Record(kubeconfig.parent / ('kubelet-tls-' + identity['node'] + '-' + identity['caSHA256'] + '.json'), stable_identity)
        approval = Approval(Commands(kubeconfig.parent, timeout=600), kubeconfig, record, ca, identity['credentialID'])
        deadline = time.monotonic() + 180
        while True:
            changed = approval.reconcile()
            if changed is not None:
                module.exit_json(changed=changed, ready=True, certificate=record.data['state']['certificate'])
            require(time.monotonic() < deadline, 'Owned kubelet did not submit a serving CSR within the bounded deadline')
            time.sleep(2)
    except (ValueError, KeyError, TypeError, AttributeError, OSError, GitOpsError, RecursionError):
        module.fail_json(msg='Serving TLS approval or verification failed; no blanket approval was attempted; inspect private intent and retry the approved maintenance command')


if __name__ == '__main__':
    main()
