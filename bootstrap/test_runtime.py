import fcntl
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

TEMPLATE = Path(sys.argv.pop(1)).read_text()
spec = importlib.util.spec_from_file_location('launcher', Path(__file__).with_name('launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class LauncherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.root_patch = patch.object(launcher, 'ROOT', self.root)
        self.boot_patch = patch.object(launcher, 'boot_id', return_value='boot-one')
        self.root_patch.start()
        self.boot_patch.start()
        self.addCleanup(self.root_patch.stop)
        self.addCleanup(self.boot_patch.stop)
        self.addCleanup(self.temp.cleanup)
        self.cfg = {'name': 'one', 'token': 'private-token', 'proxy': {}}

    def test_retry_does_not_launch_another_supervisor(self):
        held = []
        def spawn(*args, **kwargs):
            held.append(os.dup(kwargs['pass_fds'][0]))
            self.assertNotIn('AGENT37_API_KEY', kwargs['env'])
            self.assertNotIn('PRIVATE_INHERITED', kwargs['env'])
        with patch.dict(os.environ, {'AGENT37_API_KEY': 'secret', 'PRIVATE_INHERITED': 'secret'}), patch.object(launcher.subprocess, 'Popen', side_effect=spawn) as child:
            launcher.launch(self.cfg, 'source')
            launcher.launch(self.cfg, 'source')
            launcher.resume()
            self.assertEqual(child.call_count, 1)
            self.assertEqual((self.root / 'bootstrap.json').stat().st_mode & 0o777, 0o600)
            with self.assertRaises(RuntimeError):
                launcher.launch(dict(self.cfg, name='different'), 'source')
            self.assertTrue((self.root / 'bootstrap.json').exists())
        for fd in held:
            os.close(fd)
        with self.assertRaises(RuntimeError):
            launcher.launch(self.cfg, 'source')
        with self.assertRaises(RuntimeError):
            launcher.resume()

    def test_reboot_does_not_resume_previous_worker(self):
        (self.root / 'launch.json').write_text(json.dumps({'name': 'one', 'boot_id': 'old-boot'}))
        with self.assertRaises(RuntimeError):
            launcher.resume()

    def test_launch_failure_removes_credentials_and_keeps_claim(self):
        with patch.object(launcher.subprocess, 'Popen', side_effect=OSError('failed')):
            with self.assertRaises(OSError):
                launcher.launch(self.cfg, 'source')
        self.assertFalse((self.root / 'bootstrap.json').exists())
        self.assertTrue((self.root / 'launch.json').exists())
        with self.assertRaises(RuntimeError):
            launcher.launch(self.cfg, 'source')


    def test_installer_failure_does_not_reuse_bootstrap_callback(self):
        cfg = dict(self.cfg, metadata_url='http://metadata.invalid', callback_url='http://callback.invalid')
        (self.root / 'bootstrap.json').write_text(json.dumps(cfg))
        (self.root / 'launch.json').write_text(json.dumps({'name': 'one', 'boot_id': 'boot-one'}))
        fd = os.open(self.root / 'launch.lock', os.O_CREAT | os.O_RDWR, 0o600)
        class Child:
            def wait(self): return 1
        with patch.object(launcher, 'client', return_value=object()), patch.object(launcher, 'request', return_value=b'#!/bin/sh\nexit 1\n'), patch.object(launcher, 'callback') as callback, patch.object(launcher.subprocess, 'Popen', return_value=Child()):
            launcher.supervise(fd)
        self.assertEqual([call.args[1] for call in callback.call_args_list], ['installing'])
        self.assertEqual(json.loads((self.root / 'launch.json').read_text())['phase'], 'failed')
        self.assertFalse((self.root / 'bootstrap.json').exists())
        self.assertFalse((self.root / 'install-script').exists())


class TemplateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.events = []
        self.called = []
        self.fake_file = self.root / 'install-script'
        self.fake_file.write_text('secret-bearing script')
        self.ns = {'__name__': 'test_template', '__file__': str(self.fake_file)}
        exec(compile(TEMPLATE, '<rendered-template>', 'exec'), self.ns)
        self.ns['ROOT'] = self.root
        self.ns['RUNNER'] = '/fake/runner'
        self.ns['CONFIG']['token'] = 'instance-secret'
        self.ns['request'] = self.request
        self.next_id = 41

    def request(self, url, data=None):
        self.events.append((url, data))
        if url.endswith('/runner-registration-token/'):
            return b'registration-secret'
        return b'{}'

    def fake_run(self, argv, **kwargs):
        self.called.append(argv)
        if 'register' in argv:
            self.assertTrue((self.root / 'registration-token').exists())
            self.assertIn('--token-file', argv)
            self.assertNotIn('registration-secret', argv)
            (self.root / '.runner').write_text(json.dumps({'id': self.next_id, 'ephemeral': True}))
        return subprocess.CompletedProcess(argv, 0)

    def fake_daemon(self, argv, **kwargs):
        self.called.append(argv)
        self.assertFalse((self.root / 'registration-token').exists())
        self.assertFalse(self.fake_file.exists())
        self.assertNotIn('AGENT37_API_KEY', kwargs['env'])
        self.assertNotIn('AGENT37_MANAGED_TOKEN', kwargs['env'])
        self.assertNotIn('token', self.ns['CONFIG'])
        self.assertEqual(self.events[-1][1]['status'], 'idle')
        self.assertTrue(any(url.endswith('/system-info/') for url, data in self.events))
        self.assertEqual(argv[-2:], ['daemon', '--once'])
        class Child:
            def wait(self, timeout=None): return 0
            def poll(self): return 0
        return Child()

    def test_fresh_identity_callbacks_and_no_second_registration(self):
        with patch.dict(os.environ, {'AGENT37_API_KEY': 'private', 'AGENT37_MANAGED_TOKEN': 'private'}), patch.object(subprocess, 'run', side_effect=self.fake_run), patch.object(subprocess, 'Popen', side_effect=self.fake_daemon):
            self.ns['main']()
            with self.assertRaises(FileExistsError):
                self.ns['main']()
        registration = [cmd for cmd in self.called if 'register' in cmd]
        self.assertEqual(len(registration), 1)
        self.assertEqual(sum(url.endswith('/runner-registration-token/') for url, data in self.events), 1)
        self.assertEqual([data['status'] for url, data in self.events if data and 'status' in data], ['installing', 'idle'])
        self.assertEqual(json.loads((self.root / 'runner.finished').read_text())['exit_code'], 0)
        config = json.loads((self.root / 'runner.yaml').read_text())
        self.assertEqual(config['runner']['capacity'], 1)
        self.assertEqual(config['container']['network'], 'host')
        self.assertTrue(all(':docker://alpine:3.22' in label for label in config['runner']['labels']))
        self.assertEqual((self.root / '.runner').stat().st_mode & 0o777, 0o600)
        self.assertNotIn('token', self.ns['CONFIG'])
        self.assertFalse((self.root / 'runner.failed').exists())

    def test_failed_registration_removes_token_and_is_not_retried(self):
        def fail(argv, **kwargs):
            if 'register' in argv:
                raise subprocess.CalledProcessError(1, argv)
            return subprocess.CompletedProcess(argv, 0)
        with patch.object(subprocess, 'run', side_effect=fail):
            with self.assertRaises(SystemExit):
                self.ns['main']()
        self.assertFalse((self.root / 'registration-token').exists())
        self.assertTrue((self.root / 'runner.failed').exists())
        self.assertEqual(self.events[-1][1]['status'], 'failed')
        self.assertNotIn('token', self.ns['CONFIG'])


    def test_daemon_failure_stays_local_without_post_idle_callback(self):
        class FailedChild:
            def wait(self, timeout=None): return 2
            def poll(self): return 2
        def daemon(argv, **kwargs):
            self.fake_daemon(argv, **kwargs)
            return FailedChild()
        with patch.object(subprocess, 'run', side_effect=self.fake_run), patch.object(subprocess, 'Popen', side_effect=daemon):
            self.ns['main']()
        self.assertEqual([data['status'] for url, data in self.events if data and 'status' in data], ['installing', 'idle'])
        self.assertEqual(json.loads((self.root / 'runner.finished').read_text())['exit_code'], 2)
        self.assertTrue((self.root / 'runner.failed').exists())
        self.assertNotIn('token', self.ns['CONFIG'])

    def test_daemon_start_error_stays_local_without_post_idle_callback(self):
        with patch.object(subprocess, 'run', side_effect=self.fake_run), patch.object(subprocess, 'Popen', side_effect=OSError('start failed')):
            self.ns['main']()
        self.assertEqual([data['status'] for url, data in self.events if data and 'status' in data], ['installing', 'idle'])
        self.assertTrue((self.root / 'runner.failed').exists())
        self.assertNotIn('token', self.ns['CONFIG'])


if __name__ == '__main__':
    unittest.main()
