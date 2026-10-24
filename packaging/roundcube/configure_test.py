"""Reject untrusted targets and unsafe fixed-path secrets without touching PG."""
import importlib.util
from pathlib import Path
import tempfile
import subprocess
import sys
import unittest

spec = importlib.util.spec_from_file_location('roundcube_configure', Path(__file__).with_name('configure.py'))
configure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(configure)


class TargetTests(unittest.TestCase):
    def test_local_same_database_target(self):
        self.assertEqual(configure.validate_target({'URMAIL_DATABASE_URL_PLATFORM': 'postgres://urmail_platform:secret@127.0.0.1:5432/urmail', 'URMAIL_MYHOSTNAME': 'box.example.test'}), ('urmail', 5432, 'box.example.test'))

    def test_remote_socket_path_and_command_targets_refused(self):
        for uri in ['postgres://x@attacker.test/urmail', 'postgres://x@169.254.169.254/urmail', 'file:///etc/passwd', 'postgres://x@localhost/urmail%0aDROP', 'postgres://x@localhost/%2Ftmp%2Fdb', 'postgres://x@localhost:70000/urmail']:
            with self.subTest(uri=uri), self.assertRaises(ValueError):
                configure.validate_target({'URMAIL_DATABASE_URL_PLATFORM': uri, 'URMAIL_MYHOSTNAME': 'box.example.test'})

    def test_hostname_injection_refused(self):
        for hostname in ['box.test;id', 'box.test\nfoo', '*.example.test', 'localhost', '-box.example.test']:
            with self.subTest(hostname=hostname), self.assertRaises(ValueError):
                configure.validate_target({'URMAIL_DATABASE_URL_PLATFORM': 'postgres://x@localhost/urmail', 'URMAIL_MYHOSTNAME': hostname})

    def test_extra_arguments_refused_before_any_mutation(self):
        result=subprocess.run([sys.executable,str(Path(__file__).with_name("configure.py")),"/tmp/arbitrary-target"],capture_output=True,text=True)
        self.assertEqual(result.returncode,1)
        self.assertEqual(result.stdout,"")
        self.assertNotIn("password",result.stderr)

    def test_secret_symlink_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'target'
            target.write_text('untouched')
            link = Path(directory) / 'secrets.php'
            link.symlink_to(target)
            with self.assertRaises(ValueError):
                configure.private_file(link)
            self.assertEqual(target.read_text(), 'untouched')


if __name__ == '__main__':
    unittest.main()
