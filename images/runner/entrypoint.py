import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

from image_cache import verify_loaded

started = time.monotonic()
started_at = time.time()
home = Path(os.environ['HOME'])
state = home / 'garm-image'
state.mkdir(mode=0o700, exist_ok=True)
ready = state / 'ready.json'
ready.unlink(missing_ok=True)
fs = subprocess.check_output(['findmnt', '-n', '-o', 'FSTYPE', '-T', str(home)], text=True).strip()
if fs in ('overlay', 'tmpfs'):
    raise RuntimeError('Docker data must live on the persistent disk')
docker = subprocess.Popen(['dockerd', '--host=unix:///var/run/docker.sock', '--data-root=' + str(home / 'docker')])


def shutdown(signum, frame):
    ready.unlink(missing_ok=True)
    docker.terminate()


signal.signal(signal.SIGTERM, shutdown)
signal.signal(signal.SIGINT, shutdown)
try:
    deadline = time.monotonic() + 90
    while subprocess.run(['docker', 'info'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
        if docker.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('Docker failed to become ready')
        time.sleep(0.2)
    docker_ready = time.monotonic()
    image = 'garm-template-cache/alpine:3.22'
    if subprocess.run(['docker', 'image', 'inspect', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
        subprocess.run(['docker', 'load', '-i', '/opt/garm/cache/alpine.tar'], check=True, timeout=90)
    info = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image], text=True))[0]
    assert info['Architecture'] == 'amd64' and info['Os'] == 'linux'
    assert info['RootFS']['Layers'] == ['sha256:be64570a34e4c5e361d1f5d9cb42b851e7f9a33a61f91c335b287c1c7868f921']
    subprocess.run(['docker', 'tag', image, 'alpine:3.22'], check=True)
    subprocess.run(['docker', 'run', '--rm', '--pull=never', '--network=none', 'alpine:3.22', 'true'], check=True, timeout=30)
    job_image = os.environ.get('GARM_CACHED_JOB_IMAGE')
    cache_started = time.monotonic()
    if job_image:
        subprocess.run(['docker', 'load', '-i', '/opt/garm/cache/job.tar'], check=True, timeout=180)
        job_info = json.loads(subprocess.check_output(['docker', 'image', 'inspect', job_image], text=True))[0]
        expected = json.loads(Path('/opt/garm/cache/job-config.json').read_text())
        verify_loaded(job_info, expected)
        subprocess.run(['docker', 'run', '--rm', '--pull=never', '--network=none', job_image, 'true'], check=True, timeout=30)
    subprocess.run(['gitea-runner', '--version'], check=True)
    ready.write_text(json.dumps({'entrypoint_started_at': started_at, 'ready_at': time.time(),
                                'docker_ready_seconds': docker_ready - started,
                                'total_bootstrap_seconds': time.monotonic() - started,
                                'filesystem': fs, 'cached_image': image, 'cached_job_image': job_image,
                                'job_image_load_seconds': time.monotonic() - cache_started}))
    sys.exit(docker.wait())
finally:
    ready.unlink(missing_ok=True)
    if docker.poll() is None:
        docker.terminate()
        try:
            docker.wait(timeout=30)
        except subprocess.TimeoutExpired:
            docker.kill()
            docker.wait()
