import os
from pathlib import Path
import shutil
import sys

if len(sys.argv) != 3 or not sys.argv[2].endswith('.agent37.app'):
    raise SystemExit('Expected key file and Agent37 instance hostname')
node = shutil.which('node')
if not node:
    raise SystemExit('Node.js 20+ is required')
cli = Path(__file__).resolve().parent / '.state/agent37-cli/package/bin/agent37.js'
env = {'PATH':os.environ.get('PATH','/usr/bin:/bin'), 'AGENT37_API_KEY':Path(sys.argv[1]).read_text().strip()}
os.execve(node,[node,str(cli),'tunnel',sys.argv[2]],env)
