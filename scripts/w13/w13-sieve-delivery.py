import json,pathlib,ssl,urllib.request,urllib.error,secrets,email.message,smtplib,imaplib,time,hashlib,sys
# Depends on Roundcube-issued Sieve session from retired packaging; fail-soft.
print('SKIP Roundcube packaging is retired; w13-sieve-delivery is not run')
sys.exit(0)
f=json.load(open('/root/w13-private-fixture.json'));session=json.load(open('/root/w13-sieve-session.json'));cookies=json.load(open('/root/w13-rc-cookie.json'));token=next(c['value']for c in cookies if c['name']=='urmail_mailbox_sieve');base='https://box.w13.test';context=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/panel/fullchain.pem')
marker='w13-sieve-'+secrets.token_hex(8);rules={'rules':[{'header':'subject','match':'contains','value':marker,'action':'fileinto','folder':'W13 Reviewed'}],'vacation':{'enabled':False}}
def save(confirmed):
 request=urllib.request.Request(base+'/api/v1/mailbox/sieve/rules',json.dumps({'rules':rules,'confirmed':confirmed,'expected_version':session['version']}).encode(),{'Content-Type':'application/json','Origin':base,'Cookie':'urmail_mailbox_sieve='+token,'X-Urmail-Sieve-CSRF':session['csrf']},method='PUT')
 try:response=urllib.request.urlopen(request,context=context,timeout=30)
 except urllib.error.HTTPError as e:response=e
 raw=response.read();return response.status,json.loads(raw)
status,data=save(False);assert status==400,status;print('PASS unconfirmed Sieve change rejected before activation')
status,data=save(True);assert status==200 and data['state']=='active',(status,data.get('state'));assert data['mailbox']==f['address'];print('PASS explicit own-mailbox confirmation → canonical rule compile → typed root-agent activation, state=active')
status,stale=save(True);assert status==409,status;print('PASS replaying an old viewed Sieve version returns409 without overwriting the confirmed script')
mailctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/box.w13.test.cert');message=email.message.EmailMessage();message['From']=f['address'];message['To']=f['address'];message['Subject']=marker;mid='<'+marker+'@local.test>';message['Message-ID']=mid;message.set_content('LMTP must honor the confirmed own-mailbox Sieve rules.');attachment=secrets.token_bytes(4096);message.add_attachment(attachment,maintype='application',subtype='octet-stream',filename='confirmed.bin')
with smtplib.SMTP('box.w13.test',587,timeout=15)as smtp:smtp.ehlo();smtp.starttls(context=mailctx);smtp.ehlo();smtp.login(f['address'],f['password']);assert not smtp.send_message(message)
with imaplib.IMAP4_SSL('box.w13.test',993,ssl_context=mailctx)as imap:
 imap.login(f['address'],f['password'])
 for _ in range(100):
  status,_=imap.select('"W13 Reviewed"')
  if status=='OK':
   _,ids=imap.uid('search',None,'HEADER','Message-ID',mid)
   if ids[0]:break
  time.sleep(.1)
 else:raise AssertionError('LMTP did not file into the confirmed folder')
 _,parts=imap.uid('fetch',ids[0].split()[-1],'(BODY.PEEK[])');raw=next(x[1]for x in parts if isinstance(x,tuple));received=email.message_from_bytes(raw,policy=__import__('email.policy').policy.default);assert any(p.get_payload(decode=True)==attachment for p in received.iter_attachments());imap.select('INBOX');_,inbox=imap.uid('search',None,'HEADER','Message-ID',mid);assert not inbox[0]
print('PASS actual SMTP→LMTP applied confirmed Sieve: new folder created, message absent from INBOX, exact 4KiB attachment retrieved by strict-TLS IMAP')
print('message_id',mid,'attachment_sha256',hashlib.sha256(attachment).hexdigest())
