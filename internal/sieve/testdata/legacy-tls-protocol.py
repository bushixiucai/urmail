"""Add strict local STARTTLS to the shared immutable raw-protocol fixture."""
import pathlib

source = pathlib.Path('/test-base.py').read_text()

def replace_once(old, new):
    global source
    assert source.count(old) == 1, 'fixture shape changed'
    source = source.replace(old, new)

replace_once('import base64', 'import base64\nimport ssl')
replace_once("config = '''protocols", "subprocess.run(['/usr/bin/openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=box.example.test', '-addext', 'subjectAltName=DNS:box.example.test', '-keyout', '/tmp/fixture.key', '-out', '/tmp/fixture.cert'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)\nconfig = '''protocols")
replace_once('ssl = no\ndisable_plaintext_auth = no', 'ssl = required\nssl_cert = </tmp/fixture.cert\nssl_key = </tmp/fixture.key\ndisable_plaintext_auth = yes')
replace_once("    assert reply()[0].splitlines()[-1].startswith(b'OK')\n", """    assert reply()[0].splitlines()[-1].startswith(b'OK')
    command(b'STARTTLS')
    context = ssl.create_default_context(cafile='/tmp/fixture.cert')
    stream.close()
    client = context.wrap_socket(client, server_hostname='box.example.test')
    stream = client.makefile('rwb', buffering=0)
    assert reply()[0].splitlines()[-1].startswith(b'OK')
    print('PASS strict STARTTLS 127.0.0.1:4190 with trusted chain and mailbox hostname SAN')
""")
# This executes only the repository-owned synthetic test, never source PHP/config.
exec(compile(source, '/test-base.py', 'exec'))
