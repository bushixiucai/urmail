#!/usr/bin/env python3
"""Guest-only real mail fence acceptance; no release, install, send, or credentials output."""
import hashlib,json,pathlib,socket,subprocess,os
assert '01M6H575J873963SQBA6ZKG7MM' in pathlib.Path('/proc/self/mountinfo').read_text(), 'exclusive clone only'
ROOT=pathlib.Path('/root/f09-acceptance')
assert ROOT.is_dir() and pathlib.Path('/root/mailfix-mailbox.json').is_file()
CLI=ROOT/'source/tmp/remediation-mail-upgrade-20261104/bin/urmailctl'
def run(args, private=False):
 p=subprocess.run(args,text=True,capture_output=True)
 print(json.dumps({'args':args,'rc':p.returncode,'stdout':'[private business snapshot]' if private else p.stdout,'stderr':p.stderr}),flush=True)
 assert p.returncode==0
 return p.stdout
files=[p for p in pathlib.Path('/var/mail/mail/mailboxes').rglob('*') if p.is_file() and p.parent.name in ('cur','new')]
hashes={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
(ROOT/'mail-hashes-before.json').write_text(json.dumps(hashes,sort_keys=True))
business=run(['runuser','-u','postgres','--','psql','-d','urmail','-Atqc',"SELECT id,address,md5(password_hash),status FROM mailboxes ORDER BY id; SELECT id,tenant_id,status,period_start,period_end FROM subscriptions ORDER BY id;"],private=True)
(ROOT/'business-before.txt').write_text(business)
run([str(CLI),'upgrade','fence','--reason','exclusive real mail upgrade acceptance'])
hold=pathlib.Path('/var/lib/urmail-backup/upgrade.hold');receipt=hold.with_name('upgrade_receipt.json')
assert hold.is_file() and receipt.is_file()
meta=json.loads(hold.read_text());print(json.dumps({'hold':meta,'receipt':json.loads(receipt.read_text())}),flush=True)
for unit in meta['original_active_units']:
 p=subprocess.run(['systemctl','is-active',unit],capture_output=True,text=True)
 assert p.stdout.strip() not in ('active','activating','reloading'),(unit,p.stdout)
for port in (25,587,993,995,8080):
 s=socket.socket();s.settimeout(2)
 assert s.connect_ex(('127.0.0.1',port))!=0,port
 s.close()
print('PASS real service fence: original writers stopped, local mail/API traffic blocked',flush=True)
run(['runuser','-u','postgres','--','psql','-d','postgres','-c','CHECKPOINT'])
with open(ROOT/'database-before.dump','xb') as f:
 subprocess.run(['runuser','-u','postgres','--','pg_dump','--format=custom','--no-owner','urmail'],stdout=f,check=True)
 f.flush();os.fsync(f.fileno())
run(['pg_restore','--list',str(ROOT/'database-before.dump')])
guards=list(pathlib.Path('/etc/systemd/system').glob('*.d/99-urmail-upgrade-fence.conf'))
assert len(guards)==19,len(guards)
protected=[hold,receipt]+guards
before={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in protected}
p=subprocess.run([str(CLI),'upgrade','release'],capture_output=True,text=True)
print(json.dumps({'release_rc':p.returncode,'stdout':p.stdout,'stderr':p.stderr}),flush=True)
assert p.returncode!=0 and 'readiness receipt' in p.stderr+p.stdout
assert before=={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in protected}
assert hashes=={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
(ROOT/'guard-hashes.json').write_text(json.dumps(before,sort_keys=True))
print('PASS checkpoint/custom PG dump; all RFC822 hashes retained; default release refused and all 21 protected hashes unchanged',flush=True)
