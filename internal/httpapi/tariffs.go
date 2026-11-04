package httpapi

import (
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgconn"
)

func expireTariffs(r *request) error {
	return r.exec(`UPDATE "Tariff" SET "isActive"=false,"updatedAt"=NOW() WHERE "isActive" AND "effectiveTo"<CURRENT_TIMESTAMP`)
}
func dateValue(raw any, end bool) any {
	if raw == nil || raw == "" {
		return nil
	}
	s := textValue(raw)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02", "01/02/2006"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			if end && len(s) == 10 && s[4] == '-' {
				parsed = parsed.Add(24*time.Hour - time.Millisecond)
			}
			return parsed
		}
	}
	return nil
}
func numericDefault(raw, fallback any) any {
	if value := numeric(raw); value != nil {
		return value
	}
	return fallback
}
func tariffConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return problem(422, "Tarif aktif dengan nama, koridor, dan customer ini sudah ada.")
	}
	return err
}
func (b *business) tariffs(r *request) (any, error) {
	if err := expireTariffs(r); err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(t)||jsonb_build_object('customer',(SELECT jsonb_build_object('id',c.id,'code',c.code,'name',c.name,'companyName',c."companyName") FROM "Customer" c WHERE c.id=t."customerId"),'_count',jsonb_build_object('shipments',(SELECT count(*) FROM "MasterShipment" s WHERE s."tariffId"=t.id))) FROM "Tariff" t WHERE ($1 OR "isActive") AND ($2='' OR origin ILIKE $3 ESCAPE '\' OR destination ILIKE $3 ESCAPE '\' OR name ILIKE $3 ESCAPE '\') ORDER BY origin,destination,"customerType"`, !boolValue(r.c.Query("active_only"), false), textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
}
func b2bCustomer(r *request, raw any, required bool) (any, error) {
	if numeric(raw) == nil && required {
		return nil, fieldProblem("Tarif B2B wajib terikat ke satu customer B2B.", "customerId", "Pilih customer B2B.")
	}
	c, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1 AND type='b2b'`, idValue(raw))
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fieldProblem("Customer tidak ditemukan / bukan B2B.", "customerId", "Customer harus bertipe B2B.")
	}
	return c["id"], nil
}
func validateTariffRates(method any, koli, cubic any) error {
	if method == "PER_KOLI" && (numeric(koli) == nil || numeric(koli).(float64) <= 0) {
		return fieldProblem("Tarif per koli wajib diisi untuk metode harga /koli.", "ratePerKoli", "Tarif per koli wajib diisi (> 0).")
	}
	if method == "PER_CUBIC" && (numeric(cubic) == nil || numeric(cubic).(float64) <= 0) {
		return fieldProblem("Tarif per m³ wajib diisi untuk metode harga /cubic.", "ratePerCubic", "Tarif per m³ wajib diisi (> 0).")
	}
	return nil
}
func duplicateTariff(r *request, data map[string]any, id int) (bool, error) {
	duplicate, err := r.one(`SELECT to_jsonb(t) FROM "Tariff" t WHERE id<>$1 AND "isActive" AND origin=$2 AND destination=$3 AND name IS NOT DISTINCT FROM $4::text AND "customerType" IS NOT DISTINCT FROM $5::text AND "customerId" IS NOT DISTINCT FROM $6::int LIMIT 1`, id, data["origin"], data["destination"], data["name"], data["customerType"], data["customerId"])
	return duplicate != nil, err
}
func tariffLabel(t map[string]any) string {
	kind := t["customerType"]
	if kind == nil {
		kind = "semua"
	}
	return fmt.Sprintf("%s → %s (%s)", t["origin"], t["destination"], kind)
}

func (b *business) createTariff(r *request) (any, error) {
	origin, err := required(r.body, "origin")
	if err != nil {
		return nil, err
	}
	destination, err := required(r.body, "destination")
	if err != nil {
		return nil, err
	}
	var customerType, customer any
	if raw := r.body["customerType"]; raw == "b2b" || raw == "b2c" {
		customerType = raw
	}
	if customerType == "b2b" {
		customer, err = b2bCustomer(r, r.body["customerId"], true)
		if err != nil {
			return nil, err
		}
	}
	from, to := dateValue(r.body["effectiveFrom"], false), dateValue(r.body["effectiveTo"], true)
	if from == nil {
		from = time.Now().UTC()
	}
	if truthy(r.body["effectiveFrom"]) && dateValue(r.body["effectiveFrom"], false) == nil || truthy(r.body["effectiveTo"]) && to == nil || to != nil && to.(time.Time).Before(from.(time.Time)) {
		return nil, problem(422, "Masa berlaku tarif tidak valid.")
	}
	active := to == nil || !to.(time.Time).Before(time.Now())
	if err := expireTariffs(r); err != nil {
		return nil, err
	}
	data := map[string]any{"name": nullable(r.body["name"]), "origin": origin, "destination": destination, "customerType": customerType, "customerId": customer, "effectiveFrom": from, "effectiveTo": to, "isActive": active}
	duplicate, err := duplicateTariff(r, data, 0)
	if err != nil {
		return nil, err
	}
	if active && duplicate {
		name, kind := "", ""
		if data["name"] != nil {
			name = fmt.Sprintf("“%s” ", data["name"])
		}
		if customerType != nil {
			kind = fmt.Sprintf(" (%s)", customerType)
		}
		return nil, fieldProblem(fmt.Sprintf("Tarif aktif %s%s → %s%s sudah ada. Pakai nama tarif lain atau nonaktifkan yang lama.", name, origin, destination, kind), "name", "Nama tarif sudah dipakai untuk koridor ini.")
	}
	method := r.body["pricingMethod"]
	if method != "PER_KOLI" && method != "PER_CUBIC" && method != "PER_KG" && (method != "PER_KG_SIMPLE" || customerType != "b2c") {
		method = "PER_KG"
	}
	if err := validateTariffRates(method, r.body["ratePerKoli"], r.body["ratePerCubic"]); err != nil {
		return nil, err
	}
	data["pricingMethod"] = method
	if method == "PER_KG" || method == "PER_KG_SIMPLE" {
		rate, err := requiredNumber(r.body, "ratePerKg", 1)
		if err != nil {
			return nil, err
		}
		data["ratePerKg"] = rate
	} else {
		data["ratePerKg"] = numericDefault(r.body["ratePerKg"], 0)
	}
	for key, fallback := range map[string]any{"minChargeableKg": 1, "volumetricMultiplier": 250, "roundingUnitKg": 0.5, "minChargeableKoli": 1, "minChargeableM3": 0} {
		data[key] = numericDefault(r.body[key], fallback)
	}
	data["roundingMode"] = "UP"
	if r.body["roundingMode"] == "NEAREST" {
		data["roundingMode"] = "NEAREST"
	}
	data["ratePerKoli"], data["ratePerCubic"] = nil, nil
	if method == "PER_KOLI" {
		data["ratePerKoli"] = numeric(r.body["ratePerKoli"])
	}
	if method == "PER_CUBIC" {
		data["ratePerCubic"] = numeric(r.body["ratePerCubic"])
	}
	tariff, err := r.insertRow("Tariff", data)
	if err != nil {
		return nil, tariffConflict(err)
	}
	label := tariffLabel(tariff)
	if tariff["name"] != nil {
		label = textValue(tariff["name"]) + " · " + label
	}
	b.audit(r, "created", "tariff", tariff["id"], label, nil, tariff)
	return tariff, nil
}

func (b *business) updateTariff(r *request) (any, error) {
	if err := expireTariffs(r); err != nil {
		return nil, err
	}
	existing, err := r.find("Tariff", "Tariff tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	for _, key := range []string{"origin", "destination", "name"} {
		if raw, ok := r.body[key]; ok {
			data[key] = nullable(raw)
			if key != "name" && data[key] == nil {
				data[key] = existing[key]
			}
		}
	}
	kind := existing["customerType"]
	if raw, ok := r.body["customerType"]; ok {
		kind = nil
		if raw == "b2b" || raw == "b2c" {
			kind = raw
		}
		data["customerType"] = kind
	}
	if kind != "b2b" {
		data["customerId"] = nil
	} else if raw, ok := r.body["customerId"]; ok {
		c, err := b2bCustomer(r, raw, false)
		if err != nil {
			return nil, err
		}
		data["customerId"] = c
	}
	method := existing["pricingMethod"]
	if raw := r.body["pricingMethod"]; raw == "PER_KG" || raw == "PER_KG_SIMPLE" || raw == "PER_KOLI" || raw == "PER_CUBIC" {
		if raw == "PER_KG_SIMPLE" && kind == "b2b" {
			return nil, fieldProblem("Metode kg aktual hanya tersedia untuk tarif B2C.", "pricingMethod", "Pilih metode harga B2B yang tersedia.")
		}
		method = raw
		data["pricingMethod"] = raw
	}
	if raw, ok := r.body["ratePerKg"]; ok {
		if method == "PER_KG" || method == "PER_KG_SIMPLE" {
			rate, err := requiredNumber(r.body, "ratePerKg", 1)
			if err != nil {
				return nil, err
			}
			data["ratePerKg"] = rate
		} else {
			data["ratePerKg"] = numericDefault(raw, existing["ratePerKg"])
		}
	}
	for _, key := range []string{"ratePerKoli", "ratePerCubic"} {
		if raw, ok := r.body[key]; ok {
			data[key] = numeric(raw)
		}
	}
	for _, key := range []string{"minChargeableKoli", "minChargeableM3", "minChargeableKg", "volumetricMultiplier", "roundingUnitKg"} {
		if raw, ok := r.body[key]; ok {
			data[key] = numericDefault(raw, existing[key])
		}
	}
	if raw, ok := r.body["roundingMode"]; ok {
		data["roundingMode"] = "UP"
		if raw == "NEAREST" {
			data["roundingMode"] = "NEAREST"
		}
	}
	from, to := dateValue(existing["effectiveFrom"], false), dateValue(existing["effectiveTo"], false)
	if raw, ok := r.body["effectiveFrom"]; ok {
		v := dateValue(raw, false)
		if v != nil {
			from = v
		}
		data["effectiveFrom"] = from
	}
	if raw, ok := r.body["effectiveTo"]; ok {
		to = dateValue(raw, true)
		data["effectiveTo"] = to
	}
	if truthy(r.body["effectiveFrom"]) && dateValue(r.body["effectiveFrom"], false) == nil || truthy(r.body["effectiveTo"]) && to == nil || to != nil && to.(time.Time).Before(from.(time.Time)) {
		return nil, problem(422, "Masa berlaku tarif tidak valid.")
	}
	active := truthy(existing["isActive"])
	if raw, ok := r.body["isActive"]; ok {
		value, valid := raw.(bool)
		if !valid {
			return nil, problem(422, "Status aktif tarif harus berupa boolean.")
		}
		active = value
	}
	if to != nil && to.(time.Time).Before(time.Now()) {
		if r.body["isActive"] == true {
			return nil, problem(422, "Masa berlaku tarif sudah berakhir. Perbarui tanggal akhir sebelum mengaktifkan kembali.")
		}
		active = false
	}
	data["isActive"] = active
	next := map[string]any{}
	for k, v := range existing {
		next[k] = v
	}
	for k, v := range data {
		next[k] = v
	}
	if err := validateTariffRates(method, next["ratePerKoli"], next["ratePerCubic"]); err != nil {
		return nil, err
	}
	if active {
		duplicate, err := duplicateTariff(r, next, idValue(existing["id"]))
		if err != nil {
			return nil, err
		}
		if duplicate {
			return nil, problem(422, "Tarif aktif dengan nama, koridor, dan customer ini sudah ada.")
		}
	}
	tariff, err := r.updateRow("Tariff", idValue(existing["id"]), data)
	if err != nil {
		return nil, tariffConflict(err)
	}
	b.audit(r, "updated", "tariff", tariff["id"], tariffLabel(tariff), diff(existing, tariff), nil)
	return tariff, nil
}

func (b *business) deleteTariff(r *request) (any, error) {
	existing, err := r.find("Tariff", "Tariff tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	result, err := r.db.Exec(r.ctx, `DELETE FROM "Tariff" t WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM "MasterShipment" s WHERE s."tariffId"=t.id)`, existing["id"])
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		return nil, problem(409, "Tarif terhubung ke shipment dan tidak dapat dihapus. Nonaktifkan tarif ini melalui checkbox Aktif.")
	}
	b.audit(r, "deleted", "tariff", existing["id"], fmt.Sprintf("%s → %s", existing["origin"], existing["destination"]), existing, nil)
	return fiber.Map{"deleted": true}, nil
}
