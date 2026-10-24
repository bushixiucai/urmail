"""Exercise explicit, non-executing v80 preparation without root or a database."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import stat
import tempfile
import unittest
from unittest import mock

from legacy_config import parse_php_config

spec = importlib.util.spec_from_file_location('v80_configure', Path(__file__).with_name('configure.py'))
configure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(configure)
FIXTURE = Path(__file__).with_name('fixtures') / 'v80-config.inc.php'


class LegacyLiteralTests(unittest.TestCase):
    def test_complete_plugin_and_nested_literal_values_preserved(self):
        config = parse_php_config(FIXTURE.read_text())
        self.assertEqual(config['des_key'], '0123456789abcdefghijklmn')
        self.assertEqual(len(config['plugins']), 8)
        self.assertIn('carddav', config['plugins'])
        self.assertIn('password', config['plugins'])
        self.assertFalse(config['imap_conn_options']['ssl']['verify_peer'])

    def test_executable_interpolated_duplicate_and_ambiguous_inputs_refused(self):
        cases = ["<?php system('id');", "<?php $config['des_key'] = getenv('KEY');",
                 '<?php $config["des_key"] = "$secret";',
                 "<?php $config['plugins'] = ['carddav']; $config['plugins'] = ['password'];",
                 "<?php if (false) { $config['plugins'] = ['carddav']; }",
                 "<?php $config['plugins'] = array('carddav', 'key' => 'password');",
                 "<?php $config = array(); $config = array();",
                 "<?php $config['plugins'] = require '/tmp/source.php';"]
        for text in cases:
            with self.subTest(text=text), self.assertRaises(ValueError):
                parse_php_config(text)

    def test_legacy_permissions_and_symlink_refused_before_read(self):
        for symlink, owner, mode in [(True, 0, 0o644), (False, 1, 0o644), (False, 0, 0o664), (False, 0, 0o646)]:
            with self.subTest(symlink=symlink, owner=owner, mode=mode):
                path = mock.Mock()
                path.is_symlink.return_value = symlink
                path.stat.return_value = mock.Mock(st_uid=owner, st_mode=stat.S_IFREG | mode)
                with self.assertRaises(ValueError):
                    configure.legacy_regular_file(path)
                path.read_text.assert_not_called()

    def fixture(self, directory):
        root = Path(directory) / 'runtime'
        for name in ('config', 'program/include', 'public_html', 'plugins'):
            (root / name).mkdir(parents=True, exist_ok=True)
        (root / 'version').write_text('1.7.5:fixture-plugin-revisions')
        (root / 'program/include/iniset.php').write_text("<?php define('RCMAIL_VERSION', '1.7.5');")
        for name in ('index.php', 'static.php'):
            (root / 'public_html' / name).write_text('<?php // fixture')
        (root / 'config/config.inc.php').write_text(FIXTURE.read_text())
        for plugin in parse_php_config(FIXTURE.read_text())['plugins']:
            (root / 'plugins' / plugin).mkdir()
            (root / 'plugins' / plugin / (plugin + '.php')).write_text('<?php // synthetic plugin entry')
        return root

    def preflight(self, root):
        # Source permission checks have independent rejection coverage. These
        # tests never acquire root, execute source PHP or invoke PostgreSQL.
        with mock.patch.object(configure, 'V80_RUNTIME', root), mock.patch.object(configure, 'trusted_v80_directory'), mock.patch.object(configure, 'legacy_regular_file', side_effect=lambda path: path.read_text()), mock.patch.object(configure.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '8.4.21', '')) as commands:
            result = configure.preflight_v80()
        commands.assert_called_once_with([configure.V80_PHP, '-r', 'echo PHP_VERSION;'], check=True, capture_output=True, text=True, timeout=10)
        return result

    def test_preflight_preserves_source_bytes_and_redacts_des(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.fixture(directory)
            before = {str(path): path.read_bytes() for path in root.rglob('*') if path.is_file()}
            result = self.preflight(root)
            self.assertEqual(result['activation_status'], 'blocked')
            self.assertEqual(result['legacy_config']['des_key'], '0123456789abcdefghijklmn')
            self.assertNotIn('0123456789abcdefghijklmn', json.dumps(configure.public_v80_preflight(result)))
            self.assertEqual(before, {str(path): path.read_bytes() for path in root.rglob('*') if path.is_file()})

    def test_wrong_version_missing_plugin_and_symlink_refused(self):
        for problem in ('version', 'core-version', 'plugin', 'source-symlink'):
            with self.subTest(problem=problem), tempfile.TemporaryDirectory() as directory:
                root = self.fixture(directory)
                if problem == 'version':
                    (root / 'version').write_text('1.5.0')
                elif problem == 'core-version':
                    (root / 'program/include/iniset.php').write_text("<?php define('RCMAIL_VERSION', '1.6.0');")
                elif problem == 'plugin':
                    (root / 'plugins/carddav/carddav.php').unlink()
                    (root / 'plugins/carddav').rmdir()
                else:
                    (root / 'plugins/carddav/carddav.php').unlink()
                    (root / 'plugins/carddav').rmdir()
                    (root / 'plugins/carddav').symlink_to(root / 'plugins/password')
                with self.assertRaises(ValueError):
                    self.preflight(root)

    def test_prepare_is_disabled_and_reuses_des_without_writing_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.fixture(directory)
            preflight = self.preflight(root)
            managed = Path(directory) / 'managed'
            managed.mkdir()
            env = Path(directory) / 'daemon.env'
            agent = Path(directory) / 'agent.env'
            env.write_text('URMAIL_DATABASE_URL_PLATFORM=postgres://role:secret@127.0.0.1/urmail\nURMAIL_MYHOSTNAME=box.example.test\nURMAIL_ROUNDCUBE_ENABLED=true\n')
            agent.write_text('URMAIL_ROUNDCUBE_ENABLED=true\n')
            writes = {}
            real_stat = Path.stat
            def root_stat(path, *args, **kwargs):
                values = list(real_stat(path, *args, **kwargs))
                values[4] = 0
                return os.stat_result(values)
            def save(path, content, *_):
                writes[path.name] = content
                path.write_text(content)
            with mock.patch.object(configure.sys, 'argv', ['configure.py', 'configure-v80']), mock.patch.object(configure.os, 'geteuid', return_value=0), mock.patch.object(configure, 'preflight_v80', return_value=preflight), mock.patch.object(configure, 'ROOT', managed), mock.patch.object(configure, 'ENV', env), mock.patch.object(configure, 'AGENT_ENV', agent), mock.patch.object(configure, 'private_file'), mock.patch.object(configure.os, 'chown'), mock.patch.object(configure.os, 'chmod'), mock.patch.object(configure.Path, 'stat', autospec=True, side_effect=root_stat), mock.patch.object(configure.grp, 'getgrnam', return_value=mock.Mock(gr_gid=33)), mock.patch.object(configure, 'load_existing', return_value={}), mock.patch.object(configure, 'atomic_private', side_effect=save), mock.patch.object(configure.subprocess, 'run') as commands, contextlib.redirect_stdout(io.StringIO()) as output:
                configure.main()
            state = json.loads(writes['provision-state.json'])
            self.assertEqual(state['des_key'], preflight['legacy_config']['des_key'])
            self.assertEqual(state['legacy_config']['plugins'], preflight['plugins'])
            self.assertIn('password-driver.php', writes)
            self.assertIn("password_driver'] = 'urmail'", writes['password-config.inc.php'])
            self.assertIn('URMAIL_ROUNDCUBE_ENABLED=false', env.read_text())
            self.assertIn('URMAIL_ROUNDCUBE_ENABLED=false', agent.read_text())
            self.assertNotIn(state['des_key'], output.getvalue())
            self.assertEqual(commands.call_count, 1)
            self.assertEqual(commands.call_args.args[0][:3], ['runuser', '-u', 'postgres'])
            self.assertEqual((root / 'config/config.inc.php').read_text(), FIXTURE.read_text())


    def test_default_mode_cannot_take_over_source_runtime(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(configure, 'V80_RUNTIME', Path(directory)), mock.patch.object(configure.sys, 'argv', ['configure.py']), mock.patch.object(configure.os, 'geteuid', return_value=0), mock.patch.object(configure.subprocess, 'run') as commands:
            with self.assertRaises(ValueError):
                configure.main()
            commands.assert_not_called()

    def test_v80_activation_is_refused_before_any_installer(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, content in [('id', '#!/bin/sh\nprintf 0\n'), ('python3', '#!/bin/sh\nexit 0\n')]:
                path = root / name
                path.write_text(content)
                path.chmod(0o700)
            result = subprocess.run(['bash', str(Path(__file__).with_name('manage.sh')), 'install-v80'], env=dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH']), capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertIn('verified metadata import', result.stderr)
            self.assertIn('No runtime, package, database or source files changed', result.stderr)

    def test_actual_php_wrapper_appends_bridge_and_keeps_all_plugins_and_des(self):
        legacy = parse_php_config(FIXTURE.read_text())
        for mode in ['v80-preserve', 'distro']:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                secret = root / 'secrets.json'
                secret.write_text(json.dumps({'runtime_mode': mode, 'legacy_config': legacy, 'db_dsnw': 'pgsql://fixture@localhost/urmail', 'des_key': legacy['des_key'], 'bridge_key': 'synthetic-bridge-key', 'trusted_hosts': ['box.example.test'], 'mail_hostname': 'box.example.test', 'panel_url': 'https://box.example.test'}))
                secret.chmod(0o600)
                wrapper = root / 'config.php'
                text = Path(__file__).with_name('config.inc.php').read_text()
                text = text.replace("require '/etc/urmail-roundcube/secrets.php'", "json_decode(file_get_contents(" + json.dumps(str(secret)) + "), true)")
                wrapper.write_text(text)
                result = subprocess.run(['php', '-r', '$config=[];require $argv[1];echo json_encode($config);', str(wrapper)], check=True, capture_output=True, text=True)
                config = json.loads(result.stdout)
                self.assertEqual(config['des_key'], legacy['des_key'])
                self.assertEqual(config['db_dsnw'], 'pgsql://fixture@localhost/urmail')
                self.assertEqual(config['plugins'], legacy['plugins'] + ['urmail_bridge'] if mode == 'v80-preserve' else ['urmail_bridge', 'password'])
                self.assertEqual(config['plugins'].count('urmail_bridge'), 1)


    def test_plugin_level_config_removes_sql_fallback_after_main_load(self):
        plugin = Path(__file__).with_name('password-config.inc.php').resolve()
        script = "$config=['password_driver'=>'sql','password_db_dsn'=>'sqlite:legacy','password_query'=>'UPDATE users'];require $argv[1];echo json_encode($config);"
        result = subprocess.run(['php', '-r', script, str(plugin)], check=True, capture_output=True, text=True)
        config = json.loads(result.stdout)
        self.assertEqual(config['password_driver'], 'urmail')
        self.assertTrue(config['password_confirm_current'])
        self.assertEqual(config['password_minimum_length'], 8)
        self.assertFalse(config['password_log'])
        self.assertNotIn('password_db_dsn', config)
        self.assertNotIn('password_query', config)


if __name__ == '__main__':
    unittest.main()
