#!/usr/bin/env python3
"""Seed synthetic MIME/authorization only on the explicitly disposable target."""
import email.message,hashlib,http.cookiejar,imaplib,json,re,smtplib,ssl,subprocess,time,urllib.request,uuid
from pathlib import Path
assert Path('/root/core-runtime-authorized').read_text().strip()=='isolated-target24'
f=json.loads(Path('/root/mailfix-mailbox.json').read_text());v=dict(line.split('=',1) for line in Path('/root/mailfix-test.env').read_text().splitlines() if '=' in line)
assert re.fullmatch(r'[a-z0-9+_.-]+@[a-z0-9.-]+\.test',f['address'])
base='http://127.0.0.1:8080/api/v1';opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()));token=csrf=None
def call(path,payload=None):
 headers={'Content-Type':'application/json'}
 if token:headers['Authorization']='Bearer '+token
 if csrf:headers['X-CSRF-Token']=csrf
 req=urllib.request.Request(base+path,data=json.dumps(payload).encode() if payload is not None else None,headers=headers)
 with opener.open(req,timeout=30) as response:return json.load(response)
login=call('/auth/login',{'email':v['URMAIL_ADMIN_EMAIL'],'password':v['URMAIL_ADMIN_PASSWORD']});token=login['token'];csrf=login['csrf_token']
existing=call('/aliases')['aliases'];alias=next((item for item in existing if item['local_part']=='team-ui'),None)
if alias is None:alias=call('/aliases',{'domain_id':f['domain_id'],'local_part':'team-ui','target':f['address'],'kind':'alias'})['alias']
vacation=next((item for item in existing if item['local_part']=='vacation-sender'),None)
if vacation is None:vacation=call('/aliases',{'domain_id':f['domain_id'],'local_part':'vacation-sender','target':f['address'],'kind':'alias'})['alias']
sql="SET ROLE urmail_platform; UPDATE mailbox_aliases SET sender_logins=ARRAY['"+f['address']+"']::text[] WHERE tenant_id=1 AND id="+str(int(alias['id']))+';'
subprocess.run(['runuser','-u','postgres','--','psql','-d','urmail','-v','ON_ERROR_STOP=1','-c',sql],check=True,capture_output=True)
local,domain=f['address'].split('@');reply=local+'+reply@'+domain;cc=local+'+cc@'+domain;alias_address='team-ui@'+domain
message=email.message.EmailMessage();message['From']=f['address'];message['To']=alias_address;message['Cc']=cc;message['Reply-To']=reply;message['Subject']='Final webmail browser source '+uuid.uuid4().hex[:10];mid='<webmail-source-'+uuid.uuid4().hex+'@'+domain+'>';message['Message-ID']=mid
message.set_content('Synthetic original body for exact reply/forward acceptance.\n'+mid+'\n'*3)
message.add_alternative('<p>Readable synthetic source</p><img src="https://tracker.invalid/pixel"><script>window.trackerExecuted=true</script>',subtype='html')
attachment=bytes(range(256))*4;message.add_attachment(attachment,maintype='application',subtype='octet-stream',filename='acceptance.bin')
host=v['URMAIL_MYHOSTNAME'];ctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/'+host+'.cert')
with smtplib.SMTP(host,587,timeout=30) as smtp:
 smtp.ehlo();smtp.starttls(context=ctx);smtp.ehlo();smtp.login(f['address'],f['password']);smtp.sendmail(f['address'],[f['address']],message.as_bytes())
with imaplib.IMAP4_SSL(host,993,ssl_context=ctx,timeout=30) as imap:
 imap.login(f['address'],f['password']);imap.select('INBOX')
 for _ in range(100):
  status,ids=imap.uid('search',None,'HEADER','Message-ID',mid)
  if status=='OK' and ids[0]:break
  time.sleep(.1)
 else:raise AssertionError('synthetic source not delivered')
 uid=int(ids[0].split()[-1]);validity=int(imap.response('UIDVALIDITY')[1][0])
result={'source_subject':message['Subject'],'source_message_id':mid,'source_uid':uid,'source_validity':validity,'alias_address':alias_address,'vacation_sender':'vacation-sender@'+domain,'alias_id':alias['id'],'reply_to':reply,'cc':cc,'attachment_sha256':hashlib.sha256(attachment).hexdigest(),'attachment_base64':__import__('base64').b64encode(attachment).decode()}
Path('/root/webmail-browser-source.json').write_text(json.dumps(result));Path('/root/webmail-browser-source.json').chmod(0o600)
print('Prepared actual SMTP/LMTP/IMAP MIME and explicit isolated alias grant')
