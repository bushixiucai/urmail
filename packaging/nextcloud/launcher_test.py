import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

LAUNCHER = Path(__file__).resolve().parents[2]/'scripts/m8/nextcloud-rehearse.py'
SPEC = importlib.util.spec_from_file_location('rehearse_launcher', LAUNCHER)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class LauncherTests(unittest.TestCase):
    def test_docker_capability_prefix_normalization_is_strict(self):
        expected = {'CHOWN','SETUID','SETGID'}
        self.assertEqual(MODULE.normalize_capabilities(['CAP_CHOWN','CAP_SETUID','CAP_SETGID']),expected)
        self.assertEqual(MODULE.normalize_capabilities(['CHOWN','SETUID','SETGID']),expected)
        self.assertEqual(MODULE.normalize_capabilities(['ALL']),{'ALL'})
        self.assertNotEqual(MODULE.normalize_capabilities(['CAP_CHOWN','CAP_SETUID','CAP_SETGID','CAP_SYS_ADMIN']),expected)
        self.assertNotEqual(MODULE.normalize_capabilities(['CAP_CHOWN','CAP_SETUID','CAP_DAC_OVERRIDE']),expected)
        for value in [None,'CAP_CHOWN',['CAP_CAP_CHOWN'],['cap_chown'],['CAP_CHOWN ']]:
            if value == ['CAP_CAP_CHOWN']:
                self.assertNotEqual(MODULE.normalize_capabilities(value),expected)
            else:
                with self.assertRaises(MODULE.Refused):
                    MODULE.normalize_capabilities(value)

    def test_mutable_image_is_rejected_before_docker_or_host_copy(self):
        arguments = ['launcher','--php-provenance','original-v80-build-checkpoint-not-release-bundle','--image','nextcloud:latest','--mirror','/unused','--manifest','/unused','--manifest-sha256','a'*64,'--work','/unused','--mode','convert']
        with mock.patch.object(MODULE.sys,'argv',arguments), mock.patch.object(MODULE.os,'getuid',return_value=0), mock.patch.object(MODULE.subprocess,'run') as process:
            with self.assertRaisesRegex(MODULE.Refused,'IMMUTABLE_RUNTIME_IMAGE_REQUIRED'):
                MODULE.main()
            process.assert_not_called()

    def test_actual_engine_host_network_is_refused_before_start(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            mirror = root/'mirror';mirror.mkdir(mode=0o700)
            manifest = root/'manifest.private.json';manifest.write_text('{}')
            (root/'COMPLETE_REHEARSAL_ONLY').touch()
            work = root/'new-work'
            arguments = ['launcher','--php-provenance','original-v80-build-checkpoint-not-release-bundle','--image','sha256:'+'a'*64,'--mirror',str(mirror),'--manifest',str(manifest),'--manifest-sha256',MODULE.hashlib.sha256(b'{}').hexdigest(),'--work',str(work),'--mode','inspect']
            original_stat = Path.stat
            def safe_stat(path,*args,**kwargs):
                result = original_stat(path,*args,**kwargs)
                if path == mirror:
                    values = list(result);values[4] = 0
                    return os.stat_result(values)
                return result
            commands = []
            container = 'c'*64
            def engine(command,**kwargs):
                commands.append(command)
                if command[1]=='create':
                    self.assertEqual(command[command.index('--network')+1],'none')
                    self.assertNotIn('--privileged',command)
                    return subprocess.CompletedProcess(command,0,container.encode(),b'')
                if command[1]=='inspect':
                    return subprocess.CompletedProcess(command,0,json.dumps([{'HostConfig':{'NetworkMode':'host','ReadonlyRootfs':True,'Privileged':False}}]).encode(),b'')
                if command[1]=='rm':
                    self.assertEqual(command[-1],container)
                    return subprocess.CompletedProcess(command,0,b'',b'')
                self.fail('Container was started before its actual isolation config was verified')
            with mock.patch.object(MODULE.sys,'argv',arguments), mock.patch.object(MODULE.os,'getuid',return_value=0), mock.patch.object(Path,'stat',safe_stat), mock.patch.object(MODULE.os,'readlink',return_value='host-namespace'), mock.patch.object(MODULE.subprocess,'run',side_effect=engine):
                with self.assertRaisesRegex(MODULE.Refused,'CONTAINER_SECURITY_CONFIG_INVALID'):
                    MODULE.main()
            self.assertTrue(any(command[1]=='rm' for command in commands))
            self.assertFalse(any(command[1]=='start' for command in commands))
            self.assertEqual(work.stat().st_mode&0o777,0o700)


    def test_prefixed_caps_pass_actual_launcher_guard_in_controlled_engine(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);mirror=root/'mirror';mirror.mkdir(mode=0o700)
            manifest=root/'manifest.private.json';manifest.write_text('{}')
            (root/'COMPLETE_REHEARSAL_ONLY').touch();work=root/'work'
            arguments=['launcher','--php-provenance','original-v80-build-checkpoint-not-release-bundle','--image','sha256:'+'a'*64,'--mirror',str(mirror),'--manifest',str(manifest),'--manifest-sha256',MODULE.hashlib.sha256(b'{}').hexdigest(),'--work',str(work),'--mode','inspect']
            original_stat=Path.stat;created=[];container='d'*64;started=[]
            def safe_stat(path,*args,**kwargs):
                result=original_stat(path,*args,**kwargs)
                if path==mirror:
                    values=list(result);values[4]=0;return os.stat_result(values)
                return result
            def engine(command,**kwargs):
                if command[1]=='create':
                    created.extend(command);return subprocess.CompletedProcess(command,0,container.encode(),b'')
                if command[1]=='inspect':
                    mounts=[]
                    for index,argument in enumerate(created):
                        if argument=='--mount':
                            parts=created[index+1].split(',');values=dict(part.split('=',1) for part in parts if '=' in part)
                            mounts.append({'Type':'bind','Source':values['src'],'Destination':values['dst'],'RW':'readonly' not in parts})
                    record={'HostConfig':{'NetworkMode':'none','ReadonlyRootfs':True,'Privileged':False,'PortBindings':{},'SecurityOpt':['no-new-privileges'],'CapDrop':['ALL'],'CapAdd':['CAP_CHOWN','CAP_SETUID','CAP_SETGID'],'Memory':3*1024**3,'NanoCpus':1000000000,'PidsLimit':256},'Mounts':mounts,'Config':{'Labels':{'urmail.v06.nonce':created[created.index('--label')+1].split('=',1)[1]}},'State':{'ExitCode':0}}
                    return subprocess.CompletedProcess(command,0,json.dumps([record]).encode(),b'')
                if command[1]=='start':
                    started.append(True);(work/'reports/result.json').write_text(json.dumps({'rehearsal_only':True,'cutover_allowed':False,'conversion':'NOT_RUN'}))
                return subprocess.CompletedProcess(command,0,b'',b'')
            with mock.patch.object(MODULE.sys,'argv',arguments),mock.patch.object(MODULE.os,'getuid',return_value=0),mock.patch.object(Path,'stat',safe_stat),mock.patch.object(MODULE.os,'readlink',return_value='host-namespace'),mock.patch.object(MODULE.subprocess,'run',side_effect=engine),mock.patch('builtins.print'):
                MODULE.main()
            self.assertEqual(started,[True])


if __name__=='__main__':
    unittest.main()
