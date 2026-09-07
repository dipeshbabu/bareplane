#!/usr/bin/python
"""Controller health gate with bounded kubectl checks and disposable probes."""
import hashlib
import ipaddress
import json
import secrets
import signal
import subprocess
import time


IMAGE = 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
CONTROL = 'node-role.kubernetes.io/control-plane'
OUTPUT_LIMIT = 4 * 1024 * 1024


class HealthError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise HealthError(message)


def ready(obj):
    return any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in obj.get('status', {}).get('conditions', []))


class Client:
    def __init__(self, kubeconfig):
        self.kubeconfig = str(kubeconfig)
        self.deadline = time.monotonic() + 600

    def text(self, *args, data=None, timeout=20, cleanup=False):
        if not cleanup:
            timeout = min(timeout, self.deadline - time.monotonic())
            require(timeout > 0, 'The ten-minute bootstrap health deadline was exceeded')
        argv = ['kubectl', '--kubeconfig', self.kubeconfig, '--request-timeout=10s'] + list(args)
        try:
            result = subprocess.run(argv, input=json.dumps(data).encode() if data is not None else b'',
                                    stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout, check=True)
        except (OSError, subprocess.SubprocessError):
            raise HealthError('Kubernetes command failed or timed out; inspect the component without publishing credentials') from None
        require(len(result.stdout) <= OUTPUT_LIMIT, 'Kubernetes response exceeded the allowed size')
        return result.stdout.decode()

    def json(self, *args, **kwargs):
        output = self.text(*args, **kwargs)
        return json.loads(output) if output.strip() else {}

    def create(self, obj):
        return self.json('create', '-f', '-', '-o', 'json', data=obj)


def verify_nodes(nodes, controls, workers, version):
    expected = set(controls + workers)
    require(len(nodes) == len(expected) and {n['metadata']['name'] for n in nodes} == expected,
            'Registered node names/count differ from the desired topology')
    for node in nodes:
        name = node['metadata']['name']
        is_control = name in controls
        require(ready(node), 'A desired node is not Ready')
        require(node['status']['nodeInfo']['kubeletVersion'] == 'v' + version, 'A node is not running the pinned Kubernetes version')
        require((CONTROL in node['metadata'].get('labels', {})) == is_control, 'A node has the wrong control-plane role')
        tainted = any(t.get('key') == CONTROL and t.get('effect') == 'NoSchedule' for t in node.get('spec', {}).get('taints', []))
        require(tainted == is_control, 'A node has the wrong control-plane taint')


def verify_deployment(deployment):
    desired = deployment['spec'].get('replicas', 1)
    status = deployment.get('status', {})
    require(desired > 0 and status.get('observedGeneration', 0) >= deployment['metadata']['generation']
            and all(status.get(field, 0) == desired for field in ['replicas', 'updatedReplicas', 'readyReplicas', 'availableReplicas'])
            and status.get('unavailableReplicas', 0) == 0, 'Deployment is not fully rolled out and available')


def probe_pod(namespace, name, node, command):
    container = dict(name='probe', image=IMAGE, imagePullPolicy='IfNotPresent', command=command,
                     resources=dict(requests=dict(cpu='10m', memory='8Mi'), limits=dict(cpu='100m', memory='32Mi')),
                     securityContext=dict(allowPrivilegeEscalation=False, readOnlyRootFilesystem=True,
                                          capabilities=dict(drop=['ALL'])),
                     volumeMounts=[dict(name='www', mountPath='/www')])
    if name == 'server':
        container['readinessProbe'] = dict(httpGet=dict(path='/', port=8080), initialDelaySeconds=1, periodSeconds=2)
    return dict(apiVersion='v1', kind='Pod', metadata=dict(name=name, namespace=namespace, labels=dict(app='bareplane-health')),
                spec=dict(nodeName=node, restartPolicy='Never', activeDeadlineSeconds=180, terminationGracePeriodSeconds=1,
                          automountServiceAccountToken=False,
                          securityContext=dict(runAsNonRoot=True, runAsUser=65534, runAsGroup=65534, fsGroup=65534,
                                               seccompProfile=dict(type='RuntimeDefault')),
                          tolerations=[dict(key=CONTROL, operator='Exists', effect='NoSchedule')],
                          containers=[container], volumes=[dict(name='www', emptyDir=dict(sizeLimit='2Mi'))]))


def smoke(client, cluster, server_node, client_node, nonce=None):
    nonce = nonce or secrets.token_hex(8)
    require(len(nonce) == 16 and all(c in '0123456789abcdef' for c in nonce), 'Invalid smoke test identifier')
    namespace = 'bareplane-health-' + nonce
    identity = {'bareplane.io/cluster': cluster, 'bareplane.io/health-run': nonce}
    uid = None
    try:
        ns = client.create(dict(apiVersion='v1', kind='Namespace', metadata=dict(name=namespace, annotations=identity,
                           labels={'app.kubernetes.io/managed-by': 'bareplane', 'pod-security.kubernetes.io/enforce': 'restricted'})))
        uid = ns['metadata']['uid']
        require(ns['metadata'].get('annotations') == identity, 'Unexpected smoke namespace ownership')
        client.create(probe_pod(namespace, 'server', server_node, ['sh', '-ec', "printf 'bareplane-health\\n' > /www/index.html; exec httpd -f -p 8080 -h /www"]))
        client.text('-n', namespace, 'wait', '--for=condition=Ready', 'pod/server', '--timeout=90s', timeout=100)
        server = client.json('-n', namespace, 'get', 'pod', 'server', '-o', 'json')
        address = str(ipaddress.ip_address(server['status']['podIP']))
        client.create(dict(apiVersion='v1', kind='Service', metadata=dict(name='probe', namespace=namespace),
                           spec=dict(selector=dict(app='bareplane-health'), ports=[dict(port=8080, targetPort=8080)])))
        # Exclude the client from the Service endpoints; it intentionally has no listener.
        service = client.json('-n', namespace, 'get', 'service', 'probe', '-o', 'json')
        cluster_ip = str(ipaddress.ip_address(service['spec']['clusterIP']))
        pod_url = 'http://' + ('[' + address + ']' if ':' in address else address) + ':8080/'
        service_url = 'http://' + ('[' + cluster_ip + ']' if ':' in cluster_ip else cluster_ip) + ':8080/'
        script = ('check_dns() { for attempt in 1 2 3 4 5; do timeout 5 nslookup "$1" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }; '
                  + 'check_url() { for attempt in 1 2 3 4 5; do test "$(wget -q -T 5 -O - "$1")" = bareplane-health && return 0; sleep 1; done; return 1; }; '
                  + 'check_dns kubernetes.default.svc.cluster.local; '
                  + 'check_dns probe.' + namespace + '.svc.cluster.local; '
                  + 'for url in ' + pod_url + ' ' + service_url + ' http://probe.' + namespace + '.svc.cluster.local:8080/; '
                  + 'do check_url "$url"; done')
        pod = probe_pod(namespace, 'client', client_node, ['sh', '-ec', script])
        pod['metadata']['labels'] = dict(app='bareplane-health-client')
        client.create(pod)
        client.text('-n', namespace, 'wait', '--for=jsonpath={.status.phase}=Succeeded', 'pod/client', '--timeout=90s', timeout=100)
    finally:
        # Also handle an ambiguous namespace-create timeout. An exact random
        # ownership annotation is required before removing any namespace.
        try:
            current = client.json('get', 'namespace', namespace, '--ignore-not-found', '-o', 'json', cleanup=True)
            if current:
                require(current['metadata'].get('annotations') == identity
                        and current['metadata'].get('uid') and (uid is None or current['metadata']['uid'] == uid),
                        'Smoke namespace ownership changed; refusing cleanup')
                client.text('delete', '--raw=/api/v1/namespaces/' + namespace, '-f', '-',
                            data=dict(apiVersion='v1', kind='DeleteOptions', preconditions=dict(uid=current['metadata']['uid'])), cleanup=True)
                client.text('wait', '--for=delete', 'namespace/' + namespace, '--timeout=90s', timeout=100, cleanup=True)
        except (HealthError, KeyError, TypeError, ValueError):
            raise HealthError('Smoke cleanup failed; inspect the owned namespace ' + namespace + ' before handoff') from None


class Health:
    def __init__(self, client, cluster, controls, workers, version):
        self.client, self.cluster, self.controls, self.workers, self.version = client, cluster, controls, workers, version
        self.pods = []

    def api(self):
        require(self.client.text('get', '--raw=/readyz').strip() == 'ok', 'API readiness through the configured VIP failed')

    def nodes(self):
        verify_nodes(self.client.json('get', 'nodes', '-o', 'json')['items'], self.controls, self.workers, self.version)
        self.pods = self.client.json('-n', 'kube-system', 'get', 'pods', '-o', 'json')['items']

    def static_pods(self, component):
        pods = [p for p in self.pods if p['metadata']['name'].startswith(component + '-')]
        require(len(pods) == len(self.controls), 'Static pod count differs from the desired control planes')
        require({p['metadata']['name'] for p in pods} == {component + '-' + n for n in self.controls}, 'Static pod names do not match the desired control planes')
        for pod in pods:
            require(pod['spec']['nodeName'] in self.controls and pod['metadata']['name'] == component + '-' + pod['spec']['nodeName'] and ready(pod)
                    and pod['metadata'].get('annotations', {}).get('kubernetes.io/config.mirror'), 'A required static pod is not healthy or is not a mirror pod')

    def etcd(self):
        self.static_pods('etcd')
        prefix = ['-n', 'kube-system', 'exec', 'etcd-' + self.controls[0], '--', 'etcdctl',
                  '--endpoints=https://127.0.0.1:2379', '--cacert=/etc/kubernetes/pki/etcd/ca.crt',
                  '--cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt', '--key=/etc/kubernetes/pki/etcd/healthcheck-client.key']
        members = self.client.json(*prefix, 'member', 'list', '--write-out=json')['members']
        require(len(members) == len(self.controls) and {m['name'] for m in members} == set(self.controls)
                and all(not m.get('isLearner', False) for m in members), 'Stacked etcd membership differs from the desired control planes')
        endpoints = {url for m in members for url in m.get('clientURLs', [])}
        healthy = self.client.json(*prefix, 'endpoint', 'health', '--cluster', '--write-out=json', timeout=30)
        require(len(healthy) == len(self.controls) and {h['endpoint'] for h in healthy} == endpoints
                and all(h.get('health') is True for h in healthy), 'An etcd member is unhealthy')

    def vip(self):
        self.static_pods('kube-vip')

    def cilium(self):
        require(self.client.text('-n', 'kube-system', 'get', 'daemonset', 'kube-proxy', '--ignore-not-found', '-o', 'name').strip() == '',
                'kube-proxy conflicts with bootstrap-owned Cilium replacement')
        daemon = self.client.json('-n', 'kube-system', 'get', 'daemonset', 'cilium', '-o', 'json')
        status = daemon.get('status', {})
        count = len(self.controls + self.workers)
        require(status.get('observedGeneration', 0) >= daemon['metadata']['generation']
                and all(status.get(field, 0) == count for field in ['desiredNumberScheduled', 'currentNumberScheduled', 'updatedNumberScheduled', 'numberReady', 'numberAvailable'])
                and status.get('numberUnavailable', 0) == 0, 'Cilium DaemonSet is not fully ready on every desired node')
        verify_deployment(self.client.json('-n', 'kube-system', 'get', 'deployment', 'cilium-operator', '-o', 'json'))
        pods = [p for p in self.pods if p['metadata'].get('labels', {}).get('k8s-app') == 'cilium']
        require(len(pods) == count and {p['spec']['nodeName'] for p in pods} == set(self.controls + self.workers), 'Cilium agents do not cover the exact desired topology')
        for pod in sorted(pods, key=lambda p: p['metadata']['name']):
            require(ready(pod), 'A Cilium agent is not Ready')
            state = self.client.json('-n', 'kube-system', 'exec', pod['metadata']['name'], '-c', 'cilium-agent', '--',
                                     'cilium-dbg', 'status', '--output=json', '--timeout=10s')
            require(state.get('cilium', {}).get('state') == 'Ok' and state.get('kubernetes', {}).get('state') == 'Ok', 'Cilium reports an unhealthy agent or Kubernetes connection')

    def dns(self):
        verify_deployment(self.client.json('-n', 'kube-system', 'get', 'deployment', 'coredns', '-o', 'json'))

    def connectivity(self):
        namespaces = self.client.json('get', 'namespaces', '-l', 'app.kubernetes.io/managed-by=bareplane', '-o', 'json')['items']
        for namespace in namespaces:
            metadata = namespace['metadata']
            annotations = metadata.get('annotations', {})
            run = annotations.get('bareplane.io/health-run', '')
            require(not (annotations.get('bareplane.io/cluster') == self.cluster and len(run) == 16
                         and all(c in '0123456789abcdef' for c in run) and metadata['name'] == 'bareplane-health-' + run),
                    'A previous owned smoke namespace remains; complete its cleanup before retrying health')
        smoke(self.client, self.cluster, self.controls[0], (self.workers or self.controls)[-1])

    def run(self):
        results = []
        for name, check in [('api-vip', self.api), ('nodes', self.nodes), ('etcd', self.etcd), ('kube-vip', self.vip),
                            ('cilium', self.cilium), ('coredns', self.dns), ('dns-pod-service', self.connectivity)]:
            try:
                check()
            except HealthError as error:
                results.append(dict(name=name, status='FAIL', message=str(error)))
                return False, results
            except (KeyError, TypeError, ValueError, AttributeError):
                results.append(dict(name=name, status='FAIL', message='Unexpected or malformed Kubernetes response'))
                return False, results
            results.append(dict(name=name, status='PASS', message='verified'))
        return True, results


def main():
    from ansible.module_utils.basic import AnsibleModule
    from ansible.module_utils import bareplane_kubeconfig as credentials
    module = AnsibleModule(argument_spec=dict(
        kubeconfig=dict(type='path', required=True), cluster=dict(type='str', required=True), vip=dict(type='str', required=True),
        version=dict(type='str', required=True), control_planes=dict(type='list', elements='str', required=True),
        workers=dict(type='list', elements='str', required=True),
    ), supports_check_mode=True)

    def interrupted(signum, frame):
        raise HealthError('Bootstrap health verification was interrupted')

    signal.signal(signal.SIGTERM, interrupted)
    try:
        if module.check_mode:
            raise HealthError('Full bootstrap health requires temporary workloads; check mode is unsupported')
        p = module.params
        require(p['control_planes'] and len(set(p['control_planes'] + p['workers'])) == len(p['control_planes'] + p['workers']), 'Health verification requires a valid desired topology')
        path = credentials.check_destination(p['kubeconfig'])
        require(path.exists(), 'Private kubeconfig is unavailable; run the kubeconfig phase after cluster formation')
        with path.open('rb') as stream:
            content = stream.read(credentials.LIMIT + len(credentials.HEADER) + 66)
        doc = credentials.decode(content.split(b'\n', 1)[1])
        ca = credentials.binary(doc['clusters'][0]['cluster']['certificate-authority-data'])
        require(credentials.existing(path, p['cluster'], hashlib.sha256(ca).hexdigest(), credentials.endpoint(p['vip'], 6443)), 'Private kubeconfig is unavailable')
        client = Client(path)
        version = client.json('version', '--client=true', '-o', 'json')
        require(version['clientVersion']['gitVersion'] == 'v' + p['version'], 'Controller kubectl must match the pinned Kubernetes version')
        healthy, results = Health(client, p['cluster'], p['control_planes'], p['workers'], p['version']).run()
        module.exit_json(changed=False, healthy=healthy, results=results)
    except (HealthError, ValueError) as error:
        module.fail_json(msg=str(error))
    except (OSError, KeyError, TypeError, IndexError, AttributeError):
        module.fail_json(msg='Cannot verify private kubeconfig identity or start the bootstrap health gate')


if __name__ == '__main__':
    main()
