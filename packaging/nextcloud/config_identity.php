<?php
// Inspect copied configuration only inside the marked private namespace.
declare(strict_types=1);
if (!is_file('/run/urmail-v06.marker') || !is_file('/.dockerenv') || posix_getuid() === 0) { exit(70); }
$CONFIG = [];
require '/home/user-data/owncloud/config.php';
$result = [];
foreach (['instanceid','passwordsalt','secret'] as $key) {
    if (!isset($CONFIG[$key]) || !is_string($CONFIG[$key]) || $CONFIG[$key] === '') { exit(71); }
    $result[$key . '_sha256'] = hash('sha256', $CONFIG[$key]);
}
foreach (['dbtype','dbname','dbuser','dbtableprefix','datadirectory'] as $key) {
    $result[$key] = $CONFIG[$key] ?? null;
}
echo json_encode($result, JSON_THROW_ON_ERROR);
