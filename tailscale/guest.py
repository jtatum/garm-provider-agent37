import fcntl
import ipaddress
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time

ROOT = Path('/root/garm-network')
RESOLV = Path('/etc/resolv.conf')
IMAGE_READY = Path('/root/garm-image/ready.json')
TUN = Path('/dev/net/tun')
TAILSCALE = ['/usr/local/bin/tailscale', '--socket=' + str(ROOT / 'tailscaled.sock')]
BRIDGE_IP = '172.17.0.1'


def private_write(path, value):
    with open(path, 'w', opener=lambda p, flags: os.open(p, flags | os.O_NOFOLLOW, 0o600)) as output:
        output.write(value)


def run(args, timeout=10):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                          text=True, timeout=timeout).stdout


def upstream_servers(text):
    result = []
    for line in text.splitlines():
        fields = line.split()
        if len(fields) >= 2 and fields[0] == 'nameserver':
            address = ipaddress.ip_address(fields[1])
            if not (address.is_loopback or address.is_unspecified or address.is_multicast):
                result.append(str(address))
    if not result:
        raise RuntimeError('no usable upstream DNS servers')
    return list(dict.fromkeys(result))


def gateway_ip(status, hostname):
    peers = [peer for peer in (status.get('Peer') or {}).values()
             if peer.get('DNSName', '').rstrip('.').lower() == hostname]
    if status.get('BackendState') != 'Running' or len(peers) != 1 or not peers[0].get('Online'):
        return None
    addresses = [ipaddress.ip_address(value) for value in peers[0].get('TailscaleIPs', [])]
    return next((str(ip) for ip in addresses if ip.version == 4 and ip in ipaddress.ip_network('100.64.0.0/10')), None)


def dns_config(upstreams, aliases, address):
    # Interface filters keep the resolver private to the VM and its Docker networks.
    lines = ['no-resolv', 'no-hosts', 'bind-dynamic', 'interface=lo', 'interface=docker0',
             'interface=br-*', 'cache-size=150', 'pid-file=' + str(ROOT / 'dnsmasq.pid')]
    lines.extend('server=' + ip for ip in upstreams)
    lines.extend('host-record=' + alias + ',' + address for alias in aliases)
    lines.extend('local=/' + alias + '/' for alias in aliases)
    return '\n'.join(lines) + '\n'


def start_process(args):
    child = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL, start_new_session=True)
    return child


def process_identity(pid):
    return Path('/proc/' + str(pid) + '/stat').read_text().rsplit(')', 1)[1].split()[19]


def network_status():
    return json.loads(run(TAILSCALE + ['status', '--json']))


def validate_bridge():
    bridge = json.loads(run(['docker', 'network', 'inspect', 'bridge']))[0]
    gateways = [item.get('Gateway') for item in bridge['IPAM']['Config']]
    if BRIDGE_IP not in gateways:
        raise RuntimeError('Docker bridge differs from the network image contract')


def check_resolution(aliases, address):
    for name in aliases:
        addresses = {item[4][0] for item in socket.getaddrinfo(name, None, family=socket.AF_INET)}
        if addresses != {address}:
            raise RuntimeError('private hostname does not resolve to the gateway')


def verify():
    state = json.loads((ROOT / 'ready.json').read_text())
    if any(process_identity(int(pid)) != identity for pid, identity in state['processes'].items()):
        raise RuntimeError('network process was replaced')
    if gateway_ip(network_status(), state['gateway_dns']) != state['gateway_ip']:
        raise RuntimeError('tailnet gateway is unavailable or changed')
    validate_bridge()
    check_resolution(state['aliases'], state['gateway_ip'])


def setup(cfg, source):
    os.umask(0o077)
    ROOT.mkdir(mode=0o700, exist_ok=True)
    key_path = ROOT / 'auth.key'
    with (ROOT / 'lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if (ROOT / 'attempted').exists():
            raise RuntimeError('network enrollment cannot be replayed')
        image = json.loads(IMAGE_READY.read_text())
        if image.get('tailscale_enabled') is not True:
            raise RuntimeError('a Tailscale-enabled runner image is required')
        for binary in ['/usr/local/bin/tailscale', '/usr/local/bin/tailscaled', '/usr/sbin/dnsmasq']:
            if not os.access(binary, os.X_OK):
                raise RuntimeError('network image is missing required tools')
        if not TUN.exists():
            raise RuntimeError('kernel TUN device is required')
        validate_bridge()
        upstreams = upstream_servers(RESOLV.read_text())
        private_write(ROOT / 'network.py', source)
        private_write(ROOT / 'attempted', 'one-shot\n')
        children = []
        try:
            private_write(key_path, cfg.pop('auth_key'))
            children.append(start_process(['/usr/local/bin/tailscaled', '--state=mem:',
                                           '--statedir=' + str(ROOT), '--socket=' + str(ROOT / 'tailscaled.sock'),
                                           '--tun=tailscale0']))
            deadline = time.monotonic() + 15
            while not (ROOT / 'tailscaled.sock').exists():
                if children[0].poll() is not None or time.monotonic() >= deadline:
                    raise RuntimeError('tailscaled did not become ready')
                time.sleep(0.1)
            try:
                run(TAILSCALE + ['up', '--auth-key=file:' + str(key_path), '--hostname=' + cfg['hostname'],
                                '--accept-dns=false', '--accept-routes=false', '--shields-up', '--ssh=false',
                                '--timeout=45s'], timeout=50)
            finally:
                key_path.unlink(missing_ok=True)
            deadline = time.monotonic() + 30
            address = None
            while address is None:
                address = gateway_ip(network_status(), cfg['gateway_dns'])
                if address is None:
                    if time.monotonic() >= deadline:
                        raise RuntimeError('tailnet gateway is missing or offline')
                    time.sleep(0.2)
            aliases = [cfg['gateway_dns']] + (cfg.get('private_aliases') or [])
            private_write(ROOT / 'dnsmasq.conf', dns_config(upstreams, aliases, address))
            run(['/usr/sbin/dnsmasq', '--test', '--conf-file=' + str(ROOT / 'dnsmasq.conf')])
            children.append(start_process(['/usr/sbin/dnsmasq', '--keep-in-foreground',
                                           '--conf-file=' + str(ROOT / 'dnsmasq.conf')]))
            RESOLV.write_text('nameserver 127.0.0.1\noptions timeout:2 attempts:2\n')
            deadline = time.monotonic() + 10
            while True:
                try:
                    check_resolution(aliases, address)
                    break
                except (OSError, RuntimeError):
                    if children[1].poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError('private DNS failed to become ready') from None
                    time.sleep(0.1)
            run(['docker', 'run', '--rm', '--pull=never', 'alpine:3.22', 'nslookup', cfg['gateway_dns']], timeout=15)
            state = {'gateway_dns':cfg['gateway_dns'], 'gateway_ip':address, 'aliases':aliases,
                     'processes':{str(p.pid):process_identity(p.pid) for p in children}}
            private_write(ROOT / 'ready.json', json.dumps(state))
        except Exception:
            try:
                run(TAILSCALE + ['logout'], timeout=3)
            except Exception:
                pass
            for child in children:
                child.terminate()
            raise
        finally:
            key_path.unlink(missing_ok=True)


if __name__ == '__main__':
    try:
        if sys.argv[1:] != ['verify']:
            raise RuntimeError('unsupported network operation')
        verify()
    except Exception:
        raise SystemExit('worker network verification failed; replace worker')
