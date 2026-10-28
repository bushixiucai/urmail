#!/usr/bin/env python3
"""Check arbitrary external execution is unavailable to authenticated personal Sieve."""
import base64,email.message,imaplib,json,re,secrets,smtplib,socket,ssl,subprocess,time
from pathlib import Path
assert Path('/root/core-runtime-authorized').read_text().strip()=='isolated-target24'
f=json.loads(Path('/root/sieve-gate-mailbox.json').read_text());env=dict(line.split('=',1) for line in Path('/root/mailfix-test.env').read_text().splitlines() if '=' in line);host=env['URMAIL_MYHOSTNAME']
connection=socket.create_connection(('127.0.0.1',4190),timeout=10)
def response():
 result=[];line=b''
 while True:
  byte=connection.recv(1)
  if not byte:raise AssertionError('ManageSieve closed')
  line+=byte
  if len(line)>65536:raise AssertionError('oversized response')
  if line.endswith(b'\r\n'):
   result.append(line)
   if line.startswith((b'OK',b'NO',b'BYE')):
    literal=re.search(br'\{([0-9]+)\}\r\n$',line)
    if literal:
     length=int(literal.group(1));assert length<65536;data=b''
     while len(data)<length+2:data+=connection.recv(length+2-len(data))
     result.append(data)
    return b''.join(result)
   line=b''
assert response().splitlines()[-1].startswith(b'OK')
connection.sendall(b'STARTTLS\r\n');assert response().splitlines()[-1].startswith(b'OK')
ctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/'+host+'.cert');connection=ctx.wrap_socket(connection,server_hostname=host)
assert response().splitlines()[-1].startswith(b'OK')
auth=base64.b64encode(('\0'+f['address']+'\0'+f['password']).encode());connection.sendall(b'AUTHENTICATE "PLAIN" "'+auth+b'"\r\n');assert response().splitlines()[-1].startswith(b'OK')
script=b'require "vnd.dovecot.execute"; if execute "outbound-check" { keep; }'
connection.sendall(b'CHECKSCRIPT {'+str(len(script)).encode()+b'+}\r\n'+script+b'\r\n');reply=response();assert reply.startswith(b'NO') and b'unknown' in reply.lower(),reply
connection.sendall(b'LOGOUT\r\n');connection.close()
# A synthetic imported personal script must also be stopped before it can redirect.
def sql(statement):return subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-At','-v','ON_ERROR_STOP=1','-c',statement],text=True).strip()
owner=json.loads(Path('/root/mailfix-mailbox.json').read_text());period=json.loads(sql("SELECT row_to_json(s) FROM subscriptions s WHERE tenant_id=1"));active=Path('/var/mail/mail/sieve/mailfix24.test/sieve-peer.sieve');assert not active.exists()
subject='Legacy gate isolation '+secrets.token_hex(6)
try:
 active.write_text('require "copy"; redirect :copy "'+owner['address']+'"; keep;\n');active.chmod(0o644)
 sql("UPDATE subscriptions SET status='active',period_start=now()-interval '30 days',period_end=now()-interval '8 days',trial_ends_at=NULL WHERE tenant_id=1")
 assert sql('SET ROLE urmail_platform; SELECT urmail_mail_allowed(1)').splitlines()[-1]=='f'
 message=email.message.EmailMessage();message['From']=owner['address'];message['To']=f['address'];message['Subject']=subject;message.set_content('Imported personal script must not redirect after entitlement expiry')
 with smtplib.SMTP('127.0.0.1',25,timeout=15) as smtp:smtp.sendmail(owner['address'],[f['address']],message.as_bytes())
 def count(box):
  with imaplib.IMAP4_SSL(host,993,ssl_context=ctx) as imap:
   imap.login(box['address'],box['password']);imap.select('INBOX');status,ids=imap.uid('search',None,'HEADER','Subject','"'+subject+'"');assert status=='OK';return len(ids[0].split())
 for _ in range(80):
  if count(f)==1:break
  time.sleep(.2)
 time.sleep(1);assert count(f)==1 and count(owner)==0
finally:
 active.unlink(missing_ok=True);active.with_suffix('.svbin').unlink(missing_ok=True)
 quoted=lambda value:"NULL" if value is None else "'"+str(value).replace("'","''")+"'"
 sql('UPDATE subscriptions SET '+','.join(k+'='+quoted(period[k]) for k in ['status','period_start','period_end','trial_ends_at'])+' WHERE tenant_id=1')
print(json.dumps({'result':'PASS','personal_execute_rejected':True,'legacy_inbox':1,'legacy_redirect':0}))
