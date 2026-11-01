import json
import unittest
from unittest.mock import patch

import image


class ResolveJobImageTests(unittest.TestCase):
    def test_selects_amd64_and_removes_tag_but_preserves_registry_port(self):
        index = {'manifests': [
            {'platform': {'architecture': 'arm64', 'os': 'linux'}, 'digest': 'sha256:' + 'b' * 64},
            {'platform': {'architecture': 'amd64', 'os': 'linux', 'variant': 'v1'}, 'digest': 'sha256:' + 'c' * 64},
        ]}
        manifest = {'config': {'digest': 'sha256:' + 'd' * 64}}
        with patch('image.subprocess.check_output', side_effect=[json.dumps(index), json.dumps(manifest)]) as inspect:
            source, digest, reference = image.resolve_job_image('registry.example:5000/runner:latest@sha256:' + 'a' * 64)
        self.assertEqual(source, 'registry.example:5000/runner@sha256:' + 'c' * 64)
        self.assertEqual(digest, 'sha256:' + 'd' * 64)
        self.assertEqual(reference, 'garm-template-cache/job:' + 'd' * 16)
        self.assertEqual(inspect.call_args_list[0].args[0][-2], 'registry.example:5000/runner@sha256:' + 'a' * 64)

    def test_rejects_unpinned_or_ambiguous_images(self):
        with patch('image.subprocess.check_output') as inspect:
            for value in ['registry.example/runner:latest', 'user:password@registry.example/runner@sha256:' + 'a' * 64]:
                with self.assertRaises(ValueError):image.resolve_job_image(value)
            inspect.assert_not_called()
        with patch('image.subprocess.check_output', return_value=json.dumps({'manifests': []})):
            with self.assertRaises(ValueError):image.resolve_job_image('registry.example/runner@sha256:' + 'a' * 64)

class ImageVariantTests(unittest.TestCase):
    def test_network_variant_is_explicit_and_recorded_in_build_manifest(self):
        import tarfile
        import tempfile
        from pathlib import Path
        from unittest.mock import MagicMock
        for enabled in [False, True]:
            with self.subTest(enabled=enabled), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                context = root / 'images/runner'
                context.mkdir(parents=True)
                (root / 'state').mkdir()
                for name in ['Dockerfile', 'entrypoint.py', 'image_cache.py', 'alpine.tar', '.dockerignore']:
                    (context / name).write_text('ARG GARM_TAILSCALE_ENABLED=0\n' if name == 'Dockerfile' else 'test')
                opener = MagicMock()
                opener.open.return_value.__enter__.return_value.status = 200
                replies = [{'id':'test-build','upload_url':'https://upload.invalid/context'},{}]
                with patch.object(image.m,'REPO',root), patch.object(image.m,'STATE',root/'state'), \
                     patch.object(image.m,'cloud',side_effect=replies), patch.object(image.m,'save') as save, \
                     patch.object(image.urllib.request,'build_opener',return_value=opener), patch('builtins.print'):
                    image.build('test-template',tailscale=enabled)
                with tarfile.open(root/'state/image-context.tgz') as archive:
                    dockerfile = archive.extractfile('Dockerfile').read().decode()
                self.assertIn('ARG GARM_TAILSCALE_ENABLED=' + str(int(enabled)),dockerfile)
                self.assertEqual(save.call_args.args[1]['tailscale_enabled'],enabled)


if __name__ == '__main__':
    unittest.main()
