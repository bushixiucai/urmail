import imaplib, smtplib, ssl, sys
host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
user = sys.argv[2] if len(sys.argv) > 2 else "alice@example.test"
password = sys.argv[3] if len(sys.argv) > 3 else "MailPassword123"
ctx = ssl._create_unverified_context()
imap = imaplib.IMAP4_SSL(host, 993, ssl_context=ctx)
imap.login(user, password)
typ, data = imap.select("INBOX")
print("IMAP OK", typ, data[0].decode())
smtp = smtplib.SMTP(host, 587, timeout=10)
smtp.ehlo(); smtp.starttls(context=ctx); smtp.ehlo(); smtp.login(user, password)
smtp.sendmail(user, [user], "From: %s\r\nTo: %s\r\nSubject: client smoke\r\n\r\nhello\r\n" % (user, user))
smtp.quit()
print("SMTP OK")
