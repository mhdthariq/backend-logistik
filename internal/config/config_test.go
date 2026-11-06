package config_test

import (
	"testing"
	"time"

	"novalogistik/backend/internal/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name, host, port, timeout, address string
		wantTimeout                        time.Duration
		wantError                          bool
	}{
		{name: "defaults", address: "127.0.0.1:8080", wantTimeout: 10 * time.Second},
		{name: "custom", host: "0.0.0.0", port: "9000", timeout: "3s", address: "0.0.0.0:9000", wantTimeout: 3 * time.Second},
		{name: "IPv6", host: "::1", port: "8081", address: "[::1]:8081", wantTimeout: 10 * time.Second},
		{name: "localhost", host: "localhost", address: "localhost:8080", wantTimeout: 10 * time.Second},
		{name: "trim spaces", host: " 127.0.0.1 ", port: " 8080 ", timeout: " 1s ", address: "127.0.0.1:8080", wantTimeout: time.Second},
		{name: "invalid host", host: "https://localhost", wantError: true},
		{name: "port zero", port: "0", wantError: true},
		{name: "port negative", port: "-1", wantError: true},
		{name: "port too large", port: "65536", wantError: true},
		{name: "port text", port: "abc", wantError: true},
		{name: "invalid timeout", timeout: "later", wantError: true},
		{name: "zero timeout", timeout: "0s", wantError: true},
		{name: "negative timeout", timeout: "-1s", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOST", tt.host)
			t.Setenv("PORT", tt.port)
			t.Setenv("SHUTDOWN_TIMEOUT", tt.timeout)
			cfg, err := config.Load()
			if tt.wantError {
				if err == nil {
					t.Fatal("expected invalid configuration to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Address() != tt.address || cfg.ShutdownTimeout != tt.wantTimeout {
				t.Fatalf("got %+v (%s), want %s / %s", cfg, cfg.Address(), tt.address, tt.wantTimeout)
			}
		})
	}
}

func TestTenantConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, nodeEnv, preview, maxPools, maxConnections string
		valid                                            bool
		mode                                             string
	}{
		{"development", "development", "", "32", "4", true, "development"},
		{"production", "production", "", "8", "2", true, "production"},
		{"preview", "production", "true", "8", "2", true, "preview"},
		{"no pools", "development", "", "0", "4", false, ""},
		{"invalid pool count", "development", "", "abc", "4", false, ""},
		{"too many pools", "development", "", "1025", "4", false, ""},
		{"invalid connections", "development", "", "8", "0", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NODE_ENV", tt.nodeEnv)
			t.Setenv("PREVIEW_MODE", tt.preview)
			t.Setenv("TENANT_MAX_POOLS", tt.maxPools)
			t.Setenv("TENANT_MAX_CONNECTIONS", tt.maxConnections)
			t.Setenv("DATABASE_URL", "postgresql://test@localhost/test")
			cfg, err := config.Load()
			if (err == nil) != tt.valid {
				t.Fatalf("unexpected config result: %v", err)
			}
			if tt.valid && (!cfg.BusinessEnabled || cfg.Tenant.Mode != tt.mode) {
				t.Fatal("tenant mode not applied")
			}
		})
	}
}

func TestVercelDefaultsToProduction(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("NODE_ENV", "")
	t.Setenv("PREVIEW_MODE", "false")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tenant.Mode != "production" {
		t.Fatal("Vercel must validate corporate licenses by default")
	}
}
