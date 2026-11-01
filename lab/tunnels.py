import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import time

import manage as m

running = True


def stop(signum, frame):
    global running
    running = False


def main():
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    settings = m.read('settings.json')
    controller = m.read('controller.json')['controller_id']
    proxy_script = m.ROOT / 'tunnel_proxy.py'
    proxy = ' '.join(shlex.quote(s) for s in [sys.executable, str(proxy_script), settings['agent37_key_file']]) + ' %h'
    processes = {}
    logs = {}
    deadline = time.monotonic() + 1200
    m.save('watcher.json', {'pid': os.getpid(), 'started_at': time.time()})
    history_path = m.STATE / 'inventory.jsonl'
    with os.fdopen(os.open(history_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600), 'a') as history:
        try:
            while running and time.monotonic() < deadline:
                instances = m.cloud('GET', '/instances')['data']
                own = [i for i in instances if i.get('metadata', {}).get('garm-controller-id') == controller
                       and i.get('metadata', {}).get('garm-provider-id') == m.PROVIDER_ID]
                history.write(json.dumps({'at': time.time(), 'instances': [{k:i.get(k) for k in ('id','name','status','metadata')} for i in own]}) + '\n')
                history.flush()
                active = {i['id'] for i in own}
                for identifier in list(processes):
                    proc = processes[identifier]
                    if identifier not in active or proc.poll() is not None:
                        proc.terminate() if proc.poll() is None else None
                        try:
                            proc.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            proc.kill(); proc.wait()
                        logs.pop(identifier).close()
                        del processes[identifier]
                for instance in own:
                    identifier = instance['id']
                    if identifier in processes or instance['status'] != 'running':
                        continue
                    args = ['ssh','-F','/dev/null','-N','-i',str(m.STATE/'id_ed25519'),
                            '-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','ConnectTimeout=10',
                            '-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+str(m.STATE/'known_hosts'),
                            '-o','ExitOnForwardFailure=yes','-o','ServerAliveInterval=10','-o','ServerAliveCountMax=2',
                            '-o','ProxyCommand='+proxy,
                            '-R','127.0.0.1:13000:127.0.0.1:13000',
                            '-R','127.0.0.1:19997:127.0.0.1:19997',
                            'root@'+identifier+'.agent37.app']
                    logs[identifier] = (m.STATE/('tunnel-'+identifier+'.log')).open('a')
                    processes[identifier] = subprocess.Popen(args,stdin=subprocess.DEVNULL,stdout=logs[identifier],stderr=logs[identifier])
                time.sleep(1)
        finally:
            for proc in processes.values():
                if proc.poll() is None:proc.terminate()
            for proc in processes.values():
                try:proc.wait(timeout=5)
                except subprocess.TimeoutExpired:proc.kill();proc.wait()
            for log in logs.values():log.close()
            m.save('watcher-stopped.json', {'at': time.time()})


if __name__ == '__main__':
    main()
