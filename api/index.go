// Package handler exposes the Fiber application as a Vercel Function.
package handler

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"novalogistik/backend/internal/config"
	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

// Keep the application and tenant pools alive across warm invocations.
var application = sync.OnceValues(newHandler)

func newHandler() (http.Handler, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	app := httpapi.New(slog.Default())
	if cfg.BusinessEnabled {
		app = httpapi.NewWithTenants(slog.Default(), tenant.New(cfg.Tenant))
	}
	return adaptor.FiberApp(app), nil
}

// Handler is the entry point used by Vercel's Go runtime.
func Handler(w http.ResponseWriter, r *http.Request) {
	handler, err := application()
	if err != nil {
		slog.Error("initialize Vercel backend", "error", err)
		http.Error(w, "Backend configuration error", http.StatusServiceUnavailable)
		return
	}
	r.RequestURI = r.URL.RequestURI()
	handler.ServeHTTP(w, r)
}
