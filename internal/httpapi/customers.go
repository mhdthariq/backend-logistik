package httpapi

import (
	"github.com/gofiber/fiber/v3"
)

const customerSelect = `SELECT to_jsonb(c)||jsonb_build_object('marketingPartnerName',(SELECT u.name FROM "Partner" p JOIN "User" u ON u.id=p."userId" WHERE p.id=c."marketingPartnerId"),'warehouseName',(SELECT w.name FROM "Warehouse" w WHERE w.id=c."warehouseId"))`

func warehouseScope(user map[string]any) (bool, any) {
	if hasPermission(user, "*") {
		return true, nil
	}
	operator := true
	for _, p := range []string{"transport.view", "transport.create", "transport.depart", "transport.arrive"} {
		if !hasPermission(user, p) {
			operator = false
		}
	}
	if operator {
		return true, nil
	}
	for _, v := range user["roles"].([]any) {
		slug := v.(map[string]any)["slug"]
		if slug == "driver" || slug == "kenek" {
			return true, nil
		}
	}
	return false, user["warehouseId"]
}
func marketingID(user map[string]any) any {
	if user["partnerType"] == "MARKETING" {
		return user["partnerId"]
	}
	return nil
}

func (b *business) customers(r *request) (any, error) {
	unscoped, warehouse := warehouseScope(r.actor)
	// The legacy search spread replaces the warehouse OR clause. Preserve the
	// intended warehouse separation here, including when a search is supplied.
	return r.many(customerSelect+` FROM "Customer" c WHERE ($1 OR c."isActive") AND ($2::int IS NULL OR c."marketingPartnerId"=$2) AND ($3 OR c."warehouseId" IS NULL OR c."warehouseId"=$4::int) AND ($5='' OR c.name ILIKE $6 ESCAPE '\' OR c.code ILIKE $6 ESCAPE '\' OR c."companyName" ILIKE $6 ESCAPE '\' OR c.phone ILIKE $6 ESCAPE '\') ORDER BY c.id DESC`, boolValue(r.c.Query("include_inactive"), true), marketingID(r.actor), unscoped, warehouse, textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
}

func validMarketing(r *request, value any) (any, error) {
	p, err := r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE id=$1 AND type='MARKETING' AND "isActive"`, idValue(value))
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fieldProblem("Marketing partner tidak ditemukan / bukan bertipe MARKETING.", "marketingPartnerId", "Marketing partner tidak valid.")
	}
	return p["id"], nil
}
func validWarehouse(r *request, raw any) (any, error) {
	if raw == nil || raw == "" || raw == "general" || raw == "none" {
		return nil, nil
	}
	id := numeric(raw)
	if id == nil {
		return nil, fieldProblem("Gudang tidak valid.", "warehouseId", "Gudang tidak valid.")
	}
	w, err := r.one(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE id=$1 AND "isActive"`, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fieldProblem("Gudang tidak ditemukan / tidak aktif.", "warehouseId", "Gudang tidak ditemukan / tidak aktif.")
	}
	return w["id"], nil
}

func (b *business) createCustomer(r *request) (any, error) {
	typeName := "b2c"
	if r.body["type"] == "b2b" {
		typeName = "b2b"
	}
	name, err := required(r.body, "name")
	if err != nil {
		return nil, err
	}
	if typeName == "b2b" && nullable(r.body["companyName"]) == nil {
		return nil, fieldProblem("Nama perusahaan wajib diisi untuk customer B2B.", "companyName", "Nama perusahaan wajib diisi untuk customer B2B.")
	}
	partner := marketingID(r.actor)
	if partner == nil && numeric(r.body["marketingPartnerId"]) != nil {
		partner, err = validMarketing(r, r.body["marketingPartnerId"])
		if err != nil {
			return nil, err
		}
	}
	var warehouse any
	inherited := false
	if raw, exists := r.body["warehouseId"]; exists && raw != nil && raw != "" {
		warehouse, err = validWarehouse(r, raw)
		if err != nil {
			return nil, err
		}
	} else if self := marketingID(r.actor); self != nil {
		p, err := r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE id=$1`, self)
		if err != nil {
			return nil, err
		}
		if p != nil && p["warehouseId"] != nil {
			warehouse = p["warehouseId"]
			inherited = true
		}
	} else if r.actor["employeeId"] != nil && r.actor["warehouseId"] != nil {
		warehouse = r.actor["warehouseId"]
		inherited = true
	}
	code, err := r.nextCode("Customer", "code", "CUS-", 731023)
	if err != nil {
		return nil, err
	}
	c, err := r.one(`INSERT INTO "Customer" AS c (code,type,name,"companyName",phone,email,address,"marketingPartnerId","warehouseId","updatedAt") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW()) RETURNING to_jsonb(c)`, code, typeName, name, nullable(r.body["companyName"]), nullable(r.body["phone"]), nullable(r.body["email"]), nullable(r.body["address"]), partner, warehouse)
	if err != nil {
		return nil, err
	}
	after := fiber.Map{}
	for k, v := range c {
		after[k] = v
	}
	after["autoInheritedWarehouse"] = inherited
	b.audit(r, "created", "customer", c["id"], name, nil, after)
	return c, nil
}

func assertCustomer(r *request, c map[string]any) error {
	if hasPermission(r.actor, "*") {
		return nil
	}
	if partner := marketingID(r.actor); partner != nil && partner != c["marketingPartnerId"] {
		return problem(403, "Customer ini bukan milik Anda - data customer terpisah antar marketing.")
	}
	return nil
}

func (b *business) customer(r *request) (any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	c, err := r.one(customerSelect+`||jsonb_build_object('shipments',COALESCE((SELECT jsonb_agg(to_jsonb(recent)) FROM (SELECT * FROM "MasterShipment" s WHERE s."customerId"=c.id ORDER BY "createdAt" DESC LIMIT 10) recent),'[]'::jsonb)) FROM "Customer" c WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, problem(404, "Customer tidak ditemukan.")
	}
	if err := assertCustomer(r, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (b *business) updateCustomer(r *request) (any, error) {
	c, err := r.find("Customer", "Customer tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	if err := assertCustomer(r, c); err != nil {
		return nil, err
	}
	data := patchFields(r.body, c, []string{"companyName", "phone", "email", "address"}, nil, true)
	if _, ok := r.body["name"]; ok {
		name, err := required(r.body, "name")
		if err != nil {
			return nil, err
		}
		data["name"] = name
	}
	if v, ok := r.body["type"]; ok {
		data["type"] = "b2c"
		if v == "b2b" {
			data["type"] = "b2b"
		}
	}
	if r.actor["partnerType"] != "MARKETING" {
		if raw, ok := r.body["marketingPartnerId"]; ok {
			var partner any
			if raw != nil && raw != "" {
				partner, err = validMarketing(r, raw)
				if err != nil {
					return nil, err
				}
			}
			data["marketingPartnerId"] = partner
		}
		if raw, ok := r.body["warehouseId"]; ok {
			warehouse, err := validWarehouse(r, raw)
			if err != nil {
				return nil, err
			}
			data["warehouseId"] = warehouse
		}
	}
	updated, err := r.updateRow("Customer", idValue(c["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "customer", c["id"], textValue(updated["name"]), diff(c, updated), nil)
	return updated, nil
}

func (b *business) deleteCustomer(r *request) (any, error) {
	c, err := r.find("Customer", "Customer tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	if err := assertCustomer(r, c); err != nil {
		return nil, err
	}
	var count int
	if err := r.db.QueryRow(r.ctx, `SELECT count(*) FROM "MasterShipment" WHERE "customerId"=$1`, c["id"]).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		updated, err := r.updateRow("Customer", idValue(c["id"]), map[string]any{"isActive": false})
		if err != nil {
			return nil, err
		}
		b.audit(r, "deactivated", "customer", c["id"], textValue(c["name"]), nil, nil)
		return fiber.Map{"deactivated": true, "customer": updated}, nil
	}
	if err := r.exec(`DELETE FROM "Customer" WHERE id=$1`, c["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "customer", c["id"], textValue(c["name"]), c, nil)
	return fiber.Map{"deleted": true}, nil
}
