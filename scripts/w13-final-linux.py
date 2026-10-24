#!/usr/bin/env python3
"""Bind final local Linux acceptance to an explicit committed source and DEB."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


def run(arguments, **kwargs):
    return subprocess.run(arguments, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', required=True, choices=['urmail-w13-2204-final', 'urmail-w13-2404'])
    parser.add_argument('--mode', required=True, choices=['fresh', 'upgrade'])
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--package', required=True, type=Path)
    parser.add_argument('--package-sha256', required=True)
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--execute', action='store_true', help='Perform installation and the local protocol chain; otherwise only validate inputs.')
    args = parser.parse_args()
    if not re.fullmatch(r'[a-f0-9]{40}', args.source_commit) or not re.fullmatch(r'[a-f0-9]{64}', args.package_sha256):
        parser.error('Full source commit and package SHA256 are required')
    repository = Path(__file__).resolve().parents[1]
    commit = subprocess.check_output(['git', 'rev-parse', args.source_commit + '^{commit}'], cwd=repository, text=True).strip()
    if commit != args.source_commit:
        parser.error('Source commit mismatch')
    package = args.package.resolve(strict=True)
    if hashlib.sha256(package.read_bytes()).hexdigest() != args.package_sha256:
        parser.error('Package digest mismatch')
    if args.mode == 'fresh' and args.container != 'urmail-w13-2204-final':
        parser.error('Fresh acceptance is restricted to the isolated Ubuntu22 final container')
    inspect = json.loads(subprocess.check_output(['docker', 'inspect', args.container], text=True))[0]
    if not inspect['State']['Running'] or inspect['HostConfig'].get('PortBindings'):
        parser.error('Container must already run without published host ports')
    print('Validated explicit committed source, package digest and isolated target. Installation requires --execute.')
    if not args.execute:
        return
    args.evidence.mkdir(parents=True, exist_ok=False)
    evidence = args.evidence.resolve()
    suite = repository / 'scripts/w13'
    scripts = [p for p in suite.glob('w13-*.py') if p.name != 'w13-roundcube-provision.py']
    identity = {'source_commit': commit, 'package_sha256': args.package_sha256, 'package': package.name, 'mode': args.mode, 'container': args.container, 'image_id': inspect['Image'], 'harness_sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in scripts}}
    identity['docker_engine_architecture'] = subprocess.check_output(['docker', 'info', '--format', '{{.Architecture}}'], text=True).strip()
    identity['image_architecture'] = subprocess.check_output(['docker', 'image', 'inspect', inspect['Image'], '--format', '{{.Architecture}}'], text=True).strip()
    (evidence / 'identity.json').write_text(json.dumps(identity, indent=2) + '\n')
    with tempfile.TemporaryDirectory(prefix='urmail-final-') as temporary:
        temporary = Path(temporary)
        archive = temporary / 'source.tar'
        with archive.open('wb') as output:
            run(['git', 'archive', '--format=tar', commit], cwd=repository, stdout=output)
        identity['committed_source_tar_sha256'] = hashlib.sha256(archive.read_bytes()).hexdigest()
        (evidence / 'identity.json').write_text(json.dumps(identity, indent=2) + '\n')
        # Never replace the old /workspace snapshot or /evidence mount.
        run(['docker', 'exec', args.container, 'install', '-d', '-m', '0700', '/opt/urmail-final-source', '/opt/urmail-final-tests'])
        run(['docker', 'cp', str(archive), args.container + ':/root/urmail-final-source.tar'])
        run(['docker', 'exec', args.container, 'tar', '-xf', '/root/urmail-final-source.tar', '-C', '/opt/urmail-final-source'])
        run(['docker', 'cp', str(package), args.container + ':/root/urmail-final.deb'])
        for script in scripts:
            run(['docker', 'cp', str(script), args.container + ':/opt/urmail-final-tests/' + script.name])
    def guest(name, command):
        with (evidence / (name + '.log')).open('w') as output:
            run(['docker', 'exec', args.container, *command], stdout=output, stderr=subprocess.STDOUT)
    # The guest bootstrap never receives a password in its argv. An upgrade uses
    # the retained private initial credential, avoiding a random unusable reset.
    bootstrap = r'''
import json,os,pathlib,subprocess,time
mode=os.environ['W13_MODE'];package=pathlib.Path('/root/urmail-final.deb')
assert __import__('hashlib').sha256(package.read_bytes()).hexdigest()==os.environ['W13_PACKAGE_SHA256']
if mode=='fresh':assert not pathlib.Path('/etc/urmail/urmaild.env').exists(),'fresh target already contains an application installation'
env=dict(os.environ,URMAIL_PACKAGE=str(package),URMAIL_MYHOSTNAME='box.w13.test',URMAIL_PRIMARY_DOMAIN='mailbox.w13.test',URMAIL_NAMESERVERS='ns1.box.w13.test,ns2.box.w13.test',URMAIL_DNS_LISTEN_ADDRESSES='127.0.0.1',URMAIL_PUBLIC_IPV4=subprocess.check_output(['hostname','-I'],text=True).split()[0])
credentials=pathlib.Path('/root/urmail-initial-admin.txt')
if credentials.exists():
 values=dict(line.split('=',1)for line in credentials.read_text().splitlines()if '='in line)
 env.update(URMAIL_ADMIN_EMAIL=values['email'],URMAIL_ADMIN_PASSWORD=values['password'])
subprocess.run(['bash','/opt/urmail-final-source/installer/install.sh'],env=env,check=True)
if mode=='fresh':
 result=subprocess.check_output(['runuser','-u','postgres','--','psql','-d','urmail','-Atqc',"SELECT t.status||'|'||s.status FROM tenants t JOIN subscriptions s ON s.tenant_id=t.id WHERE t.slug='default'"],text=True).strip()
 assert result.startswith('pending|') and not result.endswith('|active'),result
 print('PASS fresh setup defaults to pending without an active implicit free term; finite gift is applied only by the explicit operator fixture next.')
hosts=pathlib.Path('/etc/hosts');text=hosts.read_text()
if 'box.w13.test'not in text:hosts.write_text(text+'\n127.0.0.1 box.w13.test\n')
# Local CA fixture only; this is not public ACME acceptance.
subprocess.run(['install','-m','0644','/var/lib/urmail-agent/certs/panel/fullchain.pem','/usr/local/share/ca-certificates/urmail-w13-local.crt'],check=True)
subprocess.run(['install','-m','0644','/var/lib/urmail-agent/certs/mail/box.w13.test.cert','/usr/local/share/ca-certificates/urmail-w13-mail.crt'],check=True)
subprocess.run(['update-ca-certificates'],check=True)
envpath=pathlib.Path('/etc/urmail/urmaild.env');lines=[x for x in envpath.read_text().splitlines()if not x.startswith('URMAIL_AI_LOCAL_ENDPOINTS=')];envpath.write_text('\n'.join(lines)+'\nURMAIL_AI_LOCAL_ENDPOINTS=http://127.0.0.1:11434\n')
subprocess.run(['/usr/share/urmail/roundcube/manage.sh','configure'],check=True)
subprocess.run(['/usr/share/urmail/roundcube/manage.sh','install'],check=True)
subprocess.run(['systemctl','restart','urmaild'],check=True)
for i in range(60):
 try:
  __import__('urllib.request').request.urlopen('http://127.0.0.1:8080/api/v1/healthz',timeout=2);break
 except Exception:time.sleep(1)
else:raise RuntimeError('panel process did not start; mail readiness is checked separately')
'''
    guest('install', ['env', 'W13_MODE=' + args.mode, 'W13_PACKAGE_SHA256=' + args.package_sha256, 'python3', '-c', bootstrap])
    guest('runtime', ['bash', '-lc', 'uname -m; dpkg-query -W urmail; sha256sum /usr/bin/urmail* /usr/lib/urmail/archive-pipe; systemctl is-active urmaild urmaild-agent postfix dovecot nginx rsyslog'])
    # A fixed transient local-only model service avoids background shell/PID leaks.
    model_start = r'''
import os,pathlib,signal,subprocess,time,urllib.request
subprocess.run(['systemctl','stop','urmail-w13-local-model.service'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
for entry in pathlib.Path('/proc').iterdir():
 if not entry.name.isdigit():continue
 try:args=(entry/'cmdline').read_bytes().split(b'\0')
 except OSError:continue
 if len(args)>1 and args[1] in (b'/workspace/w13-model.py',b'/evidence/w13-model.py',b'/opt/urmail-final-tests/w13-model.py') and pathlib.Path(os.fsdecode(args[0])).name in ('python3','python'):
  os.kill(int(entry.name),signal.SIGTERM)
subprocess.run(['systemd-run','--unit=urmail-w13-local-model','--collect','/usr/bin/python3','/opt/urmail-final-tests/w13-model.py'],check=True)
for i in range(40):
 try:urllib.request.urlopen('http://127.0.0.1:11434/api/tags',timeout=1);break
 except Exception:time.sleep(.25)
else:raise RuntimeError('owned local-only model fixture did not start')
'''
    guest('local-model', ['python3', '-c', model_start])
    for name in ['fixture', 'protocol', 'enable-ai', 'roundcube-chain', 'sieve-delivery', 'archive-kill', 'health', 'mailbox-revocation']:
        guest(name, ['env', 'URMAIL_W13_SOURCE=/opt/urmail-final-source', 'python3', '/opt/urmail-final-tests/w13-' + name + '.py'])
    print('Local final Linux chain completed. Review evidence and genuine external gates separately.')


if __name__ == '__main__':
    main()
