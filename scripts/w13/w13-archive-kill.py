import json,pathlib,subprocess,os,time,signal,hashlib,secrets,pwd,socket,threading
f=json.load(open('/root/w13-private-fixture.json'));root=pathlib.Path('/var/lib/urmail/archive-spool');mail=pwd.getpwnam('mailinabox');marker='w13-kill-'+secrets.token_hex(8);mid='<'+marker+'@local.test>';raw=('From: '+f['address']+'\r\nTo: '+f['address']+'\r\nMessage-ID: '+mid+'\r\nSubject: crash durable archive\r\n\r\nDurable bytes survive the killed archive pipe.\r\n').encode();digest=hashlib.sha256(raw).hexdigest()
def sql(statement):return subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-Atqc',statement],text=True).strip()
mainpid=int(subprocess.check_output(['systemctl','show','urmaild','--property=MainPID','--value'],text=True));server=socket.socket();server.bind(('127.0.0.1',55499));server.listen();connections=[]
def stall():
 try:
  while True:
   conn,_=server.accept();connections.append(conn)
 except OSError:pass
threading.Thread(target=stall,daemon=True).start()
env=pathlib.Path('/etc/urmail/w13-archive-stall.env');source=pathlib.Path('/etc/urmail/urmaild.env').read_text();env.write_text('\n'.join('URMAIL_DATABASE_URL_PLATFORM=postgres://urmail_platform:fixture@127.0.0.1:55499/urmail?sslmode=disable' if line.startswith('URMAIL_DATABASE_URL_PLATFORM=')else line for line in source.splitlines())+'\n');os.chown(env,0,mail.pw_gid);env.chmod(0o640)
recipient='archive+'+('v2:%d:%d:%s'%(f['tenant_id'],f['mailbox_id'],f['address'])).encode().hex()+'@archive.invalid'
def drop():os.setgroups([mail.pw_gid]);os.setgid(mail.pw_gid);os.setuid(mail.pw_uid)
process=None;os.kill(mainpid,signal.SIGSTOP)
try:
 before=set(root.iterdir());process=subprocess.Popen(['/usr/lib/urmail/archive-pipe','--env-file',str(env),'--recipient',recipient],stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,preexec_fn=drop,start_new_session=True);process.stdin.write(raw);process.stdin.close()
 manifest=None
 for _ in range(200):
  for path in set(root.iterdir())-before:
   candidate=path/'manifest.json'
   if candidate.exists():manifest=json.loads(candidate.read_text());spool=path;break
  if manifest:break
  time.sleep(.02)
 assert manifest,'durable manifest was not written'
 assert hashlib.sha256((spool/'raw.eml').read_bytes()).hexdigest()==digest==manifest['sha256']
 for _ in range(200):
  if connections:break
  time.sleep(.01)
 assert connections,'pipe did not reach the blocked database handshake after durable fsync'
 assert process.poll() is None,'process exited before the kill window'
 os.kill(process.pid,signal.SIGKILL);process.wait(timeout=5);assert process.returncode==-signal.SIGKILL
 assert sql("SELECT count(*) FROM archive_entries WHERE message_id='%s'"%mid)=='0'
 print('PASS SIGKILL after durable raw+manifest, before database index: no acknowledgement, source SHA256',digest)
finally:
 if process and process.poll() is None:process.kill();process.wait()
 os.kill(mainpid,signal.SIGCONT);server.close()
 for conn in connections:conn.close()
 env.unlink(missing_ok=True)
for _ in range(200):
 row=sql("SELECT path||'|'||raw_sha256 FROM archive_entries WHERE message_id='%s'"%mid)
 if row:break
 time.sleep(.1)
assert row,'resumed current worker did not recover durable input';path,stored=row.split('|');assert stored==digest and pathlib.Path(path).read_bytes()==raw
print('PASS existing worker recovered same tenant/mailbox, exact original bytes and hash after process death')
retry=subprocess.run(['/usr/lib/urmail/archive-pipe','--env-file','/etc/urmail/urmaild.env','--recipient',recipient],input=raw,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,preexec_fn=drop);assert retry.returncode==0,retry.stderr.decode()
assert sql("SELECT count(*) FROM archive_entries WHERE message_id='%s'"%mid)=='1'
print('PASS repeated original input was indexed exactly once; no message/attachment content logged')
