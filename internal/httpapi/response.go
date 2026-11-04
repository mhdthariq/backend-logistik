// Package httpapi implements the Fiber HTTP boundary for the incremental migration.
package httpapi

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/tenant"
)

// Envelope matches kirimku's composition/api-helpers.ts success contract.
type Envelope struct {
	Data any            `json:"data"`
	Meta map[string]any `json:"meta,omitempty"`
}

// Error is a public HTTP error matching kirimku's fail() contract.
type Error struct {
	Status  int                 `json:"-"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors,omitempty"`
	Code    string              `json:"code,omitempty"`
}

// Error returns the public message and implements the error interface.
func (e *Error) Error() string { return e.Message }

func errorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		var tenantError *tenant.Error
		if errors.As(err, &tenantError) {
			return c.Status(tenantError.Status).JSON(&Error{Message: tenantError.Message, Code: tenantError.Code})
		}
		var public *Error
		if errors.As(err, &public) && public.Status >= 400 && public.Status < 500 {
			return c.Status(public.Status).JSON(public)
		}
		var framework *fiber.Error
		if errors.As(err, &framework) && framework.Code >= 400 && framework.Code < 500 {
			return c.Status(framework.Code).JSON(&Error{Message: framework.Message})
		}
		logger.Error("unhandled HTTP error", "method", c.Method(), "path", c.Path(), "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(&Error{Message: "Internal server error."})
	}
}
