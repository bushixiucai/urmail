#!/usr/bin/env python3
"""Run the real HTTP/PG browser harness without persisting test passwords in evidence."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.parse import urlsplit


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fixture', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--disposable', action='store_true')
    parser.add_argument('--session', default='urmail-remediation-browser')
    parser.add_argument('--cli', default=shutil.which('playwright-cli') or str(Path.home()/'.codex/skills/playwright/scripts/playwright_cli.sh'))
    args = parser.parse_args()
    if not args.disposable or args.fixture.stat().st_mode & 0o077:
        parser.error('use --disposable with a private mode-0600 fixture')
    fixture = json.loads(args.fixture.read_text())
    for name in ('base_url', 'quarantine_url'):
        if urlsplit(fixture.get(name, '')).hostname not in ('127.0.0.1', 'localhost', '::1'):
            parser.error('the harness accepts loopback-only disposable servers')
    for url in [fixture.get('marketing',{}).get('mail_capture_url',''), fixture.get('dns',{}).get('state_url',''), fixture.get('dns',{}).get('unknown_url','')]:
        if url and urlsplit(url).hostname not in ('127.0.0.1','localhost','::1'):
            parser.error('all capture/control services must remain loopback-only')
    args.evidence.mkdir(parents=True, exist_ok=True)
    source = Path(__file__).with_suffix('.js').read_text()
    source = source.replace('__URMAIL_BROWSER_FIXTURE__', json.dumps(fixture)).replace('__URMAIL_BROWSER_EVIDENCE__', json.dumps(str(args.evidence.resolve())))
    with tempfile.TemporaryDirectory(prefix='urmail-browser-') as directory:
        code = Path(directory)/'check.js'
        code.write_text(source)
        code.chmod(0o600)
        try:
            opened = subprocess.run([args.cli, '-s='+args.session, 'open', fixture['base_url']+'/login'], capture_output=True, text=True, check=True)
            result = subprocess.run([args.cli, '-s='+args.session, 'run-code', '--filename', str(code)], capture_output=True, text=True, check=False)
            log = opened.stdout+opened.stderr+result.stdout+result.stderr
            for account in fixture['accounts'] + [fixture.get('platform_account', {})]:
                if account.get('password'):
                    log = log.replace(account['password'], '[REDACTED_TEST_PASSWORD]')
            if fixture.get('ai_api_key'):
                log = log.replace(fixture['ai_api_key'], '[REDACTED_TEST_AI_KEY]')
            if fixture.get('dns', {}).get('token'):
                log = log.replace(fixture['dns']['token'], '[REDACTED_TEST_DNS_TOKEN]')
            if fixture.get('marketing', {}).get('feedback_key'):
                log = log.replace(fixture['marketing']['feedback_key'], '[REDACTED_TEST_FEEDBACK_KEY]')
            (args.evidence/'browser-cli.log').write_text(log)
            if result.returncode != 0 or '### Error' in log or '### Result\n' not in result.stdout:
                subprocess.run([args.cli,'-s='+args.session,'run-code','async(page)=>{await page.screenshot({path:'+json.dumps(str(args.evidence.resolve()/'failure-diagnostic.png'))+',fullPage:true})}'],capture_output=True,check=False)
                raise RuntimeError('browser assertions failed; see sanitized browser-cli.log')
            facts = json.loads(result.stdout.split('### Result\n', 1)[1].split('\n###', 1)[0].strip())
            (args.evidence/'browser-http-results.json').write_text(json.dumps(facts, indent=2))
            (args.fixture.parent/'browser.done').write_text('PASS')
            print(f"PASS: {facts['routes']} routes; {len(facts['requests'])} real HTTP responses; timezone persisted; quarantine disabled registration; browser errors={len(facts['errors'])}")
        except BaseException:
            (args.fixture.parent/'browser.done').write_text('FAIL')
            raise
        finally:
            subprocess.run([args.cli, '-s='+args.session, 'close'], capture_output=True, check=False)


if __name__ == '__main__':
    main()
