package main

func setBackupUnitActionForTest(action backupUnitAction) func() {
	prev := activeBackupUnitAction
	activeBackupUnitAction = action
	return func() { activeBackupUnitAction = prev }
}

func setMigrationVerifierForTest(verifier migrationVerifierFunc) func() {
	prev := activeMigrationVerifier
	activeMigrationVerifier = verifier
	return func() { activeMigrationVerifier = prev }
}
