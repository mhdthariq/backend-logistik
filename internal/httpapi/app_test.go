package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/httpapi"
)

func TestRoutes(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"legacy API contract", "GET", "/api", `{"message":"Hello, world!"}`, 200},
		{"liveness", "GET", "/health/live", `{"data":{"status":"ok"}}`, 200},
		{"unknown route", "GET", "/missing", `{"message":"Not Found"}`, 404},
		{"unmigrated business route", "GET", "/api/v1/shipments", `{"message":"Not Found"}`, 404},
		{"unmigrated login", "POST", "/api/v1/auth/login", `{"message":"Not Found"}`, 404},
		{"unsupported method", "POST", "/api", `{"message":"Method Not Allowed"}`, 405},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := httpapi.New(nil)
			assertResponse(t, app, tt.method, tt.path, tt.status, tt.body)
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler fiber.Handler
		status  int
		body    string
		logged  bool
	}{
		{"validation fields", func(c fiber.Ctx) error {
			return &httpapi.Error{Status: 422, Message: "The username field is required.", Errors: map[string][]string{"username": {"The username field is required."}}}
		}, 422, `{"message":"The username field is required.","errors":{"username":["The username field is required."]}}`, false},
		{"license code", func(c fiber.Ctx) error {
			return fmt.Errorf("license lookup: %w", &httpapi.Error{Status: 403, Message: "License expired.", Code: "EXPIRED"})
		}, 403, `{"message":"License expired.","code":"EXPIRED"}`, false},
		{"unauthenticated", func(c fiber.Ctx) error {
			return &httpapi.Error{Status: 401, Message: "Unauthenticated."}
		}, 401, `{"message":"Unauthenticated."}`, false},
		{"framework rejection", func(c fiber.Ctx) error { return fiber.ErrBadRequest }, 400, `{"message":"Bad Request"}`, false},
		{"internal error redacted", func(c fiber.Ctx) error { return errors.New("secret database credentials") }, 500, `{"message":"Internal server error."}`, true},
		{"framework server error redacted", func(c fiber.Ctx) error { return fiber.NewError(503, "secret database credentials") }, 500, `{"message":"Internal server error."}`, true},
		{"public server error redacted", func(c fiber.Ctx) error { return &httpapi.Error{Status: 500, Message: "secret database credentials"} }, 500, `{"message":"Internal server error."}`, true},
		{"panic recovered", func(c fiber.Ctx) error { panic("secret database credentials") }, 500, `{"message":"Internal server error."}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			app := httpapi.New(slog.New(slog.NewJSONHandler(&logs, nil)))
			app.Get("/test-error", tt.handler)
			assertResponse(t, app, "GET", "/test-error", tt.status, tt.body)
			if tt.logged != (logs.Len() > 0) {
				t.Fatalf("unexpected error logging: %s", logs.String())
			}
			// A panic must not prevent the app from serving subsequent requests.
			assertResponse(t, app, "GET", "/health/live", 200, `{"data":{"status":"ok"}}`)
		})
	}
}

func TestSuccessEnvelope(t *testing.T) {
	app := httpapi.New(nil)
	app.Get("/test-meta", func(c fiber.Ctx) error {
		return c.JSON(httpapi.Envelope{Data: []int{}, Meta: map[string]any{"total": 0}})
	})
	app.Get("/test-null", func(c fiber.Ctx) error { return c.JSON(httpapi.Envelope{}) })
	assertResponse(t, app, "GET", "/test-meta", 200, `{"data":[],"meta":{"total":0}}`)
	assertResponse(t, app, "GET", "/test-null", 200, `{"data":null}`)
}

func assertResponse(t *testing.T, app *fiber.App, method, path string, status int, body string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != status {
		t.Fatalf("status %d, want %d; body %s", res.StatusCode, status, data)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatal("expected JSON content type")
	}
	var got, want any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body %s, want %s", data, body)
	}
}
