// Package nsd renders canonical authoritative DNS zones.
package nsd

// NodeConfig holds platform DNS node parameters.
type NodeConfig struct {
	ListenAddresses []string // Concrete locally bound DNS interfaces; never wildcard or resolver stub addresses.
	Hostname        string   // Primary mail hostname (RFC 1123, e.g. box.urmail.cn)
	PrimaryDomain   string   // Primary platform domain (RFC 1123, e.g. urmail.cn)
	PublicIPv4      string   // Host public IPv4 address (e.g. 1.2.3.4)
	PublicIPv6      string   // Host public IPv6 address (optional)
	Nameservers     []string // List of authoritative nameserver hostnames (e.g. ns1.urmail.cn, ns2.urmail.cn)
}

// Record represents an arbitrary custom or standard DNS resource record.
type Record struct {
	Name  string // subname or "@"
	Type  string // A, AAAA, CNAME, MX, TXT, NS, SRV, CAA
	Value string // record target or data
	TTL   int    // TTL in seconds, 0 defaults to zone TTL (86400)
	Prio  int    // Priority for MX/SRV records
}

// Zone represents a domain zone configuration.
type Zone struct {
	Domain   string   // Domain name (e.g. example.com)
	Mode     string   // "managed" (full authority) or "delegated" (subdomain/minimal)
	Serial   uint32   // SOA serial number; caller (reconcile layer) must increment on each zone update
	Explicit bool     // Use only confirmed records; never synthesize an apex website address.
	Records  []Record // Custom or additional records (e.g. DKIM, verification tokens)
	// DNSSEC optionally marks the zone as signed with the key snapshot.
	// nil keeps the zone unsigned (no signconf/key metadata rendered).
	DNSSEC *DNSSECConfig
	// Sign enables agent-managed offline signing; NSD serves the verified .signed file.
	Sign           bool
	SigningVersion string
	Secondary      *SecondaryConfig
}

// Snapshot contains all active DNS zones to be rendered.
type Snapshot struct {
	Zones []Zone
}
