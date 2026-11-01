import argparse
import json
from pathlib import Path
import secrets
import subprocess
import tempfile
import uuid


PROBE = '''import json,os,time,urllib.request,urllib.error
from pathlib import Path
base='http://127.0.0.1:9997/api/v1'
def request(method,path,body=None,token=None):
 headers={'Content-Type':'application/json'}
 if token:headers['Authorization']='Bearer '+token
 req=urllib.request.Request(base+path,method=method,headers=headers,data=json.dumps(body).encode() if body else None)
 with urllib.request.urlopen(req,timeout=3) as response:
  data=response.read()
  return json.loads(data) if data else None
admin=json.loads(Path('/etc/garm/smoke-admin.json').read_text())
deadline=time.monotonic()+60
while True:
 try:
  with urllib.request.urlopen('http://127.0.0.1:9997/ui/',timeout=1) as response:
   assert response.status==200 and b'<html' in response.read().lower()
  break
 except (OSError,urllib.error.URLError):
  if time.monotonic()>deadline:raise RuntimeError('Controller did not become ready')
  time.sleep(0.2)
try:request('POST','/first-run',admin)
except urllib.error.HTTPError as exc:
 if exc.code!=409:raise
login=request('POST','/auth/login',{'username':admin['username'],'password':admin['password']})
controller=request('PUT','/controller',{'metadata_url':base+'/metadata','callback_url':base+'/callbacks','webhook_url':'http://127.0.0.1:9997/webhooks','agent_url':'http://127.0.0.1:9997/agent'},token=login['token'])
providers=request('GET','/providers',token=login['token'])
assert os.getuid()==10001 and os.getgid()==10001
assert Path('/var/lib/garm/garm.db').exists()
assert Path('/opt/garm/templates/gitea-agent37.py.tmpl').is_file()
assert Path('/etc/ssl/certs/ca-certificates.crt').stat().st_size>0
provider_names=[p['name'] for p in providers]
assert provider_names==['agent37-smoke'],provider_names
print(json.dumps({'controller_id':controller['controller_id'],'provider_names':provider_names,'uid':os.getuid(),'web_ui':True}))
'''


def docker(*args, input=None):
    result = subprocess.run(['docker', *args], input=input, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Docker command failed: ' + result.stderr.strip())
    return result.stdout.strip()


def main(image):
    suffix = uuid.uuid4().hex[:12]
    name, volume = 'garm-controller-smoke-' + suffix, 'garm-controller-smoke-' + suffix
    with tempfile.TemporaryDirectory(prefix='garm-controller-smoke-') as directory:
        root = Path(directory)
        root.chmod(0o755)
        admin = {'username':'smokeadmin','password':secrets.token_urlsafe(32),'email':'smoke@example.invalid','full_name':'Container smoke'}
        files = {
            'smoke-admin.json':json.dumps(admin),
            'agent37-api-key':'offline-smoke-key\n',
            'provider.toml':'''provider_id = "agent37-smoke"
api_key_file = "/etc/garm/agent37-api-key"
state_dir = "/var/lib/garm/agent37"
max_instances = 45
''',
            'config.toml':f'''[default]
enable_webhook_management = false
callback_url = "http://127.0.0.1:9997/api/v1/callbacks"
metadata_url = "http://127.0.0.1:9997/api/v1/metadata"
webhook_url = "http://127.0.0.1:9997/webhooks"
[logging]
log_format = "json"
log_level = "info"
[apiserver]
bind = "0.0.0.0"
port = 9997
use_tls = false
[apiserver.webui]
enable = true
[database]
backend = "sqlite3"
passphrase = "{secrets.token_urlsafe(24)}"
[database.sqlite3]
db_file = "/var/lib/garm/garm.db"
[jwt_auth]
secret = "{secrets.token_urlsafe(48)}"
time_to_live = "1h"
[[provider]]
name = "agent37-smoke"
provider_type = "external"
disable_jit_config = true
[provider.external]
provider_executable = "/opt/garm/providers.d/garm-provider-agent37"
config_file = "/etc/garm/provider.toml"
exec_timeout_seconds = 420
''',
        }
        for filename, contents in files.items():
            path = root / filename
            path.write_text(contents)
            path.chmod(0o644)
        docker('volume', 'create', volume)
        try:
            docker('run','-d','--name',name,'--platform','linux/amd64','--read-only','--network','none',
                   '--cap-drop','ALL','--security-opt','no-new-privileges',
                   '--tmpfs','/tmp:rw,nosuid,nodev,mode=1777',
                   '--mount','type=volume,src='+volume+',dst=/var/lib/garm',
                   '--mount','type=bind,src='+str(root)+',dst=/etc/garm,readonly',image)
            before = json.loads(docker('exec','-i',name,'python3','-',input=PROBE))
            versions = docker('exec','-e','GARM_INTERFACE_VERSION=v0.1.1',
                              '-e','GARM_COMMAND=GetSupportedInterfaceVersions',
                              '-e','GARM_CONTROLLER_ID='+before['controller_id'],
                              '-e','GARM_PROVIDER_CONFIG_FILE=/etc/garm/provider.toml',name,
                              '/opt/garm/providers.d/garm-provider-agent37')
            assert json.loads(versions) == ['v0.1.0','v0.1.1']
            docker('restart','--time','20',name)
            after = json.loads(docker('exec','-i',name,'python3','-',input=PROBE))
            assert before['controller_id'] == after['controller_id']
            print(json.dumps({'image':image,'nonroot':True,'read_only_root':True,'offline':True,
                              'sqlite_restart':True,'web_ui':True,'provider_interfaces':json.loads(versions)}))
        finally:
            docker('rm','-f',name)
            docker('volume','rm',volume)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('image')
    main(parser.parse_args().image)
