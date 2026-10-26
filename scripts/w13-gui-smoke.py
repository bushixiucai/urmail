#!/usr/bin/env python3
"""Run the real Roundcube-to-AI-to-confirmed-Sieve browser chain locally."""
import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time

PROXY = '''import select,socket,socketserver,sys
upstream=sys.argv[1]
class Handler(socketserver.BaseRequestHandler):
 def handle(self):
  with socket.create_connection((upstream,443),timeout=20)as remote:
   peers=[self.request,remote]
   while True:
    ready,_,_=select.select(peers,[],[],120)
    if not ready:return
    for source in ready:
     data=source.recv(65536)
     if not data:return
     (remote if source is self.request else self.request).sendall(data)
class Server(socketserver.ThreadingTCPServer):allow_reuse_address=True;daemon_threads=True
Server(('0.0.0.0',18443),Handler).serve_forever()
'''
SEND = '''import json,secrets,ssl,smtplib,email.message,hashlib
f=json.load(open('/root/w13-private-fixture.json'));marker='gui-'+secrets.token_hex(8)
m=email.message.EmailMessage();m['From']=f['address'];m['To']=f['address'];m['Subject']='urMail protocol acceptance '+marker;m['Message-ID']='<'+marker+'@local.test>';m.set_content('A real SMTP/LMTP follow-up to the browser-confirmed rule.');attachment=secrets.token_bytes(4096);m.add_attachment(attachment,maintype='application',subtype='octet-stream',filename='gui-confirmed.bin')
with smtplib.SMTP('box.w13.test',587,timeout=20)as smtp:
 smtp.ehlo();smtp.starttls(context=ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/box.w13.test.cert'));smtp.ehlo();smtp.login(f['address'],f['password']);assert not smtp.send_message(m)
print(json.dumps({'subject':m['Subject'],'message_id':m['Message-ID'],'attachment_sha256':hashlib.sha256(attachment).hexdigest()}))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', required=True, choices=['urmail-w13-2204-final', 'urmail-w13-2404'])
    parser.add_argument('--candidate-evidence', required=True, type=Path)
    parser.add_argument('--port', type=int, default=18443)
    args = parser.parse_args()
    if not 1024 <= args.port <= 65535:
        parser.error('Use a nonprivileged loopback port')
    evidence = args.candidate_evidence.resolve()
    identity = json.loads((evidence / 'identity.json').read_text())
    if identity['container'] != args.container:
        parser.error('Candidate identity does not match the target')
    actual = subprocess.check_output(['docker', 'exec', args.container, 'sha256sum', '/root/urmail-final.deb'], text=True).split()[0]
    if actual != identity['package_sha256']:
        parser.error('Installed candidate file changed; rebind first')
    previous_result = evidence / 'gui-result.json'
    if previous_result.exists():
        previous_result.rename(evidence / ('gui-result.previous.' + str(time.time_ns()) + '.json'))
    installed_paths = ['/usr/bin/urmailctl', '/usr/bin/urmaild', '/usr/bin/urmaild-agent', '/usr/lib/urmail/archive-pipe']
    baseline_runtime = (evidence / 'runtime.log').read_text()
    installed_binary_lines = subprocess.check_output(['docker', 'exec', args.container, 'sha256sum', *installed_paths], text=True).splitlines()
    for line in installed_binary_lines:
        digest, path = line.split()
        if not re.search(r'^' + re.escape(digest) + r'\s+' + re.escape(path) + r'$', baseline_runtime, re.M):
            parser.error('Installed binaries changed since the bound protocol matrix; rerun that matrix first')
    # Roundcube packaging is retired (packaging/roundcube and /usr/share/urmail/roundcube removed).
    skip = {
        'skipped': True,
        'reason': 'Roundcube packaging is retired; GUI smoke that required packaging/roundcube and /usr/share/urmail/roundcube is not run',
        'source_commit': identity['source_commit'],
        'package_sha256': actual,
        'harness_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    }
    (evidence / 'gui-result.json').write_text(json.dumps(skip, indent=2) + '\n')
    print('SKIP Roundcube packaging is retired; w13-gui-smoke does not require packaging/roundcube or /usr/share/urmail/roundcube')
    return
    inspect = json.loads(subprocess.check_output(['docker', 'inspect', args.container], text=True))[0]
    if inspect['HostConfig'].get('PortBindings'):
        parser.error('Application container must not expose public ports')
    upstream = next(iter(inspect['NetworkSettings']['Networks'].values()))['IPAddress']
    if not ipaddress.ip_address(upstream).is_private:
        parser.error('Only the isolated private container is permitted')
    credential = json.loads(subprocess.check_output(['docker', 'exec', args.container, 'cat', '/root/w13-private-fixture.json'], text=True))
    cli = Path(os.environ.get('URMAIL_PWCLI', str(Path.home() / '.codex/skills/playwright/scripts/playwright_cli.sh')))
    image_dir = Path('output/playwright/w13-final') / args.container
    image_dir.mkdir(parents=True, exist_ok=True)
    proxy = 'urmail-w13-final-gui-proxy'
    session = 'urmail-w13-final-gui'
    with tempfile.TemporaryDirectory(prefix='urmail-gui-private-') as temporary:
        temporary = Path(temporary)
        proxy_file = temporary / 'proxy.py'
        proxy_file.write_text(PROXY)
        config = temporary / 'browser.json'
        config.write_text(json.dumps({'browser': {'launchOptions': {'headless': True, 'args': ['--host-resolver-rules=MAP box.w13.test 127.0.0.1:' + str(args.port), '--no-proxy-server']}, 'contextOptions': {'ignoreHTTPSErrors': True, 'viewport': {'width': 1440, 'height': 1080}}}}))
        subprocess.run(['docker', 'run', '--detach', '--rm', '--pull', 'never', '--name', proxy, '--publish', '127.0.0.1:' + str(args.port) + ':18443', '--mount', 'type=bind,src=' + str(proxy_file) + ',dst=/proxy.py,readonly', 'python:3.10-slim', 'python', '/proxy.py', upstream], check=True, stdout=subprocess.DEVNULL)
        def command(*arguments):
            result = subprocess.run([str(cli), '--session', session, *arguments], capture_output=True, text=True)
            text = (result.stdout + result.stderr).replace(credential['password'], '[REDACTED]')
            text = re.sub(r'(_token=)[A-Za-z0-9%_-]+', r'\1[REDACTED]', text)
            text = re.sub(r'("(?:csrf|token|ticket|request_token)"\s*:\s*")[^"]+', r'\1[REDACTED]', text)
            with (evidence / 'gui-filtered.log').open('a') as output:
                output.write(text + '\n')
            if result.returncode or '### Error' in text:
                raise RuntimeError('Browser step failed; see filtered evidence')
            return text
        def code(body):
            filename = temporary / 'private-action.js'
            filename.write_text('async page => {' + body + '}')
            filename.chmod(0o600)
            try:
                return command('run-code', '--filename', str(filename))
            finally:
                filename.unlink()
        def snapshot():
            command('snapshot')
        def screenshot(name):
            command('screenshot', '--filename', str(image_dir / (name + '.png')))
        try:
            command('open', 'https://box.w13.test/mail/', '--browser', 'chrome', '--config', str(config))
            snapshot()
            code("await page.getByRole('textbox',{name:'用户名',exact:true}).fill(" + json.dumps(credential['address']) + ");await page.getByRole('textbox',{name:'密码',exact:true}).fill(" + json.dumps(credential['password']) + ");await page.getByRole('button',{name:'登录',exact:true}).click();await page.waitForLoadState('networkidle');")
            snapshot()
            code("await page.getByRole('link',{name:/urMail protocol acceptance/}).first().click();")
            snapshot()
            code("await page.getByText('摘要',{exact:true}).click();await page.getByRole('dialog').waitFor();if(await page.evaluate(()=>rcmail.busy))throw Error('AI left the mailbox permanently busy');")
            screenshot('ai-advice')
            code("await page.getByRole('button',{name:'×',exact:true}).click();")
            snapshot()
            code("await page.getByText('本人邮箱规则',{exact:true}).click();await page.waitForURL('**/mailbox/sieve');")
            snapshot()
            code("await page.getByRole('textbox',{name:'匹配内容',exact:true}).first().fill('urMail protocol acceptance');await page.getByRole('textbox',{name:'文件夹名称',exact:true}).first().fill('W13 GUI Reviewed');await page.getByRole('checkbox',{name:/我已检查规则并确认激活/}).check();")
            screenshot('explicit-confirmation')
            code("await page.getByRole('button',{name:'保存并激活',exact:true}).click();await page.getByRole('status').filter({hasText:'规则已激活。'}).waitFor();")
            screenshot('rules-active')
            delivery = json.loads(subprocess.check_output(['docker', 'exec', '-i', args.container, 'python3', '-'], input=SEND, text=True))
            (evidence / 'gui-delivery.json').write_text(json.dumps(delivery, indent=2) + '\n')
            code("await page.getByRole('link',{name:'网页邮箱',exact:true}).click();await page.waitForLoadState('networkidle');")
            snapshot()
            code("await page.getByRole('button',{name:/设置/}).click();")
            snapshot()
            code("await page.getByRole('button',{name:/文件夹管理/}).click();")
            snapshot()
            code("await page.getByRole('treeitem',{name:/W13 GUI Reviewed/}).getByRole('checkbox').check();")
            snapshot()
            code("await page.getByRole('button',{name:/^ 邮件$/}).click();")
            snapshot()
            code("await page.getByRole('link',{name:/W13 GUI Reviewed/}).click();")
            snapshot()
            code("await page.getByRole('link',{name:" + json.dumps(delivery['subject']) + ",exact:true}).click();await page.frameLocator('iframe').getByText('A real SMTP/LMTP follow-up to the browser-confirmed rule.',{exact:true}).waitFor();")
            screenshot('actual-delivery-in-folder')
            # Exercise the actual webmail composer, not the standalone SMTP fixture.
            attachment = temporary / 'roundcube-compose-random.bin'
            attachment.write_bytes(os.urandom(8192))
            expected = {'subject': 'urMail protocol acceptance rc-compose-' + os.urandom(8).hex(), 'attachment_sha256': hashlib.sha256(attachment.read_bytes()).hexdigest(), 'attachment_bytes': attachment.stat().st_size}
            if credential['address'] != 'independent@mailbox.w13.test':
                raise RuntimeError('Composer acceptance must target only the known local fixture mailbox')
            code("await page.getByRole('button',{name:/撰写新邮件/}).click();")
            snapshot()
            code("await page.locator('#compose_to input[type=\"text\"]').fill(" + json.dumps(credential['address']) + ");await page.locator('#compose_to input[type=\"text\"]').press('Enter');await page.getByRole('textbox',{name:'主题',exact:true}).fill(" + json.dumps(expected['subject']) + ");await page.getByRole('textbox',{name:'邮件正文',exact:true}).fill('Roundcube composer sent this local-only message and its random attachment through real authenticated SMTP.');")
            code("await page.getByRole('button',{name:'添加附件',exact:true}).click();")
            command('upload', str(attachment))
            snapshot()
            code("await page.getByText('roundcube-compose-random.bin',{exact:true}).waitFor();")
            screenshot('roundcube-compose-local-only')
            code("await page.getByRole('button',{name:/发送$/}).click();await page.waitForFunction(()=>!location.search.includes('_action=compose'));")
            snapshot()
            code("await page.getByRole('link',{name:/W13 GUI Reviewed/}).click();")
            snapshot()
            subject = json.dumps(expected['subject'])
            code("const target=page.getByRole('link',{name:" + subject + ",exact:true});for(let attempt=0;attempt<20&&!await target.count();attempt++){await page.getByRole('button',{name:/刷新/}).click();await page.waitForTimeout(250);}await target.click();await page.frameLocator('iframe').getByText('Roundcube composer sent this local-only message and its random attachment through real authenticated SMTP.',{exact:true}).waitFor();")
            snapshot()
            code("await page.frameLocator('iframe').getByTitle('个人选项').click();")
            snapshot()
            downloaded = temporary / 'browser-downloaded.bin'
            code("const pending=page.waitForEvent('download');await page.frameLocator('iframe').getByRole('button',{name:/下载/}).click();const download=await pending;await download.saveAs(" + json.dumps(str(downloaded)) + ");if(await download.failure())throw Error('Browser attachment download failed');")
            if downloaded.stat().st_size != expected['attachment_bytes'] or hashlib.sha256(downloaded.read_bytes()).hexdigest() != expected['attachment_sha256']:
                raise RuntimeError('Roundcube browser download changed uploaded attachment bytes')
            screenshot('roundcube-composer-received')
            expected.update(roundcube_gui_compose_send_self_only=True, browser_download_exact_bytes=True, actual_download_sha256=hashlib.sha256(downloaded.read_bytes()).hexdigest(), actual_download_bytes=downloaded.stat().st_size)
            (evidence / 'roundcube-compose-download-result.json').write_text(json.dumps(expected, indent=2) + '\n')
            imap_helper = Path(__file__).with_name('w13') / 'w13-compose-imap.py'
            imap_output = subprocess.check_output(['docker', 'exec', '-i', args.container, 'python3', '-c', imap_helper.read_text()], input=json.dumps(expected), text=True)
            imap_proof = json.loads(imap_output)
            if not imap_proof['strict_tls_imap_verified']:
                raise RuntimeError('Strict-TLS IMAP attachment cross-check failed')
            (evidence / 'roundcube-compose-imap-crosscheck.json').write_text(imap_output)
            (evidence / 'gui-result.json').write_text(json.dumps({'source_commit': identity['source_commit'], 'package_sha256': actual, 'mailbox_gui_login': True, 'selected_real_message_ai_advice': True, 'busy_released_before_next_action': True, 'explicit_gui_confirmation': True, 'roundcube_gui_compose_send_attachment': True, 'roundcube_browser_attachment_download_exact': True, 'roundcube_sent_and_received_imap_exact': True, 'real_smtp_lmtp_followup_visible_in_gui_folder': True, 'proxy_binding': '127.0.0.1:' + str(args.port), 'gui_only_ignore_local_certificate_errors': True, 'strict_tls_protocol_evidence_separate': True, 'paid_calls': False, 'installed_binaries_match_protocol_matrix': True, 'harness_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), 'compose_imap_harness_sha256': hashlib.sha256(imap_helper.read_bytes()).hexdigest()}, indent=2) + '\n')
            print('PASS real GUI mailbox login → selected-message AI advice → explicit Sieve confirmation → actual SMTP/LMTP follow-up visible in the target GUI folder.')
        finally:
            try:
                command('close')
            finally:
                subprocess.run(['docker', 'stop', proxy], check=True, stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
