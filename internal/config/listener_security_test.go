package config

import "testing"

// TestHTTPListenerValidatesExplicitBind pins the GRAFANA-DESIGN §2.1 option
// A posture: the default stays loopback-only (applyDefaults), and the
// validator accepts explicit loopback, RFC1918/ULA private and wildcard
// binds (the compose bridge scrape face) while rejecting hostname forms and
// public unicast addresses. The /internal/metrics bearer gate is unaffected
// by the bind address.
func TestHTTPListenerValidatesExplicitBind(t *testing.T) {
	// Development keeps loopback-only, every mode.
	for _, address := range []string{"127.0.0.1:8080", "127.0.0.2:1", "[::1]:65535"} {
		cfg := Config{DevMode: true, ListenAddr: address, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
		if err := cfg.validate(); err != nil {
			t.Errorf("dev address=%s: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "172.31.0.5:8080", "example.com:8080", ":8080", "localhost:8080", "192.0.2.1:8080"} {
		cfg := Config{DevMode: true, ListenAddr: address, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
		if err := cfg.validate(); err == nil {
			t.Errorf("dev accepted non-loopback address=%s", address)
		}
	}
	// Production (compose option A): loopback, private bridge and wildcard
	// binds pass; hostnames and public unicast stay rejected.
	for _, address := range []string{"127.0.0.1:8080", "172.31.0.5:8080", "10.0.0.1:8080", "192.168.1.1:8080", "[fd00::5]:8080", "0.0.0.0:8080", "[::]:8080"} {
		cfg := Config{PolicyEnabled: true, ListenAddr: address, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
		if err := cfg.validate(); err != nil {
			t.Errorf("prod address=%s: %v", address, err)
		}
	}
	for _, address := range []string{":8080", "localhost:8080", "192.0.2.1:8080", "203.0.113.9:8080", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1:-1", "127.0.0.1"} {
		cfg := Config{PolicyEnabled: true, ListenAddr: address, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
		if err := cfg.validate(); err == nil {
			t.Errorf("prod accepted unsafe address=%s", address)
		}
	}
}

func TestDefaultListenAddrStaysLoopback(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("default ListenAddr %q must stay loopback-only", cfg.ListenAddr)
	}
}

// TestSQLiteModeAllowsDisabledPolicy is the cutover-blockers Blocker B
// contract: the slice-1 topology has no policy server, so CFG-01 must not
// force URMAIL_POLICY_ENABLED there — the render then emits no
// check_policy_service line, which is what keeps 25/587/465 from 451-ing
// every submission against a socket that will never listen. PostgreSQL
// deployments keep the requirement.
func TestSQLiteModeAllowsDisabledPolicy(t *testing.T) {
	sqliteCfg := Config{DBEngine: "sqlite", SQLitePath: "/tmp/x/urmail.db", PolicyEnabled: false, ListenAddr: "127.0.0.1:8080"}
	if err := sqliteCfg.validate(); err != nil {
		t.Fatalf("sqlite without policy rejected: %v", err)
	}
	pgCfg := Config{PolicyEnabled: false, ListenAddr: "127.0.0.1:8080", DatabaseURL: "app", DatabaseURLPlatform: "platform"}
	if err := pgCfg.validate(); err == nil {
		t.Fatal("PG without policy must stay rejected (CFG-01)")
	}
}
