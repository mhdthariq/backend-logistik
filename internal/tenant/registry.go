// Package tenant resolves corporate licenses and leases isolated PostgreSQL pools.
package tenant

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"novalogistik/backend/internal/persistence"
)

// Error is a safe, public license or tenant-resolution failure.
type Error struct {
	Status        int
	Message, Code string
}

func (e *Error) Error() string { return e.Message }

// Config controls tenant lookup, pool bounds and the legacy default-tenant fallback.
type Config struct {
	DatabaseURL, CompanyName, Mode                           string
	LicenseURL, LicenseKey, LicenseToken, LicenseApplication string
	MaxPools                                                 int
	MaxConnections                                           int32
	LicenseTimeout                                           time.Duration
}

type entry struct {
	pool *pgxpool.Pool
	refs int
	used time.Time
}

// Registry holds a bounded set of pools. In-use pools are never evicted.
type Registry struct {
	cfg      Config
	mu       sync.Mutex
	pools    map[string]*entry
	closed   bool
	licenses *licenseClient
}

// Lease is a request's tenant connection; Release must be called exactly once.
type Lease struct {
	Store       *persistence.Store
	CompanyName string
	Release     func()
}

// New creates a lazy registry without connecting to a database.
func New(cfg Config) *Registry {
	if cfg.MaxPools <= 0 {
		cfg.MaxPools = 32
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 4
	}
	if cfg.LicenseTimeout <= 0 {
		cfg.LicenseTimeout = 5 * time.Second
	}
	if cfg.CompanyName == "" {
		cfg.CompanyName = "NovaLogistik"
	}
	return &Registry{cfg: cfg, pools: make(map[string]*entry), licenses: newLicenseClient(cfg)}
}

// Resolve preserves development bypass and missing-header default DB behavior.
func (r *Registry) Resolve(ctx context.Context, corpID string) (*Lease, error) {
	dsn, company := r.cfg.DatabaseURL, r.cfg.CompanyName
	if r.cfg.Mode != "development" && strings.TrimSpace(corpID) != "" {
		data, err := r.licenses.resolve(ctx, strings.TrimSpace(corpID))
		if err != nil {
			return nil, err
		}
		dsn, err = data.connectionURL()
		if err != nil {
			return nil, err
		}
		company = data.Company
	}
	if dsn == "" {
		return nil, &Error{503, "Database belum dikonfigurasi.", "DATABASE_NOT_CONFIGURED"}
	}
	// Prisma's schema URL option is not a PostgreSQL server setting.
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid database connection URL")
	}
	q := u.Query()
	schema := q.Get("schema")
	q.Del("schema")
	u.RawQuery = q.Encode()
	dsn = u.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, &Error{503, "Backend sedang berhenti.", "SHUTTING_DOWN"}
	}
	// Include schema in the key so tenant schemas cannot share a configured pool.
	key := dsn + "|" + schema
	e := r.pools[key]
	if e == nil {
		if len(r.pools) >= r.cfg.MaxPools {
			var oldest string
			for k, candidate := range r.pools {
				if candidate.refs == 0 && (oldest == "" || candidate.used.Before(r.pools[oldest].used)) {
					oldest = k
				}
			}
			if oldest == "" {
				return nil, &Error{503, "Kapasitas tenant sedang penuh. Coba lagi.", "TENANT_CAPACITY"}
			}
			r.pools[oldest].pool.Close()
			delete(r.pools, oldest)
		}
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			return nil, fmt.Errorf("invalid tenant database configuration")
		}
		cfg.MaxConns = r.cfg.MaxConnections
		cfg.ConnConfig.ConnectTimeout = 5 * time.Second
		cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
		if schema != "" {
			cfg.ConnConfig.RuntimeParams["search_path"] = `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("create tenant pool")
		}
		e = &entry{pool: pool}
		r.pools[key] = e
	}
	e.refs++
	e.used = time.Now()
	var once sync.Once
	return &Lease{Store: &persistence.Store{Pool: e.pool}, CompanyName: company, Release: func() { once.Do(func() { r.mu.Lock(); defer r.mu.Unlock(); e.refs--; e.used = time.Now() }) }}, nil
}

// Close releases all pools after the HTTP server has drained active requests.
func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	pools := r.pools
	r.pools = make(map[string]*entry)
	r.mu.Unlock()
	for _, e := range pools {
		e.pool.Close()
	}
}
