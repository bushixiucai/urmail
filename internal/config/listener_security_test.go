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
