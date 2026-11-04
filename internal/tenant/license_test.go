package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func licenseConfig(server string) Config {
	return Config{LicenseURL: server, LicenseKey: "test-key", LicenseToken: "test-token", LicenseApplication: "test", LicenseTimeout: time.Second, MaxPools: 4}
}

func TestLicenseResponses(t *testing.T) {
	for _, tt := range []struct {
		name, body, code string
		status           int
	}{
		{"unknown", `{"Status":"Error","Message":"unknown"}`, "NOT_FOUND", 404},
		{"inactive", `{"Status":"Ok","Data":{"NonAktif":"T","KdCustomer":"A"}}`, "INACTIVE", 403},
		{"expired", `{"Status":"Ok","Data":{"DueDate":"2000-01-01","KdCustomer":"A"}}`, "EXPIRED", 403},
		{"invalid JSON", `{`, "UPSTREAM_ERROR", 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := fmt.Fprint(w, tt.body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			_, err := newLicenseClient(licenseConfig(server.URL)).resolve(context.Background(), "A")
			var public *Error
			if !errors.As(err, &public) || public.Status != tt.status || public.Code != tt.code {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLicenseCacheAndStaleError(t *testing.T) {
	var calls atomic.Int32
	var outage atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.Header.Get("Key") != "test-key" || r.Header.Get("Token") != "test-token" {
			t.Error("invalid upstream request")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("KdCustomer") == "" || r.URL.Query().Get("Aplikasi") != "test" {
			t.Error("missing upstream form/query")
		}
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		if _, err := fmt.Fprint(w, `{"Status":"Ok","Data":{"KdCustomer":"A","NamaCustomer":"Company A","Server":"localhost","Db":"tenant_a","Username":"name@db","Password":"p@ss:word"}}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := newLicenseClient(licenseConfig(server.URL))
	data, err := client.resolve(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.resolve(context.Background(), "a"); err != nil || calls.Load() != 1 {
		t.Fatalf("cache miss: %v / %d", err, calls.Load())
	}
	dsn, err := data.connectionURL()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Host != "localhost:5432" || u.User.Username() != "name@db" || password != "p@ss:word" {
		t.Fatalf("bad DSN encoding: %v", u.Host)
	}
	client.mu.Lock()
	cached := client.cache["A"]
	cached.expires = time.Now().Add(-time.Second)
	client.cache["A"] = cached
	client.mu.Unlock()
	outage.Store(true)
	if got, err := client.resolve(context.Background(), "A"); err != nil || got.Company != "Company A" {
		t.Fatalf("stale fallback failed: %v", err)
	}
	if _, err := client.resolve(context.Background(), "B"); err == nil {
		t.Fatal("must not give another tenant cached credentials")
	}
}

func TestLicenseTimeoutAndConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	cfg := licenseConfig(server.URL)
	cfg.LicenseTimeout = 10 * time.Millisecond
	_, err := newLicenseClient(cfg).resolve(context.Background(), "A")
	var public *Error
	if !errors.As(err, &public) || public.Code != "NETWORK_ERROR" {
		t.Fatalf("timeout not enforced: %v", err)
	}
	_, err = newLicenseClient(Config{}).resolve(context.Background(), "A")
	if !errors.As(err, &public) || public.Code != "CONFIGURATION_ERROR" {
		t.Fatalf("bad configuration error: %v", err)
	}
}

func TestRegistryLeasesAndCapacity(t *testing.T) {
	r := New(Config{DatabaseURL: "postgresql://test@127.0.0.1:1/default?schema=public", Mode: "development", MaxPools: 1})
	defer r.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			lease, err := r.Resolve(context.Background(), "ignored")
			if err != nil {
				t.Error(err)
				return
			}
			lease.Release()
			lease.Release()
		})
	}
	wg.Wait()
	if len(r.pools) != 1 {
		t.Fatal("requests did not reuse a pool")
	}
	lease, err := r.Resolve(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	r.cfg.DatabaseURL = "postgresql://test@127.0.0.1:1/other?schema=public"
	_, err = r.Resolve(context.Background(), "")
	var public *Error
	if !errors.As(err, &public) || public.Code != "TENANT_CAPACITY" {
		t.Fatalf("in-use pool evicted: %v", err)
	}
	lease.Release()
	other, err := r.Resolve(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	r.Close()
	if _, err := r.Resolve(context.Background(), ""); err == nil {
		t.Fatal("registry reopened after close")
	}
}
