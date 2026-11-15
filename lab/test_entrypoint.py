import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


launcher = load('launcher', 'bootstrap/launcher.py')
network = load('network', 'tailscale/guest.py')
image_cache = load('image_cache', 'images/runner/image_cache.py')
with patch.dict(sys.modules, launcher=launcher, network=network, image_cache=image_cache):
    entrypoint = load('entrypoint', 'images/runner/entrypoint.py')


class EntrypointTests(unittest.TestCase):
    def test_creation_environment_starts_one_supervisor_after_image_and_network(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = {'name': 'worker', 'token': 'private-token', 'proxy': {}}
            env = {'GARM_BOOTSTRAP_CONFIG': json.dumps(config),
                   'GARM_NETWORK_CONFIG': json.dumps({'auth_key': 'one-use-key'})}
            events, held = [], []
            docker = Mock()
            docker.wait.return_value = 0
            docker.poll.return_value = 0
            def prepare(cfg):
                self.assertTrue((root / 'launch.json').exists())
                self.assertNotIn('GARM_BOOTSTRAP_CONFIG', os.environ)
                self.assertNotIn('GARM_NETWORK_CONFIG', os.environ)
                self.assertEqual(cfg, {'auth_key': 'one-use-key'})
                events.append('prepare')
            def spawn(*args, **kwargs):
                held.append(os.dup(kwargs['pass_fds'][0]))
                self.assertNotIn('GARM_BOOTSTRAP_CONFIG', kwargs['env'])
                self.assertNotIn('GARM_NETWORK_CONFIG', kwargs['env'])
                events.append('supervisor')
            with patch.object(launcher, 'ROOT', root), patch.object(launcher, 'boot_id', return_value='boot-one'), \
                 patch.object(entrypoint, 'ready', root / 'ready.json'), patch.object(entrypoint, 'docker', docker), \
                 patch.object(entrypoint, 'prepare', side_effect=prepare), patch.object(entrypoint.signal, 'signal'), \
                 patch.object(launcher.subprocess, 'Popen', side_effect=spawn), patch.dict(os.environ, env):
                try:
                    self.assertEqual(entrypoint.main(), 0)
                    self.assertEqual(events, ['prepare', 'supervisor'])
                    saved = json.loads((root / 'bootstrap.json').read_text())
                    self.assertEqual(saved, config)
                    with patch.object(launcher, 'boot_id', return_value='boot-two'), patch.dict(os.environ, env):
                        with self.assertRaises(RuntimeError):
                            entrypoint.main()
                    self.assertEqual(events, ['prepare', 'supervisor'])
                finally:
                    for fd in held:
                        os.close(fd)


if __name__ == '__main__':
    unittest.main()
