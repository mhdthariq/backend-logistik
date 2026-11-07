package httpapi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

func (b *business) registerDeliveries(app *fiber.App) {
	app.Post("/api/v1/deliveries/:id/complete", b.handle("delivery.confirm", true, true, b.completeDelivery))
	app.Get("/api/v1/deliveries", b.handle("delivery.view", true, false, b.deliveries))
	app.Post("/api/v1/deliveries", b.handle("delivery.assign_kurir", true, true, b.createDelivery))
	app.Get("/api/v1/deliveries/:id", b.handle("delivery.view", true, false, b.delivery))
	app.Put("/api/v1/deliveries/:id", b.handle("delivery.assign_kurir", true, true, b.updateDelivery))
	app.Delete("/api/v1/deliveries/:id", b.handle("delivery.assign_kurir", true, true, b.deleteDelivery))
	app.Post("/api/v1/deliveries/:id/scans", b.handle("delivery.scan", true, true, b.scanDelivery))
}

func (r *request) deliveryTask(lock, assignment bool) (map[string]any, map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, nil, err
	}
	// Lock shipment before task, consistent with package edits and arrival undo.
	s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s JOIN "Delivery" d ON d."masterId"=s.id WHERE d.id=$1`+lockClause(lock, "s"), id)
	if err != nil {
		return nil, nil, err
	}
	if s == nil {
		return nil, nil, problem(404, "Delivery tidak ditemukan.")
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, nil, err
	}
	d, err := r.one(`SELECT to_jsonb(d) FROM "Delivery" d WHERE id=$1`+lockClause(lock, "d"), id)
	if err != nil {
		return nil, nil, err
	}
	if d == nil {
		return nil, nil, problem(404, "Delivery tidak ditemukan.")
	}
	if assignment && !hasPermission(r.actor, "delivery.assign_kurir") && (d["kurirId"] == nil || idValue(d["kurirId"]) != idValue(r.actor["employeeId"])) {
		return nil, nil, problem(403, fmt.Sprintf("Task %s ditugaskan ke kurir lain - hanya kurir bersangkutan atau supervisor yang boleh memproses.", d["deliveryCode"]))
	}
	return d, s, nil
}

func (b *business) deliveries(r *request) (any, error) {
	mine := r.c.Query("mine") == "true" || !hasPermission(r.actor, "delivery.assign_kurir")
	mode := strings.TrimSpace(r.c.Query("fulfillmentMode"))
	if !truthy(r.actor["isOwner"]) && (hasRole(r.actor, "driver") || hasRole(r.actor, "kenek")) {
		mode = "DIRECT"
	}
	if mode != "DIRECT" && mode != "STANDARD" {
		mode = ""
	}
	args := []any{strings.TrimSpace(r.c.Query("status")), mine, r.actor["employeeId"], mode, strings.TrimSpace(r.c.Query("search"))}
	filterSQL, err := r.warehouseListFilter(&args, func(parameter string) string { return shipmentWarehouseMatch("s", parameter, false) }, false)
	if err != nil {
		return nil, err
	}
	rows, meta, err := r.listRows(`SELECT to_jsonb(d)||jsonb_build_object('master',to_jsonb(s),'customer',to_jsonb(c),'originWarehouseName',w.name,
	 'details',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'detailCode',p."detailCode",'description',p.description,'scanned',EXISTS(SELECT 1 FROM "HandoverScan" h WHERE h."deliveryId"=d.id AND h."detailId"=p.id AND h.result IN ('ok','duplicate'))) ORDER BY p.id) FROM "DetailShipment" p WHERE p."masterId"=s.id),'[]'::jsonb),
	 'scannedCount',(SELECT count(DISTINCT h."detailId") FROM "HandoverScan" h WHERE h."deliveryId"=d.id AND h.result IN ('ok','duplicate')),
	 'pickedUpFromCheckpoint1',s."fulfillmentMode"='DIRECT' AND EXISTS (
	 SELECT 1 FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId"
	 JOIN "CheckpointRecord" cr ON cr."transportId"=t.id
	 WHERE ts."shipmentId"=s.id AND cr."checkpointId"=(SELECT cp.id FROM "Checkpoint" cp WHERE cp."routeId"=t."routeId" ORDER BY cp.sequence LIMIT 1)))
	`, ` FROM "Delivery" d JOIN "MasterShipment" s ON s.id=d."masterId" JOIN "Customer" c ON c.id=s."customerId" LEFT JOIN "Warehouse" w ON w.id=s."originWarehouseId"
	 WHERE ($1='' OR d.status=$1) AND (NOT $2 OR d."kurirId"=$3)
	 AND ($4='' OR s."fulfillmentMode"=$4)
	 AND ($5='' OR strpos(lower(d."deliveryCode"),lower($5))>0 OR strpos(lower(s."masterCode"),lower($5))>0 OR strpos(lower(COALESCE(NULLIF(c."companyName",''),c.name)),lower($5))>0)
	`+filterSQL, ` ORDER BY d."createdAt" DESC,d.id DESC`, "d", ``, args)
	if err != nil {
		return nil, err
	}
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return nil, err
	}
	unscoped, warehouse := warehouseScope(r.actor)
	out := []map[string]any{}
	for _, d := range rows {
		s := d["master"].(map[string]any)
		gudang := shipmentWarehouseIDs(s, warehouses)
		visible := unscoped
		for _, id := range gudang {
			visible = visible || id == idValue(warehouse)
		}
		if !visible {
			continue
		}
		customer := d["customer"].(map[string]any)
		name := customer["companyName"]
		if !truthy(name) {
			name = customer["name"]
		}
		row := map[string]any{}
		for _, key := range []string{"id", "deliveryCode", "status", "kurirId", "notes", "proofOfDelivery", "masterId", "photoUrl", "latitude", "longitude", "createdAt", "completedAt", "originWarehouseName", "details", "scannedCount", "pickedUpFromCheckpoint1"} {
			row[key] = d[key]
		}
		for _, key := range []string{"masterCode", "destination", "originWarehouseId", "priceAmount", "fulfillmentMode"} {
			row[key] = s[key]
		}
		row["masterStatus"], row["customerName"], row["customerType"], row["gudangIds"] = s["status"], name, customer["type"], gudang
		row["address"], row["customerPhone"] = customer["address"], customer["phone"]
		details := d["details"].([]any)
		all := len(details) > 0
		for _, detail := range details {
			all = all && detail.(map[string]any)["scanned"] == true
		}
		row["detailsCount"], row["allScanned"] = len(details), all
		out = append(out, proofVisible(r.actor, row))
	}
	return listResult(out, meta), nil
}

func (b *business) delivery(r *request) (any, error) {
	d, s, err := r.deliveryTask(false, !hasPermission(r.actor, "delivery.assign_kurir"))
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
	scans, err := r.many(`SELECT to_jsonb(h)||jsonb_build_object('scannedBy',CASE WHEN u.id IS NULL THEN NULL ELSE jsonb_build_object('id',u.id,'name',u.name) END) FROM "HandoverScan" h LEFT JOIN "User" u ON u.id=h."scannedById" WHERE h."deliveryId"=$1 ORDER BY h."scannedAt" DESC,h.id DESC`, d["id"])
	if err != nil {
		return nil, err
	}
	progress, err := r.handoverProgress(s, "delivery", d["id"])
	if err != nil {
		return nil, err
	}
	s["customer"], s["details"] = customer, details
	d["master"], d["scans"], d["progress"] = s, scans, progress
	return proofVisible(r.actor, d), nil
}

func (r *request) nextDeliveryCode() (string, error) {
	if err := r.exec(`SELECT pg_advisory_xact_lock(731026)`); err != nil {
		return "", err
	}
	rows, err := r.many(`SELECT jsonb_build_object('code',"deliveryCode") FROM "Delivery" WHERE "deliveryCode" LIKE 'DLV-2026-%'`)
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
	return fmt.Sprintf("DLV-2026-%06d", n), nil
}

func (b *business) createDelivery(r *request) (any, error) {
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
	if s["status"] == "AT_DEST_GUDANG" {
		return nil, problem(422, "Driver baru check-in di gudang tujuan - paket belum discan / diterima Admin Gudang. Scan paketnya dulu di menu Shipments (status Tiba di Gudang Tujuan).")
	}
	if s["status"] != "ARRIVED_AT_GUDANG" && s["status"] != "RECEIVED_AT_GUDANG" {
		return nil, problem(422, fmt.Sprintf("Shipment harus tiba di gudang terlebih dahulu (saat ini: %s).", s["status"]))
	}
	if s["status"] == "ARRIVED_AT_GUDANG" && s["destReceivedAt"] == nil {
		return nil, problem(422, "Shipment dari gudang lain belum discan / diterima Admin Gudang - scan paketnya dulu di menu Shipments (status Tiba di Gudang Tujuan).")
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
	code, err := r.nextDeliveryCode()
	if err != nil {
		return nil, err
	}
	d, err := r.insertRow("Delivery", map[string]any{"deliveryCode": code, "masterId": s["id"], "kurirId": kurir, "status": "ASSIGNED", "notes": nullable(r.body["notes"]), "latitude": optionalNumber(r.body["latitude"]), "longitude": optionalNumber(r.body["longitude"])})
	if err != nil {
		return nil, err
	}
	if err = r.tracking(s, "DELIVERY_ASSIGNED", fmt.Sprintf("Kurir %s ditugaskan untuk pengiriman (%s)", employee["name"], code)); err != nil {
		return nil, err
	}
	b.audit(r, "created", "delivery", d["id"], code, nil, map[string]any{"master": s["masterCode"], "kurir": employee["name"]})
	return d, nil
}

func (b *business) updateDelivery(r *request) (any, error) {
	d, _, err := r.deliveryTask(true, false)
	if err != nil {
		return nil, err
	}
	if d["status"] == "COMPLETED" || d["status"] == "FAILED" {
		return nil, problem(422, fmt.Sprintf("Delivery dengan status %s tidak bisa diubah.", d["status"]))
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
	updated, err := r.updateRow("Delivery", idValue(d["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "delivery", d["id"], textValue(d["deliveryCode"]), diff(d, updated), nil)
	return proofVisible(r.actor, updated), nil
}

func (b *business) deleteDelivery(r *request) (any, error) {
	d, s, err := r.deliveryTask(true, false)
	if err != nil {
		return nil, err
	}
	if d["status"] == "COMPLETED" {
		return nil, problem(422, "Delivery yang sudah selesai tidak bisa dihapus.")
	}
	if err = r.exec(`DELETE FROM "Delivery" WHERE id=$1`, d["id"]); err != nil {
		return nil, err
	}
	d["master"] = s
	b.audit(r, "deleted", "delivery", d["id"], textValue(d["deliveryCode"]), d, nil)
	return map[string]any{"deleted": true}, nil
}

func (b *business) scanDelivery(r *request) (any, error) {
	d, s, err := r.deliveryTask(true, true)
	if err != nil {
		return nil, err
	}
	if d["status"] == "COMPLETED" {
		return nil, problem(422, "Delivery sudah selesai - tidak perlu scan lagi.")
	}
	if d["status"] == "FAILED" {
		return nil, problem(422, "Delivery ditandai gagal.")
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
	before, err := r.handoverProgress(s, "delivery", d["id"])
	if err != nil {
		return nil, err
	}
	level, result, message := "detail", "unexpected", "Kode tidak dikenali - bukan paket untuk shipment ini."
	var detail any
	if payload == s["masterCode"] || payload == s["resi"] || payload == d["deliveryCode"] {
		level, result = "master", "ok"
		message = "QR master terbaca - lanjut scan semua paket untuk customer ini."
		if before.IsB2B {
			message = "Master Resi B2B terbaca - semua paket pada konsinyasi ini otomatis ter-scan. Silakan konfirmasi serah terima."
		}
	} else {
		for _, p := range before.Details {
			if p["detailCode"] == payload {
				detail, result = p["id"], "ok"
				if p["scanned"] == true {
					result = "duplicate"
				}
				break
			}
		}
	}
	scan, err := r.one(`INSERT INTO "HandoverScan" AS h ("deliveryId",context,"scanLevel","detailId",payload,result,method,"scannedById") VALUES ($1,'delivery',$2,$3,$4,$5,$6,$7) RETURNING to_jsonb(h)`, d["id"], level, detail, payload, result, method, r.actor["id"])
	if err != nil {
		return nil, err
	}
	progress, err := r.handoverProgress(s, "delivery", d["id"])
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
			message = "Semua paket sudah discan - silakan konfirmasi serah terima ke customer."
		}
		if result == "duplicate" {
			action, message = "duplicate_scan", "Paket ini sudah pernah discan."
		}
		b.audit(r, action, "delivery", d["id"], fmt.Sprintf("%s · %s", d["deliveryCode"], method), nil, nil)
	}
	return map[string]any{"scan": scan, "message": message, "progress": progress}, nil
}
