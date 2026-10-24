<?php
// Loaded by the plugin after the main wrapper. Never inherit a remote endpoint
// or a delegated authentication identity from the retained source configuration.
$secrets = require '/etc/urmail-roundcube/secrets.php';
$hostname = $secrets['mail_hostname'] ?? null;
if (!is_string($hostname) || strlen($hostname) > 253 ||
    !preg_match('/\A[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\z/i', $hostname) ||
    strpos($hostname, '..') !== false) {
    throw new RuntimeException('Managed mail TLS hostname is unavailable.');
}
// Roundcube 1.7 uses a URI for port and STARTTLS. The removed port/usetls keys
// must not be mistaken for a working connection policy.
unset($config['managesieve_port'], $config['managesieve_usetls']);
$config['managesieve_host'] = 'tls://127.0.0.1:4190';
$config['managesieve_auth_type'] = null;
$config['managesieve_auth_cid'] = null;
$config['managesieve_auth_pw'] = null;
$config['managesieve_debug'] = false;
$config['managesieve_conn_options'] = ['ssl' => [
    'verify_peer' => true,
    'verify_peer_name' => true,
    'allow_self_signed' => false,
    'peer_name' => $hostname,
]];
// Preserve arbitrary original rules through the vendor's raw editor.
$config['managesieve_raw_editor'] = true;
unset($secrets, $hostname);
