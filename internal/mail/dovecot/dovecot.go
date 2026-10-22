package dovecot

// Dovecot configuration file names relative to /etc/dovecot.
const (
	FileDovecotConf      = "conf.d/urmail/dovecot.conf"
	FileDovecotSQL       = "conf.d/urmail/dovecot-sql.conf.ext"
	FileDBConf           = "conf.d/urmail/db.conf"
	FileDovecotMailboxes = "conf.d/urmail/dovecot-mailboxes.conf.ext"
	FileDovecotQuota     = "conf.d/urmail/dovecot-quota.conf.ext"
)

// Generate is a pure function that renders Dovecot configuration files.
// Output keys represent paths relative to /etc/dovecot.
// Returns exactly 5 configuration files.
func Generate(node NodeConfig) (map[string]string, error) {
	if err := Validate(node); err != nil {
		return nil, err
	}

	result := make(map[string]string, 5)
	result[FileDovecotConf] = renderDovecotConf(node)
	result[FileDovecotSQL] = renderDovecotSQL(node)
	result[FileDBConf] = renderDBConf(node)
	result[FileDovecotMailboxes] = renderDovecotMailboxes()
	result[FileDovecotQuota] = renderDovecotQuota()

	return result, nil
}
