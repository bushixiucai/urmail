"""Check the managed plugin configuration without real credentials or servers."""
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

ASSET = pathlib.Path(__file__).resolve().parents[1] / 'managesieve-config.inc.php'


class ManagedSieveConfigTests(unittest.TestCase):
    def evaluate(self, hostname):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            secrets = root / 'secrets.php'
            secrets.write_text('<?php return ' + ('[]' if hostname is None else "['mail_hostname'=>" + json.dumps(hostname) + ']') + ';')
            code = ASSET.read_text().replace("'/etc/urmail-roundcube/secrets.php'", repr(str(secrets)))
            self.assertEqual(ASSET.read_text().count("'/etc/urmail-roundcube/secrets.php'"), 1)
            configured = root / 'managed.php'
            configured.write_text(code)
            harness = root / 'test.php'
            harness.write_text("<?php $config=['managesieve_host'=>['source'=>'remote.example.test'], 'managesieve_port'=>123, 'managesieve_usetls'=>false, 'managesieve_auth_cid'=>'delegated-admin', 'managesieve_auth_pw'=>'old-private-password', 'managesieve_debug'=>true]; require " + repr(str(configured)) + "; echo json_encode($config, JSON_THROW_ON_ERROR);")
            return subprocess.run(['php', '-n', str(harness)], capture_output=True, check=False)

    def test_replaces_endpoint_and_auth_proxy_with_uri_and_strict_tls(self):
        result = self.evaluate('box.example.test')
        self.assertEqual(result.returncode, 0)
        config = json.loads(result.stdout)
        self.assertEqual(config['managesieve_host'], 'tls://127.0.0.1:4190')
        self.assertNotIn('managesieve_port', config)
        self.assertNotIn('managesieve_usetls', config)
        self.assertIsNone(config['managesieve_auth_type'])
        self.assertIsNone(config['managesieve_auth_cid'])
        self.assertIsNone(config['managesieve_auth_pw'])
        self.assertFalse(config['managesieve_debug'])
        self.assertTrue(config['managesieve_raw_editor'])
        self.assertEqual(config['managesieve_conn_options']['ssl'], {
            'verify_peer': True, 'verify_peer_name': True,
            'allow_self_signed': False, 'peer_name': 'box.example.test',
        })
        self.assertNotIn(b'old-private-password', result.stdout)

    def test_reviewed_vendor_consumes_uri_port_starttls_and_context(self):
        vendor = os.environ.get('URMAIL_ROUNDCUBE_VENDOR_ROOT')
        if not vendor:
            self.skipTest('URMAIL_ROUNDCUBE_VENDOR_ROOT is not configured')
        root = pathlib.Path(vendor)
        utils = root / 'rcube_utils.php'
        engine = root / 'plugins/managesieve/lib/Roundcube/rcube_sieve_engine.php'
        self.assertTrue(utils.is_file())
        self.assertTrue(engine.is_file())
        with tempfile.TemporaryDirectory() as temp:
            secret = pathlib.Path(temp) / 'secrets.php'
            secret.write_text("<?php return ['mail_hostname'=>'box.example.test'];")
            managed = pathlib.Path(temp) / 'managed.php'
            managed.write_text(ASSET.read_text().replace("'/etc/urmail-roundcube/secrets.php'", repr(str(secret))))
            result = subprocess.run(['php', '-n', str(pathlib.Path(__file__).with_name('managesieve_vendor_contract_test.php')), str(utils), str(engine), str(managed)], capture_output=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertIn(b'PASS real vendor engine/utils', result.stdout)
            self.assertNotIn(b'fixture-session-password', result.stdout)
            print(result.stdout.decode().strip())

    def test_missing_or_invalid_tls_identity_fails_before_connection(self):
        for hostname in (None, '', '../outside', 'box..example.test', '-bad.example.test', 'bad-.example.test', 'a'*64 + '.test', 'x\nremote.test', 'tls://remote.test', 'box_example.test'):
            with self.subTest(hostname=hostname):
                self.assertNotEqual(self.evaluate(hostname).returncode, 0)


if __name__ == '__main__':
    unittest.main()
