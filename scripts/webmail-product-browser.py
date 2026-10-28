#!/usr/bin/env python3
"""Run one real installed Webmail browser phase with private fixture credentials."""
import argparse,json,subprocess,tempfile,time,shutil
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--fixture',type=Path,required=True);p.add_argument('--evidence',type=Path,required=True);p.add_argument('--phase',required=True);p.add_argument('--disposable',action='store_true');a=p.parse_args()
if not a.disposable or a.fixture.stat().st_mode&0o077:p.error('private fixture and --disposable required')
f=json.loads(a.fixture.read_text());assert f['base_url']=='http://127.0.0.1:5193';a.evidence.mkdir(parents=True,exist_ok=True)
if (a.evidence/(a.phase+'.log')).exists():
 old=a.evidence/'attempts'/(a.phase+'-'+str(time.time_ns()));old.mkdir(parents=True)
 for suffix in ['.json','.log']:
  prior=a.evidence/(a.phase+suffix)
  if prior.exists():shutil.copy2(prior,old/prior.name)
source=Path(__file__).with_suffix('.js').read_text().replace('__WEBMAIL_FIXTURE__',json.dumps(f)).replace('__WEBMAIL_EVIDENCE__',json.dumps(str(a.evidence.resolve()))).replace('__WEBMAIL_PHASE__',json.dumps(a.phase))
cli=str(Path.home()/'.codex/skills/playwright/scripts/playwright_cli.sh')
with tempfile.TemporaryDirectory(prefix='urmail-webmail-browser-') as directory:
 path=Path(directory)/'run.js';path.write_text(source);path.chmod(0o600)
 result=subprocess.run([cli,'-s=urmail-webmail-final','run-code','--filename',str(path)],capture_output=True,text=True);log=(result.stdout+result.stderr).replace(f['password'],'[REDACTED_FIXTURE_PASSWORD]');(a.evidence/(a.phase+'.log')).write_text(log)
 if result.returncode or '### Error' in log or '### Result\n' not in result.stdout:raise RuntimeError('browser phase failed; inspect sanitized '+a.phase+'.log')
 facts=json.loads(result.stdout.split('### Result\n',1)[1].split('\n###',1)[0].strip());(a.evidence/(a.phase+'.json')).write_text(json.dumps(facts,indent=2));print(json.dumps({k:v for k,v in facts.items() if k not in ('aria','requests')}))
