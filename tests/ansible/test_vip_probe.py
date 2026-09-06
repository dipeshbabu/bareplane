"""Offline tests for interface selection and ARP conflict classification."""
import importlib.util
import ipaddress
from pathlib import Path
import struct
import unittest
from unittest.mock import patch

source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/roles/api_vip/library/bareplane_vip_probe.py'
spec = importlib.util.spec_from_file_location('vip_probe', source)
vip = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vip)


class VIPProbeTests(unittest.TestCase):
    def setUp(self):
        self.addresses = [dict(ifname='ens19', address='02:00:00:00:00:01', flags=['UP'],
                               addr_info=[dict(family='inet', local='10.0.0.10', prefixlen=24)])]
        self.routes = [dict(dev='ens19', prefsrc='10.0.0.10')]

    def test_interface_selected_from_facts(self):
        facts = vip.discover('10.0.0.100', self.addresses, self.routes)
        self.assertEqual(facts, dict(interface='ens19', mac='02:00:00:00:00:01', node_address='10.0.0.10', owns_vip=False))

    def test_owned_vip_uses_physical_interface_not_local_route(self):
        self.addresses[0]['addr_info'].append(dict(family='inet', local='10.0.0.100', prefixlen=32))
        facts = vip.discover('10.0.0.100', self.addresses, [dict(dev='lo', type='local')])
        self.assertTrue(facts['owns_vip'])
        self.assertEqual(facts['interface'], 'ens19')

    def test_reject_unsupported_addresses_and_network_edges(self):
        for address in ['::1', '2001:db8::1', '127.0.0.1', '0.0.0.1', '255.255.255.255', '10.0.0.0', '10.0.0.255', '10.0.1.100', '10.0.0.10']:
            with self.subTest(address=address), self.assertRaises(ValueError):
                vip.discover(address, self.addresses, self.routes)

    def test_reject_routed_or_ambiguous_or_down_interfaces(self):
        for routes in [[], self.routes * 2, [dict(dev='ens19', gateway='10.0.0.1')], [dict(dev='missing')]]:
            with self.subTest(routes=routes), self.assertRaises(ValueError):
                vip.discover('10.0.0.100', self.addresses, routes)
        self.addresses[0]['flags'] = []
        with self.assertRaises(ValueError):
            vip.discover('10.0.0.100', self.addresses, self.routes)

    def test_probe_does_not_claim_the_address(self):
        packet = vip.probe_packet('10.0.0.100', '02:00:00:00:00:01')
        self.assertEqual(len(packet), 42)
        self.assertEqual(packet[:6], bytes.fromhex('ffffffffffff'))
        self.assertEqual(packet[28:32], bytes(4))
        self.assertEqual(packet[38:42], ipaddress.IPv4Address('10.0.0.100').packed)

    def test_conflicts_and_approved_ha_participants(self):
        mac = '02:00:00:00:00:02'
        probe = vip.probe_packet('10.0.0.100', mac)
        self.assertTrue(vip.conflicting_sender(probe, '10.0.0.100', set(), set()))
        self.assertFalse(vip.conflicting_sender(probe, '10.0.0.100', set(), {mac}))
        response = bytearray(probe)
        response[20:22] = struct.pack('!H', 2)
        response[28:32] = ipaddress.IPv4Address('10.0.0.100').packed
        self.assertTrue(vip.conflicting_sender(response, '10.0.0.100', set(), {mac}))
        self.assertFalse(vip.conflicting_sender(response, '10.0.0.100', {mac}, {mac}))
        for packet in [b'', bytes(42), bytes(response[:30])]:
            self.assertFalse(vip.conflicting_sender(packet, '10.0.0.100', set(), set()))

    def test_mac_validation(self):
        for mac in ['00:00:00:00:00:00', 'ff:ff:ff:ff:ff:ff', '01:00:00:00:00:01', 'bad', 'zz:00:00:00:00:00']:
            with self.subTest(mac=mac), self.assertRaises(ValueError):
                vip.mac_bytes(mac)

    def test_local_unmanaged_vip_is_refused_before_network_probe(self):
        facts = dict(interface='ens19', mac='02:00:00:00:00:01', owns_vip=True)
        with patch.object(vip.socket, 'socket') as network, self.assertRaisesRegex(ValueError, 'without a matching managed manifest'):
            vip.probe('10.0.0.100', facts, set(), set())
        network.assert_not_called()


if __name__ == '__main__':
    unittest.main()
