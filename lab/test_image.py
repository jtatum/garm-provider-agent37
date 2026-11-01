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


if __name__ == '__main__':
    unittest.main()
