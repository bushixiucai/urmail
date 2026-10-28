#!/usr/bin/env python3
"""Run an isolated real-auth archive investigation browser journey."""
import argparse,json,os,shutil,subprocess,tempfile
from pathlib import Path
from urllib.parse import urlsplit
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--fixture',type=Path,required=True);p.add_argument('--evidence',type=Path,required=True);p.add_argument('--disposable',action='store_true');p.add_argument('--cli',default=shutil.which('playwright-cli') or str(Path.home()/'.codex/skills/playwright/scripts/playwright_cli.sh'));a=p.parse_args()
if not a.disposable or a.fixture.stat().st_mode&0o077:p.error('private fixture and --disposable required')
f=json.loads(a.fixture.read_text())
if urlsplit(f['base_url']).hostname not in ('127.0.0.1','localhost','::1'):p.error('loopback fixture required')
a.evidence.mkdir(mode=0o700,parents=True,exist_ok=True)
source=Path(__file__).with_suffix('.js').read_text().replace('__ARC_FIXTURE__',json.dumps(f)).replace('__ARC_EVIDENCE__',json.dumps(str(a.evidence.resolve())))
with tempfile.TemporaryDirectory(prefix='urmail-arc-browser-code-') as directory:
 code=Path(directory)/'run.js';code.write_text(source);code.chmod(0o600)
 result=subprocess.run([a.cli,'-s=urmail-arc-browser','run-code','--filename',str(code)],capture_output=True,text=True)
 log=result.stdout+result.stderr
 for account in f['accounts']:log=log.replace(account['password'],'[REDACTED_TEST_PASSWORD]')
 (a.evidence/'browser-cli.log').write_text(log)
 if result.returncode or '### Error' in log or '### Result\n' not in result.stdout:
  subprocess.run([a.cli,'-s=urmail-arc-browser','run-code','async(page)=>{await page.screenshot({path:'+json.dumps(str(a.evidence/'failure.png'))+',fullPage:true})}'],capture_output=True)
  raise RuntimeError('archive browser journey failed; inspect sanitized browser-cli.log')
 facts=json.loads(result.stdout.split('### Result\n',1)[1].split('\n###',1)[0].strip());(a.evidence/'browser-results.json').write_text(json.dumps(facts,indent=2))
 print(json.dumps({k:v for k,v in facts.items() if k not in ('requests','aria')}))
 # Keep the backend alive until the independently installed CLI verifies the ZIP.
