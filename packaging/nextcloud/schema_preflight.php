<?php
// Run only in the guarded, private container as the owner of copied config.php.
declare(strict_types=1);
if (!is_file('/run/urmail-v06.marker') || !is_file('/.dockerenv') || posix_getuid() === 0) { exit(70); }
if (!chdir('/usr/local/lib/owncloud')) { exit(73); }
define('OC_CONSOLE', 1);
require_once '/usr/local/lib/owncloud/lib/base.php';

$mode = $argv[1] ?? 'preflight';
if (!in_array($mode, ['preflight', 'convert'], true)) {
	fwrite(STDERR, "UNSUPPORTED_SCHEMA_MODE\n");
	exit(74);
}

try {
	$password = file_get_contents('php://stdin');
	$command = new \OC\Core\Command\Db\ConvertType(
		\OCP\Server::get(\OCP\IConfig::class),
		\OCP\Server::get(\OC\DB\ConnectionFactory::class),
		\OCP\Server::get(\OCP\App\IAppManager::class)
	);
	$input = new \Symfony\Component\Console\Input\ArrayInput([
		'type' => 'pgsql', 'username' => 'urmail_nextcloud',
		'hostname' => '/run/urmail-pg', 'database' => 'urmail',
		'--port' => '5432', '--all-apps' => true, '--password' => $password,
	], $command->getDefinition());
	$input->setInteractive(false);
	$output = new \Symfony\Component\Console\Output\BufferedOutput();
	$invoke = function ($method, ...$args) use ($command) {
		$reflection = new \ReflectionMethod($command, $method);
		return $reflection->invoke($command, ...$args);
	};
	$invoke('validateInput', $input, $output);
	$from = \OCP\Server::get(\OC\DB\Connection::class);
	$to = $invoke('getToDBConnection', $input, $output);
	$config = \OCP\Server::get(\OCP\IConfig::class);
	$prefix = $config->getSystemValue('dbtableprefix', 'oc_');

	if ($mode === 'preflight') {
		// The Python guard checked an empty private schema under the same real LOGIN.
		$invoke('createSchema', $from, $to, $input, $output);
		$source = $invoke('getTables', $from);
		$target = $invoke('getTables', $to);
		$missing = array_values(array_diff($source, $target));
		// Missing tables are reported for the harness residual-coverage step.
		// Exiting non-zero here would hide the JSON and force --no-interaction convert to skip rows.
		echo json_encode(['source_tables'=>$source, 'target_tables'=>$target,
			'missing_tables'=>$missing, 'prefix'=>$prefix], JSON_THROW_ON_ERROR);
		exit(0);
	}

	// convert: residual tables must already exist; do not use stock occ (PgSqlTools breaks on private schema).
	$source = $invoke('getTables', $from);
	$target = $invoke('getTables', $to);
	$missing = array_values(array_diff($source, $target));
	if ($missing) {
		file_put_contents('/home/user-data/owncloud/preflight-error.private', 'MISSING_BEFORE_COPY:'.implode(',', $missing));
		exit(71);
	}
	$intersecting = array_values(array_intersect($target, $source));
	$config->setSystemValue('maintenance', true);
	try {
		$schema = $from->createSchema();
		foreach ($intersecting as $tableName) {
			$output->writeln('<info> - '.$tableName.'</info>');
			$invoke('copyTable', $from, $to, $schema->getTable($tableName), $input, $output);
		}
		// Intentionally skip PgSqlTools::resynchronizeDatabaseSequences — it emits
		// invalid SQL (MAX() FROM) against nextcloud.* sequences. Harness resyncs.
		$invoke('saveDBInfo', $input);
	} catch (\Throwable $error) {
		$config->setSystemValue('maintenance', false);
		throw $error;
	}
	$config->setSystemValue('maintenance', false);
	echo json_encode([
		'tables_copied' => count($intersecting),
		'prefix' => $prefix,
		'official_sequence_resync' => 'SKIPPED_PRIVATE_SCHEMA_BUG',
		'config_dbtype' => $config->getSystemValue('dbtype'),
	], JSON_THROW_ON_ERROR);
	exit(0);
} catch (\Throwable $error) {
	// Details are retained only in the root-private rehearsal log, never stdout.
	file_put_contents('/home/user-data/owncloud/preflight-error.private', get_class($error) . ': ' . $error->getMessage());
	exit(72);
}
