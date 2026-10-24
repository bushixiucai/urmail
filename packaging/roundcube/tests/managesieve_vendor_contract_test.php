<?php
// Source-only vendor connection contract. The network client is captured here;
// the separate isolated Dovecot test exercises actual strict STARTTLS traffic.
if ($argc !== 4) { throw new RuntimeException('Expected reviewed utils, engine and synthetic configuration paths.'); }
if (!defined('INTL_IDNA_VARIANT_UTS46')) { define('INTL_IDNA_VARIANT_UTS46', 1); }
class fixture_config {
    public function __construct(public array $values) {}
    public function get($key, $default = null) { return $this->values[$key] ?? $default; }
}
class fixture_plugins {
    public function exec_hook($name, $args) {
        if ($name !== 'managesieve_connect') { throw new RuntimeException('Unexpected hook'); }
        return $args;
    }
}
class rcube {
    public static $instance;
    public static function get_instance() { return self::$instance; }
    public static function raise_error(...$args) { throw new RuntimeException('Unexpected vendor error'); }
}
class rcube_sieve {
    public static array $captured;
    public function __construct(...$args) { self::$captured = $args; }
    public function error() { return 0; }
}
require $argv[1];
require $argv[2];
$config = ['managesieve_host'=>['source'=>'remote.example.test'], 'managesieve_auth_cid'=>'source-delegate', 'managesieve_auth_pw'=>'fixture-old-private', 'managesieve_debug'=>true, 'managesieve_disabled_extensions'=>[]];
require $argv[3];
$_SERVER['SERVER_NAME'] = $_SERVER['HTTP_HOST'] = 'box.example.test';
$_SESSION = ['storage_host'=>'untrusted-session.example.test'];
rcube::$instance = (object) ['config'=>new fixture_config($config), 'plugins'=>new fixture_plugins()];
$class = new ReflectionClass('rcube_sieve_engine');
$engine = $class->newInstanceWithoutConstructor();
$property = $class->getProperty('rc');
$property->setValue($engine, rcube::$instance);
if ($engine->connect('old@example.test', 'fixture-session-password') !== 0) { throw new RuntimeException('Vendor connect failed'); }
$args = rcube_sieve::$captured;
if ($args[0] !== 'old@example.test' || $args[1] !== 'fixture-session-password' ||
    $args[2] !== '127.0.0.1' || $args[3] !== 4190 || $args[4] !== null || $args[5] !== true ||
    $args[7] !== false || $args[8] !== null || $args[9] !== null ||
    $args[10] !== ['ssl'=>['verify_peer'=>true,'verify_peer_name'=>true,'allow_self_signed'=>false,'peer_name'=>'box.example.test']]) {
    throw new RuntimeException('Vendor did not consume the fixed strict STARTTLS contract.');
}
echo "PASS real vendor engine/utils consume fixed host, URI port, STARTTLS, direct identity and strict TLS context\n";
