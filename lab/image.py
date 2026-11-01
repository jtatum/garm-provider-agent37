import argparse
import hashlib
import json
import re
import tarfile
import time
import urllib.request

import manage as m


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def build(name):
    if not re.fullmatch(r'[a-z][a-z0-9-]{2,62}', name):
        raise RuntimeError('Use a lowercase workspace template name')
    context = m.REPO / 'images/runner'
    names = ['Dockerfile', 'entrypoint.py', 'alpine.tar', '.dockerignore']
    if any(not (context / n).is_file() or (context / n).is_symlink() for n in names):
        raise RuntimeError('Prepare the four regular image context files first')
    manifest = {n:hashlib.sha256((context/n).read_bytes()).hexdigest() for n in names}
    archive = m.STATE / 'image-context.tgz'
    with tarfile.open(archive, 'w:gz') as tar:
        for n in names:tar.add(context/n, arcname=n, recursive=False)
    archive.chmod(0o600)
    size = archive.stat().st_size
    result = m.cloud('POST','/template-builds',{'name':name,'size_bytes':size})
    m.save('image-build.json',{'id':result['id'],'name':name,'started_at':time.time(),
                              'context_bytes':size,'files_sha256':manifest})
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
    args=parser.parse_args()
    if args.operation=='build':build(args.name)
    else:status()
