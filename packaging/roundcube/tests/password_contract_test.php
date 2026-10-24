<?php
namespace urmail_password_test;
use RuntimeException;
// Run with php -n: isolated native-driver/session contract without external mail.
define('PASSWORD_ERROR', 2);
define('PASSWORD_SUCCESS', 0);
define('PASSWORD_CONSTRAINT_VIOLATION', 5);
foreach (['CURLOPT_POST','CURLOPT_POSTFIELDS','CURLOPT_RETURNTRANSFER','CURLOPT_TIMEOUT','CURLOPT_CONNECTTIMEOUT','CURLOPT_FOLLOWLOCATION','CURLOPT_PROXY','CURLOPT_PROTOCOLS','CURLOPT_HTTPHEADER','CURLPROTO_HTTP','CURLINFO_RESPONSE_CODE'] as $index => $name) { if (!defined($name)) { define($name, $index + 100); } }
class rcube_plugin {}
class rcube { public static function write_log($name, $message) { throw new RuntimeException('Unexpected sensitive-path log'); } }
class test_config { public function get($name, $default = null) { return ['urmail_bridge_key' => str_repeat('k', 32), 'urmail_bridge_url' => 'http://127.0.0.1:8080/api/v1/roundcube/bridge'][$name] ?? $default; } }
class test_user { public function get_username() { return 'owner@example.test'; } }
class test_storage { public $connected = true; public function check_connection() { return $this->connected; } }
class test_plugins { public $bridge; public function get_plugin($name) { return $name === 'urmail_bridge' ? $this->bridge : null; } }
class rcmail {
    public static $instance;
    public $task = 'settings'; public $action = 'plugin.password-save'; public $csrf = true;
    public $config; public $user; public $plugins; public $storage;
    public static function get_instance() { return self::$instance; }
    public function check_request() { return $this->csrf; }
    public function get_storage() { return $this->storage; }
    public function kill_session() { $_SESSION = ['temp' => true];  }
}
function file_get_contents($path) { return str_starts_with($path, '/usr/share/urmail/locales/') ? '{"page.webmail.authorization_unavailable":"Unavailable","error.MAILBOX_PASSWORD_CURRENT_INVALID":"Current password rejected"}' : \file_get_contents($path); }
function header($value, $replace = true, $status = 0) { $GLOBALS['headers'][] = [$value, $status]; }
function curl_init($url) { if ($url !== 'http://127.0.0.1:8080/api/v1/roundcube/bridge') { throw new RuntimeException('Unsafe endpoint'); } return (object) []; }
function curl_setopt_array($handle, $options) {
    $body = json_decode($options[CURLOPT_POSTFIELDS], true, 32, JSON_THROW_ON_ERROR);
    if ($body !== ['action' => 'change_password', 'session' => 'session-token', 'address' => 'owner@example.test', 'old_password' => 'old-secret', 'new_password' => 'new-secret']) { throw new RuntimeException('Driver identity or password payload changed'); }
    if ($options[CURLOPT_FOLLOWLOCATION] !== false || $options[CURLOPT_PROXY] !== '') { throw new RuntimeException('Credential redirection allowed'); }
    $headers = [];
    foreach ($options[CURLOPT_HTTPHEADER] as $header) { [$name, $value] = explode(':', $header, 2); $headers[$name] = trim($value); }
    $timestamp = $headers['X-Urmail-Timestamp'] ?? ''; $nonce = $headers['X-Urmail-Nonce'] ?? '';
    if (!ctype_digit($timestamp) || !preg_match('/^[a-f0-9]{64}$/D', $nonce)
        || !hash_equals(hash_hmac('sha256', "POST\n/api/v1/roundcube/bridge\n$timestamp\n$nonce\n" . $options[CURLOPT_POSTFIELDS], str_repeat('k', 32)), $headers['X-Urmail-Signature'] ?? '')) { throw new RuntimeException('Password request proof did not bind path and body'); }
    $GLOBALS['requests']++;
}
function curl_exec($handle) { if (!empty($GLOBALS['unknown_response'])) { return false; } if (!empty($GLOBALS['remote_reject'])) { return '{"error":{"code":"MAILBOX_PASSWORD_CURRENT_INVALID"}}'; } return '{"changed":true,"reauthentication_required":true,"imap_revocation":"pending"}'; }
function curl_getinfo($handle, $key) { return !empty($GLOBALS['unknown_response']) ? 0 : (!empty($GLOBALS['remote_reject']) ? ($GLOBALS['remote_status'] ?? 403) : 200); }
function curl_close($handle) {}
foreach (['urmail_bridge.php', 'password_driver.php'] as $source) {
    $code = file_get_contents(dirname(__DIR__) . '/plugins/urmail_bridge/' . $source);
    eval('namespace urmail_password_test; use RuntimeException; use Throwable; ' . substr($code, 5));
}
$rc = rcmail::$instance = new rcmail(); $rc->config = new test_config(); $rc->user = new test_user(); $rc->plugins = new test_plugins(); $rc->storage = new test_storage();
$rc->plugins->bridge = new urmail_bridge(); $driver = new rcube_urmail_password(); $requests = 0;
$_SESSION = ['urmail_mailbox_session' => 'session-token', 'password' => 'old-encrypted-session-password']; $_SERVER['REQUEST_METHOD'] = 'POST';
foreach (['csrf', 'connected', 'username', 'session', 'method', 'action'] as $case) {
    $rc->csrf = $case !== 'csrf'; $rc->storage->connected = $case !== 'connected'; $rc->action = $case === 'action' ? 'other' : 'plugin.password-save';
    $_SESSION['urmail_mailbox_session'] = $case === 'session' ? '' : 'session-token'; $_SERVER['REQUEST_METHOD'] = $case === 'method' ? 'GET' : 'POST';
    if ($driver->save('old-secret', 'new-secret', $case === 'username' ? 'other@example.test' : 'owner@example.test') !== PASSWORD_ERROR) { throw new RuntimeException('Missing authenticated self-service prerequisite accepted'); }
}
if ($requests !== 0) { throw new RuntimeException('Rejected prerequisite sent credentials'); }
$rc->csrf = true; $rc->storage->connected = true; $rc->action = 'plugin.password-save'; $_SESSION['urmail_mailbox_session'] = 'session-token'; $_SERVER['REQUEST_METHOD'] = 'POST';
if ($driver->save('old-secret', str_repeat('中', 7), 'owner@example.test') !== PASSWORD_CONSTRAINT_VIOLATION || $requests !== 0) { throw new RuntimeException('Unicode short secret was not rejected before transport'); }
if (($argv[1] ?? '') === 'rejected') {
    $GLOBALS['remote_reject'] = true;
    if ($driver->save('old-secret', 'new-secret', 'owner@example.test') !== PASSWORD_ERROR || isset($_SESSION['urmail_password_changed']) || $_SESSION['password'] !== 'old-encrypted-session-password') { throw new RuntimeException('Known rejection changed current session'); }
    echo "KNOWN_REJECTION_PRESERVES_SESSION\n"; exit;
}
if (in_array(($argv[1] ?? ''), ['unknown', 'server-error'], true)) {
    if (($argv[1] ?? '') === 'server-error') { $GLOBALS['remote_reject'] = true; $GLOBALS['remote_status'] = 503; } else { $GLOBALS['unknown_response'] = true; }
    register_shutdown_function(function () {
        if ($_SESSION !== ['temp' => true] || !in_array(['Location: ./?_task=login&_urmail_password_unknown=1', 303], $GLOBALS['headers'] ?? [], true)) { throw new RuntimeException('Unknown change outcome retained cached credentials'); }
        echo "UNKNOWN_OUTCOME_REAUTHENTICATION_REQUIRED\n";
    });
    $driver->save('old-secret', 'new-secret', 'owner@example.test');
    throw new RuntimeException('Unknown result continued password form session');
}
if ($driver->save('old-secret', 'new-secret', 'owner@example.test') !== PASSWORD_SUCCESS || empty($_SESSION['urmail_password_changed']) || $requests !== 1) { throw new RuntimeException('Native password driver contract failed'); }
if (($argv[1] ?? '') === 'logout') {
    register_shutdown_function(function () {
        if ($_SESSION !== ['temp' => true] || !in_array(['Location: ./?_task=login&_urmail_password_changed=1', 303], $GLOBALS['headers'] ?? [], true)) { throw new RuntimeException('Changed password did not revoke current session and redirect'); }
        echo "SESSION_KILLED_AND_REDIRECTED\n";
    });
    $rc->plugins->bridge->password_changed(['old_pass' => 'old-secret', 'new_pass' => 'new-secret']);
    throw new RuntimeException('Password core resumed and could cache new credentials');
}
echo "PASSWORD_DRIVER_CONTRACT_PASS\n";
