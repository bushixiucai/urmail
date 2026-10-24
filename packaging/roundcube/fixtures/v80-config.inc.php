<?php
/* Synthetic literal-only fixture; never a production key or database. */
$config = array();
$config['db_dsnw'] = 'sqlite:////home/user-data/mail/roundcube/roundcube.sqlite?mode=0640';
$config['imap_host'] = 'ssl://localhost:993';
$config['imap_conn_options'] = array('ssl' => array('verify_peer' => false, 'verify_peer_name' => false));
$config['smtp_host'] = 'tls://127.0.0.1';
$config['cipher_method'] = 'AES-256-CBC';
$config['des_key'] = '0123456789abcdefghijklmn';
$config['plugins'] = array('html5_notifier', 'archive', 'zipdownload', 'password', 'managesieve', 'jqueryui', 'persistent_login', 'carddav');
$config['skin'] = 'elastic';
$config['product_name'] = 'Synthetic v80 Webmail';
$config['language'] = 'en_US';
$config['session_path'] = '/mail/';
$config['session_samesite'] = 'Strict';
$config['quota_zero_as_unlimited'] = true;
?>
