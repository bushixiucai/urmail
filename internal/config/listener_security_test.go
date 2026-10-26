package config

import "testing"

func TestHTTPListenerRequiresExplicitLoopbackInEveryMode(t *testing.T) {
	for _, dev := range []bool{false, true} {
		for _, address := range []string{"127.0.0.1:8080", "127.0.0.2:1", "[::1]:65535"} {
			cfg := Config{DevMode: dev, PolicyEnabled: true, ListenAddr: address, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
			if err := cfg.validate(); err != nil {
				t.Errorf("dev=%t address=%s: %v", dev, address, err)
			}
		}
		for _, address := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "localhost:8080", "192.0.2.1:8080", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1:-1", "127.0.0.1"} {
			cfg := Config{DevMode: dev, PolicyEnabled: true, ListenAddr: address, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
			if err := cfg.validate(); err == nil {
				t.Errorf("dev=%t accepted unsafe address=%s", dev, address)
			}
		}
	}
}
