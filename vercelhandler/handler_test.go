package vercelhandler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestHandlerForwardsCorporateHeader(t *testing.T) {
	var lookups int
	license := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups++
		if r.Method != http.MethodPost || r.Header.Get("Key") != "key" || r.Header.Get("Token") != "token" || r.URL.Query().Get("KdCustomer") != "RJL2507" || r.URL.Query().Get("Aplikasi") != "test" {
			t.Error("license request lost corporate ID or configured headers")
		}
		_, _ = fmt.Fprint(w, `{"Status":"Ok","Data":{"KdCustomer":"RJL2507","NonAktif":"F","Server":"127.0.0.1:1","Db":"test"}}`)
	}))
	defer license.Close()
	t.Setenv("NODE_ENV", "production")
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("LICENSE_API_URL", license.URL)
	t.Setenv("LICENSE_API_KEY", "key")
	t.Setenv("LICENSE_API_TOKEN", "token")
	t.Setenv("LICENSE_APLIKASI", "test")
	application = sync.OnceValues(newHandler)
	t.Cleanup(func() { application = sync.OnceValues(newHandler) })
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.RequestURI = ""
		req.Header.Set("x-corp-id", "RJL2507")
		res := httptest.NewRecorder()
		Handler(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
	}
	if lookups != 1 {
		t.Fatalf("warm invocation did not reuse license cache: lookups=%d", lookups)
	}
}

func TestHandler(t *testing.T) {
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("LICENSE_API_URL", "")
	application = sync.OnceValues(newHandler)
	t.Cleanup(func() { application = sync.OnceValues(newHandler) })
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/api?probe=1", http.StatusOK},
		{"/health/live", http.StatusOK},
		{"/does-not-exist", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		req.RequestURI = "" // Vercel supplies a net/http server request.
		res := httptest.NewRecorder()
		Handler(res, req)
		if res.Code != test.status || !json.Valid(res.Body.Bytes()) {
			t.Fatalf("%s: status=%d body=%s", test.path, res.Code, res.Body.String())
		}
	}
	if first, _ := application(); first == nil {
		t.Fatal("application was not cached")
	}
}

func TestHandlerInvalidConfiguration(t *testing.T) {
	t.Setenv("PORT", "invalid")
	application = sync.OnceValues(newHandler)
	t.Cleanup(func() { application = sync.OnceValues(newHandler) })
	res := httptest.NewRecorder()
	Handler(res, httptest.NewRequest(http.MethodGet, "/api", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", res.Code)
	}
}
