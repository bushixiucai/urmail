#!/usr/bin/env python3
"""Run unmodified installer on exact owned fixture, retain hold and private snapshots."""
import pathlib,subprocess,json,hashlib,os,socket
assert '01M6H575J873963SQBA6ZKG7MM' in pathlib.Path('/proc/self/mountinfo').read_text()
r=pathlib.Path('/root/f09-acceptance');src=r/'frozen';cli=src/'tmp/remediation-mail-upgrade-20261104/frozen/bin/urmailctl'
def snapshot():
 paths=[p for p in pathlib.Path('/var/mail/mail/mailboxes').rglob('*') if p.is_file() and p.parent.name in ('cur','new')]
 paths += [pathlib.Path('/etc/urmail/master.key')]
 return {str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}
def business():
 return subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-Atqc','SELECT id,address,md5(password_hash),status FROM mailboxes ORDER BY id; SELECT id,tenant_id,status,period_start,period_end FROM subscriptions ORDER BY id;'])
before=snapshot();b=business();(r/'frozen-before-hashes.json').write_text(json.dumps(before,sort_keys=True))
for fam in ('-4','-6'):
 assert not subprocess.check_output(['ip',fam,'route','show','default']).strip()
env=os.environ.copy();env['URMAIL_PACKAGE']=str(src/'dist/urmail_0.5.0+f09.20261104.3_arm64.deb')
p=subprocess.Popen(['bash',str(src/'installer/install.sh')],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
competed=False
for line in p.stdout:
 print(line,end='',flush=True)
 if not competed and 'establishing upgrade fence and pausing mail writers' in line:
  q=subprocess.run([str(cli),'upgrade','fence','--timeout','100ms'],capture_output=True,text=True)
  print(json.dumps({'competing_cli_rc':q.returncode,'stdout':q.stdout,'stderr':q.stderr}),flush=True)
  assert q.returncode!=0 and 'lock' in q.stderr
  competed=True
code=p.wait();print('INSTALLER_EXIT',code,flush=True)
assert competed and code==2
assert before==snapshot();assert b==business()
checkpoints=sorted(pathlib.Path('/root/urmail-upgrade-backups').glob('config.*/manifest.json'),key=lambda x:x.stat().st_mtime)
assert checkpoints
cp=checkpoints[-1].parent
q=subprocess.run(['python3','/usr/share/urmail/upgrade-checkpoint.py','verify',str(cp)],capture_output=True,text=True)
print(json.dumps({'checkpoint_verify_rc':q.returncode,'stdout':q.stdout,'stderr':q.stderr}),flush=True);assert q.returncode==0
for unit in ('postfix','dovecot','urmaild','urmaild-agent'):
 state=subprocess.check_output(['systemctl','show',unit,'-p','ActiveState'],text=True);assert 'ActiveState=inactive' in state
for port in (25,587,993,995,8080):
 s=socket.socket();s.settimeout(2);assert s.connect_ex(('127.0.0.1',port))!=0;s.close()
protected=[pathlib.Path('/var/lib/urmail-backup/upgrade.hold'),pathlib.Path('/var/lib/urmail-backup/upgrade_receipt.json')]+list(pathlib.Path('/etc/systemd/system').glob('*.d/99-urmail-upgrade-fence.conf'))
h={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in protected}
q=subprocess.run(['/usr/bin/urmailctl','upgrade','release'],capture_output=True,text=True);print(json.dumps({'default_release_rc':q.returncode,'stdout':q.stdout,'stderr':q.stderr}),flush=True);assert q.returncode!=0
assert h=={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in protected}
print(subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-Atqc','SELECT version,dirty FROM schema_migrations'],text=True))
print('PASS real installer prepared-held exit2; concurrent CLI excluded; checkpoint verified; mail/key/business retained; default release refused',flush=True)
