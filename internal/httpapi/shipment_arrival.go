package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (b *business) arriveShipment(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	mode := "scan"
	if r.body["mode"] == "walk_in" || r.body["mode"] == "transport" {
		mode = textValue(r.body["mode"])
	}
	warehouseID := r.body["warehouseId"]
	if mode == "transport" {
		if s["status"] != "AT_DEST_GUDANG" && (s["status"] != "ARRIVED_AT_GUDANG" || s["destReceivedAt"] != nil) {
			return nil, problem(422, fmt.Sprintf("Mode transport hanya untuk shipment AT_DEST_GUDANG / menunggu scan Admin Gudang (saat ini: %s).", s["status"]))
		}
		if s["destReceivedAt"] != nil {
			return nil, problem(422, "Shipment ini sudah diterima & diverifikasi scan di gudang tujuan.")
		}
		var count int
		if err = r.db.QueryRow(r.ctx, `SELECT count(*) FROM "DetailShipment" WHERE "masterId"=$1`, s["id"]).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, problem(422, "Shipment belum punya detail barang.")
		}
		warehouseID = s["arrivedWarehouseId"]
		if warehouseID == nil {
			warehouseID = s["destinationWarehouseId"]
		}
	}
	w, err := r.one(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE id=$1`, idValue(warehouseID))
	if err != nil {
		return nil, err
	}
	if w == nil || w["isActive"] != true {
		if mode == "transport" {
			return nil, problem(422, "Gudang tujuan shipment belum jelas - hubungi admin untuk memperbaiki data.")
		}
		return nil, fieldProblem("Gudang tujuan wajib dipilih.", "warehouseId", "Gudang tujuan wajib dipilih.")
	}
	if r.actor["isOwner"] != true && r.actor["warehouseId"] != w["id"] {
		if mode == "transport" {
			return nil, problem(403, "Anda hanya bisa menerima paket transport di gudang Anda sendiri.")
		}
		return nil, problem(403, "Anda hanya bisa mengonfirmasi arrival di gudang Anda sendiri.")
	}
	if mode != "transport" {
		switch s["status"] {
		case "CREATED", "READY_FOR_PICKUP", "PICKED_UP":
		default:
			return nil, problem(422, fmt.Sprintf("Shipment dengan status %s tidak bisa dikonfirmasi tiba di gudang.", s["status"]))
		}
		if mode == "scan" && s["status"] != "PICKED_UP" {
			return nil, problem(422, fmt.Sprintf("Mode scan hanya untuk shipment PICKED_UP (saat ini: %s). Gunakan walk-in untuk pelanggan yang datang langsung.", s["status"]))
		}
		if mode == "walk_in" && s["status"] != "CREATED" && s["status"] != "READY_FOR_PICKUP" {
			return nil, problem(422, fmt.Sprintf("Walk-in hanya untuk shipment CREATED / READY_FOR_PICKUP (saat ini: %s).", s["status"]))
		}
	}
	context := "gudang_arrival"
	if mode == "transport" {
		context = "transport_arrival"
	}
	p, err := r.arrivalProgress(s, context)
	if err != nil {
		return nil, err
	}
	if mode != "walk_in" {
		if p.Total == 0 {
			return nil, problem(422, "Shipment belum punya detail barang.")
		}
		if !p.AllScanned {
			return nil, problem(422, fmt.Sprintf("Belum semua paket discan (%d/%d) - scan semua paket atau gunakan tombol \"Scan Semua Paket\".", p.Scanned, p.Total))
		}
	}
	data := map[string]any{"status": "RECEIVED_AT_GUDANG", "arrivalPreviousStatus": s["status"], "arrivedWarehouseId": w["id"]}
	if s["originWarehouseId"] == nil {
		data["originWarehouseId"] = w["id"]
	}
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	description := fmt.Sprintf("Pelanggan %s menyerahkan langsung di %s (walk-in, tanpa scan)", textValue(customer["name"]), w["name"])
	event := "RECEIVED_AT_GUDANG"
	label := fmt.Sprintf("%s → RECEIVED_AT_GUDANG (%s) @ %s", s["masterCode"], mode, w["name"])
	if mode == "walk_in" {
		label = fmt.Sprintf("%s → RECEIVED_AT_GUDANG (walk-in) @ %s", s["masterCode"], w["name"])
	}
	if mode == "scan" {
		description = fmt.Sprintf("Paket diterima di %s - %d paket terverifikasi scan", w["name"], p.Total)
		if customer != nil {
			description += fmt.Sprintf(" (kurir drop-off, shipment %s)", customer["name"])
		}
	}
	if mode == "transport" {
		data = map[string]any{"status": "ARRIVED_AT_GUDANG", "destReceivedAt": time.Now().UTC(), "arrivalPreviousStatus": "AT_DEST_GUDANG"}
		event = "RECEIVED_FROM_TRANSPORT"
		description = fmt.Sprintf("Paket diterima di %s dari transport", w["name"])
		origin, err := r.one(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE id=$1`, s["originWarehouseId"])
		if err != nil {
			return nil, err
		}
		if origin != nil {
			description += fmt.Sprintf(" (asal %s)", origin["name"])
		}
		description += fmt.Sprintf(" - %d paket terverifikasi scan", p.Total)
		label = fmt.Sprintf("%s → received @ %s (transport drop-off)", s["masterCode"], w["name"])
	}
	updated, err := r.updateRow("MasterShipment", idValue(s["id"]), data)
	if err != nil {
		return nil, err
	}
	if mode != "transport" {
		if err = r.exec(`UPDATE "Pickup" SET status='COMPLETED',"completedAt"=NOW(),"updatedAt"=NOW() WHERE "masterId"=$1 AND status IN ('ASSIGNED','IN_PROGRESS','PICKED_UP')`, s["id"]); err != nil {
			return nil, err
		}
	}
	if notes := textValue(r.body["notes"]); notes != "" {
		description += " - " + notes
	}
	if err = r.tracking(s, event, description); err != nil {
		return nil, err
	}
	b.audit(r, "status_change", "shipment", s["id"], label, nil, map[string]any{"mode": mode, "warehouse": w["name"], "packages": p.Total})
	updated["warehouseName"], updated["mode"] = w["name"], mode
	return updated, nil
}

func (b *business) undoArrival(r *request) (any, error) {
	// Protect downstream dependency checks as well as the receipt being reversed.
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	if id := number(r.c.Params("id")); id <= 0 || id > 9007199254740991 || id != float64(idValue(id)) {
		return nil, problem(422, "Shipment tidak valid.")
	}
	reason, _ := r.body["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" || jsStringLength(reason) > 1000 {
		return nil, problem(422, "Alasan wajib diisi (maksimal 1000 karakter).")
	}
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	destination := s["status"] == "ARRIVED_AT_GUDANG" && s["destReceivedAt"] != nil
	previous := s["arrivalPreviousStatus"]
	if previous == nil {
		event, err := r.one(`SELECT to_jsonb(t) FROM "TrackingEvent" t WHERE "masterId"=$1 AND event IN ('PICKED_UP','READY_FOR_PICKUP','CREATED') ORDER BY "occurredAt" DESC,id DESC LIMIT 1`, s["id"])
		if err != nil {
			return nil, err
		}
		if event != nil {
			previous = event["event"]
		} else {
			var completed bool
			if err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "Pickup" WHERE "masterId"=$1 AND status='COMPLETED')`, s["id"]).Scan(&completed); err != nil {
				return nil, err
			}
			previous = "CREATED"
			if completed {
				previous = "PICKED_UP"
			}
		}
	}
	origin := s["status"] == "RECEIVED_AT_GUDANG" && (previous == "CREATED" || previous == "READY_FOR_PICKUP" || previous == "PICKED_UP")
	if !destination && !origin {
		return nil, problem(409, "Penerimaan gudang ini tidak dapat di-undo.")
	}
	var downstream, expenses bool
	err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "Delivery" WHERE "masterId"=$1 AND status<>'CANCELLED') OR ($2 AND EXISTS(SELECT 1 FROM "TransportShipment" WHERE "shipmentId"=$1)), EXISTS(SELECT 1 FROM "TransportExpense" WHERE "shipmentId"=$1 AND status<>'CANCELLED')`, s["id"], origin).Scan(&downstream, &expenses)
	if err != nil {
		return nil, err
	}
	if downstream {
		return nil, problem(409, "Shipment sudah memiliki proses lanjutan. Batalkan proses tersebut terlebih dahulu.")
	}
	if expenses {
		return nil, problem(409, "Batalkan atau hapus biaya shipment terlebih dahulu sebelum undo penerimaan.")
	}
	status, context := textValue(previous), "gudang_arrival"
	data := map[string]any{"destReceivedAt": nil, "arrivalPreviousStatus": nil}
	if destination {
		status, context = "AT_DEST_GUDANG", "transport_arrival"
	} else {
		data["arrivedWarehouseId"] = nil
	}
	data["status"] = status
	if _, err = r.updateRow("MasterShipment", idValue(s["id"]), data); err != nil {
		return nil, err
	}
	if err = r.exec(`DELETE FROM "HandoverScan" WHERE "masterId"=$1 AND context=$2`, s["id"], context); err != nil {
		return nil, err
	}
	if origin && status == "PICKED_UP" {
		if err = r.exec(`UPDATE "Pickup" SET status='PICKED_UP',"completedAt"=NULL,"updatedAt"=NOW() WHERE "masterId"=$1 AND status='COMPLETED'`, s["id"]); err != nil {
			return nil, err
		}
	}
	if err = r.tracking(s, "ARRIVAL_UNDONE", fmt.Sprintf("Penerimaan gudang dibatalkan → %s: %s", status, reason)); err != nil {
		return nil, err
	}
	// Undo requires its audit to commit with the reversal (unlike best-effort
	// receipt/scan audits), preserving the legacy transaction boundary.
	before, err := json.Marshal(map[string]any{"status": s["status"], "destReceivedAt": s["destReceivedAt"]})
	if err != nil {
		return nil, err
	}
	after, err := json.Marshal(map[string]any{"status": status, "reason": reason})
	if err != nil {
		return nil, err
	}
	err = r.exec(`INSERT INTO "AuditLog" (action,"entityType","entityId","entityLabel","actorId","beforeData","afterData") VALUES ('undo_arrival','shipment',$1,$2,$3,$4,$5)`, s["id"], s["masterCode"], r.actor["id"], string(before), string(after))
	return map[string]any{"status": status}, err
}
