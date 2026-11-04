// Command api runs the NovaLogistik HTTP server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/config"
	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := httpapi.New(logger)
	if cfg.BusinessEnabled {
		registry := tenant.New(cfg.Tenant)
		defer registry.Close()
		app = httpapi.NewWithTenants(logger, registry)
	}
	logger.Info("starting backend", "address", cfg.Address())
	if err := app.Listen(cfg.Address(), fiber.ListenConfig{
		GracefulContext:       ctx,
		ShutdownTimeout:       cfg.ShutdownTimeout,
		DisableStartupMessage: true,
	}); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}
