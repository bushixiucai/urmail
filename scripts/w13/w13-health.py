import os
import json
from importlib.machinery import SourceFileLoader
Panel=SourceFileLoader('panel',os.environ.get('URMAIL_W13_SOURCE','/workspace')+'/scripts/remediation-http-smoke.py').load_module().Panel
creds=dict(line.strip().split('=',1)for line in open('/root/urmail-initial-admin.txt')if '='in line);panel=Panel('http://127.0.0.1:8080');panel.login(creds['email'],creds['password']);print(json.dumps({'health':panel.call('GET','/api/v1/mail/health'),'collectors':panel.call('GET','/api/v1/platform/mail/collectors')},indent=2))
