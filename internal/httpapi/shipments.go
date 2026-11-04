package httpapi

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgconn"
)

func (b *business) registerShipments(app *fiber.App) {
	app.Get("/api/v1/shipments", b.handle("shipment.view", true, false, b.shipments))
	app.Post("/api/v1/shipments", b.handle("shipment.create", true, true, b.createShipment))
	app.Get("/api/v1/shipments/:id", b.handle("shipment.view", true, false, b.shipment))
	app.Put("/api/v1/shipments/:id", b.handle("shipment.update", true, true, b.updateShipment))
	app.Delete("/api/v1/shipments/:id", b.handle("", true, true, b.deleteShipment))
	app.Get("/api/v1/shipments/:id/details", b.handle("shipment_detail.view", true, false, b.shipmentDetails))
	app.Post("/api/v1/shipments/:id/details", b.handle("shipment_detail.create", true, true, b.createShipmentDetails))
	app.Put("/api/v1/shipments/:id/details/batch", b.handle("shipment_detail.update", true, true, b.updateShipmentDetails))
	app.Delete("/api/v1/shipments/:id/details/batch", b.handle("shipment_detail.delete", true, true, b.deleteShipmentDetails))
	app.Put("/api/v1/shipment-details/:id", b.handle("shipment_detail.update", true, true, b.updateShipmentDetail))
	app.Delete("/api/v1/shipment-details/:id", b.handle("shipment_detail.delete", true, true, b.deleteShipmentDetail))
	app.Get("/api/v1/shipments/:id/tracking", b.handle("shipment.view_tracking", true, false, b.shipmentTracking))
	app.Post("/api/v1/shipments/:id/print-log", b.handle("shipment.print_resi", true, false, b.shipmentPrint))
	app.Post("/api/v1/shipments/:id/price", b.handle("shipment.view", true, true, b.priceShipment))
	app.Get("/api/v1/shipments/:id/documents", b.handle("shipment.view", true, false, b.shipmentDocuments))
	app.Post("/api/v1/shipments/:id/documents", b.handle("shipment.update", true, true, b.createShipmentDocument))
	app.Delete("/api/v1/shipments/:id/documents/:documentId", b.handle("shipment.update", true, true, b.deleteShipmentDocument))
}

const invoiceLinesJSON = `COALESCE((SELECT jsonb_agg(jsonb_build_object('invoice',jsonb_build_object('id',i.id,'invoiceNumber',i."invoiceNumber",'status',i.status))) FROM "InvoiceLine" l JOIN "Invoice" i ON i.id=l."invoiceId" WHERE l."shipmentId"=s.id),'[]'::jsonb)`

func (b *business) shipments(r *request) (any, error) {
	filter := textValue(r.c.Query("invoice", "all"))
	if filter != "all" && filter != "on_invoice" && filter != "not_invoice" {
		return nil, problem(422, "Filter invoice tidak valid.")
	}
	mode := strings.ToUpper(textValue(r.c.Query("fulfillmentMode")))
	if mode != "DIRECT" && mode != "STANDARD" {
		mode = ""
	}
	marketing := r.actor["partnerType"] == "MARKETING"
	rows, err := r.many(`SELECT to_jsonb(s)||jsonb_build_object('customer',to_jsonb(c),'payments',
	 COALESCE((SELECT jsonb_agg(jsonb_build_object('amount',p.amount,'status',p.status)) FROM "Payment" p WHERE p."masterId"=s.id),'[]'::jsonb),
	 'invoiceLines',`+invoiceLinesJSON+`,'_count',jsonb_build_object('details',(SELECT count(*) FROM "DetailShipment" d WHERE d."masterId"=s.id),'pickups',(SELECT count(*) FROM "Pickup" p WHERE p."masterId"=s.id),'deliveries',(SELECT count(*) FROM "Delivery" d WHERE d."masterId"=s.id),'payments',(SELECT count(*) FROM "Payment" p WHERE p."masterId"=s.id)))
	 FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId"
	 WHERE (NOT $1 OR s."createdByPartnerId"=$2) AND ($3='' OR s.status=$3) AND ($4='' OR c.type=$4) AND ($5='' OR s."fulfillmentMode"=$5)
	 AND ($6='all' OR ($6='on_invoice')=EXISTS(SELECT 1 FROM "InvoiceLine" l WHERE l."shipmentId"=s.id))
	 AND ($7='' OR s."masterCode" ILIKE $8 ESCAPE '\' OR s.origin ILIKE $8 ESCAPE '\' OR s.destination ILIKE $8 ESCAPE '\' OR c.name ILIKE $8 ESCAPE '\') ORDER BY s."createdAt" DESC`, marketing, idValue(r.actor["partnerId"]), textValue(r.c.Query("status")), textValue(r.c.Query("customerType")), mode, filter, textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
	if err != nil {
		return nil, err
	}
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w`)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	visibleIDs := []int{}
	for _, s := range rows {
		if !shipmentVisible(r.actor, s, warehouses) {
			continue
		}
		s["gudangIds"] = shipmentWarehouseIDs(s, warehouses)
		shipmentWarehouseNames(s, warehouses)
		visibleIDs = append(visibleIDs, idValue(s["id"]))
		out = append(out, s)
	}
	if len(out) == 0 {
		return out, nil
	}
	details, err := r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=ANY($1::int[])`, visibleIDs)
	if err != nil {
		return nil, err
	}
	byMaster := map[int][]map[string]any{}
	for _, d := range details {
		id := idValue(d["masterId"])
		byMaster[id] = append(byMaster[id], d)
	}
	latest := map[int]map[string]any{}
	withTracking := hasPermission(r.actor, "shipment.view_tracking")
	if withTracking {
		events, err := r.many(`SELECT DISTINCT ON ("masterId") to_jsonb(e) FROM "TrackingEvent" e WHERE "masterId"=ANY($1::int[]) ORDER BY "masterId","occurredAt" DESC`, visibleIDs)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			latest[idValue(e["masterId"])] = e
		}
	}
	for _, s := range out {
		id := idValue(s["id"])
		s["totals"] = shipmentTotals(byMaster[id], true)
		if withTracking {
			events := []map[string]any{}
			if e := latest[id]; e != nil {
				events = append(events, e)
			}
			s["trackingEvents"] = events
		}
	}
	return out, nil
}

func shipmentWarehouseNames(s map[string]any, warehouses []map[string]any) {
	for _, side := range []string{"origin", "destination", "arrived"} {
		s[side+"WarehouseName"] = nil
		for _, w := range warehouses {
			if w["id"] == s[side+"WarehouseId"] {
				s[side+"WarehouseName"] = w["name"]
				break
			}
		}
	}
}

func percentage(body map[string]any) (any, error) {
	p := numeric(body["discountPercentage"])
	if p == nil || p.(float64) < 0 || p.(float64) > 100 {
		return nil, fieldProblem("Persentase discount harus antara 0% dan 100%.", "discountPercentage", "Masukkan persentase antara 0 dan 100.")
	}
	return p, nil
}

func (b *business) createShipment(r *request) (any, error) {
	cid := idValue(r.body["customerId"])
	if cid == 0 {
		return nil, fieldProblem("Customer wajib dipilih.", "customerId", "Customer wajib dipilih.")
	}
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, cid)
	if err != nil {
		return nil, err
	}
	if customer == nil {
		return nil, fieldProblem("Customer tidak ditemukan.", "customerId", "Customer tidak ditemukan.")
	}
	if partner := marketingID(r.actor); partner != nil && customer["marketingPartnerId"] != partner {
		return nil, &Error{Status: 403, Message: "Customer ini bukan milik Anda - Marketing hanya bisa membuat shipment untuk customer sendiri.", Errors: map[string][]string{"customerId": {"Customer ini bukan milik Anda."}}}
	}
	data := map[string]any{"customerId": cid, "status": "CREATED", "tariffId": nil, "discountAmount": math.Max(0, number(r.body["discountAmount"])), "insuranceAmount": math.Max(0, number(r.body["insuranceAmount"])), "discountPercentage": nil}
	if r.body["discountType"] == "PERCENTAGE" {
		p, err := percentage(r.body)
		if err != nil {
			return nil, err
		}
		data["discountAmount"] = 0
		if p.(float64) > 0 {
			data["discountPercentage"] = p
		}
	}
	if tid := idValue(r.body["tariffId"]); tid > 0 {
		tariff, err := r.one(`SELECT to_jsonb(t) FROM "Tariff" t WHERE id=$1 AND "isActive" AND "effectiveFrom"<=NOW() AND ("effectiveTo" IS NULL OR "effectiveTo">=NOW())`, tid)
		if err != nil {
			return nil, err
		}
		if tariff == nil {
			return nil, fieldProblem("Tarif tidak ditemukan / tidak aktif.", "tariffId", "Tarif tidak ditemukan / tidak aktif.")
		}
		if tariff["customerType"] != nil && tariff["customerType"] != customer["type"] {
			a, c := strings.ToUpper(textValue(tariff["customerType"])), strings.ToUpper(textValue(customer["type"]))
			return nil, fieldProblem(fmt.Sprintf("Tarif ini hanya untuk customer %s - customer terpilih bertipe %s.", a, c), "tariffId", fmt.Sprintf("Tarif %s tidak cocok untuk customer %s.", a, c))
		}
		if tariff["customerType"] == "b2b" && tariff["customerId"] != customer["id"] {
			return nil, fieldProblem("Tarif B2B ini bukan milik customer terpilih.", "tariffId", "Pilih tarif yang terdaftar untuk customer ini.")
		}
		data["tariffId"], data["origin"], data["destination"] = tariff["id"], tariff["origin"], tariff["destination"]
	} else {
		data["origin"], data["destination"] = nullable(r.body["origin"]), nullable(r.body["destination"])
	}
	if data["origin"] == nil || data["destination"] == nil {
		return nil, fieldProblem("Rute wajib dipilih dari daftar tarif.", "tariffId", "Rute wajib dipilih dari daftar tarif.")
	}
	allSame := true
	for receiver, sender := range map[string]string{"penerimaName": "pengirimName", "penerimaContact": "pengirimPhone", "penerimaAddress": "pengirimAddress"} {
		fallback := map[string]string{"pengirimName": "name", "pengirimPhone": "phone", "pengirimAddress": "address"}[sender]
		a := textValue(r.body[sender])
		if a == "" {
			a = textValue(customer[fallback])
		}
		c := textValue(r.body[receiver])
		if c == "" || !strings.EqualFold(a, c) {
			allSame = false
		}
	}
	if allSame {
		return nil, fieldProblem("Data Pengirim dan Penerima identik (nama, kontak, dan alamat sama persis). Ubah minimal salah satu field penerima sebelum membuat shipment.", "penerimaName", "Pengirim dan Penerima tidak boleh identik.")
	}
	mode := "STANDARD"
	if strings.EqualFold(textValue(r.body["fulfillmentMode"]), "DIRECT") {
		mode = "DIRECT"
	}
	if mode == "DIRECT" && !hasPermission(r.actor, "shipment.create_direct") {
		return nil, problem(403, "Missing permission: shipment.create_direct")
	}
	data["fulfillmentMode"] = mode
	data["originWarehouseId"], data["destinationWarehouseId"] = numeric(r.body["originWarehouseId"]), numeric(r.body["destinationWarehouseId"])
	if mode == "DIRECT" && data["originWarehouseId"] == nil {
		_, data["originWarehouseId"] = warehouseScope(r.actor)
	}
	data["createdByPartnerId"] = marketingID(r.actor)
	data["discountFundedBy"] = "COMPANY"
	if data["createdByPartnerId"] != nil {
		data["discountFundedBy"] = "MARKETING"
	}
	for _, field := range []string{"penerimaName", "penerimaAddress", "penerimaContact", "pengirimName", "pengirimPhone", "pengirimEmail", "pengirimAddress"} {
		data[field] = nullable(r.body[field])
	}
	for field, source := range map[string]string{"pengirimName": "name", "pengirimPhone": "phone", "pengirimEmail": "email", "pengirimAddress": "address"} {
		if data[field] == nil {
			data[field] = customer[source]
		}
	}
	if nullable(r.body["pengirimName"]) == nil && customer["type"] == "b2b" && truthy(customer["companyName"]) {
		data["pengirimName"] = customer["companyName"]
	}
	data["paymentTrackingEnabled"] = customer["type"] == "b2c" && r.actor["partnerType"] != "MARKETING" && hasRole(r.actor, "admin-gudang")
	inputs, _ := r.body["details"].([]any)
	if len(inputs) > 100 {
		return nil, problem(422, "Maksimal 100 baris detail per shipment.")
	}
	total := 0
	for _, raw := range inputs {
		d, ok := raw.(map[string]any)
		if !ok {
			return nil, problem(422, "Detail harus berupa object JSON.")
		}
		n, err := quantity(d["quantity"])
		if err != nil {
			return nil, err
		}
		total += n
	}
	if total > 10000 {
		return nil, problem(422, "Maksimal 10000 paket per shipment.")
	}
	code, err := r.nextShipmentCode()
	if err != nil {
		return nil, err
	}
	data["masterCode"], data["resi"] = code, code
	s, err := r.insertRow("MasterShipment", data)
	if err != nil {
		return nil, err
	}
	for _, raw := range inputs {
		d := raw.(map[string]any)
		if nullable(d["description"]) == nil {
			continue
		}
		if _, err = r.insertDetails(s, d); err != nil {
			return nil, err
		}
	}
	if err = r.tracking(s, "CREATED", fmt.Sprintf("Shipment %s dibuat oleh %s", code, r.actor["name"])); err != nil {
		return nil, err
	}
	b.audit(r, "created", "shipment", s["id"], code, nil, s)
	return r.one(`SELECT to_jsonb(s)||jsonb_build_object('customer',to_jsonb(c),'details',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM "DetailShipment" d WHERE d."masterId"=s.id),'[]'::jsonb)) FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId" WHERE s.id=$1`, s["id"])
}

func hasRole(actor map[string]any, slug string) bool {
	for _, raw := range actor["roles"].([]any) {
		if raw.(map[string]any)["slug"] == slug {
			return true
		}
	}
	return false
}

func (r *request) nextShipmentCode() (string, error) {
	if err := r.exec(`SELECT pg_advisory_xact_lock(731024)`); err != nil {
		return "", err
	}
	rows, err := r.many(`SELECT jsonb_build_object('n',substring("masterCode" from '^MKT-.*?([0-9]+)\s*$')::bigint) FROM "MasterShipment" WHERE "masterCode" ~ '^MKT-.*[0-9]\s*$'`)
	if err != nil {
		return "", err
	}
	used := map[int]bool{}
	for _, row := range rows {
		used[int(number(row["n"]))] = true
	}
	n := 7
	for used[n] {
		n++
	}
	return fmt.Sprintf("MKT-%06d", n), nil
}

func (r *request) tracking(s map[string]any, event, description string) error {
	return r.exec(`INSERT INTO "TrackingEvent" ("masterId",event,description,"actorId") VALUES ($1,$2,$3,$4)`, s["id"], event, description, r.actor["id"])
}

func (b *business) updateShipment(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	if s["status"] != "CREATED" {
		return nil, problem(422, "Shipment hanya bisa diubah saat status CREATED.")
	}
	data := map[string]any{}
	if raw, exists := r.body["tariffId"]; exists {
		data["tariffId"] = nil
		if tid := idValue(raw); tid > 0 {
			t, err := r.one(`SELECT to_jsonb(t) FROM "Tariff" t WHERE id=$1 AND "isActive"`, tid)
			if err != nil {
				return nil, err
			}
			if t == nil {
				return nil, problem(422, "Tarif tidak ditemukan / tidak aktif.")
			}
			data["tariffId"], data["origin"], data["destination"] = t["id"], t["origin"], t["destination"]
		}
	}
	for _, field := range []string{"origin", "destination"} {
		if raw, exists := r.body[field]; exists && data[field] == nil {
			data[field] = nullable(raw)
			if data[field] == nil {
				data[field] = s[field]
			}
		}
	}
	for _, field := range []string{"originWarehouseId", "destinationWarehouseId"} {
		if raw, exists := r.body[field]; exists {
			data[field] = numeric(raw)
			if !truthy(raw) {
				data[field] = nil
			}
		}
	}
	if raw, exists := r.body["insuranceAmount"]; exists {
		data["insuranceAmount"] = math.Max(0, number(raw))
	}
	if r.body["discountType"] == "PERCENTAGE" {
		p, err := percentage(r.body)
		if err != nil {
			return nil, err
		}
		data["discountAmount"], data["discountPercentage"] = 0, nil
		if p.(float64) > 0 {
			data["discountPercentage"] = p
		}
	} else if raw, exists := r.body["discountAmount"]; exists {
		data["discountAmount"], data["discountPercentage"] = math.Max(0, number(raw)), nil
	}
	for _, field := range []string{"penerimaName", "penerimaAddress", "penerimaContact", "pengirimName", "pengirimPhone", "pengirimEmail", "pengirimAddress"} {
		if raw, exists := r.body[field]; exists {
			data[field] = nullable(raw)
		}
	}
	after, err := r.updateRow("MasterShipment", idValue(s["id"]), data)
	if err != nil {
		return nil, err
	}
	before := map[string]any{}
	for key, value := range after {
		if fmt.Sprint(s[key]) != fmt.Sprint(value) {
			before[key] = map[string]any{"before": s[key], "after": value}
		}
	}
	b.audit(r, "updated", "shipment", s["id"], textValue(s["masterCode"]), before, nil)
	return after, nil
}

func (b *business) deleteShipment(r *request) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	if !hasPermission(r.actor, "shipment.delete") && !hasPermission(r.actor, "shipment.delete_cancelled") {
		return nil, problem(403, "Missing permission: shipment.delete or shipment.delete_cancelled")
	}
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	if s["status"] != "CREATED" && s["status"] != "CANCELLED" {
		return nil, problem(422, "Shipment hanya bisa dihapus saat status CREATED atau CANCELLED.")
	}
	permission := "shipment.delete"
	if s["status"] == "CANCELLED" {
		permission = "shipment.delete_cancelled"
	}
	if !hasPermission(r.actor, permission) {
		return nil, problem(403, "Missing permission: "+permission)
	}
	var history bool
	err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "InvoiceLine" WHERE "shipmentId"=$1 UNION ALL SELECT 1 FROM "Payment" WHERE "masterId"=$1 UNION ALL SELECT 1 FROM "Pickup" WHERE "masterId"=$1 UNION ALL SELECT 1 FROM "Delivery" WHERE "masterId"=$1 UNION ALL SELECT 1 FROM "TransportShipment" WHERE "shipmentId"=$1 UNION ALL SELECT 1 FROM "WalletTransaction" WHERE "referenceType"='shipment' AND "referenceId"=$1 UNION ALL SELECT 1 FROM "HandoverScan" WHERE "masterId"=$1 OR "detailId" IN (SELECT id FROM "DetailShipment" WHERE "masterId"=$1) UNION ALL SELECT 1 FROM "Discrepancy" WHERE "masterId"=$1)`, s["id"]).Scan(&history)
	if err != nil {
		return nil, err
	}
	if history {
		return nil, problem(422, "Shipment memiliki catatan keuangan atau operasional terkait dan tidak bisa dihapus.")
	}
	if err = r.exec(`DELETE FROM "MasterShipment" WHERE id=$1`, s["id"]); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23503" {
			return nil, problem(409, "Shipment atau catatan terkait berubah bersamaan. Silakan muat ulang dan coba lagi.")
		}
		return nil, err
	}
	b.audit(r, "deleted", "shipment", s["id"], textValue(s["masterCode"]), s, nil)
	return map[string]any{"deleted": true}, nil
}

func (b *business) shipmentTracking(r *request) (any, error) {
	s, err := r.shipment(false)
	if err != nil {
		return nil, err
	}
	return r.trackingEvents(s["id"])
}

func (r *request) trackingEvents(id any) ([]map[string]any, error) {
	// Credentials are never included in actor objects, even on internal routes.
	return r.many(`SELECT to_jsonb(e)||jsonb_build_object('actor',(SELECT to_jsonb(u)-'passwordHash' FROM "User" u WHERE u.id=e."actorId")) FROM "TrackingEvent" e WHERE "masterId"=$1 ORDER BY "occurredAt" DESC`, id)
}

func (b *business) shipmentPrint(r *request) (any, error) {
	s, err := r.find("MasterShipment", "Shipment tidak ditemukan.", false)
	if err != nil {
		return nil, err
	}
	roles := []string{}
	if truthy(r.actor["isOwner"]) {
		roles = append(roles, "Owner")
	} else {
		for _, raw := range r.actor["roles"].([]any) {
			roles = append(roles, textValue(raw.(map[string]any)["name"]))
		}
	}
	b.audit(r, "printed_resi", "shipment", s["id"], textValue(s["masterCode"]), nil, map[string]any{"printedAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "accountName": r.actor["name"], "roles": roles})
	return map[string]any{"logged": true}, nil
}
