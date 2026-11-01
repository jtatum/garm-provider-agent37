import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('guest', Path(__file__).with_name('guest.py'))
guest = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guest)


def status():
    return {'BackendState':'Running', 'Peer':{'key':{
        'DNSName':'ci.example.ts.net.', 'TailscaleIPs':['100.64.1.2','fd7a:115c:a1e0::1'], 'Online':True}}}


class NetworkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.resolv = self.root / 'resolv.conf'
        self.resolv.write_text('nameserver 1.1.1.1\nnameserver 8.8.8.8\n')
        self.image = self.root / 'image.json'
        self.image.write_text('{"tailscale_enabled":true}')
        self.tun = self.root / 'tun'
        self.tun.touch()
        (self.root / 'tailscaled.sock').touch()
        self.cfg = {'auth_key':'tskey-auth-test-secret','hostname':'garm-test',
                    'gateway_dns':'ci.example.ts.net','private_aliases':['git.internal.example']}
        self.children = [Mock(pid=100), Mock(pid=101)]
        for child in self.children:
            child.poll.return_value = None
        self.calls = []
        for name, value in [('ROOT',self.root),('RESOLV',self.resolv),('IMAGE_READY',self.image),('TUN',self.tun)]:
            mock = patch.object(guest,name,value)
            mock.start()
            self.addCleanup(mock.stop)

    def command(self, args, timeout=10):
        self.calls.append(args)
        if args[:3] == ['docker','network','inspect']:
            return json.dumps([{'IPAM':{'Config':[{'Gateway':'172.17.0.1'}]}}])
        if args[-2:] == ['status','--json']:
            return json.dumps(status())
        if 'up' in args:
            self.assertEqual((self.root / 'auth.key').read_text(), 'tskey-auth-test-secret')
            self.assertEqual((self.root / 'auth.key').stat().st_mode & 0o777, 0o600)
            self.assertNotIn('tskey-auth-test-secret', ' '.join(args))
            self.assertIn('--accept-routes=false', args)
            self.assertIn('--accept-dns=false', args)
            self.assertIn('--shields-up', args)
        return ''

    def context(self):
        from contextlib import ExitStack
        stack = ExitStack()
        stack.enter_context(patch.object(guest.os,'access',return_value=True))
        stack.enter_context(patch.object(guest,'run',side_effect=self.command))
        stack.enter_context(patch.object(guest,'start_process',side_effect=self.children))
        stack.enter_context(patch.object(guest,'process_identity',return_value='start-tick'))
        stack.enter_context(patch.object(guest.socket,'getaddrinfo',return_value=[(2,1,6,'',('100.64.1.2',0))]))
        return stack

    def test_success_removes_key_and_cannot_reenroll(self):
        with self.context():
            guest.setup(dict(self.cfg),'source-without-credentials')
            guest.verify()
            with self.assertRaises(RuntimeError):
                guest.setup(dict(self.cfg),'source-without-credentials')
        self.assertFalse((self.root/'auth.key').exists())
        self.assertNotIn('tskey-auth-test-secret', (self.root/'ready.json').read_text())
        self.assertNotIn('tskey-auth-test-secret', (self.root/'network.py').read_text())
        self.assertEqual((self.root/'ready.json').stat().st_mode & 0o777, 0o600)
        conf = (self.root/'dnsmasq.conf').read_text()
        self.assertIn('server=1.1.1.1',conf)
        self.assertIn('host-record=git.internal.example,100.64.1.2',conf)
        self.assertIn('host-record=ci.example.ts.net,100.64.1.2',conf)
        self.assertIn('interface=br-*',conf)
        self.assertNotIn('interface=eth0',conf)
        self.assertEqual(self.resolv.read_text().splitlines()[0],'nameserver 127.0.0.1')
        self.assertTrue(any(args[:3]==['docker','run','--rm'] for args in self.calls))

    def test_failed_join_cleans_key_and_keeps_claim(self):
        original = self.command
        def failure(args, timeout=10):
            if 'up' in args:
                original(args,timeout)
                raise RuntimeError('join rejected')
            return original(args,timeout)
        self.command = failure
        with self.context():
            with self.assertRaises(RuntimeError):
                guest.setup(dict(self.cfg),'safe-source')
            with self.assertRaises(RuntimeError):
                guest.setup(dict(self.cfg),'safe-source')
        self.assertFalse((self.root/'auth.key').exists())
        self.assertTrue((self.root/'attempted').exists())
        self.assertTrue(any(args[-1]=='logout' for args in self.calls))
        self.children[0].terminate.assert_called_once()

    def test_wrong_image_fails_before_enrollment(self):
        self.image.write_text('{}')
        with self.context():
            with self.assertRaises(RuntimeError):
                guest.setup(dict(self.cfg),'safe-source')
        self.assertFalse((self.root/'attempted').exists())
        self.assertFalse(self.calls)

    def test_gateway_must_be_exact_online_tailnet_peer(self):
        data=status()
        self.assertEqual(guest.gateway_ip(data,'ci.example.ts.net'),'100.64.1.2')
        self.assertIsNone(guest.gateway_ip(data,'another.example.ts.net'))
        data['Peer']['key']['Online']=False
        self.assertIsNone(guest.gateway_ip(data,'ci.example.ts.net'))
        data=status();data['Peer']['key']['TailscaleIPs']=['192.168.1.1']
        self.assertIsNone(guest.gateway_ip(data,'ci.example.ts.net'))
        data=status();data['Peer']['other']=dict(data['Peer']['key'])
        self.assertIsNone(guest.gateway_ip(data,'ci.example.ts.net'))

    def test_upstream_resolver_cannot_loop(self):
        self.assertEqual(guest.upstream_servers('nameserver 127.0.0.53\nnameserver 1.1.1.1\nnameserver 1.1.1.1\n'),['1.1.1.1'])
        with self.assertRaises(RuntimeError):
            guest.upstream_servers('nameserver 127.0.0.1\n')
        with self.assertRaises(ValueError):
            guest.upstream_servers('nameserver invalid\n')

    def test_verify_rejects_changed_process_or_dns(self):
        with self.context():
            guest.setup(dict(self.cfg),'safe-source')
            with patch.object(guest,'process_identity',return_value='different-start'):
                with self.assertRaises(RuntimeError):guest.verify()
            with patch.object(guest.socket,'getaddrinfo',return_value=[(2,1,6,'',('192.168.1.1',0))]):
                with self.assertRaises(RuntimeError):guest.verify()

    def test_wrong_docker_bridge_is_rejected(self):
        with patch.object(guest,'run',return_value='[{"IPAM":{"Config":[{"Gateway":"10.0.0.1"}]}}]'):
            with self.assertRaises(RuntimeError):guest.validate_bridge()


if __name__ == '__main__':
    unittest.main()
