"""Fake-client health gate and real command-boundary regression coverage."""
import copy
import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/library/bareplane_health.py'
spec = importlib.util.spec_from_file_location('health', source)
health = importlib.util.module_from_spec(spec)
spec.loader.exec_module(health)


def node(name, control):
    return dict(metadata=dict(name=name, labels={health.CONTROL: ''} if control else {}),
                spec=dict(taints=[dict(key=health.CONTROL, effect='NoSchedule')] if control else []),
                status=dict(nodeInfo=dict(kubeletVersion='v1.36.4'), conditions=[dict(type='Ready', status='True')]))


def deployment():
    return dict(metadata=dict(generation=2), spec=dict(replicas=1), status=dict(observedGeneration=2,
                replicas=1, updatedReplicas=1, readyReplicas=1, availableReplicas=1))


class FakeClient:
    def __init__(self, controls, workers):
        self.controls, self.workers = controls, workers
        self.nodes = [node(n, n in controls) for n in controls + workers]
        self.pods = []
        for name in controls:
            for component in ['etcd', 'kube-vip']:
                self.pods.append(dict(metadata=dict(name=component + '-' + name, annotations={'kubernetes.io/config.mirror': 'owned'}),
                                      spec=dict(nodeName=name), status=dict(conditions=[dict(type='Ready', status='True')])))
        for name in controls + workers:
            self.pods.append(dict(metadata=dict(name='cilium-' + name, labels={'k8s-app': 'cilium'}),
                                  spec=dict(nodeName=name), status=dict(conditions=[dict(type='Ready', status='True')])))
        self.members = [dict(name=n, clientURLs=['https://' + n + ':2379']) for n in controls]
        self.namespace = None
        self.created = []
        self.calls = []
        self.fail = None
        self.replace_uid = False
        self.foreign_owner = False
        self.preexisting = []

    def text(self, *args, **kwargs):
        self.calls.append((args, kwargs))
        if '--raw=/readyz' in args:
            if self.fail == 'api':
                raise health.HealthError('fixture API failure')
            return 'ok'
        if 'kube-proxy' in args:
            return 'daemonset/kube-proxy' if self.fail == 'proxy' else ''
        if 'pod/client' in args and self.fail == 'client':
            raise health.HealthError('fixture connectivity timeout')
        if args[0] == 'delete':
            if self.fail == 'cleanup':
                raise health.HealthError('fixture cleanup failure')
            self.namespace = None
        return ''

    def json(self, *args, **kwargs):
        self.calls.append((args, kwargs))
        if args[:2] == ('get', 'nodes'):
            return dict(items=self.nodes)
        if 'pods' in args:
            return dict(items=self.pods)
        if 'member' in args:
            return dict(members=self.members)
        if 'endpoint' in args:
            return [dict(endpoint=m['clientURLs'][0], health=self.fail != 'etcd') for m in self.members]
        if 'daemonset' in args:
            count = len(self.nodes)
            return dict(metadata=dict(generation=1), status=dict(observedGeneration=1, desiredNumberScheduled=count,
                        currentNumberScheduled=count, updatedNumberScheduled=count, numberReady=count, numberAvailable=count))
        if 'deployment' in args:
            result = deployment()
            if 'coredns' in args and self.fail == 'dns':
                result['status']['readyReplicas'] = 0
            return result
        if 'cilium-dbg' in args:
            return dict(cilium=dict(state='Failure' if self.fail == 'cilium' else 'Ok'), kubernetes=dict(state='Ok'))
        if 'pod' in args:
            return dict(status=dict(podIP='10.244.0.5'))
        if 'service' in args:
            return dict(spec=dict(clusterIP='10.96.0.10'))
        if 'namespaces' in args:
            return dict(items=self.preexisting)
        if 'namespace' in args:
            result = copy.deepcopy(self.namespace)
            if result and self.replace_uid:
                result['metadata']['uid'] = 'replacement'
            if result and self.foreign_owner:
                result['metadata']['annotations']['bareplane.io/health-run'] = 'foreign'
            return result or {}
        raise AssertionError('Unexpected fake-client request: ' + repr(args))

    def create(self, obj):
        self.created.append(copy.deepcopy(obj))
        if obj['kind'] == 'Namespace':
            self.namespace = copy.deepcopy(obj)
            self.namespace['metadata']['uid'] = 'created-uid'
            if self.fail == 'ambiguous-create':
                raise health.HealthError('fixture namespace create timeout')
            return copy.deepcopy(self.namespace)
        return obj


class HealthTests(unittest.TestCase):
    def test_single_and_ha_mixed_topologies(self):
        for controls, workers in [(['cp1'], []), (['cp1', 'cp2', 'cp3'], []), (['cp1', 'cp2', 'cp3'], ['worker1', 'worker2'])]:
            with self.subTest(controls=controls, workers=workers):
                client = FakeClient(controls, workers)
                healthy, results = health.Health(client, 'lab', controls, workers, '1.36.4').run()
                self.assertTrue(healthy, results)
                self.assertEqual([r['name'] for r in results], ['api-vip', 'nodes', 'etcd', 'kube-vip', 'cilium', 'coredns', 'dns-pod-service'])
                self.assertIsNone(client.namespace)
                probes = [obj for obj in client.created if obj['kind'] == 'Pod']
                self.assertEqual(probes[0]['spec']['nodeName'], controls[0])
                self.assertEqual(probes[1]['spec']['nodeName'], (workers or controls)[-1])

    def test_fail_fast_before_smoke_on_unhealthy_components(self):
        for failure, expected in [('api', 'api-vip'), ('etcd', 'etcd'), ('cilium', 'cilium'), ('proxy', 'cilium'), ('dns', 'coredns')]:
            client = FakeClient(['cp1'], ['worker1'])
            client.fail = failure
            healthy, results = health.Health(client, 'lab', ['cp1'], ['worker1'], '1.36.4').run()
            self.assertFalse(healthy)
            self.assertEqual(results[-1]['name'], expected)
            self.assertEqual(results[-1]['status'], 'FAIL')
            self.assertEqual(client.created, [])

    def test_missing_extra_wrong_role_version_and_not_ready_nodes(self):
        original = [node('cp1', True), node('worker1', False)]
        cases = [original[:1], original + [node('extra', False)]]
        for section, field, value in [('metadata', 'labels', {}), ('spec', 'taints', []),
                                      ('status', 'conditions', []), ('status', 'nodeInfo', dict(kubeletVersion='v1.35.8'))]:
            nodes = copy.deepcopy(original)
            nodes[0][section][field] = value
            cases.append(nodes)
        for nodes in cases:
            with self.subTest(nodes=nodes), self.assertRaises(health.HealthError):
                health.verify_nodes(nodes, ['cp1'], ['worker1'], '1.36.4')

    def test_static_pods_must_match_node_placement(self):
        client = FakeClient(['cp1', 'cp2', 'cp3'], [])
        client.pods[0]['spec']['nodeName'] = 'cp2'
        gate = health.Health(client, 'lab', client.controls, [], '1.36.4')
        gate.nodes()
        with self.assertRaises(health.HealthError):
            gate.static_pods('etcd')

    def test_etcd_learners_or_missing_members_are_refused(self):
        for learner in [False, True]:
            client = FakeClient(['cp1', 'cp2', 'cp3'], [])
            if learner:
                client.members[1]['isLearner'] = True
            else:
                client.members.pop()
            healthy, results = health.Health(client, 'lab', client.controls, [], '1.36.4').run()
            self.assertFalse(healthy)
            self.assertEqual(results[-1]['name'], 'etcd')

    def test_pending_rollouts_and_zero_replica_deployments_fail(self):
        for field, value in [('observedGeneration', 1), ('updatedReplicas', 0), ('availableReplicas', 0), ('unavailableReplicas', 1)]:
            obj = deployment()
            obj['status'][field] = value
            with self.subTest(field=field), self.assertRaises(health.HealthError):
                health.verify_deployment(obj)
        obj = deployment()
        obj['spec']['replicas'] = 0
        with self.assertRaises(health.HealthError):
            health.verify_deployment(obj)

    def test_probe_failure_and_ambiguous_create_still_cleanup(self):
        for failure in ['client', 'ambiguous-create']:
            client = FakeClient(['cp1'], [])
            client.fail = failure
            with self.subTest(failure=failure), self.assertRaises(health.HealthError):
                health.smoke(client, 'lab', 'cp1', 'cp1')
            self.assertIsNone(client.namespace)
            deletion = [kwargs['data'] for args, kwargs in client.calls if args[0] == 'delete']
            self.assertEqual(deletion[0]['preconditions'], dict(uid='created-uid'))

    def test_cleanup_never_deletes_foreign_or_recreated_namespace(self):
        for field in ['foreign_owner', 'replace_uid']:
            client = FakeClient(['cp1'], [])
            setattr(client, field, True)
            with self.assertRaisesRegex(health.HealthError, 'cleanup failed'):
                health.smoke(client, 'lab', 'cp1', 'cp1')
            self.assertFalse(any(args[0] == 'delete' for args, _ in client.calls))

    def test_cleanup_failure_blocks_handoff(self):
        client = FakeClient(['cp1'], [])
        client.fail = 'cleanup'
        healthy, results = health.Health(client, 'lab', ['cp1'], [], '1.36.4').run()
        self.assertFalse(healthy)
        self.assertIn('Smoke cleanup failed', results[-1]['message'])

    def test_leftover_owned_probe_namespace_blocks_a_new_run(self):
        client = FakeClient(['cp1'], [])
        nonce = '0123456789abcdef'
        client.preexisting = [dict(metadata=dict(name='bareplane-health-' + nonce,
                              annotations={'bareplane.io/cluster': 'lab', 'bareplane.io/health-run': nonce}))]
        healthy, results = health.Health(client, 'lab', ['cp1'], [], '1.36.4').run()
        self.assertFalse(healthy)
        self.assertIn('previous owned smoke namespace remains', results[-1]['message'])
        self.assertEqual(client.created, [])

    def test_probes_are_short_lived_restricted_and_digest_pinned(self):
        pod = health.probe_pod('namespace', 'server', 'cp1', ['httpd'])
        self.assertEqual(pod['spec']['activeDeadlineSeconds'], 180)
        self.assertFalse(pod['spec']['automountServiceAccountToken'])
        self.assertTrue(pod['spec']['securityContext']['runAsNonRoot'])
        self.assertIn('@sha256:', pod['spec']['containers'][0]['image'])
        self.assertEqual(pod['spec']['containers'][0]['securityContext']['capabilities']['drop'], ['ALL'])

    def test_command_timeouts_and_errors_do_not_echo_private_output(self):
        secret = b'DO-NOT-PRINT-PRIVATE-CREDENTIALS'
        error = subprocess.CalledProcessError(1, ['kubectl'], output=secret, stderr=secret)
        with patch.object(health.subprocess, 'run', side_effect=error) as run, self.assertRaises(health.HealthError) as caught:
            health.Client('/private/admin.conf').text('get', '--raw=/readyz')
        self.assertNotIn(secret.decode(), str(caught.exception))
        self.assertEqual(run.call_args.kwargs['timeout'], 20)
        self.assertIn('--kubeconfig', run.call_args.args[0])

    def test_overall_deadline_does_not_disable_cleanup_budget(self):
        client = health.Client('/private/admin.conf')
        client.deadline = 0
        with patch.object(health.subprocess, 'run') as command, self.assertRaisesRegex(health.HealthError, 'ten-minute'):
            client.text('get', '--raw=/readyz')
        command.assert_not_called()
        result = subprocess.CompletedProcess(['kubectl'], 0, stdout=b'')
        with patch.object(health.subprocess, 'run', return_value=result) as command:
            client.text('delete', '--raw=/api/v1/namespaces/test', cleanup=True)
        self.assertEqual(command.call_args.kwargs['timeout'], 20)


if __name__ == '__main__':
    unittest.main()
