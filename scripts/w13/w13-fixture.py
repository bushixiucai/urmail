import json,secrets,subprocess,time,os,datetime
from importlib.machinery import SourceFileLoader
Panel=SourceFileLoader('panel',os.environ.get('URMAIL_W13_SOURCE','/workspace')+'/scripts/remediation-http-smoke.py').load_module().Panel
creds=dict(line.strip().split('=',1)for line in open('/root/urmail-initial-admin.txt')if '='in line)
# Explicit, finite operator gift for protocol acceptance; never inherit a hidden free term.
setup_env=dict(os.environ,URMAIL_SETUP_PASSWORD=creds['password'])
free_until=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=30)).isoformat().replace('+00:00','Z')
subprocess.run(['urmailctl','setup','--email',creds['email'],'--free-until',free_until,'--free-reason','Explicit local acceptance operator gift'],env=setup_env,check=True,stdout=subprocess.DEVNULL)
p=Panel('http://127.0.0.1:8080');p.login(creds['email'],creds['password'])
domains=p.call('GET','/api/v1/domains')['domains']; d=next((x for x in domains if x['fqdn']=='mailbox.w13.test'),None)
if d is None:d=p.call('POST','/api/v1/domains',{'fqdn':'mailbox.w13.test','mode':'managed'},expected=201)['domain']
d['tenant_id']=int(subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-Atqc','SELECT tenant_id FROM domains WHERE id='+str(d['id'])],text=True).strip())
subprocess.run(['runuser','-u','postgres','--','psql','-d','urmail','-v','ON_ERROR_STOP=1','-c',"SET ROLE urmail_platform; UPDATE domains SET status='active',verified_at=now(),provisioning_state='ready' WHERE id=%d AND tenant_id=%d"%(d['id'],d['tenant_id'])],check=True,stdout=subprocess.DEVNULL)
path='/root/w13-private-fixture.json'
if os.path.exists(path):
 f=json.load(open(path));assert f['tenant_id']==d['tenant_id'] and f['domain_id']==d['id'];b={'id':f['mailbox_id'],'address':f['address']}
else:
 password=secrets.token_urlsafe(24)
 b=p.call('POST','/api/v1/mailboxes',{'domain_id':d['id'],'local_part':'independent','password':password,'quota_bytes':67108864},expected=201)['mailbox']
 f={'address':b['address'],'password':password,'mailbox_id':b['id'],'tenant_id':d['tenant_id'],'domain_id':d['id']}
 fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600);os.write(fd,json.dumps(f).encode());os.close(fd)
for _ in range(180):
 result=subprocess.run(['urmailctl','config','status'],capture_output=True,text=True)
 if result.returncode==0:
  lookup=subprocess.run(['postmap','-q',b['address'],'hash:/etc/postfix/urmail/virtual_mailbox.map'],capture_output=True,text=True)
  if lookup.stdout.strip()=='1':break
 time.sleep(1)
else:raise RuntimeError('mailbox config not ready')
print('PASS own-mailbox fixture and current generation applied; domain ownership activated only as an explicit local PG fixture, not public DNS validation')
print('tenant',d['tenant_id'],'domain',d['id'],'mailbox',b['id'],'address',b['address'])
