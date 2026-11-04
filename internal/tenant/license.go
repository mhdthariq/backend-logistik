package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type licenseData struct {
	CorpID, Company, DueDate, Server, DB, Username, Password string
	Active                                                   bool
}
type cachedLicense struct {
	data    licenseData
	expires time.Time
}
type licenseClient struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	cache map[string]cachedLicense
}

func newLicenseClient(cfg Config) *licenseClient {
	return &licenseClient{cfg: cfg, http: &http.Client{Timeout: cfg.LicenseTimeout}, cache: make(map[string]cachedLicense)}
}

func (l *licenseClient) resolve(ctx context.Context, corpID string) (licenseData, error) {
	key := strings.ToUpper(corpID)
	l.mu.Lock()
	cached, exists := l.cache[key]
	l.mu.Unlock()
	if exists && time.Now().Before(cached.expires) {
		return cached.data, nil
	}
	data, err := l.fetch(ctx, corpID)
	if err != nil {
		// Preserve the legacy stale-on-upstream-error contract within a bounded cache.
		if exists {
			return cached.data, nil
		}
		return licenseData{}, err
	}
	if !data.Active {
		return licenseData{}, &Error{403, fmt.Sprintf("Corporate ID %q tidak aktif. Hubungi admin untuk mengaktifkan kembali.", data.CorpID), "INACTIVE"}
	}
	if data.DueDate != "" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02", "2006-01-02T15:04:05", "01/02/2006"} {
			if due, parseErr := time.Parse(layout, data.DueDate); parseErr == nil {
				if due.Before(time.Now()) {
					return licenseData{}, &Error{403, fmt.Sprintf("Lisensi Corporate ID %q telah berakhir pada %s.", data.CorpID, data.DueDate), "EXPIRED"}
				}
				break
			}
		}
	}
	l.mu.Lock()
	if len(l.cache) >= l.cfg.MaxPools {
		for k := range l.cache {
			delete(l.cache, k)
			break
		}
	}
	l.cache[key] = cachedLicense{data, time.Now().Add(time.Minute)}
	l.mu.Unlock()
	return data, nil
}

func (l *licenseClient) fetch(ctx context.Context, corpID string) (licenseData, error) {
	cfg := l.cfg
	if cfg.LicenseURL == "" || cfg.LicenseKey == "" || cfg.LicenseToken == "" || cfg.LicenseApplication == "" {
		return licenseData{}, &Error{503, "Konfigurasi server lisensi belum lengkap. Hubungi administrator.", "CONFIGURATION_ERROR"}
	}
	u, err := url.Parse(cfg.LicenseURL)
	if err != nil {
		return licenseData{}, &Error{503, "Konfigurasi server lisensi belum lengkap. Hubungi administrator.", "CONFIGURATION_ERROR"}
	}
	values := url.Values{"KdCustomer": {corpID}, "Aplikasi": {cfg.LicenseApplication}}
	q := u.Query()
	for k, v := range values {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return licenseData{}, &Error{503, "Konfigurasi server lisensi belum lengkap. Hubungi administrator.", "CONFIGURATION_ERROR"}
	}
	req.Header.Set("Key", cfg.LicenseKey)
	req.Header.Set("Token", cfg.LicenseToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := l.http.Do(req)
	if err != nil {
		return licenseData{}, &Error{503, "Tidak dapat menghubungi server lisensi. Periksa koneksi internet server dan coba lagi.", "NETWORK_ERROR"}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return licenseData{}, &Error{502, fmt.Sprintf("Server lisensi mengembalikan error (HTTP %d).", res.StatusCode), "UPSTREAM_ERROR"}
	}
	var payload struct {
		Status, Message string
		Data            map[string]any
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil {
		return licenseData{}, &Error{502, "Respons server lisensi tidak valid.", "UPSTREAM_ERROR"}
	}
	if payload.Status != "Ok" || payload.Data == nil {
		message := payload.Message
		if message == "" {
			message = "Corporate ID tidak ditemukan."
		}
		return licenseData{}, &Error{404, message, "NOT_FOUND"}
	}
	d := payload.Data
	corp := clean(d["KdCustomer"])
	if corp == "" {
		corp = corpID
	}
	company := clean(d["NamaCustomer"])
	if company == "" {
		company = corpID
	}
	return licenseData{CorpID: corp, Company: company, DueDate: clean(d["DueDate"]), Active: !strings.EqualFold(clean(d["NonAktif"]), "T"), Server: clean(d["Server"]), DB: clean(d["Db"]), Username: clean(d["Username"]), Password: clean(d["Password"])}, nil
}

func clean(v any) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "-" {
		return ""
	}
	return s
}

func (d licenseData) connectionURL() (string, error) {
	if d.Server == "" || d.DB == "" {
		return "", &Error{409, fmt.Sprintf("Database belum disiapkan untuk Corporate ID %q. Hubungi admin.", d.CorpID), "NO_DATABASE"}
	}
	host := d.Server
	if net.ParseIP(host) != nil {
		host = net.JoinHostPort(host, "5432")
	} else if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "5432")
	}
	username := d.Username
	if username == "" {
		username = "postgres"
	}
	u := url.URL{Scheme: "postgresql", Host: host, Path: "/" + d.DB, User: url.UserPassword(username, d.Password)}
	return u.String(), nil
}
