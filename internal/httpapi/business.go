package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"novalogistik/backend/internal/persistence"
	"novalogistik/backend/internal/tenant"
)

type business struct {
	tenants          *tenant.Registry
	logger           *slog.Logger
	trackingAttempts trackingLimiter
}
type request struct {
	ctx     context.Context
	db      persistence.Queryer
	store   *persistence.Store
	company string
	actor   map[string]any
	body    map[string]any
	c       fiber.Ctx
	audits  []func()
}
type action func(*request) (any, error)

// NewWithTenants registers migrated business routes against request-scoped tenant pools.
// The caller owns the registry and must close it after the server drains.
func NewWithTenants(logger *slog.Logger, tenants *tenant.Registry) *fiber.App {
	app := New(logger)
	if logger == nil {
		logger = slog.Default()
	}
	b := &business{tenants: tenants, logger: logger}
	b.registerIdentity(app)
	b.registerMaster(app)
	b.registerShipments(app)
	b.registerPickups(app)
	b.registerDeliveries(app)
	b.registerFleetCapacity(app)
	b.registerTransports(app)
	b.registerReturnTasks(app)
	b.registerPayments(app)
	b.registerPartners(app)
	b.registerWallet(app)
	b.registerInvoices(app)
	b.registerRepairs(app)
	b.registerFinance(app)
	b.registerPartnerViews(app)
	app.Get("/api/v1/gudang", b.handle("shipment.view", true, false, b.gudang))
	b.registerPublicTracking(app)
	return app
}

func (b *business) handle(permission string, authenticated, transaction bool, fn action) fiber.Handler {
	return func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
		defer cancel()
		lease, err := b.tenants.Resolve(ctx, strings.Clone(c.Get("x-corp-id")))
		if err != nil {
			return err
		}
		defer lease.Release()
		r := &request{ctx: ctx, db: lease.Store.Pool, store: lease.Store, company: lease.CompanyName, c: c, body: map[string]any{}}
		// Match req.json().catch(() => ({})) on malformed input.
		if len(c.Body()) > 0 {
			if err := json.Unmarshal(c.Body(), &r.body); err != nil || r.body == nil {
				r.body = map[string]any{}
			}
		}
		if authenticated {
			r.actor, err = authUser(ctx, r.db, bearer(c), false)
			if err != nil {
				return err
			}
			if r.actor == nil {
				return problem(401, "Unauthenticated.")
			}
			if permission != "" && !hasPermission(r.actor, permission) {
				return problem(403, "Missing permission: "+permission)
			}
		}
		var data any
		if transaction {
			err = r.store.Transaction(ctx, func(tx persistence.Queryer) error {
				r.db = tx
				var callErr error
				data, callErr = fn(r)
				return callErr
			})
		} else {
			data, err = fn(r)
		}
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01") {
				if permission == "payment.verify" && strings.HasSuffix(c.Path(), "/verify") {
					return problem(409, "Pembayaran berubah bersamaan. Muat ulang lalu coba lagi.")
				}
				return problem(409, "Data berubah bersamaan. Silakan coba lagi.")
			}
			return err
		}
		for _, audit := range r.audits {
			audit()
		}
		if envelope, ok := data.(Envelope); ok {
			return c.JSON(envelope)
		}
		return c.JSON(Envelope{Data: data})
	}
}

func problem(status int, message string) *Error { return &Error{Status: status, Message: message} }
func fieldProblem(message, field, detail string) *Error {
	return &Error{Status: 422, Message: message, Errors: map[string][]string{field: {detail}}}
}
func textValue(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
func nullable(v any) any {
	s := textValue(v)
	if s == "" {
		return nil
	}
	return s
}
func required(body map[string]any, field string) (string, error) {
	s := textValue(body[field])
	if s == "" {
		message := "The " + field + " field is required."
		return "", fieldProblem(message, field, message)
	}
	return s, nil
}
func idValue(v any) int {
	n, err := strconv.ParseFloat(textValue(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 1 || n > 2147483647 {
		return 0
	}
	return int(n)
}
func boolValue(v any, fallback bool) bool {
	switch v {
	case true, "true", float64(1), "1":
		return true
	case false, "false", float64(0), "0":
		return false
	}
	return fallback
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	default:
		return true
	}
}
func ids(v any) ([]int, bool) {
	list, ok := v.([]any)
	out := make([]int, 0)
	for _, v := range list {
		n := idValue(v)
		if n > 0 {
			out = append(out, n)
		}
	}
	return out, ok
}
func bearer(c fiber.Ctx) string {
	h := c.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
func hasPermission(user map[string]any, permission string) bool {
	if truthy(user["isOwner"]) {
		return true
	}
	for _, v := range user["permissions"].([]any) {
		if v == "*" || v == permission {
			return true
		}
	}
	return false
}
func (r *request) id() (int, error) {
	n, err := strconv.Atoi(r.c.Params("id"))
	if err != nil || n <= 0 {
		return 0, problem(422, "Invalid request parameters.")
	}
	return n, nil
}
func (r *request) one(query string, args ...any) (map[string]any, error) {
	return persistence.Object(r.ctx, r.db, query, args...)
}
func (r *request) many(query string, args ...any) ([]map[string]any, error) {
	return persistence.Objects(r.ctx, r.db, query, args...)
}
func (r *request) exec(query string, args ...any) error {
	_, err := r.db.Exec(r.ctx, query, args...)
	return err
}

// updateRow accepts only application-built column maps, never raw client keys.
func (r *request) updateRow(table string, id int, data map[string]any) (map[string]any, error) {
	args := []any{id}
	set := make([]string, 0, len(data))
	for k, v := range data {
		args = append(args, v)
		set = append(set, pgx.Identifier{k}.Sanitize()+fmt.Sprintf("=$%d", len(args)))
	}
	if len(set) == 0 {
		return r.one(`SELECT to_jsonb(t) FROM `+pgx.Identifier{table}.Sanitize()+` t WHERE id=$1`, id)
	}
	if table != "Role" {
		set = append(set, `"updatedAt"=CURRENT_TIMESTAMP`)
	}
	return r.one(`UPDATE `+pgx.Identifier{table}.Sanitize()+` t SET `+strings.Join(set, ",")+` WHERE id=$1 RETURNING to_jsonb(t)`, args...)
}

// insertRow receives application-defined columns and parameterizes every value.
func (r *request) insertRow(table string, data map[string]any) (map[string]any, error) {
	columns, placeholders := []string{}, []string{}
	args := []any{}
	for key, value := range data {
		columns = append(columns, pgx.Identifier{key}.Sanitize())
		args = append(args, value)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	columns = append(columns, `"updatedAt"`)
	placeholders = append(placeholders, `CURRENT_TIMESTAMP`)
	return r.one(`INSERT INTO `+pgx.Identifier{table}.Sanitize()+` AS t (`+strings.Join(columns, ",")+`) VALUES (`+strings.Join(placeholders, ",")+`) RETURNING to_jsonb(t)`, args...)
}

func (b *business) audit(r *request, action, entity string, id any, label string, before, after any) {
	// Audit is intentionally best-effort as in the legacy service. A separate pool
	// query avoids poisoning a successful business transaction on audit failure.
	var actor any
	if r.actor != nil {
		actor = r.actor["id"]
	}
	encode := func(v any) any {
		if v == nil {
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return string(raw)
	}
	beforeData, afterData := encode(before), encode(after)
	r.audits = append(r.audits, func() {
		_, err := r.store.Pool.Exec(r.ctx, `INSERT INTO "AuditLog" (action,"entityType","entityId","entityLabel","actorId","beforeData","afterData") VALUES ($1,$2,$3,$4,$5,$6,$7)`, action, entity, id, label, actor, beforeData, afterData)
		if err != nil {
			b.logger.Error("audit write failed", "entity", entity, "action", action)
		}
	})
}
