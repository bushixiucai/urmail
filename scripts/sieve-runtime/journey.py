#!/usr/bin/env python3
"""Exercise live Sieve entitlements only on this explicitly disposable host."""
import urllib.error
import datetime,email.message,imaplib,json,re,secrets,smtplib,ssl,subprocess,time,urllib.request,http.cookiejar
from pathlib import Path
assert Path('/root/core-runtime-authorized').read_text().strip()=='isolated-target24'
assert subprocess.check_output(['hostname'],text=True).strip()=='urmail-recovery-target-24'
f=json.loads(Path('/root/mailfix-mailbox.json').read_text());env=dict(line.split('=',1) for line in Path('/root/mailfix-test.env').read_text().splitlines() if '=' in line);host=env['URMAIL_MYHOSTNAME'];domain=f['address'].split('@')[1];assert domain.endswith('.test')
ctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/'+host+'.cert');base='http://127.0.0.1:8080/api/v1'
def sql(statement):
 return subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-At','-v','ON_ERROR_STOP=1','-c',statement],text=True).strip()
jar=http.cookiejar.CookieJar();opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
def call(path,body=None,token=None,method=None,csrf=None):
 headers={'Content-Type':'application/json'}
 if token:headers['Authorization']='Bearer '+token
 if csrf:headers['X-CSRF-Token']=csrf
 req=urllib.request.Request(base+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
 try:
  with opener.open(req,timeout=30) as response:return json.load(response)
 except urllib.error.HTTPError as error:raise RuntimeError(str(error)+" "+error.read().decode()) from None
login=call('/auth/login',{'email':env['URMAIL_ADMIN_EMAIL'],'password':env['URMAIL_ADMIN_PASSWORD']});admin=login['token'];csrf=login['csrf_token']
fixture=Path('/root/sieve-gate-mailbox.json')
if fixture.exists():peer=json.loads(fixture.read_text())
else:
 # Bound the existing fixture allocation using the real API after actual usage sync.
 owner=next(m for m in call('/mailboxes',token=admin)['mailboxes'] if m['address']==f['address'])
 if owner['quota_bytes']>536870912:call('/mailboxes/'+str(owner['id'])+'/quota',{'quota_bytes':536870912},admin,method='POST',csrf=csrf)
 password=secrets.token_urlsafe(24);created=call('/mailboxes',{'domain_id':f['domain_id'],'local_part':'sieve-peer','password':password,'quota_bytes':134217728},admin,csrf=csrf);peer={'address':'sieve-peer@'+domain,'password':password};fixture.write_text(json.dumps(peer));fixture.chmod(0o600)
for n in range(150):
 try:
  with imaplib.IMAP4_SSL(host,993,ssl_context=ctx) as imap:imap.login(peer['address'],peer['password'])
  break
 except imaplib.IMAP4.error:time.sleep(.3)
else:raise AssertionError('second owned mailbox not active')
session=call('/mailbox/session',{'email':f['address'],'password':f['password']})['token']
old_rules=call('/mailbox/rules',token=session);period=json.loads(sql("SELECT row_to_json(s) FROM subscriptions s WHERE tenant_id=1"));runid=secrets.token_hex(5);results=[]
def save_rules(label):
 current=call('/mailbox/rules',token=session);now=datetime.datetime.now(datetime.UTC)
 rules={'rules':[],'forwarding':{'enabled':True,'target':peer['address'],'keep_copy':True},'vacation':{'enabled':True,'subject':'Sieve gate vacation '+label+' '+runid,'message':'Finite synthetic auto reply '+label+' '+runid,'days':1,'from':(now-datetime.timedelta(minutes=2)).isoformat(),'until':(now+datetime.timedelta(hours=1)).isoformat()}}
 call('/mailbox/rules',{'rules':rules,'confirmed':True,'expected_version':current['version']},session,method='PUT')
 for _ in range(100):
  state=call('/mailbox/rules',token=session)
  if state['state']=='active':return rules
  time.sleep(.2)
 raise AssertionError('rules not applied')
def count(address,password,subject):
 with imaplib.IMAP4_SSL(host,993,ssl_context=ctx) as imap:
  imap.login(address,password);imap.select('INBOX');status,ids=imap.uid('search',None,'HEADER','Subject','"'+subject+'"');assert status=='OK';return len(ids[0].split())
def send(label):
 subject='Sieve gate inbound '+label+' '+runid;message=email.message.EmailMessage();message['From']=peer['address'];message['To']=f['address'];message['Subject']=subject;message['Message-ID']='<sieve-'+label+'-'+runid+'@'+domain+'>';message['Auto-Submitted']='no';message.set_content('Real inbound delivery retained '+runid)
 with smtplib.SMTP('127.0.0.1',25,timeout=15) as smtp:smtp.sendmail(peer['address'],[f['address']],message.as_bytes())
 return subject
def phase(label,expect,change=None,entitled=None):
 rules=save_rules(label)
 if change:change()
 allowed=sql('SET ROLE urmail_platform; SELECT urmail_mail_allowed(1)').splitlines()[-1];assert (allowed=='t')==(expect if entitled is None else entitled),(label,allowed)
 subject=send(label)
 for _ in range(100):
  original=count(f['address'],f['password'],subject);forwarded=count(peer['address'],peer['password'],subject);vacation=count(peer['address'],peer['password'],rules['vacation']['subject'])
  if original==1 and (not expect or forwarded==1 and vacation==1):break
  time.sleep(.2)
 time.sleep(1)
 forwarded=count(peer['address'],peer['password'],subject);vacation=count(peer['address'],peer['password'],rules['vacation']['subject']);assert original==1 and forwarded==int(expect) and vacation==int(expect),(label,original,forwarded,vacation)
 results.append({'phase':label,'live_entitlement':allowed,'original_inbox':original,'forwarded':forwarded,'vacation':vacation})
try:
 phase('valid',True)
 phase('six-day-grace',True,lambda:sql("UPDATE subscriptions SET status='active',period_start=now()-interval '30 days',period_end=now()-interval '6 days',trial_ends_at=NULL WHERE tenant_id=1"))
 phase('eight-day-expired',False,lambda:sql("UPDATE subscriptions SET status='active',period_start=now()-interval '30 days',period_end=now()-interval '8 days',trial_ends_at=NULL WHERE tenant_id=1"))
 # Saving is done while the old script is still present; subsequent delivery
 # observes the entitlement live rather than depending on another compilation.
 sql("UPDATE subscriptions SET status='active',period_start=now()-interval '30 days',period_end=now()+interval '1 day',trial_ends_at=NULL WHERE tenant_id=1")
 phase('restored',True)
 phase('gate-unavailable',False,lambda:Path('/run/urmail-policy/sieve-gate.sock').chmod(0),entitled=True)
 Path('/run/urmail-policy/sieve-gate.sock').chmod(0o666)
 phase('gate-recovered',True)
finally:
 Path('/run/urmail-policy/sieve-gate.sock').chmod(0o666)
 quoted=lambda value:"NULL" if value is None else "'"+str(value).replace("'","''")+"'"
 sql('UPDATE subscriptions SET '+','.join(k+'='+quoted(period[k]) for k in ['status','period_start','period_end','trial_ends_at'])+' WHERE tenant_id=1')
 current=call('/mailbox/rules',token=session);call('/mailbox/rules',{'rules':old_rules['rules'],'confirmed':True,'expected_version':current['version']},session,method='PUT')
Path('/root/sieve-gate-result.json').write_text(json.dumps({'result':'PASS','runid':runid,'samples':results},indent=2));print(json.dumps({'result':'PASS','samples':results}))
