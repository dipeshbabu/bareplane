#!/usr/bin/python
"""Shared read-only IPv4 interface discovery and bounded ARP conflict checks."""
import ipaddress
import json
import socket
import struct
import subprocess
import time


def discover(vip_text, addresses, routes):
    vip = ipaddress.ip_address(vip_text)
    if vip.version != 4 or vip.is_unspecified or vip.is_loopback or vip.is_multicast or vip.is_link_local or vip.is_reserved or vip.packed[0] == 0:
        raise ValueError('The ARP VIP phase requires a unicast IPv4 address on the control-plane LAN')
    owners = [entry for entry in addresses if any(a.get('local') == str(vip) for a in entry.get('addr_info', []))]
    if len(owners) > 1:
        raise ValueError('VIP appears on multiple local interfaces')
    if owners:
        interface = owners[0]
    else:
        if len(routes) != 1 or routes[0].get('gateway') or routes[0].get('type', 'unicast') != 'unicast':
            raise ValueError('VIP must be directly reachable on the control-plane LAN, without a gateway')
        matches = [entry for entry in addresses if entry.get('ifname') == routes[0].get('dev')]
        if len(matches) != 1:
            raise ValueError('Cannot uniquely identify the VIP interface')
        interface = matches[0]
    name = interface.get('ifname', '')
    if not name or len(name) > 15 or name == 'lo' or 'UP' not in interface.get('flags', []):
        raise ValueError('VIP requires an active non-loopback interface')
    mac = interface.get('address', '').lower()
    mac_bytes(mac)
    candidates = []
    for address in interface.get('addr_info', []):
        if address.get('family') != 'inet' or address.get('local') == str(vip):
            continue
        node = ipaddress.ip_interface(f"{address['local']}/{address['prefixlen']}")
        if vip in node.network and vip not in (node.network.network_address, node.network.broadcast_address):
            candidates.append(str(node.ip))
    if len(candidates) != 1:
        raise ValueError('VIP must share exactly one local IPv4 subnet and differ from the node address')
    return dict(interface=name, mac=mac, node_address=candidates[0], owns_vip=bool(owners))


def mac_bytes(value):
    parts = value.split(':')
    if len(parts) != 6 or any(len(part) != 2 for part in parts):
        raise ValueError('Invalid Ethernet hardware address')
    result = bytes.fromhex(''.join(parts))
    if result == bytes(6) or result[0] & 1:
        raise ValueError('VIP requires a unicast Ethernet hardware address')
    return result


def probe_packet(vip, mac):
    hardware = mac_bytes(mac)
    return bytes.fromhex('ffffffffffff') + hardware + b'\x08\x06' + struct.pack(
        '!HHBBH6s4s6s4s', 1, 0x0800, 6, 4, 1, hardware, bytes(4), bytes(6), ipaddress.IPv4Address(vip).packed)


def conflicting_sender(packet, vip, owner_macs, probe_macs):
    if len(packet) < 42 or packet[12:14] != b'\x08\x06':
        return False
    hardware, protocol, hlen, plen, operation, sender, source, _, target = struct.unpack('!HHBBH6s4s6s4s', packet[14:42])
    if (hardware, protocol, hlen, plen) != (1, 0x0800, 6, 4) or operation not in (1, 2):
        return False
    mac = ':'.join(f'{part:02x}' for part in sender)
    desired = ipaddress.IPv4Address(vip).packed
    if source == desired:
        return mac not in owner_macs or packet[6:12] != sender
    # Other cluster members may probe concurrently; unrelated simultaneous
    # probes count as a conflict, as specified by IPv4 address conflict detection.
    return source == bytes(4) and target == desired and mac not in probe_macs


def probe(vip, facts, owner_macs, probe_macs):
    if facts['owns_vip'] and facts['mac'] not in owner_macs:
        raise ValueError('VIP is locally assigned without a matching managed manifest')
    deadline = time.monotonic() + 4
    packet = probe_packet(vip, facts['mac'])
    with socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(0x0806)) as conn:
        conn.bind((facts['interface'], 0))
        next_send, sent, received = 0, 0, 0
        while time.monotonic() < deadline:
            now = time.monotonic()
            if sent < 3 and now >= next_send:
                conn.send(packet)
                sent += 1
                next_send = now + 1
            conn.settimeout(min(0.2, max(0.001, deadline - now)))
            try:
                response = conn.recv(2048)
            except socket.timeout:
                continue
            received += 1
            if received > 4096:
                raise ValueError('ARP traffic limit exceeded; cannot establish VIP availability')
            if conflicting_sender(response, vip, owner_macs, probe_macs):
                raise ValueError('VIP is claimed or being probed by an unrelated host')


def ip_json(args):
    result = subprocess.run(['ip', '-j'] + args, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5, check=True)
    if len(result.stdout) > 65536:
        raise ValueError('Interface discovery exceeded its output limit')
    return json.loads(result.stdout)


def main():
    from ansible.module_utils.basic import AnsibleModule
    module = AnsibleModule(argument_spec=dict(
        vip=dict(type='str', required=True), check_conflicts=dict(type='bool', default=False),
        owner_macs=dict(type='list', elements='str', default=[]), probe_macs=dict(type='list', elements='str', default=[])),
        supports_check_mode=True)
    try:
        vip = str(ipaddress.ip_address(module.params['vip']))
        facts = discover(vip, ip_json(['address', 'show']), ip_json(['route', 'get', vip]))
        if module.params['check_conflicts']:
            owners = {value.lower() for value in module.params['owner_macs']}
            participants = {value.lower() for value in module.params['probe_macs']} | {facts['mac']}
            for value in owners | participants:
                mac_bytes(value)
            probe(vip, facts, owners, participants)
        module.exit_json(changed=False, **facts)
    except ValueError as exc:
        module.fail_json(msg=str(exc))
    except (OSError, subprocess.SubprocessError, KeyError, TypeError):
        module.fail_json(msg='VIP discovery/probing failed; verify iproute2, root privileges, and the control-plane LAN')


if __name__ == '__main__':
    main()
