import os
import json,ssl,urllib.request,urllib.error,pathlib,urllib.parse
from importlib.machinery import SourceFileLoader
Panel=SourceFileLoader('panel',os.environ.get('URMAIL_W13_SOURCE','/workspace')+'/scripts/remediation-http-smoke.py').load_module().Panel
base='https://box.w13.test';context=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/panel/fullchain.pem');cookies=json.load(open('/root/w13-rc-cookie.json'));sieve=next(c['value']for c in cookies if c['name']=='urmail_mailbox_sieve');rccookie='; '.join(c['name']+'='+c['value']for c in cookies if c['path'].startswith('/mail'))
def get(path,headers):
 req=urllib.request.Request(base+path,headers=headers)
 try:response=urllib.request.urlopen(req,context=context,timeout=20)
 except urllib.error.HTTPError as e:response=e
 raw=response.read();return response.status,raw
creds=dict(line.strip().split('=',1)for line in open('/root/urmail-initial-admin.txt')if '='in line);panel=Panel('http://127.0.0.1:8080');panel.login(creds['email'],creds['password']);status,_=get('/api/v1/mailbox/sieve/session',{'Authorization':'Bearer '+panel.token});assert status==401,status;print('PASS platform/panel credential alone cannot acquire mailbox Sieve principal')
status,_=get('/api/v1/auth/me',{'Cookie':'urmail_mailbox_sieve='+sieve});assert status==401,status;print('PASS scoped Sieve cookie cannot authenticate as a panel account')
csrf=json.load(open('/root/w13-rc-env.json'))['token'];status,_=get('/mail/?_task=logout&_token='+urllib.parse.quote(csrf),{'Cookie':rccookie});assert status==200,status
status,_=get('/api/v1/mailbox/sieve/session',{'Cookie':'urmail_mailbox_sieve='+sieve});assert status in (401,403),status
print('PASS actual Roundcube logout hook durably revoked the mailbox parent session; previously issued Sieve cookie immediately denied')
