<?php
// Owned by the managed installer; never include the legacy SQLite SQL driver.
unset($config['password_db_dsn'], $config['password_query'], $config['password_crypt_hash'],
    $config['password_algorithm'], $config['password_algorithm_prefix']);
$config['password_driver'] = 'urmail';
$config['password_confirm_current'] = true;
$config['password_minimum_length'] = 8;
$config['password_log'] = false;
