#!/usr/bin/env python3
"""F09 real-systemd persistence only: disposable isolated OrbStack machines, never mail acceptance.

Machines must already have been created by this run with --isolated --isolate-network.
No mounts, sudo, package install, database, break-glass release or deletion are used.
Run once per fresh machine; always stops (not deletes) the named owned machine.
Historical 19-unit fixture only: it does NOT model distro Postfix instances or
Dovecot socket activation. The later real-mail rehearsal found that gap; use
its dedicated repaired real activation-graph rehearsal for current acceptance.
"""
import argparse
import json
from pathlib import Path
import subprocess

ALLOWED = {f"urmail-remediation-fence-{v}-20261104" for v in (22, 24)}
GUEST = r'''import hashlib, json, os, pathlib, subprocess, sys
P = pathlib.Path
ROOT = P('/var/lib/urmail-backup')
STATE = P('/var/lib/f09-acceptance')
services = ['urmail-backup-auto.service', 'urmail-cert-renew.service', 'urmail-usage-sync.service', 'urmail-prometheus.service', 'urmail-alertmanager.service', 'nginx.service'] + ['php8.%s-fpm.service' % v for v in range(1, 7)] + ['postfix.service', 'dovecot.service', 'urmaild.service', 'urmaild-agent.service']
timers = ['urmail-backup-auto.timer', 'urmail-cert-renew.timer', 'urmail-usage-sync.timer']
units = services + timers

def run(*args, ok=True):
    r = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    print(json.dumps({'argv':args, 'rc':r.returncode, 'output':r.stdout}), flush=True)
    if ok: assert r.returncode == 0, args
    return r

def prop(unit, key):
    return run('systemctl', 'show', unit, '--property='+key, '--value').stdout.strip()

def snapshot():
    paths = [ROOT/'upgrade.hold', ROOT/'upgrade_receipt.json'] + [P('/etc/systemd/system')/(u+'.d')/'99-urmail-upgrade-fence.conf' for u in units]
    return {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}

def verify():
    hold = json.loads((ROOT/'upgrade.hold').read_text())
    receipt = json.loads((ROOT/'upgrade_receipt.json').read_text())
    assert set(hold['original_active_units']) == set(units)
    assert set(receipt['original_active_units']) == set(units)
    assert receipt['upgrade_held'] is True
    assert set(hold['guarded_units']) == set(units)
    assert (ROOT/'upgrade.hold').stat().st_ino != (ROOT/'upgrade_receipt.json').stat().st_ino
    for u in units:
        assert prop(u, 'ActiveState') == 'inactive', u
        assert run('systemctl', 'is-enabled', u).stdout.strip() == 'enabled'
        guard = P('/etc/systemd/system')/(u+'.d')/'99-urmail-upgrade-fence.conf'
        assert 'ConditionPathExists=!/var/lib/urmail-backup/upgrade.hold' in guard.read_text()
    run('/usr/local/bin/urmailctl', 'upgrade', 'status', '--json')

assert os.geteuid() == 0
assert P('/proc/1/comm').read_text().strip() == 'systemd'
assert not P('/mnt/mac').exists(), 'unexpected host integration'
run('systemctl', '--version')
run('findmnt', '-rn', '-o', 'TARGET,SOURCE,FSTYPE')
if sys.argv[1] == 'before':
    assert not STATE.exists(), 'one-shot fresh machine test only'
    assert not ROOT.exists(), 'unexpected maintenance state'
    STATE.mkdir(parents=True)
    for u in units:
        dest = P('/etc/systemd/system')/u
        assert not dest.exists(), 'will not overwrite installed unit '+u
        if u.endswith('.service'):
            text = '[Unit]\nDescription=F09 DUMMY sleep, NOT real mail service\n[Service]\nType=simple\nExecStart=/usr/bin/sleep infinity\nExecStartPost=/usr/bin/touch /run/f09-start-'+u+'\nUser=nobody\nGroup=nogroup\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nReadWritePaths=/run\nCapabilityBoundingSet=\n[Install]\nWantedBy=multi-user.target\n'
            # Root-owned /run is not writable to nobody: allow only a dedicated owned marker directory.
            text = text.replace('/run/f09-start-', '/run/f09-acceptance/start-').replace('ReadWritePaths=/run', 'ReadWritePaths=/run/f09-acceptance')
        else:
            text = '[Unit]\nDescription=F09 DUMMY timer\n[Timer]\nOnBootSec=30s\nOnUnitActiveSec=60s\nUnit='+u.replace('.timer','.service')+'\n[Install]\nWantedBy=timers.target\n'
        dest.write_text(text)
    # tmpfiles recreates marker directory each boot, with no marker files.
    P('/etc/tmpfiles.d/f09-acceptance.conf').write_text('d /run/f09-acceptance 0755 nobody nogroup -\n')
    run('systemd-tmpfiles', '--create', '/etc/tmpfiles.d/f09-acceptance.conf')
    run('systemctl', 'daemon-reload')
    run('systemctl', 'enable', '--now', *units)
    for u in units: assert prop(u, 'ActiveState') == 'active', u
    for u in services: assert P('/run/f09-acceptance/start-'+u).exists(), u
    (STATE/'pid1-before').write_text(P('/proc/1/stat').read_text().split()[21])
    run('/usr/local/bin/urmailctl', 'upgrade', 'fence', '--reason', 'F09 real-systemd dummy persistence acceptance')
    verify()
    hashes = snapshot()
    r = run('/usr/local/bin/urmailctl', 'upgrade', 'release', ok=False)
    assert r.returncode != 0 and 'no bound protocol readiness receipt' in r.stdout
    assert snapshot() == hashes, 'default release mutated fence'
    (STATE/'hashes.json').write_text(json.dumps(hashes))
    print('F09 BEFORE REBOOT PASS', flush=True)
else:
    assert P('/proc/1/stat').read_text().split()[21] != (STATE/'pid1-before').read_text(), 'PID1 was not restarted'
    assert snapshot() == json.loads((STATE/'hashes.json').read_text()), 'fence changed across reboot'
    verify()
    for u in units:
        r = run('systemctl', 'start', u, ok=False)
        # ConditionPathExists skip returns 0 on systemd: safety assertion is no activation, not exit status.
        assert prop(u, 'ActiveState') == 'inactive', u
        assert prop(u, 'ConditionResult') == 'no', u
        if u in services:
            assert prop(u, 'MainPID') == '0', u
            assert not P('/run/f09-acceptance/start-'+u).exists(), u
    assert snapshot() == json.loads((STATE/'hashes.json').read_text())
    run('journalctl', '-b', '--no-pager', '-u', 'postfix.service', '-u', 'urmaild.service', '-u', 'urmail-backup-auto.timer')
    print('F09 AFTER REBOOT PASS: enabled units remain inactive; manual start condition-skipped; hold/receipt/dropins unchanged', flush=True)
'''

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--machine', required=True, choices=sorted(ALLOWED))
    p.add_argument('--binary', required=True, type=Path)
    p.add_argument('--evidence', required=True, type=Path)
    a = p.parse_args()
    a.evidence.mkdir(parents=True, exist_ok=True)
    log = (a.evidence/(a.machine+'.log')).open('x')
    def cmd(*args, check=True):
        r = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        line = json.dumps({'argv':args, 'rc':r.returncode, 'output':r.stdout})
        print(line, flush=True); print(line, file=log, flush=True)
        if check and r.returncode: raise RuntimeError(args)
        return r
    try:
        for key in ('isolated', 'isolate_network'):
            r = cmd('orbctl', 'config', 'get', 'machine.'+a.machine+'.'+key)
            assert r.stdout.strip() == 'true', 'machine is not isolated'
        mounts = cmd('orbctl', 'config', 'get', 'machine.'+a.machine+'.mounts')
        assert not mounts.stdout.strip(), 'unexpected selective host mounts'
        # orb push is deliberately used, never a host bind mount.
        cmd('orbctl', 'push', '-m', a.machine, str(a.binary.resolve()), 'urmailctl-f09')
        cmd('orbctl', 'run', '-m', a.machine, '-u', 'root', 'install', '-m', '0755', '/home/'+cmd('orbctl','run','-m',a.machine,'whoami').stdout.strip()+'/urmailctl-f09', '/usr/local/bin/urmailctl')
        cmd('orbctl', 'run', '-m', a.machine, '-u', 'root', 'sha256sum', '/usr/local/bin/urmailctl')
        cmd('orbctl', 'run', '-m', a.machine, '-u', 'root', 'python3', '-c', GUEST, 'before')
        cmd('orbctl', 'restart', a.machine)
        boot = cmd('orbctl', 'run', '-m', a.machine, '-u', 'root', 'systemctl', 'is-system-running', '--wait', check=False)
        assert boot.stdout.strip() in ('running', 'degraded'), 'boot did not finish'
        if boot.returncode:
            cmd('orbctl', 'run', '-m', a.machine, '-u', 'root', 'systemctl', '--failed', '--no-pager')
        cmd('orbctl', 'run', '-m', a.machine, '-u', 'root', 'python3', '-c', GUEST, 'after')
        print('PASS '+a.machine, flush=True)
    finally:
        cmd('orbctl', 'stop', a.machine, check=False)
        log.close()

if __name__ == '__main__':
    main()
