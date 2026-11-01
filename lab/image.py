import argparse
import hashlib
import io
import json
import re
import subprocess
import tarfile
import time
import urllib.request

import manage as m


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def resolve_job_image(reference):
    if not re.fullmatch(r'[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}', reference):
        raise ValueError('Job image must be pinned by registry digest')
    def inspect(value):
        return json.loads(subprocess.check_output(['docker', 'buildx', 'imagetools', 'inspect', value, '--raw'], text=True))
    repository, digest = reference.split('@')
    prefix, separator, leaf = repository.rpartition('/')
    repository = prefix + separator + leaf.split(':')[0]
    reference = repository + '@' + digest
    manifest = inspect(reference)
    if 'manifests' in manifest:
        matches = [item for item in manifest['manifests'] if item.get('platform', {}).get('architecture') == 'amd64' and item.get('platform', {}).get('os') == 'linux']
        if len(matches) != 1:
            raise ValueError('Expected exactly one Linux amd64 manifest')
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', matches[0]['digest']):
            raise ValueError('Invalid platform manifest digest')
        reference = repository + '@' + matches[0]['digest']
        manifest = inspect(reference)
    digest = manifest['config']['digest']
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', digest):
        raise ValueError('Invalid image configuration digest')
    return reference, digest, 'garm-template-cache/job:' + digest.split(':')[1][:16]


def build(name, job_image=None, tailscale=False):
    if not re.fullmatch(r'[a-z][a-z0-9-]{2,62}', name):
        raise RuntimeError('Use a lowercase workspace template name')
    context = m.REPO / 'images/runner'
    names = ['Dockerfile', 'entrypoint.py', 'image_cache.py', 'alpine.tar', '.dockerignore']
    if any(not (context / n).is_file() or (context / n).is_symlink() for n in names):
        raise RuntimeError('Prepare the five regular image context files first')
    files = {n:(context/n).read_bytes() for n in names}
    if tailscale:
        declaration = b'ARG GARM_TAILSCALE_ENABLED=0'
        if files['Dockerfile'].count(declaration) != 1:
            raise RuntimeError('Runner Dockerfile is missing its optional Tailscale build argument')
        files['Dockerfile'] = files['Dockerfile'].replace(declaration, b'ARG GARM_TAILSCALE_ENABLED=1')
    cached = None
    if job_image:
        source, digest, reference = resolve_job_image(job_image)
        cached = {'source': job_image, 'platform_source': source, 'config_digest': digest, 'reference': reference}
        extra = f'''\nRUN apt-get update && apt-get install -y --no-install-recommends skopeo \\
    && skopeo copy --override-os linux --override-arch amd64 docker://{source} docker-archive:/opt/garm/cache/job.tar:{reference} \\
    && python3 /opt/garm/image_cache.py /opt/garm/cache/job.tar {digest} > /opt/garm/cache/job-config.json \\
    && dpkg-query -W > /opt/garm/packages.txt \\
    && rm -rf /var/lib/apt/lists/*
ENV GARM_CACHED_JOB_IMAGE={reference}
'''
        files['Dockerfile'] += extra.encode()
    manifest = {n:hashlib.sha256(data).hexdigest() for n,data in files.items()}
    archive = m.STATE / 'image-context.tgz'
    with tarfile.open(archive, 'w:gz') as tar:
        for n,data in files.items():
            info = tarfile.TarInfo(n)
            info.size = len(data)
            info.mode = 0o644
            tar.addfile(info, io.BytesIO(data))
    archive.chmod(0o600)
    size = archive.stat().st_size
    result = m.cloud('POST','/template-builds',{'name':name,'size_bytes':size})
    m.save('image-build.json',{'id':result['id'],'name':name,'started_at':time.time(),
                              'tailscale_enabled':tailscale,'context_bytes':size,'files_sha256':manifest,'cached_job_image':cached})
    if not result['upload_url'].startswith('https://'):
        raise RuntimeError('Build service returned a non-HTTPS upload URL')
    req = urllib.request.Request(result['upload_url'],data=archive.read_bytes(),method='PUT',
                                 headers={'Content-Length':str(size)})
    with urllib.request.build_opener(NoRedirect).open(req,timeout=180) as response:
        if not 200<=response.status<300:raise RuntimeError('Build-context upload failed')
    m.cloud('POST','/template-builds/'+result['id']+'/start',{})
    print(json.dumps({'id':result['id'],'name':name,'status':'building'}))


def status():
    saved = m.read('image-build.json')
    result = m.cloud('GET','/template-builds/'+saved['id'])
    m.save('image-build-status.json',result)
    print(json.dumps({k:result.get(k) for k in ('id','status')}))
    if result.get('status')=='succeeded':
        template = m.cloud('GET','/templates/'+saved['name'])
        m.save('image-template.json',template)
        print(json.dumps({k:template.get(k) for k in ('name','revision','image_digest')}))
    elif result.get('status')=='failed':
        m.save('image-build-logs.json',m.cloud('GET','/template-builds/'+saved['id']+'/logs?offset=0'))
        raise RuntimeError('Build failed; details retained in private state')


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('operation',choices=['build','status'])
    parser.add_argument('--name',default='garm-gitea-runner')
    parser.add_argument('--job-image')
    parser.add_argument('--tailscale',action='store_true',help='Build the optional Tailscale/Docker DNS image variant')
    args=parser.parse_args()
    if args.operation=='build':build(args.name,args.job_image,args.tailscale)
    else:status()
