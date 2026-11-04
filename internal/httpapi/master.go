package httpapi

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
)

func (b *business) registerMaster(app *fiber.App) {
	app.Get("/api/v1/tariffs", b.handle("tariff.view", true, true, b.tariffs))
	app.Post("/api/v1/tariffs", b.handle("tariff.create", true, true, b.createTariff))
	app.Put("/api/v1/tariffs/:id", b.handle("tariff.update", true, true, b.updateTariff))
	app.Delete("/api/v1/tariffs/:id", b.handle("tariff.update", true, true, b.deleteTariff))
	app.Get("/api/v1/options", b.handle("", true, true, b.options))
	app.Get("/api/v1/customers", b.handle("customer.view", true, false, b.customers))
	app.Post("/api/v1/customers", b.handle("customer.create", true, true, b.createCustomer))
	app.Get("/api/v1/customers/:id", b.handle("customer.view", true, false, b.customer))
	app.Put("/api/v1/customers/:id", b.handle("customer.update", true, true, b.updateCustomer))
	app.Delete("/api/v1/customers/:id", b.handle("customer.delete", true, true, b.deleteCustomer))
	app.Get("/api/v1/vehicles", b.handle("vehicle.view", true, false, b.vehicles))
	app.Post("/api/v1/vehicles", b.handle("vehicle.create", true, true, b.createVehicle))
	app.Put("/api/v1/vehicles/:id", b.handle("vehicle.update", true, true, b.updateVehicle))
	app.Delete("/api/v1/vehicles/:id", b.handle("vehicle.update", true, true, b.deleteVehicle))
	app.Get("/api/v1/settings", b.handle("", true, false, b.settings))
	app.Put("/api/v1/settings", b.handle("", true, true, b.updateSettings))
	app.Get("/api/v1/warehouses", b.handle("warehouse.view", true, false, b.warehouses))
	app.Post("/api/v1/warehouses", b.handle("warehouse.create", true, true, b.createWarehouse))
	app.Put("/api/v1/warehouses/:id", b.handle("warehouse.update", true, true, b.updateWarehouse))
	app.Delete("/api/v1/warehouses/:id", b.handle("warehouse.delete", true, true, b.deleteWarehouse))
	app.Get("/api/v1/bank-accounts", b.handle("", true, false, b.bankAccounts))
	app.Post("/api/v1/bank-accounts", b.handle("bank_account.create", true, true, b.createBankAccount))
	app.Put("/api/v1/bank-accounts/:id", b.handle("bank_account.update", true, true, b.updateBankAccount))
	app.Delete("/api/v1/bank-accounts/:id", b.handle("bank_account.delete", true, true, b.disableBankAccount))
	app.Get("/api/v1/routes", b.handle("checkpoint.view", true, false, b.routes))
	app.Post("/api/v1/routes", b.handle("checkpoint.create", true, true, b.createRoute))
	app.Get("/api/v1/routes/:id", b.handle("checkpoint.view", true, false, b.route))
	app.Put("/api/v1/routes/:id", b.handle("checkpoint.update", true, true, b.updateRoute))
	app.Delete("/api/v1/routes/:id", b.handle("checkpoint.delete", true, true, b.deleteRoute))
	app.Post("/api/v1/routes/:id/checkpoints", b.handle("checkpoint.create", true, true, b.appendCheckpoint))
	app.Put("/api/v1/routes/:id/checkpoints", b.handle("checkpoint.update", true, true, b.saveCheckpoints))
	app.Put("/api/v1/checkpoints/:id", b.handle("checkpoint.update", true, true, b.updateCheckpoint))
	app.Delete("/api/v1/checkpoints/:id", b.handle("checkpoint.delete", true, true, b.deleteCheckpoint))
}

func numeric(v any) any {
	if v == nil || v == "" {
		return nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
	if b, ok := v.(bool); ok {
		if b {
			return float64(1)
		}
		return float64(0)
	}
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return n
}

func (r *request) find(table, message string, lock bool) (map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	query := `SELECT to_jsonb(t) FROM ` + pgx.Identifier{table}.Sanitize() + ` t WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	row, err := r.one(query, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, problem(404, message)
	}
	return row, nil
}

func (r *request) nextCode(table, column, prefix string, lockID int) (string, error) {
	if err := r.exec(`SELECT pg_advisory_xact_lock($1)`, lockID); err != nil {
		return "", err
	}
	rows, err := r.many(`SELECT jsonb_build_object('code',` + pgx.Identifier{column}.Sanitize() + `) FROM ` + pgx.Identifier{table}.Sanitize())
	if err != nil {
		return "", err
	}
	used := map[int]bool{}
	pattern := regexp.MustCompile(`(\d+)\s*$`)
	for _, row := range rows {
		code := textValue(row["code"])
		if strings.HasPrefix(code, prefix) {
			match := pattern.FindStringSubmatch(code)
			if len(match) > 1 {
				n, _ := strconv.Atoi(match[1])
				used[n] = true
			}
		}
	}
	n := 1
	for used[n] {
		n++
	}
	return fmt.Sprintf("%s%06d", prefix, n), nil
}

func (b *business) warehouses(r *request) (any, error) {
	return r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE ($1 OR "isActive") AND ($2='' OR name ILIKE $3 ESCAPE '\' OR code ILIKE $3 ESCAPE '\' OR city ILIKE $3 ESCAPE '\' OR address ILIKE $3 ESCAPE '\') ORDER BY code`, boolValue(r.c.Query("include_inactive"), true), textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
}

func (b *business) createWarehouse(r *request) (any, error) {
	name, err := required(r.body, "name")
	if err != nil {
		return nil, err
	}
	code, err := r.nextCode("Warehouse", "code", "WH-", 731022)
	if err != nil {
		return nil, err
	}
	w, err := r.one(`INSERT INTO "Warehouse" AS w (code,name,city,address,latitude,longitude,notes,"customerSupportContact","isActive","updatedAt") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true,NOW()) RETURNING to_jsonb(w)`, code, name, nullable(r.body["city"]), nullable(r.body["address"]), numeric(r.body["latitude"]), numeric(r.body["longitude"]), nullable(r.body["notes"]), nullable(r.body["customerSupportContact"]))
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "warehouse", w["id"], name, nil, w)
	return w, nil
}

func (b *business) updateWarehouse(r *request) (any, error) {
	existing, err := r.find("Warehouse", "Gudang tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	data := patchFields(r.body, existing, []string{"name", "city", "address", "notes", "customerSupportContact"}, []string{"latitude", "longitude"}, true)
	w, err := r.updateRow("Warehouse", idValue(existing["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "warehouse", w["id"], textValue(w["name"]), diff(existing, w), nil)
	return w, nil
}

func patchFields(body, existing map[string]any, textKeys, numberKeys []string, active bool) map[string]any {
	data := map[string]any{}
	for _, key := range textKeys {
		if v, ok := body[key]; ok {
			data[key] = nullable(v)
			if key == "name" && data[key] == nil {
				data[key] = existing[key]
			}
		}
	}
	for _, key := range numberKeys {
		if v, ok := body[key]; ok {
			data[key] = numeric(v)
		}
	}
	if active {
		if v, ok := body["isActive"]; ok {
			data["isActive"] = truthy(v)
		}
	}
	return data
}

func (b *business) deleteWarehouse(r *request) (any, error) {
	w, err := r.find("Warehouse", "Gudang tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	var count int
	if err := r.db.QueryRow(r.ctx, `SELECT (SELECT count(*) FROM "MasterShipment" WHERE "originWarehouseId"=$1)+(SELECT count(*) FROM "MasterShipment" WHERE "destinationWarehouseId"=$1)`, w["id"]).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		updated, err := r.updateRow("Warehouse", idValue(w["id"]), map[string]any{"isActive": false})
		if err != nil {
			return nil, err
		}
		b.audit(r, "deactivated", "warehouse", w["id"], textValue(w["name"]), nil, nil)
		return fiber.Map{"deactivated": true, "warehouse": updated}, nil
	}
	if err := r.exec(`DELETE FROM "Warehouse" WHERE id=$1`, w["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "warehouse", w["id"], textValue(w["name"]), w, nil)
	return fiber.Map{"deleted": true}, nil
}

func (b *business) bankAccounts(r *request) (any, error) {
	canView := hasPermission(r.actor, "bank_account.view")
	if !canView && !hasPermission(r.actor, "invoice.create") && !hasPermission(r.actor, "invoice.update") {
		return nil, problem(403, "Anda tidak memiliki izin melihat rekening.")
	}
	return r.many(`SELECT to_jsonb(b) FROM "BankAccount" b WHERE ($1 OR "isActive") ORDER BY "isActive" DESC,"bankName",id`, canView && r.c.Query("active") != "true")
}

func parseBankAccount(body map[string]any) (map[string]any, error) {
	bankName, _ := body["bankName"].(string)
	bankName = strings.TrimSpace(bankName)
	accountNumber, _ := body["accountNumber"].(string)
	accountNumber = regexp.MustCompile(`[\s-]`).ReplaceAllString(accountNumber, "")
	accountName, _ := body["accountName"].(string)
	accountName = strings.TrimSpace(accountName)
	if bankName == "" || jsLength(bankName) > 100 {
		return nil, problem(422, "Nama bank wajib diisi, maksimal 100 karakter.")
	}
	if !regexp.MustCompile(`^\d{4,40}$`).MatchString(accountNumber) {
		return nil, problem(422, "No. rekening harus 4–40 digit angka.")
	}
	if accountName == "" || jsLength(accountName) > 150 {
		return nil, problem(422, "Nama rekening wajib diisi, maksimal 150 karakter.")
	}
	active := true
	if v, ok := body["isActive"]; ok {
		var valid bool
		active, valid = v.(bool)
		if !valid {
			return nil, problem(422, "Status aktif rekening tidak valid.")
		}
	}
	return map[string]any{"bankName": bankName, "accountNumber": accountNumber, "accountName": accountName, "isActive": active}, nil
}

func (b *business) createBankAccount(r *request) (any, error) {
	input, err := parseBankAccount(r.body)
	if err != nil {
		return nil, err
	}
	account, err := r.one(`INSERT INTO "BankAccount" AS b ("bankName","accountNumber","accountName","isActive","updatedAt") VALUES ($1,$2,$3,$4,NOW()) RETURNING to_jsonb(b)`, input["bankName"], input["accountNumber"], input["accountName"], input["isActive"])
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "bank_account", account["id"], textValue(account["bankName"]), nil, input)
	return account, nil
}

func (b *business) updateBankAccount(r *request) (any, error) {
	before, err := r.find("BankAccount", "Rekening tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	input, err := parseBankAccount(r.body)
	if err != nil {
		return nil, err
	}
	account, err := r.updateRow("BankAccount", idValue(before["id"]), input)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "bank_account", account["id"], textValue(account["bankName"]), before, input)
	return account, nil
}

func (b *business) disableBankAccount(r *request) (any, error) {
	before, err := r.find("BankAccount", "Rekening tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	account, err := r.updateRow("BankAccount", idValue(before["id"]), map[string]any{"isActive": false})
	if err != nil {
		return nil, err
	}
	b.audit(r, "deactivated", "bank_account", account["id"], textValue(account["bankName"]), before, fiber.Map{"isActive": false})
	return account, nil
}
