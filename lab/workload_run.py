import argparse
import base64
import concurrent.futures
import json
import os
from pathlib import Path
import re
import subprocess
import time
import tomllib
import urllib.request

import integration as lifecycle
import manage as m

PROBE = '''import json,os,time
from pathlib import Path
mem={k:int(v.split()[0])*1024 for k,v in (line.split(':',1) for line in Path('/proc/meminfo').read_text().splitlines())}
cpu=[int(v) for v in Path('/proc/stat').read_text().splitlines()[0].split()[1:]]
disk=os.statvfs('/root')
result={'at':time.time(),'cpu_ticks':cpu,'memory_total':mem['MemTotal'],'memory_available':mem['MemAvailable'],'disk_total':disk.f_blocks*disk.f_frsize,'disk_available':disk.f_bavail*disk.f_frsize,'load':os.getloadavg()}
for key,path in [('image_ready','/root/garm-image/ready.json'),('runner_started','/root/garm/runner.started')]:
 try:result[key]=json.loads(Path(path).read_text())
 except (FileNotFoundError,json.JSONDecodeError):pass
print(json.dumps(result))
'''


def probe(identifier):
    key = Path(m.read('settings.json')['agent37_key_file']).read_text().strip()
    body = {'command': "python3 - <<'GARM_RESOURCE_PROBE'\n" + PROBE + '\nGARM_RESOURCE_PROBE', 'user': 'root', 'name': 'workload-resource-' + str(time.time_ns())}
    request = urllib.request.Request('https://sandbox.agent37.app/v1/instances/' + identifier + '/exec',
                                     data=json.dumps(body).encode(), headers={'X-Agent37-Key': key, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=15) as response:
        data = json.load(response)
    if data['exit_code'] != 0:
        raise RuntimeError('Resource probe unavailable')
    return json.loads(data['stdout'])


def save_logs(jobs, directory):
    credentials = json.loads(Path(m.read('settings.json')['gitea_credentials']).read_text())
    auth = base64.b64encode((credentials['username'] + ':' + credentials['password']).encode()).decode()
    directory.mkdir(mode=0o700, exist_ok=True)
    errors = []
    for job in jobs:
        request = urllib.request.Request(m.GITEA + lifecycle.PATH + '/actions/jobs/' + str(job['id']) + '/logs', headers={'Authorization': 'Basic ' + auth})
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                data = response.read()
            path = directory / (str(job['id']) + '.log')
            with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'wb') as output:
                output.write(data)
        except Exception as exc:
            errors.append({'job_id': job['id'], 'error': type(exc).__name__})
    return errors


def run(workflow, concurrency, expected_jobs, record, timeout):
    if not re.fullmatch(r'[a-z0-9-]+\.ya?ml', workflow) or not re.fullmatch(r'[a-z0-9-]+', record):
        raise ValueError('Invalid workflow or record name')
    if expected_jobs < 1 or timeout < 1:
        raise ValueError('Job count and timeout must be positive')
    pool = m.garm('GET', '/pools/' + m.read('pool.json')['id'])
    provider = tomllib.loads((m.STATE / 'provider.toml').read_text())
    if pool.get('enabled', False) or pool.get('min_idle_runners', 0) != 0 or lifecycle.instances() or lifecycle.cloud_owned():
        raise RuntimeError('Expected disabled zero-idle pool with no workers')
    if pool['max_runners'] != concurrency or provider['max_instances'] != concurrency:
        raise RuntimeError('Pool and provider caps must match requested concurrency')
    billing = m.cloud('GET', '/billing')
    if billing['instance_limit'] - len(m.cloud('GET', '/instances')['data']) < concurrency or billing.get('past_due'):
        raise RuntimeError('Insufficient workspace capacity')
    old_ids = {r['id'] for r in m.gitea('GET', lifecycle.PATH + '/actions/runs?limit=50')['workflow_runs']}
    result = {'workflow': workflow, 'concurrency': concurrency, 'expected_jobs': expected_jobs, 'billing_before': billing,
              'pool': {k:pool.get(k) for k in ('image','flavor','extra_specs')}, 'observations': [], 'samples': {}, 'run_id': None}
    watcher = None
    executor = concurrent.futures.ThreadPoolExecutor(max_workers=10)
    pending = {}
    last_sample = {}
    all_vms = set()
    jobs = []
    completed = False
    filename = record + '.json'
    try:
        m.key()
        with (m.STATE / 'tunnels.log').open('a') as log:
            watcher = subprocess.Popen(['python3', str(m.ROOT / 'tunnels.py')], stdin=subprocess.DEVNULL, stdout=log, stderr=log)
        m.garm('PUT', '/pools/' + pool['id'], {'enabled': True})
        result['dispatched_at'] = time.time()
        m.gitea('POST', lifecycle.PATH + '/actions/workflows/' + workflow + '/dispatches', {'ref': 'main'})
        deadline = time.monotonic() + timeout
        last_status = None
        while time.monotonic() < deadline:
            if not result['run_id']:
                runs = m.gitea('GET', lifecycle.PATH + '/actions/runs?limit=50')['workflow_runs']
                matches = [r for r in runs if r['id'] not in old_ids and r.get('path','').split('@')[0] == workflow]
                if matches:
                    result['run_id'] = matches[0]['id']
                    print('Run ' + str(result['run_id']) + ' dispatched: ' + workflow, flush=True)
            if result['run_id']:
                jobs = m.gitea('GET', lifecycle.PATH + '/actions/runs/' + str(result['run_id']) + '/jobs?limit=100')['jobs']
            own = lifecycle.cloud_owned()
            current = lifecycle.instances()
            all_vms.update(vm['id'] for vm in own)
            result['observations'].append({'at': time.time(), 'cloud_ids': [vm['id'] for vm in own], 'garm': current,
                'jobs': [{k:j.get(k) for k in ('id','name','status','conclusion','runner_id','started_at','completed_at')} for j in jobs]})
            if len(own) > concurrency:
                raise RuntimeError('Configured VM cap exceeded')
            for identifier, future in list(pending.items()):
                if future.done():
                    try: result['samples'].setdefault(identifier, []).append(future.result())
                    except Exception: pass
                    del pending[identifier]
            for vm in own:
                identifier = vm['id']
                if vm['status'] == 'running' and identifier not in pending and time.monotonic() - last_sample.get(identifier, 0) > 5:
                    pending[identifier] = executor.submit(probe, identifier)
                    last_sample[identifier] = time.monotonic()
            status = (len(own), sum(j['status']=='in_progress' for j in jobs), sum(j['status']=='completed' for j in jobs), sum(j.get('conclusion')=='failure' for j in jobs))
            if status != last_status:
                print(f'{time.time()-result["dispatched_at"]:.1f}s: VMs={status[0]} running={status[1]} completed={status[2]}/{expected_jobs} failed={status[3]}', flush=True)
                last_status = status
            result['jobs'] = jobs
            result['vm_ids'] = sorted(all_vms)
            m.save(filename, result)
            if len(jobs) == expected_jobs and all(j['status']=='completed' for j in jobs) and not own and not current:
                completed = True
                result['scale_zero_at'] = time.time()
                result['success'] = all(j['conclusion']=='success' for j in jobs)
                result['distinct_runner_count'] = len({j['runner_id'] for j in jobs if j.get('runner_id')})
                if result['success'] and (result['distinct_runner_count'] != expected_jobs or len(all_vms) != expected_jobs):
                    result['success'] = False
                    result['error'] = 'Expected one distinct runner and observed VM per successful job'
                break
            time.sleep(2)
        if not completed:
            raise RuntimeError('Workload or scale-to-zero exceeded deadline')
    except (Exception, KeyboardInterrupt) as exc:
        result['error'] = type(exc).__name__ + ': ' + str(exc)
        raise
    finally:
        try:
            try: m.garm('PUT', '/pools/' + pool['id'], {'enabled': False})
            except Exception as exc: result['disable_error'] = type(exc).__name__
            if not completed and result['run_id']:
                try: m.gitea('POST', lifecycle.PATH + '/actions/runs/' + str(result['run_id']) + '/cancel', {})
                except Exception: pass
            result['cleanup'] = lifecycle.cleanup()
            result['billing_after'] = m.cloud('GET', '/billing')
            result['log_errors'] = save_logs(jobs, m.STATE / (record + '-logs'))
        finally:
            if watcher:
                watcher.terminate()
                try: watcher.wait(timeout=10)
                except subprocess.TimeoutExpired: watcher.kill(); watcher.wait()
            executor.shutdown(wait=True, cancel_futures=True)
            for identifier, future in pending.items():
                if not future.cancelled():
                    try: result['samples'].setdefault(identifier, []).append(future.result())
                    except Exception: pass
            m.save(filename, result)
    print(json.dumps({'run_id': result['run_id'], 'success': result['success'], 'jobs': len(jobs), 'vms': len(all_vms),
                      'elapsed_seconds': result['scale_zero_at']-result['dispatched_at']}), flush=True)
    return result['success']


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--workflow', required=True)
    parser.add_argument('--concurrency', type=int, choices=range(1,11), required=True)
    parser.add_argument('--jobs', type=int, required=True)
    parser.add_argument('--record', required=True)
    parser.add_argument('--timeout', type=int, default=1200)
    args = parser.parse_args()
    raise SystemExit(0 if run(args.workflow, args.concurrency, args.jobs, args.record, args.timeout) else 1)
