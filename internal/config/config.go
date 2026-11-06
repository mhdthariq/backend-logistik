// Package config loads and validates backend process settings.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"novalogistik/backend/internal/tenant"
)

// Config contains HTTP and tenant settings.
type Config struct {
	Host            string
	Port            int
	ShutdownTimeout time.Duration
	Tenant          tenant.Config
	BusinessEnabled bool
}

// Load reads environment variables, applying local development defaults.
func Load() (Config, error) {
	cfg := Config{Host: "127.0.0.1", Port: 8080, ShutdownTimeout: 10 * time.Second}
	if host := strings.TrimSpace(os.Getenv("HOST")); host != "" {
		if net.ParseIP(host) == nil && host != "localhost" {
			return Config{}, fmt.Errorf("HOST must be an IP address or localhost")
		}
		cfg.Host = host
	}
	if value := strings.TrimSpace(os.Getenv("PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("PORT must be an integer between 1 and 65535")
		}
		cfg.Port = port
	}
	if value := strings.TrimSpace(os.Getenv("SHUTDOWN_TIMEOUT")); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive Go duration")
		}
		cfg.ShutdownTimeout = timeout
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NODE_ENV")))
	if mode == "" && os.Getenv("VERCEL") == "1" {
		mode = "production"
	}
	if mode != "production" {
		mode = "development"
	} else if os.Getenv("PREVIEW_MODE") == "true" {
		mode = "preview"
	}
	cfg.Tenant = tenant.Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), CompanyName: os.Getenv("NEXT_PUBLIC_COMPANY_NAME"), Mode: mode,
		LicenseURL: os.Getenv("LICENSE_API_URL"), LicenseKey: os.Getenv("LICENSE_API_KEY"), LicenseToken: os.Getenv("LICENSE_API_TOKEN"), LicenseApplication: os.Getenv("LICENSE_APLIKASI"),
		MaxPools: 32, MaxConnections: 4, LicenseTimeout: 5 * time.Second,
	}
	cfg.BusinessEnabled = cfg.Tenant.DatabaseURL != "" || cfg.Tenant.LicenseURL != ""
	for key, target := range map[string]*int{"TENANT_MAX_POOLS": &cfg.Tenant.MaxPools} {
		if raw := os.Getenv(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 1024 {
				return Config{}, fmt.Errorf("%s must be an integer between 1 and 1024", key)
			}
			*target = n
		}
	}
	if raw := os.Getenv("TENANT_MAX_CONNECTIONS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || n > 100 {
			return Config{}, fmt.Errorf("TENANT_MAX_CONNECTIONS must be an integer between 2 and 100")
		}
		cfg.Tenant.MaxConnections = int32(n)
	}
	return cfg, nil
}

// Address returns the listener address, including brackets for IPv6 hosts.
func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}
