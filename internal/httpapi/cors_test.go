package httpapi_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func TestCORS(t *testing.T) {
	for _, tt := range []struct {
		name, allowed, origin, method string
		wantOrigin                    bool
	}{
		{"login preflight", "http://localhost:3000,http://127.0.0.1:3000", "http://localhost:3000", "OPTIONS", true},
		{"second origin", "http://localhost:3000,http://127.0.0.1:3000", "http://127.0.0.1:3000", "OPTIONS", true},
		{"untrusted origin", "http://localhost:3000", "https://untrusted.example", "OPTIONS", false},
		{"disabled", "", "http://localhost:3000", "OPTIONS", false},
		{"error response", "http://localhost:3000", "http://localhost:3000", "POST", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CORS_ALLOW_ORIGINS", tt.allowed)
			registry := tenant.New(tenant.Config{})
			defer registry.Close()
			app := httpapi.NewWithTenants(nil, registry)
			req := httptest.NewRequest(tt.method, "/api/v1/auth/login", strings.NewReader(`{}`))
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Content-Type", "application/json")
			if tt.method == "OPTIONS" {
				req.Header.Set("Access-Control-Request-Method", "POST")
				req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-corp-id")
			}
			res, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := res.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			if got := res.Header.Get("Access-Control-Allow-Origin"); (got == tt.origin) != tt.wantOrigin || (!tt.wantOrigin && got != "") {
				t.Fatalf("unexpected allowed origin %q", got)
			}
			if tt.wantOrigin && tt.method == "OPTIONS" {
				if res.StatusCode != fiber.StatusNoContent {
					t.Fatalf("preflight status = %d", res.StatusCode)
				}
				for _, header := range []string{"authorization", "content-type", "x-corp-id"} {
					if !strings.Contains(strings.ToLower(res.Header.Get("Access-Control-Allow-Headers")), header) {
						t.Fatalf("missing preflight header %s", header)
					}
				}
				if !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "POST") {
					t.Fatal("login POST is not allowed")
				}
			}
			if res.Header.Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("cookie credentials must remain disabled")
			}
		})
	}
}
