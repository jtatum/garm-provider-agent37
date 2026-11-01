import hashlib
import json
import sys
import tarfile


def archive_config(path, digest):
    with tarfile.open(path) as archive:
        manifests = json.load(archive.extractfile('manifest.json'))
        if len(manifests) != 1:
            raise ValueError('Expected one cached job image')
        data = archive.extractfile(manifests[0]['Config']).read(1024 * 1024 + 1)
    if len(data) > 1024 * 1024 or 'sha256:' + hashlib.sha256(data).hexdigest() != digest:
        raise ValueError('Cached image configuration digest mismatch')
    return json.loads(data)


def verify_loaded(info, expected):
    if info['Architecture'] != 'amd64' or info['Os'] != 'linux':
        raise ValueError('Cached image must be Linux amd64')
    if expected['architecture'] != info['Architecture'] or expected['os'] != info['Os']:
        raise ValueError('Cached image platform mismatch')
    if info['RootFS']['Layers'] != expected['rootfs']['diff_ids']:
        raise ValueError('Cached image filesystem mismatch')
    if any(info['Config'].get(key) != value for key, value in expected['config'].items()):
        raise ValueError('Cached image runtime configuration mismatch')


if __name__ == '__main__':
    print(json.dumps(archive_config(sys.argv[1], sys.argv[2])))
