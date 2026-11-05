package httpapi

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"novalogistik/backend/internal/capacity"
	"novalogistik/backend/internal/pricing"
)

func dropProgress(ships []map[string]any) map[string]any {
	loaded, approved := 0, 0
	for _, ts := range ships {
		if ts["dropStatus"] == "LOADED" || ts["dropStatus"] == "AT_DROP_POINT" {
			loaded++
		}
		if ts["dropStatus"] == "DELIVERY_APPROVED" {
			approved++
		}
	}
	total := len(ships)
	return map[string]any{"total": total, "loaded": loaded, "dropped": total - loaded, "approved": approved, "allDropped": total > 0 && loaded == 0, "allApproved": total > 0 && approved == total}
}
func (b *business) transportDrops(r *request) (any, error) {
	t, err := r.transport(false)
	if err != nil {
		return nil, err
	}
	cps := checkpoints(t, true)
	if t["trackingMode"] == "ACTIVITY" {
		cps = []map[string]any{}
	}
	var final any
	if len(cps) > 0 {
		final = cps[len(cps)-1]["id"]
	}
	checked := map[int]bool{}
	records, _ := t["checkpointRecords"].([]map[string]any)
	for _, cr := range records {
		checked[idValue(cr["checkpointId"])] = true
	}
	cpRows := []map[string]any{}
	seq := map[int]int{}
	for _, cp := range cps {
		cpRows = append(cpRows, map[string]any{"id": cp["id"], "name": cp["name"], "sequence": cp["sequence"], "checkedIn": checked[idValue(cp["id"])], "isFinal": cp["id"] == final})
		seq[idValue(cp["id"])] = idValue(cp["sequence"])
	}
	items := []map[string]any{}
	groups := []map[string]any{}
	groupIndex := map[int]int{}
	remaining := []map[string]any{}
	for _, ts := range transportShips(t) {
		s := object(ts["master"])
		cp := ts["dropCheckpointId"]
		if t["trackingMode"] == "ACTIVITY" {
			cp = nil
		}
		effective := fallback(cp, final)
		items = append(items, map[string]any{"shipmentId": s["id"], "masterCode": s["masterCode"], "customerName": ts["customerName"], "destination": s["destination"], "penerimaName": s["penerimaName"], "dropCheckpointId": cp, "effectiveDropCheckpointId": effective, "dropStatus": ts["dropStatus"], "droppedAt": ts["droppedAt"], "deliveryApprovedAt": ts["deliveryApprovedAt"]})
		key := idValue(effective)
		index, ok := groupIndex[key]
		if !ok {
			index = len(groups)
			groupIndex[key] = index
			groups = append(groups, map[string]any{"checkpointId": effective, "shipmentIds": []any{}})
		}
		groups[index]["shipmentIds"] = append(groups[index]["shipmentIds"].([]any), s["id"])
		if ts["dropStatus"] == "LOADED" || ts["dropStatus"] == "AT_DROP_POINT" {
			remaining = append(remaining, ts)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := seq[idValue(groups[i]["checkpointId"])], seq[idValue(groups[j]["checkpointId"])]
		if a == 0 {
			a = 1000000000
		}
		if b == 0 {
			b = 1000000000
		}
		return a < b
	})
	_, initial := loadTotals(transportShips(t))
	_, left := loadTotals(remaining)
	dropped := capacity.Load{Weight: pricing.Round((initial.Weight-left.Weight)*100) / 100, Volume: pricing.Round((initial.Volume-left.Volume)*1e6) / 1e6, Koli: initial.Koli - left.Koli}
	progress := dropProgress(transportShips(t))
	return map[string]any{"trackingMode": t["trackingMode"], "transportMode": t["transportMode"], "transportStatus": t["status"], "deliveryApprovedAt": t["deliveryApprovedAt"], "checkpoints": cpRows, "shipments": items, "groups": groups, "progress": progress, "vehicleEmpty": progress["allApproved"], "loadSummary": map[string]any{"initialLoad": initial, "remainingLoad": left, "droppedLoad": dropped, "remainingCapacity": vehicleCapacity(object(t["vehicle"]), left, threshold(r))}}, nil
}
func (r *request) transportShipment(t map[string]any) (map[string]any, error) {
	id := idValue(r.c.Params("shipmentId"))
	if id == 0 {
		return nil, problem(422, "ID shipment tidak valid.")
	}
	for _, ts := range transportShips(t) {
		if idValue(ts["shipmentId"]) == id {
			return ts, nil
		}
	}
	return nil, problem(404, "Resi tidak ada di transport ini.")
}
func (b *business) dropTransportShipment(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	ts, err := r.transportShipment(t)
	if err != nil {
		return nil, err
	}
	if r.body["action"] != "ASSIGN" && r.body["action"] != "DROP" {
		return nil, problem(422, `action harus "ASSIGN" atau "DROP".`)
	}
	activity := t["trackingMode"] == "ACTIVITY"
	cps := checkpoints(t, true)
	if r.body["action"] == "ASSIGN" {
		if !hasPermission(r.actor, "transport.create") {
			return nil, problem(403, "Missing permission: transport.create")
		}
		if activity {
			return nil, problem(422, "Activity tracking tidak menggunakan checkpoint drop.")
		}
		if t["transportMode"] != "MULTI_DROP" {
			return nil, problem(422, "Transport ini bukan MULTI_DROP.")
		}
		if t["status"] != "PLANNED" && t["status"] != "DEPARTED" {
			return nil, problem(422, fmt.Sprintf("Transport berstatus %s - titik drop tidak bisa diubah.", t["status"]))
		}
		if ts["dropStatus"] != "LOADED" && ts["dropStatus"] != "AT_DROP_POINT" {
			return nil, problem(422, fmt.Sprintf("Resi sudah %s - titik drop tidak bisa diubah.", ts["dropStatus"]))
		}
		var cp map[string]any
		for _, c := range cps {
			if idValue(c["id"]) == idValue(r.body["dropCheckpointId"]) {
				cp = c
			}
		}
		if cp == nil {
			return nil, fieldProblem("Checkpoint drop tidak ditemukan pada rute transport ini.", "dropCheckpointId", "Pilih checkpoint dari rute.")
		}
		if cp["id"] == cps[0]["id"] {
			return nil, fieldProblem("Checkpoint pertama adalah titik berangkat - pilih checkpoint setelahnya.", "dropCheckpointId", "Bukan titik berangkat.")
		}
		if err = r.exec(`UPDATE "TransportShipment" SET "dropCheckpointId"=$3 WHERE "transportId"=$1 AND "shipmentId"=$2`, t["id"], ts["shipmentId"], cp["id"]); err != nil {
			return nil, err
		}
		b.audit(r, "update", "transport", t["id"], fmt.Sprintf("%s · %s drop di %s", t["transportCode"], object(ts["master"])["masterCode"], cp["name"]), nil, nil)
		return map[string]any{"shipmentId": ts["shipmentId"], "dropCheckpointId": cp["id"], "dropStatus": ts["dropStatus"]}, nil
	}
	if !crew(r.actor, t) && !hasPermission(r.actor, "transport.arrive") {
		return nil, problem(403, "Hanya driver/kenek yang ditugaskan (atau admin) yang boleh menurunkan resi.")
	}
	var cp map[string]any
	cpID := idValue(ts["dropCheckpointId"])
	if cpID == 0 && len(cps) > 0 {
		cpID = idValue(cps[len(cps)-1]["id"])
	}
	for _, c := range cps {
		if idValue(c["id"]) == cpID {
			cp = c
		}
	}
	if !activity && cp == nil {
		return nil, problem(422, "Titik drop resi belum ditentukan.")
	}
	if t["transportMode"] != "MULTI_DROP" {
		return nil, problem(422, "Transport ini bukan MULTI_DROP - resi turun di tujuan akhir.")
	}
	if t["status"] != "DEPARTED" {
		return nil, problem(422, fmt.Sprintf("Drop hanya untuk transport DEPARTED (saat ini: %s).", t["status"]))
	}
	if ts["dropStatus"] != "LOADED" && ts["dropStatus"] != "AT_DROP_POINT" {
		return nil, problem(422, fmt.Sprintf("Resi sudah berstatus %s.", ts["dropStatus"]))
	}
	if !activity {
		checked := false
		records, _ := t["checkpointRecords"].([]map[string]any)
		for _, cr := range records {
			checked = checked || idValue(cr["checkpointId"]) == cpID
		}
		if !checked {
			return nil, problem(422, "Kendaraan belum check-in di checkpoint drop resi ini.")
		}
	}
	name := textValue(object(cp)["name"])
	if name == "" {
		name = textValue(fallback(object(ts["master"])["destination"], fallback(t["destination"], "tujuan resi")))
	}
	if err = r.exec(`UPDATE "TransportShipment" SET "dropStatus"='DROPPED',"droppedAt"=NOW(),"droppedById"=$3 WHERE "transportId"=$1 AND "shipmentId"=$2`, t["id"], ts["shipmentId"], r.actor["id"]); err != nil {
		return nil, err
	}
	if err = r.receiveTransportShipment(t, ts, object(ts["master"])["fulfillmentMode"] == "DIRECT", false, fmt.Sprintf("Resi diturunkan di %s (transport %s) oleh %s - menunggu persetujuan delivery", name, t["transportCode"], r.actor["name"])); err != nil {
		return nil, err
	}
	b.audit(r, "drop", "transport", t["id"], fmt.Sprintf("%s · %s diturunkan di %s", t["transportCode"], object(ts["master"])["masterCode"], name), nil, nil)
	return map[string]any{"shipmentId": ts["shipmentId"], "dropStatus": "DROPPED", "checkpointName": object(cp)["name"]}, nil
}
func (r *request) assertDestinationApprover(s map[string]any) error {
	unscoped, wid := warehouseScope(r.actor)
	if unscoped {
		return nil
	}
	if idValue(s["destinationWarehouseId"]) == idValue(wid) && idValue(wid) > 0 {
		return nil
	}
	var match bool
	if err := r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "Warehouse" WHERE id=$1 AND "isActive" AND lower(trim(city))=lower(trim($2)))`, wid, s["destination"]).Scan(&match); err != nil {
		return err
	}
	if match {
		return nil
	}
	return problem(403, "Delivery hanya bisa disetujui oleh Admin Gudang tujuan resi ini.")
}
func (b *business) approveTransportDrop(r *request) (any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	t, err := r.loadTransport(id, true)
	if err != nil {
		return nil, err
	}
	ts, err := r.transportShipment(t)
	if err != nil {
		return nil, err
	}
	if err = r.assertDestinationApprover(object(ts["master"])); err != nil {
		return nil, err
	}
	if ts["dropStatus"] == "DELIVERY_APPROVED" {
		return nil, problem(422, "Resi sudah disetujui.")
	}
	if ts["dropStatus"] != "DROPPED" && ts["dropStatus"] != "DELIVERY_PENDING" {
		return nil, problem(422, "Resi belum diturunkan - delivery belum bisa disetujui.")
	}
	if err = r.exec(`UPDATE "TransportShipment" SET "dropStatus"='DELIVERY_APPROVED',"deliveryApprovedAt"=NOW(),"deliveryApprovedById"=$3 WHERE "transportId"=$1 AND "shipmentId"=$2`, t["id"], ts["shipmentId"], r.actor["id"]); err != nil {
		return nil, err
	}
	if err = r.tracking(object(ts["master"]), "DELIVERY_APPROVED", fmt.Sprintf("Delivery disetujui oleh %s (transport %s)", r.actor["name"], t["transportCode"])); err != nil {
		return nil, err
	}
	b.audit(r, "approve", "transport", t["id"], fmt.Sprintf("%s · %s DELIVERY_APPROVED", t["transportCode"], object(ts["master"])["masterCode"]), nil, nil)
	return map[string]any{"shipmentId": ts["shipmentId"], "dropStatus": "DELIVERY_APPROVED"}, nil
}
func (b *business) approveTransportDelivery(r *request) (any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	t, err := r.loadTransport(id, true)
	if err != nil {
		return nil, err
	}
	if t["deliveryApprovedAt"] != nil {
		return nil, problem(422, "Transport ini sudah disetujui.")
	}
	progress := dropProgress(transportShips(t))
	if progress["total"] == 0 {
		return nil, problem(422, "Transport tidak punya resi.")
	}
	if progress["allApproved"] != true {
		return nil, problem(422, fmt.Sprintf("Belum semua resi disetujui (%d/%d).", progress["approved"], progress["total"]))
	}
	for _, ts := range transportShips(t) {
		if err = r.assertDestinationApprover(object(ts["master"])); err != nil {
			return nil, err
		}
	}
	updated, err := r.updateRow("Transport", id, map[string]any{"deliveryApprovedAt": time.Now().UTC(), "deliveryApprovedById": r.actor["id"]})
	if err != nil {
		return nil, err
	}
	b.audit(r, "approve", "transport", t["id"], strings.Join([]string{textValue(t["transportCode"]), "→ DELIVERY APPROVED (kendaraan kosong)"}, " "), nil, nil)
	return map[string]any{"id": t["id"], "deliveryApprovedAt": updated["deliveryApprovedAt"]}, nil
}
