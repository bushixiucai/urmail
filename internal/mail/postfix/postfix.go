package postfix

// Target output file paths relative to /etc/postfix.
const (
	FileMainCF            = "main.cf"
	FileMasterCF          = "master.cf"
	FileVirtualMailboxMap = "urmail/virtual_mailbox.map"
	FileVirtualAliasMap   = "urmail/virtual_alias.map"
	FileSenderLoginMap    = "urmail/sender_login.map"
)

// Generate is a pure function that validates the snapshot and node configuration,
// then renders the 5 Postfix configuration files.
//
// If validation fails, it returns (nil, error) - fail closed.
func Generate(node NodeConfig, snap Snapshot) (map[string]string, error) {
	if err := Validate(node, snap); err != nil {
		return nil, err
	}

	result := make(map[string]string, 5)
	result[FileMainCF] = renderMainCF(node, snap)
	result[FileMasterCF] = renderMasterCF(node)
	result[FileVirtualMailboxMap] = renderVirtualMailboxMap(snap)
	result[FileVirtualAliasMap] = renderVirtualAliasMap(snap)
	result[FileSenderLoginMap] = renderSenderLoginMap(snap)

	return result, nil
}
