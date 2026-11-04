#!/usr/bin/env python3
"""Exact exclusive real-mail clone only; repair fence or verify post-boot, never release."""
import pathlib,subprocess,json,hashlib,socket,sys,os
assert '01M6H575J873963SQBA6ZKG7MM' in pathlib.Path('/proc/self/mountinfo').read_text()
r=pathlib.Path('/root/f09-acceptance');cli=r/'guard-repair/urmailctl';phase=sys.argv[1]
def run(args,expected=0):
 p=subprocess.run(args,text=True,capture_output=True);print(json.dumps({'args':args,'rc':p.returncode,'stdout':p.stdout,'stderr':p.stderr}),flush=True);assert p.returncode==expected;return p.stdout
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
protected=[pathlib.Path('/var/lib/urmail-backup/upgrade.hold'),pathlib.Path('/var/lib/urmail-backup/upgrade_receipt.json')]
if phase=='repair':
 before={str(p):digest(p) for p in protected};(r/'guard-repair/binding-before.json').write_text(json.dumps(before))
 print('BEFORE',subprocess.run(['ss','-lntp'],capture_output=True,text=True).stdout,flush=True)
 run([str(cli),'upgrade','fence','--reason','repair actual socket and instance guard'])
 assert before=={str(p):digest(p) for p in protected}
else:
 before=json.loads((r/'guard-repair/binding-before.json').read_text());assert before=={str(p):digest(p) for p in protected}
guards=list(pathlib.Path('/etc/systemd/system').glob('*.d/99-urmail-upgrade-fence.conf'));assert len(guards)==22,len(guards)
for p in guards:
 if p.parent.name.endswith(('.socket.d','.timer.d')):assert '[Service]' not in p.read_text(),str(p)
for unit in ('postfix.service','postfix@-.service','postfix@f09probe.service','dovecot.socket','dovecot.service','urmaild.service','urmaild-agent.service'):
 p=subprocess.run(['systemctl','start',unit],capture_output=True,text=True)
 state=run(['systemctl','show',unit,'-p','ActiveState','-p','ConditionResult'])
 assert 'ActiveState=inactive' in state and 'ConditionResult=no' in state,(unit,state)
for host,family in (('127.0.0.1',socket.AF_INET),('::1',socket.AF_INET6)):
 for port in (25,465,587,143,993,995,8080):
  s=socket.socket(family,socket.SOCK_STREAM);s.settimeout(2);code=s.connect_ex((host,port));s.close();print('PORT',host,port,code,flush=True);assert code!=0,(host,port)
assert before=={str(p):digest(p) for p in protected}
cp=max(pathlib.Path('/root/urmail-upgrade-backups').glob('config.*/manifest.json'),key=lambda x:x.stat().st_mtime).parent
run(['python3','/usr/share/urmail/upgrade-checkpoint.py','verify',str(cp)])
allprotected=protected+guards;hashes={str(p):digest(p) for p in allprotected}
p=subprocess.run([str(cli),'upgrade','release'],capture_output=True,text=True);print('DEFAULT_RELEASE',p.returncode,p.stdout,p.stderr,flush=True);assert p.returncode!=0 and 'readiness receipt' in p.stderr
assert hashes=={str(p):digest(p) for p in allprotected}
mailbefore=json.loads((r/'frozen-before-hashes.json').read_text());assert all(digest(pathlib.Path(p))==h for p,h in mailbefore.items())
print('PASS',phase,'22 guards; actual/template/socket manual starts skipped; IPv4+IPv6 ports refused; original binding/checkpoint/mail/key retained; release refused',flush=True)
print('PID1_START',pathlib.Path('/proc/1/stat').read_text().split()[21],flush=True)
