// Package persistence provides explicit SQL adapters for the Prisma-owned schema.
package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queryer is the common request-scoped database and transaction interface.
type Queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Store wraps a leased tenant pool; it never changes schema or seeds data.
type Store struct{ Pool *pgxpool.Pool }

// Transaction commits only when the complete use case succeeds.
func (s *Store) Transaction(ctx context.Context, fn func(Queryer) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Rollback after Commit deliberately returns ErrTxClosed.
		_ = tx.Rollback(cleanup)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// Object decodes a single SQL JSON object. An absent row is returned as nil.
func Object(ctx context.Context, db Queryer, query string, args ...any) (map[string]any, error) {
	var raw []byte
	if err := db.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query object: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	normalizeDates(object)
	return object, nil
}

// Objects decodes SQL JSON rows, returning [] rather than null for an empty result.
func Objects(ctx context.Context, db Queryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query objects: %w", err)
	}
	defer rows.Close()
	objects := make([]map[string]any, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan object: %w", err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, fmt.Errorf("decode object: %w", err)
		}
		normalizeDates(object)
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read objects: %w", err)
	}
	return objects, nil
}

// Prisma DateTime values are UTC instants even though the schema uses timestamp
// without time zone. Match Date.toISOString() including millisecond precision.
func normalizeDates(object map[string]any) {
	for key, value := range object {
		switch v := value.(type) {
		case map[string]any:
			normalizeDates(v)
		case []any:
			for _, item := range v {
				if row, ok := item.(map[string]any); ok {
					normalizeDates(row)
				}
			}
		case string:
			if strings.HasSuffix(key, "At") || key == "effectiveFrom" || key == "effectiveTo" || key == "validFrom" || key == "validTo" {
				for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
					if parsed, err := time.Parse(layout, v); err == nil {
						object[key] = parsed.UTC().Format("2006-01-02T15:04:05.000Z")
						break
					}
				}
			}
		}
	}
}
