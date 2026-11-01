import base64
import fcntl
import json
import os
from pathlib import Path
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path('/root/garm')
LIMIT = 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError('redirect refused')


def write_private(path, data):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as stream:
        stream.write(data)


def boot_id():
    return Path('/proc/sys/kernel/random/boot_id').read_text().strip()


def state(**changes):
    path = ROOT / 'launch.json'
    value = json.loads(path.read_text())
    value.update(changes)
    write_private(ROOT / 'launch.next', json.dumps(value))
    (ROOT / 'launch.next').replace(path)


def clean_environment(proxy):
    env = {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
           'HOME': '/root', 'LANG': 'C.UTF-8'}
    for name in ('http_proxy', 'https_proxy', 'no_proxy'):
        if proxy.get(name):
            env[name] = env[name.upper()] = proxy[name]
    return env


def client(config):
    context = ssl.create_default_context()
    if config.get('ca_bundle'):
        context.load_verify_locations(cadata=config['ca_bundle'])
    return urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=context))


def request(opener, url, token, data=None):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in ('http', 'https') or not parsed.netloc or parsed.username or parsed.password:
        raise RuntimeError('invalid bootstrap URL')
    headers = {'Authorization': 'Bearer ' + token, 'Accept': 'application/json'}
    if data is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=json.dumps(data).encode() if data is not None else None, headers=headers)
    with opener.open(req, timeout=10) as response:
        body = response.read(LIMIT + 1)
        if len(body) > LIMIT:
            raise RuntimeError('bootstrap response too large')
        return body


def callback(config, status, message):
    url = config['callback_url'].rstrip('/')
    if not url.endswith('/status'):
        url += '/status'
    try:
        request(client(config), url, config['token'], {'status': status, 'message': message})
    except Exception:
        pass


def resume():
    try:
        claim = json.loads((ROOT / 'launch.json').read_text())
        if claim['boot_id'] != boot_id():
            raise RuntimeError('worker rebooted; replace it')
        fd = os.open(ROOT / 'launch.lock', os.O_RDWR)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        finally:
            os.close(fd)
    except (OSError, ValueError, KeyError):
        raise RuntimeError('worker has no active supervisor') from None
    raise RuntimeError('worker finished or was interrupted; replace it')


def launch(config, source):
    os.umask(0o077)
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(ROOT, 0o700)
    fd = os.open(ROOT / 'launch.lock', os.O_CREAT | os.O_RDWR, 0o600)
    claimed = False
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            claim = json.loads((ROOT / 'launch.json').read_text())
            if claim.get('name') != config['name'] or claim.get('boot_id') != boot_id():
                raise RuntimeError('worker ownership mismatch')
            return
        if (ROOT / 'launch.json').exists():
            raise RuntimeError('worker already attempted; replace it')
        write_private(ROOT / 'launch.json', json.dumps({'name': config['name'], 'boot_id': boot_id(), 'phase': 'claimed'}))
        claimed = True
        write_private(ROOT / 'bootstrap.json', json.dumps(config))
        write_private(ROOT / 'launcher.py', source)
        with open(ROOT / 'bootstrap.log', 'ab', buffering=0) as log:
            subprocess.Popen([sys.executable, str(ROOT / 'launcher.py'), 'supervise', str(fd)],
                             stdin=subprocess.DEVNULL, stdout=log, stderr=log, close_fds=True,
                             pass_fds=(fd,), start_new_session=True,
                             env=clean_environment(config.get('proxy', {})))
    except Exception:
        if claimed:
            (ROOT / 'bootstrap.json').unlink(missing_ok=True)
        raise
    finally:
        os.close(fd)


def supervise(fd):
    config = None
    script = ROOT / 'install-script'
    try:
        config = json.loads((ROOT / 'bootstrap.json').read_text())
        (ROOT / 'bootstrap.json').unlink()
        state(phase='running', pid=os.getpid())
        if config.get('ca_bundle'):
            cert = Path('/usr/local/share/ca-certificates/garm-instance.crt')
            write_private(cert, config['ca_bundle'])
            subprocess.run(['update-ca-certificates'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        callback(config, 'installing', 'fetching runner installation template')
        opener = client(config)
        deadline = time.monotonic() + 180
        while True:
            try:
                content = request(opener, config['metadata_url'].rstrip('/') + '/install-script/', config['token'])
                break
            except urllib.error.HTTPError as exc:
                if exc.code not in (408, 425, 429, 500, 502, 503, 504):
                    raise RuntimeError('install template request rejected') from None
            except (urllib.error.URLError, TimeoutError):
                pass
            if time.monotonic() >= deadline:
                raise RuntimeError('install template unavailable')
            time.sleep(1)
        if not content.startswith(b'#!'):
            raise RuntimeError('install template lacks interpreter')
        write_private(script, content.decode())
        os.chmod(script, 0o700)
        child = subprocess.Popen([str(script)], cwd=ROOT, env=clean_environment(config.get('proxy', {})))
        config.clear()
        config = None
        del content
        if child.wait():
            raise RuntimeError('runner installation failed')
        state(phase='finished')
    except Exception:
        if config:
            callback(config, 'failed', 'bootstrap failed; replace this worker')
        state(phase='failed')
    finally:
        script.unlink(missing_ok=True)
        (ROOT / 'bootstrap.json').unlink(missing_ok=True)
        os.close(fd)


if __name__ == '__main__':
    try:
        if len(sys.argv) == 3 and sys.argv[1] == 'supervise':
            supervise(int(sys.argv[2]))
        elif len(sys.argv) == 2 and sys.argv[1] == 'resume':
            resume()
        else:
            raise RuntimeError('invalid bootstrap operation')
    except Exception:
        print('bootstrap rejected; replace worker or inspect private status', file=sys.stderr)
        sys.exit(1)
