"""Cross-check a GUI-composed local message over strict-TLS IMAP."""
import email
import email.policy
import hashlib
import imaplib
import json
import ssl
import sys

expected = json.load(sys.stdin)
assert expected['subject'].startswith('urMail protocol acceptance rc-compose-') and len(expected['subject']) < 200
fixture = json.load(open('/root/w13-private-fixture.json'))
assert fixture['address'] == 'independent@mailbox.w13.test'
result = {}
context = ssl.create_default_context(cafile='/var/lib/urmail-agent/certs/mail/box.w13.test.cert')
with imaplib.IMAP4_SSL('box.w13.test', 993, ssl_context=context) as imap:
    imap.login(fixture['address'], fixture['password'])
    for folder in ['W13 GUI Reviewed', 'Sent']:
        assert imap.select('"' + folder + '"')[0] == 'OK'
        status, found = imap.uid('search', None, 'HEADER', 'Subject', json.dumps(expected['subject']))
        assert status == 'OK' and found[0]
        status, parts = imap.uid('fetch', found[0].split()[-1], '(BODY.PEEK[])')
        assert status == 'OK'
        raw = next(part[1] for part in parts if isinstance(part, tuple))
        message = email.message_from_bytes(raw, policy=email.policy.default)
        attachments = [part.get_payload(decode=True) for part in message.iter_attachments()]
        assert len(attachments) == 1 and len(attachments[0]) == expected['attachment_bytes']
        assert hashlib.sha256(attachments[0]).hexdigest() == expected['attachment_sha256']
        assert fixture['address'] in message['From'] and fixture['address'] in message['To'] and not message.get('Cc')
        result[folder] = {'message_id': str(message['Message-ID']), 'attachment_sha256': hashlib.sha256(attachments[0]).hexdigest(), 'bytes': len(attachments[0])}
assert result['Sent']['message_id'] == result['W13 GUI Reviewed']['message_id']
print(json.dumps({'strict_tls_imap_verified': True, 'sent_and_lmtp_received_copy': result}, indent=2))
