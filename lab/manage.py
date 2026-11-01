import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent
STATE = ROOT / '.state'
REPO = ROOT.parent
GARM = 'http://127.0.0.1:19997/api/v1'
GITEA = 'http://127.0.0.1:13000/api/v1'
PROVIDER_ID = 'agent37-local-lab'


def save(name, value):
    STATE.mkdir(mode=0o700, exist_ok=True)
    with os.fdopen(os.open(STATE / name, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as f:
        json.dump(value, f, indent=2)
        f.write('\n')


def read(name):
    return json.loads((STATE / name).read_text())


def request(base, method, path, body=None, headers=None):
    hdr = {'Accept': 'application/json', **(headers or {})}
    if body is not None:
        hdr['Content-Type'] = 'application/json'
    req = urllib.request.Request(base + path, method=method, headers=hdr,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            data = r.read()
            return json.loads(data) if data else None
    except urllib.error.HTTPError as exc:
        save('last-api-error.json', {'method': method, 'path': path, 'status': exc.code, 'body': exc.read().decode()})
        raise RuntimeError(f'{method} {path} returned HTTP {exc.code}; details retained privately') from None


def gitea(method, path, body=None):
    cred_path = Path(read('settings.json')['gitea_credentials'])
    credentials = json.loads(cred_path.read_text())
    auth = base64.b64encode((credentials['username'] + ':' + credentials['password']).encode()).decode()
    return request(GITEA, method, path, body, {'Authorization': 'Basic ' + auth})


def garm(method, path, body=None):
    return request(GARM, method, path, body, {'Authorization': 'Bearer ' + read('login.json')['token']})


def cloud(method, path, body=None):
    key = Path(read('settings.json')['agent37_key_file']).read_text().strip()
    return request('https://api.agent37.com/v1', method, path, body, {'Authorization': 'Bearer ' + key})


def start():
    log = (STATE / 'garm.log').open('a')
    p = subprocess.Popen([str(STATE / 'bin/garm'), '--config', str(STATE / 'garm.toml')],
                         stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
    save('garm-process.json', {'pid': p.pid})
    log.close()
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if p.poll() is not None:
            raise RuntimeError('GARM exited; see private garm.log')
        try:
            urllib.request.urlopen('http://127.0.0.1:19997/', timeout=1).close()
            return
        except urllib.error.HTTPError:
            return
        except OSError:
            time.sleep(0.2)
    raise RuntimeError('GARM did not listen within 30 seconds')


def stop():
    if not (STATE / 'garm-process.json').exists():
        return
    pid = read('garm-process.json')['pid']
    ps = subprocess.run(['ps', '-p', str(pid), '-o', 'command='], capture_output=True, text=True)
    if not ps.returncode:
        if str(STATE / 'bin/garm') not in ps.stdout:
            raise RuntimeError('Refusing to signal an unexpected process')
        os.kill(pid, signal.SIGTERM)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if subprocess.run(['kill', '-0', str(pid)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
                break
            time.sleep(0.2)
        else:
            raise RuntimeError('GARM did not stop gracefully')
    (STATE / 'garm-process.json').unlink(missing_ok=True)


def configure(args):
    STATE.mkdir(mode=0o700, exist_ok=True)
    settings = {'agent37_key_file': str(Path(args.key_file).resolve()),
                'gitea_credentials': str(Path(args.gitea_credentials).resolve())}
    save('settings.json', settings)
    if not (STATE / 'admin.json').exists():
        save('admin.json', {'username': 'garmadmin', 'password': secrets.token_urlsafe(32),
                            'email': 'garm-lab@example.invalid', 'full_name': 'GARM Agent37 lab'})
    if not (STATE / 'server-secrets.json').exists():
        save('server-secrets.json', {'jwt': secrets.token_urlsafe(48), 'database': secrets.token_urlsafe(24)})
    conf = read('server-secrets.json')
    text = f'''[default]
enable_webhook_management = true
callback_url = "http://127.0.0.1:19997/api/v1/callbacks"
metadata_url = "http://127.0.0.1:19997/api/v1/metadata"
webhook_url = "http://host.docker.internal:19997/webhooks"
[logging]
log_format = "text"
log_level = "info"
[apiserver]
bind = "127.0.0.1"
port = 19997
use_tls = false
[database]
backend = "sqlite3"
passphrase = "{conf['database']}"
[database.sqlite3]
db_file = "{STATE / 'garm.db'}"
[jwt_auth]
secret = "{conf['jwt']}"
time_to_live = "48h"
'''
    if (REPO / 'bin/garm-provider-agent37').exists():
        text += f'''[[provider]]
name = "agent37-lab"
provider_type = "external"
disable_jit_config = true
[provider.external]
provider_executable = "{REPO / 'bin/garm-provider-agent37'}"
config_file = "{STATE / 'provider.toml'}"
exec_timeout_seconds = 240
'''
    for name, value in [('garm.toml', text), ('provider.toml', f'''provider_id = "{PROVIDER_ID}"
api_key_file = "{settings['agent37_key_file']}"
state_dir = "{STATE / 'provider-state'}"
region = "us"
max_instances = {args.concurrency}
default_disk_gb = 20
create_timeout = "3m"
poll_interval = "1s"
''')]:
        with os.fdopen(os.open(STATE / name, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as f:
            f.write(value)
    print(json.dumps({'configured': True, 'provider_available': (REPO / 'bin/garm-provider-agent37').exists()}))


def setup():
    admin = read('admin.json')
    if not (STATE / 'initialized.json').exists():
        request(GARM, 'POST', '/first-run', admin)
        save('initialized.json', {'at': time.time()})
    login = request(GARM, 'POST', '/auth/login', {'username': admin['username'], 'password': admin['password']})
    save('login.json', login)
    controller = garm('PUT', '/controller', {'metadata_url': 'http://127.0.0.1:19997/api/v1/metadata',
                      'callback_url': 'http://127.0.0.1:19997/api/v1/callbacks',
                      'webhook_url': 'http://host.docker.internal:19997/webhooks',
                      'agent_url': 'http://127.0.0.1:19997/agent', 'minimum_job_age_backoff': 0})
    save('controller.json', controller)
    endpoints = garm('GET', '/gitea/endpoints') or []
    if not any(e['name'] == 'local-gitea' for e in endpoints):
        garm('POST', '/gitea/endpoints', {'name': 'local-gitea', 'description': 'Isolated local Gitea lab',
              'api_base_url': 'http://127.0.0.1:13000/', 'base_url': 'http://127.0.0.1:13000/'})
    if not (STATE / 'gitea-pat.json').exists():
        token = gitea('POST', '/users/lab-admin/tokens', {'name': 'garm-agent37-local-lab',
                    'scopes': ['write:repository', 'write:organization']})
        save('gitea-pat.json', token)
    credentials = garm('GET', '/gitea/credentials') or []
    if not any(c['name'] == 'local-gitea' for c in credentials):
        garm('POST', '/gitea/credentials', {'name': 'local-gitea', 'endpoint': 'local-gitea',
              'auth_type': 'pat', 'pat': {'oauth2_token': read('gitea-pat.json')['sha1']}})
    repos = garm('GET', '/repositories') or []
    repo = next((r for r in repos if r['owner'] == 'lab-admin' and r['name'] == 'runner-proof'), None)
    if repo is None:
        repo = garm('POST', '/repositories', {'owner': 'lab-admin', 'name': 'runner-proof',
                'credentials_name': 'local-gitea', 'forge_type': 'gitea',
                'webhook_secret': secrets.token_urlsafe(32), 'pool_balancer_type': 'roundrobin'})
    save('repository.json', repo)
    hooks = gitea('GET', '/repos/lab-admin/runner-proof/hooks')
    target = controller['webhook_url'].rstrip('/') + '/' + controller['controller_id']
    if not any(h.get('config', {}).get('url') == target for h in hooks):
        try:
            garm('POST', '/repositories/' + repo['id'] + '/webhook', {'webhook_endpoint_type': 'direct'})
        except RuntimeError:
            hooks = gitea('GET', '/repos/lab-admin/runner-proof/hooks')
            if not any(h.get('config', {}).get('url') == target for h in hooks):
                raise
    print(json.dumps({'controller_id': controller.get('controller_id'), 'repository_id': repo['id']}))


def pool(concurrency=2):
    template_path = REPO / 'templates/gitea-agent37.py.tmpl'
    templates = garm('GET', '/templates') or []
    body = {'name': 'agent37-gitea', 'description': 'Fresh Agent37 VM Gitea runner',
            'os_type': 'linux', 'forge_type': 'gitea',
            'data': base64.b64encode(template_path.read_bytes()).decode()}
    existing = next((t for t in templates if t['name'] == body['name']), None)
    if existing:
        t = garm('PUT', '/templates/' + str(existing['id']), {'data': body['data']})
    else:
        t = garm('POST', '/templates', body)
    save('template.json', t)
    rid = read('repository.json')['id']
    pools = garm('GET', '/repositories/' + rid + '/pools') or []
    existing = next((p for p in pools if p['provider_name'] == 'agent37-lab'), None)
    spec = {'provider_name': 'agent37-lab', 'max_runners': concurrency, 'min_idle_runners': 0,
            'image': 'garm-gitea-runner@1', 'flavor': 'default', 'os_type': 'linux',
            'os_arch': 'amd64', 'tags': ['garm-agent37'], 'enabled': False,
            'runner_prefix': 'a37lab', 'runner_bootstrap_timeout': 5, 'template_id': t['id'],
            'extra_specs': {'extra_context': {'agent37_container_network': 'host', 'agent37_default_image': 'alpine:3.22'}}}
    p = garm('PUT', '/pools/' + existing['id'], spec) if existing else garm('POST', '/repositories/' + rid + '/pools', spec)
    save('pool.json', p)
    print(json.dumps({'pool_id': p['id'], 'enabled': p.get('enabled', False)}))


def key():
    key_path = STATE / 'id_ed25519'
    if key_path.exists():
        raise RuntimeError('Temporary key already exists; reconcile before replacing')
    subprocess.run(['ssh-keygen', '-t', 'ed25519', '-N', '', '-C', PROVIDER_ID, '-f', str(key_path)], check=True, stdout=subprocess.DEVNULL)
    result = cloud('POST', '/ssh-keys', {'name': PROVIDER_ID, 'public_key': key_path.with_suffix('.pub').read_text().strip()})
    save('ssh-key.json', result)
    print(json.dumps({'registered_key_id': result['id']}))


def status():
    instances = cloud('GET', '/instances')['data']
    print(json.dumps({'garm_instances': garm('GET', '/instances'),
                      'cloud_instances': [{k:i.get(k) for k in ('id','name','status','metadata')} for i in instances],
                      'gitea_runners': gitea('GET', '/repos/lab-admin/runner-proof/actions/runners')}))


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('operation', choices=['configure','start','stop','restart','setup','pool','key','status'])
    p.add_argument('--key-file')
    p.add_argument('--concurrency',type=int,choices=range(1,11),default=2)
    p.add_argument('--gitea-credentials', default=str(REPO.parent / 'garm-provider-boat/lab/.state/credentials.json'))
    args = p.parse_args()
    if args.operation == 'configure':
        configure(args)
    elif args.operation == 'pool':
        pool(args.concurrency)
    elif args.operation == 'restart':
        stop(); start()
    else:
        globals()[args.operation]()
