import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import time
import tomllib

import manage as m

PATH = '/repos/lab-admin/runner-proof'


def instances():
    return [{k:i.get(k) for k in ('id','name','provider_id','pool_id','agent_id','status','runner_status')}
            for i in (m.garm('GET', '/instances') or [])]


def cloud_owned():
    controller = m.read('controller.json')['controller_id']
    return [i for i in m.cloud('GET','/instances')['data'] if
            i.get('metadata',{}).get('garm-controller-id') == controller and
            i.get('metadata',{}).get('garm-provider-id') == m.PROVIDER_ID]


def run_jobs(run_id):
    jobs = {}
    for page in range(1, 102):
        response = m.gitea('GET', PATH + '/actions/runs/' + str(run_id) + '/jobs?limit=100&page=' + str(page))
        rows = response['jobs']
        if not rows:
            return list(jobs.values())
        before = len(jobs)
        jobs.update((job['id'], job) for job in rows)
        if len(jobs) >= response.get('total_count', float('inf')):
            return list(jobs.values())
        if len(jobs) == before:
            raise RuntimeError('Job pagination did not advance')
    raise RuntimeError('Job pagination exceeded lab limit')


def install_workflow(shards):
    path = PATH + '/contents/.gitea/workflows/garm-agent37.yaml'
    source=(m.ROOT/'burst-workflow.yaml').read_text()
    matrix='        shard: [one, two, three]'
    if source.count(matrix)!=1:raise RuntimeError('Unexpected workflow matrix template')
    source=source.replace(matrix,'        shard: '+json.dumps(shards))
    body = {'content':base64.b64encode(source.encode()).decode(),
            'message':'test: verify GARM Agent37 queue and VM lifecycle','branch':'main'}
    tree = m.gitea('GET',PATH+'/git/trees/main?recursive=true')['tree']
    exists = any(f['path']=='.gitea/workflows/garm-agent37.yaml' for f in tree)
    if exists:
        old = m.gitea('GET',path)
        body['sha'] = old['sha']
    m.gitea('PUT' if exists else 'POST',path,body)


def cleanup():
    errors = []
    pool = m.read('pool.json')
    try:
        m.garm('PUT','/pools/'+pool['id'],{'enabled':False})
    except Exception as exc:
        errors.append('disable pool: '+str(exc))
        try:m.stop()
        except Exception as stop_error:errors.append('stop controller: '+str(stop_error))
    try:
        remaining = cloud_owned()
        if remaining:
            env = dict(os.environ)
            env.update({'GARM_COMMAND':'RemoveAllInstances','GARM_CONTROLLER_ID':m.read('controller.json')['controller_id'],
                        'GARM_PROVIDER_CONFIG_FILE':str(m.STATE/'provider.toml'),'GARM_INTERFACE_VERSION':'v0.1.1'})
            result = subprocess.run([str(m.REPO/'bin/garm-provider-agent37')],env=env,capture_output=True,text=True,timeout=60)
            m.save('emergency-cleanup.json',{'returncode':result.returncode,'stderr':result.stderr})
            if result.returncode:errors.append('provider cleanup failed; see private evidence')
    except Exception as exc:
        errors.append('VM cleanup: '+str(exc))
    try:
        key = m.read('ssh-key.json') if (m.STATE/'ssh-key.json').exists() else None
        keys = m.cloud('GET','/ssh-keys')['data']
        if key and any(k['id']==key['id'] for k in keys):
            m.cloud('DELETE','/ssh-keys/'+key['id'])
        for name in ['id_ed25519','id_ed25519.pub']:(m.STATE/name).unlink(missing_ok=True)
    except Exception as exc:
        errors.append('SSH key cleanup: '+str(exc))
    result = {'at':time.time(),'errors':errors,
              'remaining_cloud_ids':[i['id'] for i in cloud_owned()],
              'remaining_workspace_instances':len(m.cloud('GET','/instances')['data']),
              'remaining_workspace_ssh_keys':len(m.cloud('GET','/ssh-keys')['data']),
              'gitea_runners':m.gitea('GET',PATH+'/actions/runners')}
    m.save('cleanup.json',result)
    if errors or result['remaining_cloud_ids']:
        raise RuntimeError('Incomplete lab cleanup; inspect private cleanup.json')
    return result


def main(expect_failure=False, concurrency=2, job_count=3):
    if not 1<=concurrency<=m.MAX_CONCURRENCY or not concurrency<job_count<=100:
        raise ValueError(f'Require 1–{m.MAX_CONCURRENCY} concurrent VMs and more jobs than VMs, up to 100 jobs')
    provider=tomllib.loads((m.STATE/'provider.toml').read_text())
    pool=m.garm('GET','/pools/'+m.read('pool.json')['id'])
    if provider['max_instances']!=concurrency or pool['max_runners']!=concurrency:
        raise RuntimeError('Provider and GARM pool caps must match --concurrency')
    if pool.get('enabled',False) or pool.get('min_idle_runners',0)!=0 or instances() or cloud_owned():
        raise RuntimeError('Start with a disabled zero-idle pool and no lab workers')
    billing=m.cloud('GET','/billing')
    available=billing['instance_limit']-len(m.cloud('GET','/instances')['data'])
    if available<concurrency or billing.get('past_due',False):
        raise RuntimeError('Workspace does not currently have the requested capacity')
    shards=['job-'+str(i).zfill(2) for i in range(1,job_count+1)]
    install_workflow(shards)
    old_ids = {r['id'] for r in m.gitea('GET',PATH+'/actions/runs?limit=20')['workflow_runs']}
    gate = 'garm-lab-release-' + str(int(time.time()))
    result = {'started_at':time.time(),'gate':gate,'observations':[],'run_id':None,'expected_failures':int(expect_failure),
              'concurrency':concurrency,'job_count':job_count,'billing_before':billing}
    watcher = None
    try:
        m.key()
        log = (m.STATE/'tunnels.log').open('a')
        watcher = subprocess.Popen(['python3',str(m.ROOT/'tunnels.py')],stdin=subprocess.DEVNULL,stdout=log,stderr=log)
        log.close()
        m.garm('PUT','/pools/'+m.read('pool.json')['id'],{'enabled':True})
        result['dispatched_at']=time.time()
        m.gitea('POST',PATH+'/actions/workflows/garm-agent37.yaml/dispatches',{'ref':'main','inputs':{'gate':gate,'fail_shard':shards[-1] if expect_failure else 'none'}})
        print(f'Dispatched {job_count} gated jobs; waiting for {concurrency} running workers.',flush=True)
        deadline = time.monotonic()+300
        while time.monotonic()<deadline:
            rows = m.gitea('GET',PATH+'/actions/runs?limit=20')['workflow_runs']
            matches = [r for r in rows if r['id'] not in old_ids and r.get('path','').split('@')[0]=='garm-agent37.yaml']
            if matches:result['run_id']=matches[0]['id']
            jobs = run_jobs(result['run_id']) if result['run_id'] else []
            own = cloud_owned()
            observed = {'at':time.time(),'cloud_ids':[i['id'] for i in own],'garm':instances(),
                        'jobs':[{k:j.get(k) for k in ('id','status','conclusion','runner_id','started_at','completed_at')} for j in jobs]}
            result['observations'].append(observed)
            m.save('integration.json',result)
            assert len(own)<=concurrency,'Provider exceeded its configured VM cap'
            if any(j.get('conclusion')=='failure' for j in jobs):raise RuntimeError('Proof job failed before release')
            busy = [j for j in jobs if j['status']=='in_progress']
            if len(busy)==concurrency and len(jobs)==job_count:
                assert len(own)==concurrency
                assert sum(j['status']=='queued' for j in jobs)==job_count-concurrency
                result['before_restart']=observed
                break
            time.sleep(1)
        else:raise RuntimeError('Did not observe the requested running concurrency and queued remainder within five minutes')
        print(f'{concurrency} jobs running at the cap; restarting GARM before releasing the queue.',flush=True)
        m.stop();m.start()
        deadline=time.monotonic()+30
        expected=set(result['before_restart']['cloud_ids'])
        while time.monotonic()<deadline:
            own=cloud_owned()
            assert {i['id'] for i in own}==expected,'Controller restart replaced or added active workers'
            current=instances()
            if len([i for i in current if i['provider_id'] in expected])==concurrency:
                result['after_restart']={'at':time.time(),'cloud_ids':sorted(expected),'garm':current}
                break
            time.sleep(0.5)
        else:raise RuntimeError('GARM did not recover active instance records')
        m.gitea('POST',PATH+'/tags',{'tag_name':gate,'target':'main','message':'Release isolated GARM capacity test'})
        result['released_at']=time.time()
        print(f'Controller recovered all {concurrency} workers; gate released.',flush=True)
        deadline=time.monotonic()+600
        while time.monotonic()<deadline:
            jobs=run_jobs(result['run_id'])
            own=cloud_owned()
            observed={'at':time.time(),'cloud_ids':[i['id'] for i in own],'garm':instances(),
                      'jobs':[{k:j.get(k) for k in ('id','status','conclusion','runner_id','started_at','completed_at')} for j in jobs]}
            result['observations'].append(observed)
            m.save('integration.json',result)
            assert len(own)<=concurrency,'Provider exceeded its configured VM cap'
            if len(jobs)==job_count and all(j['status']=='completed' for j in jobs):
                conclusions=sorted(j['conclusion'] for j in jobs)
                assert conclusions==(['failure']+['success']*(job_count-1) if expect_failure else ['success']*job_count),'Unexpected job conclusions'
                assert len({j['runner_id'] for j in jobs})==job_count,'Runner identity was reused'
                result['jobs']=jobs
                if not own and not instances():break
            time.sleep(1)
        else:raise RuntimeError('Jobs or automatic scale-to-zero did not complete')
        all_vms={identifier for o in result['observations'] for identifier in o['cloud_ids']}
        result['allocation_exact'] = len(all_vms) == job_count
        result['observed_vm_count'] = len(all_vms)
        result['surplus_vm_count'] = max(0, len(all_vms) - job_count)
        result['success']=True
        result['vm_ids']=sorted(all_vms)
        result['completed_at']=time.time()
        m.save('integration.json',result)
        print(json.dumps({'run_id':result['run_id'],'success':True,'vm_ids':result['vm_ids'],'runner_ids':[j['runner_id'] for j in jobs]}),flush=True)
    except Exception as exc:
        result['failure']=type(exc).__name__+': '+str(exc)
        try:m.garm('PUT','/pools/'+m.read('pool.json')['id'],{'enabled':False})
        except Exception:pass
        if result['run_id']:
            try:m.gitea('POST',PATH+'/actions/runs/'+str(result['run_id'])+'/cancel',{})
            except Exception:pass
        for vm in cloud_owned():
            try:m.save('failure-logs-'+vm['id']+'.json',m.cloud('GET','/instances/'+vm['id']+'/logs?tail=200'))
            except Exception:pass
        m.save('integration.json',result)
        raise
    finally:
        try:
            clean=cleanup()
            result['cleanup']=clean
            result['billing_after']=m.cloud('GET','/billing')
            m.save('integration.json',result)
            print(json.dumps(clean),flush=True)
        finally:
            if watcher:
                watcher.terminate()
                try:watcher.wait(timeout=10)
                except subprocess.TimeoutExpired:watcher.kill();watcher.wait()


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--expect-failure',action='store_true')
    parser.add_argument('--concurrency',type=int,choices=range(1,m.MAX_CONCURRENCY+1),default=2)
    parser.add_argument('--jobs',type=int)
    args=parser.parse_args()
    main(args.expect_failure,args.concurrency,args.jobs if args.jobs is not None else args.concurrency+1)
