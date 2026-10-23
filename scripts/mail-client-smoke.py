#!/usr/bin/env python3
"""Verify SMTP submission and retrieval of the same message and attachment."""
import argparse
import email
import email.policy
import imaplib
import poplib
import secrets
import smtplib
import ssl
import time
from email.message import EmailMessage
from email.utils import make_msgid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host")
    parser.add_argument("user")
    parser.add_argument("password")
    parser.add_argument("--insecure", action="store_true", help="Only for explicitly self-signed test environments")
    parser.add_argument("--ca-file")
    args = parser.parse_args()
    context = ssl.create_default_context(cafile=args.ca_file)
    if args.insecure:
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
    marker = secrets.token_hex(12)
    attachment = secrets.token_bytes(8192)
    message = EmailMessage()
    message["From"] = args.user
    message["To"] = args.user
    message["Subject"] = "urMail protocol acceptance " + marker
    message["Message-ID"] = make_msgid(domain="acceptance.test")
    message.set_content("Protocol acceptance " + marker)
    message.add_attachment(attachment, maintype="application", subtype="octet-stream", filename="acceptance.bin")
    with smtplib.SMTP(args.host, 587, timeout=15) as smtp:
        smtp.ehlo()
        smtp.starttls(context=context)
        smtp.ehlo()
        smtp.login(args.user, args.password)
        refused = smtp.send_message(message)
        assert not refused, refused
    print("SMTP submission accepted")
    with imaplib.IMAP4_SSL(args.host, 993, ssl_context=context) as imap:
        imap.login(args.user, args.password)
        assert imap.select("INBOX")[0] == "OK"
        deadline = time.monotonic() + 30
        found = None
        while time.monotonic() < deadline:
            imap.noop()
            status, data = imap.uid("search", None, "HEADER", "Message-ID", message["Message-ID"])
            if status == "OK" and data[0]:
                found = data[0].split()[-1]
                break
            time.sleep(0.25)
        assert found, "SMTP accepted the message, but IMAP did not receive it"
        status, data = imap.uid("fetch", found, "(BODY.PEEK[])")
        assert status == "OK"
        raw = next(part[1] for part in data if isinstance(part, tuple))
        received = email.message_from_bytes(raw, policy=email.policy.default)
        assert received["Message-ID"] == message["Message-ID"]
        assert marker in received.get_body(preferencelist=("plain",)).get_content()
        assert any(part.get_payload(decode=True) == attachment for part in received.iter_attachments())
        assert received.get("DKIM-Signature"), "DKIM signing header is missing"
    print("IMAP received the unique message, exact attachment and DKIM header")
    pop = poplib.POP3_SSL(args.host, 995, timeout=15, context=context)
    try:
        pop.user(args.user)
        pop.pass_(args.password)
        count, _ = pop.stat()
        found = False
        for number in range(count, max(0, count - 50), -1):
            _, lines, _ = pop.retr(number)
            candidate = email.message_from_bytes(b"\r\n".join(lines), policy=email.policy.default)
            if candidate["Message-ID"] == message["Message-ID"]:
                found = True
                assert any(part.get_payload(decode=True) == attachment for part in candidate.iter_attachments())
                break
        assert found, "The submitted message was not available through POP3"
    finally:
        pop.quit()
    print("POP3 retrieved the same message and exact attachment")
    print("TLS verification:", "DISABLED (self-signed test only)" if args.insecure else "enabled")
    print("DKIM header presence does not establish public DNS signature validation")


if __name__ == "__main__":
    main()
