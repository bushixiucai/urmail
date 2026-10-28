#!/usr/bin/env python3
"""Measure real installed mail paths, with actual AI APIs/marketing worker noise."""
import os,hashlib
import argparse,concurrent.futures,email.message,http.cookiejar,imaplib,json,math,smtplib,ssl,subprocess,threading,time,urllib.request,uuid
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('phase',choices=['prepare','baseline','noise']);a=p.parse_args()
assert Path('/root/core-runtime-authorized').read_text().strip()=='isolated-target24'
v=dict(x.split('=',1) for x in Path('/root/mailfix-test.env').read_text().splitlines() if '=' in x);f=json.loads(Path('/root/mailfix-mailbox.json').read_text());host=v['URMAIL_MYHOSTNAME'];base='http://127.0.0.1:8080/api/v1'
def api(path,payload=None,token=None,csrf=None,method=None,opener=None):
 headers={'Content-Type':'application/json'}
 if token:headers['Authorization']='Bearer '+token
 if csrf:headers['X-CSRF-Token']=csrf
 request=urllib.request.Request(base+path,data=json.dumps(payload).encode() if payload is not None else None,headers=headers,method=method)
 with (opener or urllib.request).urlopen(request,timeout=30) if opener is None else opener.open(request,timeout=30) as response:return json.load(response) if response.status!=204 else {}
# Openers expose .open rather than .urlopen; keep authentication cookie context.
def admin():
 opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
 login=api('/auth/login',{'email':v['URMAIL_ADMIN_EMAIL'],'password':v['URMAIL_ADMIN_PASSWORD']},opener=opener)
 return lambda path,body=None,method=None:api(path,body,login['token'],login['csrf_token'],method,opener)
if a.phase=='prepare':
 call=admin()
 call('/ai/configuration',{'mode':'local','provider':'ollama','endpoint':'http://127.0.0.1:11434','model':'bounded-fixture','enabled':True,'local_only':True},'PUT')
 channel=call('/platform/tenants/1/marketing/channel',{'name':'Core isolated TLS gateway','kind':'dedicated_gateway','enabled':True,'smtp':{'host':host,'port':1465,'from':f['address'],'dedicated':True,'implicit_tls':True,'feedback_key':'core-perf-fixture-key-000000000000000000'}})['channel']
 template=call('/marketing/templates',{'name':'Core bounded template','subject':'Core isolation sample','text_body':'Only the explicitly isolated TLS sink receives this fixture.'})['template']
 contacts=call('/marketing/lists',{'name':'Core consent fixture'})['list']
 call('/marketing/lists/'+str(contacts['id'])+'/contacts',{'contacts':[{'email':f'sample-{i}@core.perf.test','consent_source':'explicit disposable benchmark fixture','opted_in':True} for i in range(100)]})
 campaign=call('/marketing/campaigns',{'name':'Core worker budget','channel_id':channel['id'],'template_id':template['id'],'list_id':contacts['id']})['campaign']
 Path('/root/core-perf-state.json').write_text(json.dumps({'campaign':campaign['id']}));print('Prepared actual API configuration and draft campaign; no sends yet');raise SystemExit
state=json.loads(Path('/root/core-perf-state.json').read_text())
token=api('/mailbox/session',{'email':f['address'],'password':f['password']})['token']
ctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/'+host+'.cert')
stop=threading.Event();noise={'ok':0,'errors':[]};noise_lock=threading.Lock();raw={name:[] for name in ['smtp','imap','delivery','http_list','http_detail']};failures=[]
if a.phase=='noise':
 target=api('/mailbox/ai/target',token=token)
 digest=target['configuration_digest']
 def ai_noise():
  while not stop.is_set():
   try:
    result=api('/mailbox/ai/assist',{'operation':'summarize','body':'Bounded explicitly selected benchmark fixture','request_key':'core-perf-'+uuid.uuid4().hex,'configuration_digest':digest,'confirmed':True},token)
    with noise_lock:noise['ok']+=1
   except Exception as e:
    with noise_lock:noise['errors'].append(type(e).__name__)
    time.sleep(.1)
 pool=concurrent.futures.ThreadPoolExecutor(max_workers=4);futures=[pool.submit(ai_noise) for _ in range(4)]
 call=admin();call('/marketing/campaigns/'+str(state['campaign'])+'/confirm',{'confirm':True,'scheduled_at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())})
smtp_stages={key:[] for key in ('mail','rcpt','data')}
class TimedSMTP(smtplib.SMTP):
 def mail(self,*args,**kwargs):
  start=time.perf_counter();result=super().mail(*args,**kwargs);smtp_stages['mail'].append((time.perf_counter()-start)*1000);return result
 def rcpt(self,*args,**kwargs):
  start=time.perf_counter();result=super().rcpt(*args,**kwargs);smtp_stages['rcpt'].append((time.perf_counter()-start)*1000);return result
 def data(self,*args,**kwargs):
  start=time.perf_counter();result=super().data(*args,**kwargs);smtp_stages['data'].append((time.perf_counter()-start)*1000);return result
pid=subprocess.run(['systemctl','show','urmaild','--value','-p','MainPID'],check=True,capture_output=True,text=True).stdout.strip()
actual=Path('/proc')/pid/'exe'
assert str(actual.resolve())=='/usr/bin/urmaild','unexpected recovery service override'
start=time.perf_counter();resources={'active_executable':str(actual.resolve()),'cpus':os.cpu_count(),'load_average':os.getloadavg(),'daemon_sha256':hashlib.sha256(actual.read_bytes()).hexdigest()}
with TimedSMTP(host,587,timeout=20) as smtp,imaplib.IMAP4_SSL(host,993,ssl_context=ctx,timeout=20) as imap:
 t=time.perf_counter();smtp.ehlo();smtp.starttls(context=ctx);smtp.ehlo();smtp.login(f['address'],f['password']);smtp_login_ms=(time.perf_counter()-t)*1000;t=time.perf_counter();imap.login(f['address'],f['password']);imap.select('INBOX');imap_login_ms=(time.perf_counter()-t)*1000
 api('/mailbox/folders/INBOX/messages?limit=10',token=token)
 for index in range(100):
  cycle=time.perf_counter();mid='<core-'+uuid.uuid4().hex+'@'+host+'>';message=email.message.EmailMessage();message['From']=f['address'];message['To']=f['address'];message['Message-ID']=mid;message['Subject']=a.phase+' actual mail '+str(index);message.set_content('Exact core mail fixture '+mid)
  try:
   t=time.perf_counter();smtp.send_message(message);raw['smtp'].append((time.perf_counter()-t)*1000)
   delivery=time.perf_counter()
   for attempt in range(100):
    t=time.perf_counter();status,ids=imap.uid('search',None,'HEADER','Message-ID',mid)
    if status=='OK' and ids[0]:
     uid=ids[0].split()[-1];status,data=imap.uid('fetch',uid,'(BODY.PEEK[])');wire=next(x[1] for x in data if isinstance(x,tuple));assert mid.encode() in wire;raw['imap'].append((time.perf_counter()-t)*1000);break
    time.sleep(.05)
   else:raise AssertionError('delivery not visible')
   raw['delivery'].append((time.perf_counter()-delivery)*1000+raw['smtp'][-1])
   t=time.perf_counter();listing=api('/mailbox/folders/INBOX/messages?limit=10',token=token);raw['http_list'].append((time.perf_counter()-t)*1000)
   t=time.perf_counter();detail=api('/mailbox/folders/INBOX/messages/'+uid.decode(),token=token);raw['http_detail'].append((time.perf_counter()-t)*1000);assert mid in json.dumps(detail)
  except Exception as e:failures.append({'sample':index,'class':type(e).__name__,'reason':str(e)[:150]})
  time.sleep(max(0,1.1-(time.perf_counter()-cycle)))
end=time.perf_counter();foreground_standins=json.loads(Path('/root/core-standins.json').read_text()) if Path('/root/core-standins.json').exists() else {};stop.set()
if a.phase=='noise':
 pool.shutdown(wait=True)
limits={'smtp':1000,'imap':500,'delivery':5000,'http_list':300 if a.phase=='noise' else 250,'http_detail':300 if a.phase=='noise' else 250}
stats={}
for name,values in raw.items():
 ordered=sorted(values);stats[name]={'samples':len(values),'p50_ms':ordered[math.ceil(.5*len(ordered))-1] if ordered else None,'p95_ms':ordered[math.ceil(.95*len(ordered))-1] if ordered else None,'max_ms':max(values) if values else None,'budget_ms':limits[name]}
result={'phase':a.phase,'resources':resources,'cold_auth_ms':{'smtp_tls_auth':smtp_login_ms,'imap_auth_select':imap_login_ms},'smtp_stages_ms':smtp_stages,'elapsed_seconds':end-start,'stats':stats,'errors':failures,'ai_noise':noise,'standins_during_foreground':foreground_standins}
result['pass']=not failures and all(v['samples']==100 and v['p95_ms']<=v['budget_ms'] for v in stats.values())
if a.phase=='noise':result['pass']=result['pass'] and noise['ok']>=40 and result['standins_during_foreground'].get('marketing',0)>=40
queue_deadline=time.monotonic()+30
while True:
 queued=len(subprocess.run(['postqueue','-j'],check=True,capture_output=True,text=True).stdout.splitlines())
 if queued==0 or time.monotonic()>queue_deadline:break
 time.sleep(1)
result['postfix_queue_after_grace']=queued;result['pass']=result['pass'] and queued==0
if a.phase=='noise':
 deadline=time.monotonic()+180
 while True:
  sql="SET ROLE urmail_platform; SELECT COALESCE(json_object_agg(status,n),'{}') FROM (SELECT status,count(*) n FROM marketing_recipients WHERE tenant_id=1 AND campaign_id="+str(int(state['campaign']))+" GROUP BY status)s;"
  output=subprocess.run(['runuser','-u','postgres','--','psql','-d','urmail','-At','-c',sql],check=True,capture_output=True,text=True).stdout.strip().splitlines()[-1]
  statuses=json.loads(output)
  if statuses.get('sent')==100 or time.monotonic()>deadline:break
  time.sleep(1)
 result['marketing_after_drain']=statuses
 sink=json.loads(Path('/root/core-standins.json').read_text());result['final_standins']=sink
 result['pass']=result['pass'] and statuses=={'sent':100} and sink['marketing']==100 and len(set(sink['marketing_ids']))==100 and sink['rejected']==0
Path('/root/core-perf-'+a.phase+'.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2));raise SystemExit(0 if result['pass'] else 1)
