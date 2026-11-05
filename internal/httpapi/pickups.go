package httpapi

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

func (b *business) registerPickups(app *fiber.App) {
	app.Post("/api/v1/pickups/:id/confirm", b.handle("pickup.confirm", true, true, b.confirmPickup))
	app.Get("/api/v1/pickups", b.handle("pickup.view", true, false, b.pickups))
	app.Post("/api/v1/pickups", b.handle("pickup.create", true, true, b.createPickup))
	app.Get("/api/v1/pickups/:id", b.handle("pickup.view", true, false, b.pickup))
	app.Put("/api/v1/pickups/:id", b.handle("pickup.assign_kurir", true, true, b.updatePickup))
	app.Delete("/api/v1/pickups/:id", b.handle("pickup.assign_kurir", true, true, b.cancelPickup))
	app.Post("/api/v1/pickups/:id/scans", b.handle("pickup.scan", true, true, b.scanPickup))
}

func pickupExecutor(actor map[string]any) bool {
	return !hasPermission(actor, "pickup.create") && !hasPermission(actor, "pickup.assign_kurir")
}

func proofVisible(actor, record map[string]any) map[string]any {
	if !hasPermission(actor, "proof_photo.view") {
		record["photoUrl"] = nil
		for _, key := range []string{"locationLabel", "timeZone", "capturedAt", "mockLocation", "captureLatitude", "captureLongitude", "captureAccuracy"} {
			delete(record, key)
		}
	}
	return record
}

func (r *request) pickupTask(lock, assignment bool) (map[string]any, map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, nil, err
	}
	// Every pickup mutation locks the shipment before the task, matching receipt
	// and cancellation handlers and serializing scans with package edits.
	s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s JOIN "Pickup" p ON p."masterId"=s.id WHERE p.id=$1`+lockClause(lock, "s"), id)
	if err != nil {
		return nil, nil, err
	}
	if s == nil {
		return nil, nil, problem(404, "Pickup tidak ditemukan.")
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, nil, err
	}
	p, err := r.one(`SELECT to_jsonb(p) FROM "Pickup" p WHERE id=$1`+lockClause(lock, "p"), id)
	if err != nil {
		return nil, nil, err
	}
	if p == nil {
		return nil, nil, problem(404, "Pickup tidak ditemukan.")
	}
	if assignment && !hasPermission(r.actor, "pickup.assign_kurir") && !hasPermission(r.actor, "*") {
		if p["kurirId"] == nil || idValue(p["kurirId"]) != idValue(r.actor["employeeId"]) {
			return nil, nil, problem(403, fmt.Sprintf("Task %s ditugaskan ke kurir lain - hanya kurir bersangkutan atau supervisor yang boleh memproses.", p["pickupCode"]))
		}
	}
	return p, s, nil
}

func lockClause(lock bool, alias string) string {
	if lock {
		return " FOR UPDATE OF " + alias
	}
	return ""
}

func (b *business) pickups(r *request) (any, error) {
	mine := r.c.Query("mine") == "true" || pickupExecutor(r.actor)
	if mine && idValue(r.actor["employeeId"]) == 0 {
		return []map[string]any{}, nil
	}
	mode := strings.TrimSpace(r.c.Query("fulfillmentMode"))
	if !truthy(r.actor["isOwner"]) && (hasRole(r.actor, "driver") || hasRole(r.actor, "kenek")) {
		mode = "DIRECT"
	}
	if mode != "DIRECT" && mode != "STANDARD" {
		mode = ""
	}
	rows, err := r.many(`SELECT to_jsonb(p)||jsonb_build_object('master',to_jsonb(s),'customer',to_jsonb(c),
	 'detailsCount',(SELECT count(*) FROM "DetailShipment" d WHERE d."masterId"=s.id),
	 'scannedCount',(SELECT count(DISTINCT h."detailId") FROM "HandoverScan" h WHERE h."pickupId"=p.id AND h.result<>'unexpected'),
	 'pickedUpFromCheckpoint1',s."fulfillmentMode"='DIRECT' AND EXISTS (
	 SELECT 1 FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId"
	 JOIN "CheckpointRecord" cr ON cr."transportId"=t.id
	 WHERE ts."shipmentId"=s.id AND cr."checkpointId"=(SELECT cp.id FROM "Checkpoint" cp WHERE cp."routeId"=t."routeId" ORDER BY cp.sequence LIMIT 1)))
	 FROM "Pickup" p JOIN "MasterShipment" s ON s.id=p."masterId" JOIN "Customer" c ON c.id=s."customerId"
	 WHERE ($1='' OR p.status=$1) AND (NOT $2 OR p."kurirId"=$3)
	 AND ($4='' OR s."fulfillmentMode"=$4)
	 AND ($5='' OR strpos(lower(p."pickupCode"),lower($5))>0 OR strpos(lower(s."masterCode"),lower($5))>0 OR strpos(lower(c.name),lower($5))>0)
	 ORDER BY p."createdAt" DESC,p.id DESC`, strings.TrimSpace(r.c.Query("status")), mine, r.actor["employeeId"], mode, strings.TrimSpace(r.c.Query("search")))
	if err != nil {
		return nil, err
	}
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return nil, err
	}
	unscoped, warehouse := warehouseScope(r.actor)
	out := []map[string]any{}
	for _, p := range rows {
		s := p["master"].(map[string]any)
		// Pickup history belongs to its origin branch, regardless of current status.
		origin := map[string]any{"origin": s["origin"], "originWarehouseId": s["originWarehouseId"]}
		gudang := shipmentWarehouseIDs(origin, warehouses)
		visible := unscoped
		for _, id := range gudang {
			visible = visible || id == idValue(warehouse)
		}
		if !visible {
			continue
		}
		customer := p["customer"].(map[string]any)
		name := customer["companyName"]
		if !truthy(name) {
			name = customer["name"]
		}
		row := map[string]any{}
		for _, key := range []string{"id", "pickupCode", "status", "kurirId", "notes", "masterId", "photoUrl", "latitude", "longitude", "createdAt", "completedAt", "detailsCount", "scannedCount", "pickedUpFromCheckpoint1"} {
			row[key] = p[key]
		}
		for _, key := range []string{"origin", "destination", "masterCode", "fulfillmentMode"} {
			row[key] = s[key]
		}
		row["masterStatus"], row["customerName"], row["customerType"], row["gudangIds"] = s["status"], name, customer["type"], gudang
		row["pickupAddress"], row["pickupContact"], row["pickupSenderName"] = s["pengirimAddress"], s["pengirimPhone"], s["pengirimName"]
		out = append(out, proofVisible(r.actor, row))
	}
	return out, nil
}

func (b *business) pickup(r *request) (any, error) {
	p, s, err := r.pickupTask(false, pickupExecutor(r.actor))
	if err != nil {
		return nil, err
	}
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	details, err := r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=$1 ORDER BY id`, s["id"])
	if err != nil {
		return nil, err
	}
	scans, err := r.many(`SELECT to_jsonb(h)||jsonb_build_object('scannedBy',CASE WHEN u.id IS NULL THEN NULL ELSE jsonb_build_object('id',u.id,'name',u.name) END) FROM "HandoverScan" h LEFT JOIN "User" u ON u.id=h."scannedById" WHERE h."pickupId"=$1 ORDER BY h."scannedAt" DESC,h.id DESC`, p["id"])
	if err != nil {
		return nil, err
	}
	progress, err := r.handoverProgress(s, "pickup", p["id"])
	if err != nil {
		return nil, err
	}
	payment, err := r.paymentSummary(s)
	if err != nil {
		return nil, err
	}
	s["customer"], s["details"] = customer, details
	p["master"], p["scans"], p["progress"], p["paymentSummary"] = s, scans, progress, payment
	return proofVisible(r.actor, p), nil
}

var pickupSuffix = regexp.MustCompile(`([0-9]+)\s*$`)

func (r *request) nextPickupCode() (string, error) {
	if err := r.exec(`SELECT pg_advisory_xact_lock(731025)`); err != nil {
		return "", err
	}
	rows, err := r.many(`SELECT jsonb_build_object('code',"pickupCode") FROM "Pickup" WHERE "pickupCode" LIKE 'PICK-2026-%'`)
	if err != nil {
		return "", err
	}
	used := map[int]bool{}
	for _, row := range rows {
		if match := pickupSuffix.FindStringSubmatch(textValue(row["code"])); len(match) > 1 {
			n, err := strconv.Atoi(match[1])
			if err == nil {
				used[n] = true
			}
		}
	}
	n := 1
	for used[n] {
		n++
	}
	return fmt.Sprintf("PICK-2026-%06d", n), nil
}

func (b *business) createPickup(r *request) (any, error) {
	s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s WHERE id=$1 FOR UPDATE`, idValue(r.body["masterId"]))
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fieldProblem("Shipment wajib dipilih.", "masterId", "Shipment wajib dipilih.")
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, err
	}
	if s["status"] != "READY_FOR_PICKUP" {
		return nil, problem(422, fmt.Sprintf("Shipment harus berstatus READY_FOR_PICKUP (saat ini: %s).", s["status"]))
	}
	if number(s["priceAmount"]) <= 0 {
		return nil, problem(422, "Harga shipment belum dihitung - hitung harga sebelum membuat task pickup.")
	}
	var invoiceRequired bool
	if err = r.db.QueryRow(r.ctx, `SELECT c.type='b2b' AND NOT EXISTS (SELECT 1 FROM "InvoiceLine" l WHERE l."shipmentId"=s.id) FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId" WHERE s.id=$1`, s["id"]).Scan(&invoiceRequired); err != nil {
		return nil, err
	}
	if invoiceRequired {
		return nil, problem(422, "Shipment B2B belum ditagirkan ke invoice manapun - tambahkan shipment ini ke invoice perusahaan customer sebelum membuat task pickup.")
	}
	kurir := idValue(r.body["kurirId"])
	if kurir == 0 {
		return nil, fieldProblem("Kurir wajib dipilih.", "kurirId", "Kurir wajib dipilih.")
	}
	employee, err := r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1`, kurir)
	if err != nil {
		return nil, err
	}
	if employee == nil {
		return nil, fieldProblem("Kurir tidak ditemukan.", "kurirId", "Kurir tidak ditemukan.")
	}
	code, err := r.nextPickupCode()
	if err != nil {
		return nil, err
	}
	p, err := r.insertRow("Pickup", map[string]any{"pickupCode": code, "masterId": s["id"], "kurirId": kurir, "status": "ASSIGNED", "notes": nullable(r.body["notes"]), "latitude": optionalNumber(r.body["latitude"]), "longitude": optionalNumber(r.body["longitude"])})
	if err != nil {
		return nil, err
	}
	if err = r.tracking(s, "PICKUP_ASSIGNED", fmt.Sprintf("Kurir %s ditugaskan untuk penjemputan (%s)", employee["name"], code)); err != nil {
		return nil, err
	}
	b.audit(r, "created", "pickup", p["id"], code, nil, map[string]any{"master": s["masterCode"], "kurir": employee["name"]})
	return p, nil
}

func optionalNumber(v any) any {
	if v == nil || textValue(v) == "" {
		return nil
	}
	n, err := strconv.ParseFloat(textValue(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return n
}

func (b *business) updatePickup(r *request) (any, error) {
	p, _, err := r.pickupTask(true, false)
	if err != nil {
		return nil, err
	}
	if p["status"] == "COMPLETED" || p["status"] == "CANCELLED" || p["status"] == "PICKED_UP" {
		return nil, problem(422, fmt.Sprintf("Pickup dengan status %s tidak bisa diubah.", p["status"]))
	}
	data := map[string]any{}
	for _, key := range []string{"kurirId", "latitude", "longitude"} {
		if v, exists := r.body[key]; exists {
			data[key] = optionalNumber(v)
		}
	}
	if v, exists := r.body["notes"]; exists {
		data["notes"] = nullable(v)
	}
	if v := r.body["status"]; v == "ASSIGNED" || v == "IN_PROGRESS" {
		data["status"] = v
	}
	if data["kurirId"] != nil {
		employee, err := r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1`, idValue(data["kurirId"]))
		if err != nil {
			return nil, err
		}
		if employee == nil {
			return nil, fieldProblem("Kurir tidak ditemukan.", "kurirId", "Kurir tidak ditemukan.")
		}
	}
	updated, err := r.updateRow("Pickup", idValue(p["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "pickup", p["id"], textValue(p["pickupCode"]), diff(p, updated), nil)
	return proofVisible(r.actor, updated), nil
}

func (b *business) cancelPickup(r *request) (any, error) {
	p, s, err := r.pickupTask(true, false)
	if err != nil {
		return nil, err
	}
	if p["status"] == "COMPLETED" {
		return nil, problem(422, "Pickup yang sudah selesai tidak bisa dihapus.")
	}
	if p["status"] == "PICKED_UP" {
		return nil, problem(422, "Paket sudah diambil kurir (PICKED_UP) - pickup tidak bisa dibatalkan; tunggu paket tiba di gudang.")
	}
	if _, err = r.updateRow("Pickup", idValue(p["id"]), map[string]any{"status": "CANCELLED"}); err != nil {
		return nil, err
	}
	if s["status"] == "PICKED_UP" {
		if _, err = r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": "READY_FOR_PICKUP"}); err != nil {
			return nil, err
		}
	}
	if err = r.tracking(s, "PICKUP_CANCELLED", fmt.Sprintf("Pickup %s dibatalkan", p["pickupCode"])); err != nil {
		return nil, err
	}
	b.audit(r, "cancelled", "pickup", p["id"], textValue(p["pickupCode"]), nil, nil)
	return map[string]any{"cancelled": true}, nil
}

func (b *business) scanPickup(r *request) (any, error) {
	p, s, err := r.pickupTask(true, true)
	if err != nil {
		return nil, err
	}
	if p["status"] == "COMPLETED" {
		return nil, problem(422, "Pickup sudah selesai - tidak perlu scan lagi.")
	}
	if p["status"] == "CANCELLED" {
		return nil, problem(422, "Pickup sudah dibatalkan.")
	}
	payload, err := required(r.body, "payload")
	if err != nil {
		return nil, err
	}
	payload, err = normalizeScan(payload, r.c.Get("x-corp-id"))
	if err != nil {
		return nil, err
	}
	method := "TYPED"
	if r.body["method"] == "SCANNED" {
		method = "SCANNED"
	}
	before, err := r.handoverProgress(s, "pickup", p["id"])
	if err != nil {
		return nil, err
	}
	level, result, message := "detail", "unexpected", "Kode tidak dikenali - tidak cocok dengan detail barang manapun pada shipment ini."
	var detail any
	if payload == s["masterCode"] || payload == s["resi"] || payload == p["pickupCode"] {
		level, result = "master", "ok"
		message = "QR master terbaca - lanjut scan semua paket (detail barang)."
		if before.IsB2B {
			message = "Master Resi B2B terbaca - semua paket pada konsinyasi ini otomatis ter-scan. Silakan konfirmasi pickup."
		}
	} else {
		for _, d := range before.Details {
			if d["detailCode"] == payload {
				detail, result = d["id"], "ok"
				if d["scanned"] == true {
					result = "duplicate"
				}
				break
			}
		}
	}
	scan, err := r.one(`INSERT INTO "HandoverScan" AS h ("pickupId",context,"scanLevel","detailId",payload,result,method,"scannedById") VALUES ($1,'pickup',$2,$3,$4,$5,$6,$7) RETURNING to_jsonb(h)`, p["id"], level, detail, payload, result, method, r.actor["id"])
	if err != nil {
		return nil, err
	}
	progress, err := r.handoverProgress(s, "pickup", p["id"])
	if err != nil {
		return nil, err
	}
	if detail != nil {
		action, input := "scanned", "diketik"
		if method == "SCANNED" {
			input = "scan"
		}
		message = fmt.Sprintf("Paket OK (%d/%d) · %s.", progress.Scanned, progress.Total, input)
		if progress.AllScanned {
			message = "Semua paket sudah discan - silakan konfirmasi pickup."
		}
		if result == "duplicate" {
			action, message = "duplicate_scan", "Paket ini sudah pernah discan."
		}
		b.audit(r, action, "pickup", p["id"], fmt.Sprintf("%s · %s", p["pickupCode"], method), nil, nil)
	}
	return map[string]any{"scan": scan, "message": message, "progress": progress}, nil
}
