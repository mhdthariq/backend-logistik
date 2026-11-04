package httpapi

import (
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
)

// New constructs an independent app without opening a port or touching a database.
// Only Phase 1 routes are registered; business endpoints remain on Next.js.
func New(logger *slog.Logger) *fiber.App {
	if logger == nil {
		logger = slog.Default()
	}
	app := fiber.New(fiber.Config{
		AppName:      "NovaLogistik backend",
		ErrorHandler: errorHandler(logger),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		BodyLimit:    16 * 1024 * 1024,
	})
	app.Use(recover.New())
	app.Get("/api", func(c fiber.Ctx) error {
		// This legacy route is intentionally not wrapped in the v1 envelope.
		return c.JSON(fiber.Map{"message": "Hello, world!"})
	})
	app.Get("/health/live", func(c fiber.Ctx) error {
		// Process liveness only; this makes no database readiness claim.
		return c.JSON(Envelope{Data: fiber.Map{"status": "ok"}})
	})
	return app
}
