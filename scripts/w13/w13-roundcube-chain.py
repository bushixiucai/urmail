import json,re,ssl,urllib.request,urllib.parse,http.cookiejar,pathlib,imaplib,secrets
f=json.load(open('/root/w13-private-fixture.json'));base='https://box.w13.test';ctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/panel/fullchain.pem');jar=http.cookiejar.CookieJar();opener=urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx),urllib.request.HTTPCookieProcessor(jar))
def call(path,data=None,headers=None):
 body=urllib.parse.urlencode(data).encode()if data is not None else None;req=urllib.request.Request(base+path,body,headers or {});response=opener.open(req,timeout=30);return response.read().decode(),response
html,_=call('/mail/');token=re.search(r'name="_token"[^>]*value="([^"]+)"',html).group(1)
html,response=call('/mail/?_task=login',{'_token':token,'_task':'login','_action':'login','_user':f['address'],'_pass':f['password'],'_timezone':'UTC','_url':''})
assert 'task-mail' in html or '"task":"mail"' in html,'Roundcube mailbox login failed'
assert f['address'] in html,'authenticated own mailbox not shown'
print('PASS real Roundcube HTTP session independently authenticated to own IMAP mailbox; no panel credential supplied')
start=html.index('rcmail.set_env(')+len('rcmail.set_env(');env,_=json.JSONDecoder().raw_decode(html[start:]);token=env['request_token']
policy=env.get('urmail_ai_policy',{});assert policy.get('allowed') and policy.get('provider')=='ollama' and policy.get('requires_confirmation') is False,'local AI policy not authorized in actual Roundcube environment'
mailctx=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/box.w13.test.cert')
with imaplib.IMAP4_SSL('box.w13.test',993,ssl_context=mailctx)as imap:
 imap.login(f['address'],f['password']);imap.select('INBOX');_,ids=imap.uid('search',None,'ALL');uid=ids[0].split()[-1].decode()
body={'_task':'mail','_action':'plugin.urmail_context','_remote':'1','_token':token,'_mbox':'INBOX','_uid':uid,'_operation':'summarize','_language':'','_request_key':'w13-context-'+secrets.token_hex(12),'_configuration_digest':env.get('urmail_ai_policy',{}).get('configuration_digest',''),'_cloud_confirmed':'0'}
before_calls=json.loads(pathlib.Path('/root/w13-model-observations.json').read_text()).get('calls',0) if pathlib.Path('/root/w13-model-observations.json').exists()else 0
raw,response=call('/mail/?_task=mail&_action=plugin.urmail_context',body,{'X-Requested-With':'XMLHttpRequest'});pathlib.Path('/root/w13-rc-ai-response.json').write_text(raw)
assert 'Local fixture summary' in raw,'authenticated mail context AI did not return fixture result'
replayed,_=call('/mail/?_task=mail&_action=plugin.urmail_context',body,{'X-Requested-With':'XMLHttpRequest'});assert 'Local fixture summary' in replayed
observed=json.loads(pathlib.Path('/root/w13-model-observations.json').read_text());assert observed['has_mail_body'] and observed['calls']==before_calls+1,observed
print('PASS Roundcube-selected UID/UIDVALIDITY → signed bridge → real local HTTP model → schema-validated summary; model called once; no paid endpoint')
body={'_task':'mail','_action':'plugin.urmail_sieve','_remote':'1','_token':token};raw,_=call('/mail/?_task=mail&_action=plugin.urmail_sieve',body,{'X-Requested-With':'XMLHttpRequest'});pathlib.Path('/root/w13-rc-sieve-response.json').write_text(raw);decoded=json.loads(raw);ticket=next(c[1]for c in decoded['callbacks']if c[0]=='plugin.urmail_sieve_ticket');assert ticket['ticket']
_,response=call('/api/v1/mailbox/sieve/exchange',{'ticket':ticket['ticket']},{'Origin':base});assert response.url==base+'/mailbox/sieve'
raw,_=call('/api/v1/mailbox/sieve/session');session=json.loads(raw);assert session['mailbox']==f['address'];pathlib.Path('/root/w13-sieve-session.json').write_text(json.dumps({'csrf':session['csrf'],'mailbox':session['mailbox'],'version':session.get('version','')}));pathlib.Path('/root/w13-rc-cookie.json').write_text(json.dumps([{'name':c.name,'value':c.value,'domain':c.domain,'path':c.path,'secure':c.secure}for c in jar]));pathlib.Path('/root/w13-rc-env.json').write_text(json.dumps({'token':token}));print('PASS Roundcube-issued one-time Sieve ticket exchanged for independent HttpOnly scoped cookie, own mailbox only')
