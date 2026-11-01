import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('image_cache', Path(__file__).resolve().parents[1] / 'images/runner/image_cache.py')
cache = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cache)


class ImageCacheTests(unittest.TestCase):
    def test_verified_archive_accepts_manifest_style_ids_but_rejects_changed_content(self):
        expected = {'architecture':'amd64', 'os':'linux', 'rootfs':{'diff_ids':['sha256:layer']}, 'config':{'Env':['A=B'], 'Cmd':['bash']}}
        data = json.dumps(expected).encode()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'job.tar'
            with tarfile.open(path, 'w') as archive:
                for name, content in [('manifest.json', b'[{"Config":"config.json"}]'), ('config.json', data)]:
                    info = tarfile.TarInfo(name)
                    info.size = len(content)
                    archive.addfile(info, io.BytesIO(content))
            actual = cache.archive_config(path, 'sha256:' + hashlib.sha256(data).hexdigest())
            with self.assertRaises(ValueError):cache.archive_config(path, 'sha256:' + '0' * 64)
        info = {'Id':'sha256:manifest-not-config', 'Architecture':'amd64', 'Os':'linux', 'RootFS':{'Layers':['sha256:layer']}, 'Config':{'Env':['A=B'], 'Cmd':['bash']}}
        cache.verify_loaded(info, actual)
        for field, value in [('RootFS', {'Layers':['sha256:changed']}), ('Config', {'Env':['A=CHANGED'], 'Cmd':['bash']}), ('Architecture', 'arm64')]:
            changed = copy.deepcopy(info)
            changed[field] = value
            with self.assertRaises(ValueError):cache.verify_loaded(changed, actual)


if __name__ == '__main__':
    unittest.main()
