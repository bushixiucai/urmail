#!/usr/bin/env python3
"""Create and run one new, marker-bound Nextcloud rehearsal container; no host PHP."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys


class Refused(Exception):
    pass


def require(value, code):
    if not value:
        raise Refused(code)


def normalize_capabilities(values):
    # Docker engines expose canonical names with or without the CAP_ prefix.
    require(isinstance(values,list) and all(isinstance(value,str) and re.fullmatch(r'(?:CAP_)?[A-Z][A-Z0-9_]*',value) for value in values), 'CONTAINER_CAPABILITIES_INVALID')
    return {value.removeprefix('CAP_') for value in values}


def run(command, log, check=True):
    result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=7200)
    with log.open('ab') as stream:
        stream.write(result.stdout+b'\n'+result.stderr+b'\n')
    log.chmod(0o600)
    require(not check or result.returncode == 0, 'CONTAINER_ENGINE_STEP_FAILED')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Immutable built runtime image ID sha256:...')
    parser.add_argument('--php-provenance', required=True, choices=['original-v80-build-checkpoint-not-release-bundle','verified-v80-release-plus-same-source-pdo-pgsql'])
    parser.add_argument('--mirror', required=True)
    parser.add_argument('--manifest', required=True)
    parser.add_argument('--manifest-sha256', required=True)
    parser.add_argument('--work', required=True)
    parser.add_argument('--mode', required=True, choices=['inspect','preflight','convert'])
    parser.add_argument('--docker', default='/usr/bin/docker', choices=['/usr/bin/docker','/usr/local/bin/docker'])
    args = parser.parse_args()
    require(os.getuid() == 0, 'PRIVATE_STAGING_ROOT_REQUIRED')
    require(re.fullmatch(r'sha256:[a-f0-9]{64}', args.image), 'IMMUTABLE_RUNTIME_IMAGE_REQUIRED')
    mirror, manifest, work = Path(args.mirror), Path(args.manifest), Path(args.work)
    require(mirror.is_absolute() and mirror.is_dir() and not mirror.is_symlink(), 'PRIVATE_MIRROR_REQUIRED')
    require(mirror.stat().st_uid == 0 and mirror.stat().st_mode & 0o077 == 0, 'PRIVATE_MIRROR_PERMISSIONS_REQUIRED')
    require(manifest.is_file() and not manifest.is_symlink(), 'PRIVATE_MANIFEST_REQUIRED')
    require(manifest.parent == mirror.parent and (mirror.parent/'COMPLETE_REHEARSAL_ONLY').is_file() and not (mirror.parent/'COMPLETE_REHEARSAL_ONLY').is_symlink(), 'COMPLETE_REHEARSAL_MARKER_REQUIRED')
    require(re.fullmatch(r'[a-f0-9]{64}', args.manifest_sha256) and hashlib.sha256(manifest.read_bytes()).hexdigest() == args.manifest_sha256, 'MANIFEST_SHA256_MISMATCH')
    require(work.is_absolute() and not work.exists() and not work.is_symlink() and work.parent.is_dir(), 'NEW_PRIVATE_WORK_REQUIRED')
    require(not work.is_relative_to(mirror) and not mirror.is_relative_to(work), 'SEPARATE_WORK_REQUIRED')
    harness = Path(__file__).resolve().parents[2]/'packaging/nextcloud'
    for path in [mirror, manifest, work, harness]:
        require(',' not in str(path) and '\n' not in str(path) and '\r' not in str(path), 'MOUNT_PATH_INVALID')
    work.mkdir(mode=0o700)
    for name in ['code','data','postgres','reports']:
        (work/name).mkdir(mode=0o700)
    nonce = secrets.token_hex(32)
    marker = {'purpose':'urmail-nextcloud-v06-rehearsal-only','nonce':nonce,
              'host_namespaces':{name:os.readlink('/proc/self/ns/'+name) for name in ['mnt','net','pid']},
              'manifest_sha256':args.manifest_sha256,'runtime_image_id':args.image,'php_provenance':args.php_provenance,'rehearsal_only':True,'cross_app_consistent':False}
    marker_file = work/'marker.private.json'
    marker_file.write_text(json.dumps(marker))
    marker_file.chmod(0o600)
    log = work/'launcher.private.log'
    mounts = [(mirror,'/source',True),(harness,'/harness',True),(manifest,'/run/source-manifest.private',True),
              (marker_file,'/run/urmail-v06.marker',True),(work/'code','/usr/local/lib/owncloud',False),
              (work/'data','/home/user-data/owncloud',False),(work/'postgres','/var/lib/urmail-rehearsal-pg',False),
              (work/'reports','/work',False)]
    command = [args.docker,'create','--network','none','--read-only','--cap-drop','ALL',
               '--cap-add','CHOWN','--cap-add','SETUID','--cap-add','SETGID',
               '--security-opt','no-new-privileges','--pids-limit','256','--cpus','1','--memory','3g',
               '--tmpfs','/tmp:rw,nosuid,nodev,noexec,size=256m','--tmpfs','/run/urmail-pg:rw,nosuid,nodev,noexec,size=16m',
               '--user','0:0','--label','urmail.v06.nonce='+nonce,'--entrypoint','/usr/bin/python3']
    for source,target,readonly in mounts:
        command += ['--mount',f'type=bind,src={source},dst={target}'+(',readonly' if readonly else '')]
    command += [args.image,'/harness/container_entry.py','--mode',args.mode]
    container = None
    try:
        container = run(command,log).stdout.decode().strip()
        require(re.fullmatch(r'[a-f0-9]{64}', container), 'CONTAINER_ID_INVALID')
        inspected = json.loads(run([args.docker,'inspect',container],log).stdout)[0]
        config = inspected['HostConfig']
        require(config['NetworkMode']=='none' and config['ReadonlyRootfs'] and not config['Privileged'] and not config.get('PortBindings'), 'CONTAINER_SECURITY_CONFIG_INVALID')
        require(any(option in ['no-new-privileges','no-new-privileges=true'] for option in config.get('SecurityOpt',[])) and config.get('PidMode','') != 'host' and config.get('IpcMode','') != 'host', 'CONTAINER_NAMESPACE_SECURITY_INVALID')
        require(normalize_capabilities(config.get('CapDrop'))=={'ALL'} and normalize_capabilities(config.get('CapAdd'))=={'CHOWN','SETUID','SETGID'}, 'CONTAINER_CAPABILITIES_INVALID')
        require(config['Memory']==3*1024**3 and config['NanoCpus']==1000000000 and config['PidsLimit']==256, 'CONTAINER_RESOURCE_LIMITS_INVALID')
        actual = {(mount['Destination'],mount['Source'],not mount['RW']) for mount in inspected['Mounts'] if mount['Type']=='bind'}
        require(actual=={(target,str(source),readonly) for source,target,readonly in mounts}, 'CONTAINER_MOUNTS_INVALID')
        require(inspected['Config']['Labels'].get('urmail.v06.nonce')==nonce, 'CONTAINER_MARKER_LABEL_INVALID')
        result = run([args.docker,'start','--attach',container],log,check=False)
        state = json.loads(run([args.docker,'inspect',container],log).stdout)[0]['State']
        report_file = work/'reports/result.json'
        if result.returncode or state['ExitCode'] or not report_file.is_file():
            failure = work/'reports/failure.json'
            reason = json.loads(failure.read_text()).get('reason','CONTAINER_RUNTIME_FAILED') if failure.is_file() else 'CONTAINER_RUNTIME_FAILED'
            require(re.fullmatch(r'[A-Z0-9_]+',reason), 'CONTROLLED_FAILURE_REQUIRED')
            raise Refused(reason)
        report = json.loads(report_file.read_text())
        require(report.get('rehearsal_only') is True and report.get('cutover_allowed') is False, 'REHEARSAL_RESULT_INVALID')
        print(json.dumps(report))
    finally:
        if container and re.fullmatch(r'[a-f0-9]{64}',container):
            run([args.docker,'rm','--force',container],log,check=False)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        reason = str(error) if isinstance(error,Refused) else 'CONTROLLED_LAUNCHER_FAILURE'
        print(json.dumps({'status':'BLOCKED','reason':reason,'rehearsal_only':True}))
        sys.exit(1)
