package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"novalogistik/backend/internal/persistence"
)

type transportListDB struct {
	persistence.Queryer
	count, calls int
}

func (db *transportListDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	db.calls++
	n := 0
	if strings.Contains(sql, `FROM "Transport" t`) {
		n = db.count
	}
	return &transportListRows{remaining: n}, nil
}

func (db *transportListDB) QueryRow(context.Context, string, ...any) pgx.Row {
	db.calls++
	return &transportListRows{}
}

type transportListRows struct {
	pgx.Rows
	remaining int
}

func (rows *transportListRows) Next() bool {
	rows.remaining--
	return rows.remaining >= 0
}
func (rows *transportListRows) Scan(dest ...any) error {
	*dest[0].(*[]byte) = []byte(`{"id":1,"route":{},"vehicle":{},"shipments":[]}`)
	return nil
}
func (rows *transportListRows) Close()     {}
func (rows *transportListRows) Err() error { return nil }

func TestTransportListQueryCountDoesNotGrowWithRows(t *testing.T) {
	for _, count := range []int{0, 1, 40} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := &transportListDB{count: count}
			app := New(nil)
			app.Get("/transports", func(c fiber.Ctx) error {
				data, err := (&business{}).transports(&request{ctx: t.Context(), db: db, c: c, actor: map[string]any{"isOwner": true}})
				if err != nil {
					return err
				}
				if len(data.([]map[string]any)) != count {
					t.Errorf("list dropped rows: got %d, want %d", len(data.([]map[string]any)), count)
				}
				return c.SendStatus(200)
			})
			resp, err := app.Test(httptest.NewRequest("GET", "/transports", nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || db.calls > 3 {
				t.Fatalf("%d transports: status %d, %d database calls; want at most 3", count, resp.StatusCode, db.calls)
			}
		})
	}
}
