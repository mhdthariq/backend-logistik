package httpapi

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func (r *request) syncReturnTransport(id any, status string) error {
	if status == "COMPLETED" {
		status = "ARRIVED"
	}
	return r.exec(`UPDATE "ReturnTask" SET status=$2,"updatedAt"=NOW() WHERE "returnTransportId"=$1 AND status<>'CANCELLED'`, id, status)
}
func (r *request) departLoadedShipments(t map[string]any, cp map[string]any) error {
	for _, ts := range transportShips(t) {
		s := object(ts["master"])
		if s["status"] == "RECEIVED_AT_GUDANG" {
			if _, err := r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": "IN_TRANSPORT"}); err != nil {
				return err
			}
		}
		desc := fmt.Sprintf("Transport %s berangkat (rute %s)", t["transportCode"], textValue(object(t["route"])["name"]))
		if cp != nil {
			desc = fmt.Sprintf("Transport %s berangkat melalui check-in checkpoint %s", t["transportCode"], cp["name"])
			if s["fulfillmentMode"] == "DIRECT" {
				desc = fmt.Sprintf("Driver tiba di '%s'", cp["name"])
			}
		}
		if err := r.tracking(s, "IN_TRANSPORT", desc); err != nil {
			return err
		}
	}
	return r.syncReturnTransport(t["id"], "DEPARTED")
}
func (b *business) departTransport(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if t["status"] != "PLANNED" {
		return nil, problem(422, fmt.Sprintf("Transport berstatus %s, hanya PLANNED yang bisa depart.", t["status"]))
	}
	if len(transportShips(t)) == 0 {
		var returning bool
		if err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "ReturnTask" WHERE "returnTransportId"=$1)`, t["id"]).Scan(&returning); err != nil {
			return nil, err
		}
		if !returning {
			return nil, problem(422, "Transport belum memiliki shipment.")
		}
	}
	if t["routeId"] == nil {
		return nil, problem(422, "Rute wajib dipilih sebelum transport berangkat - lengkapi data transport terlebih dahulu.")
	}
	if t["plannedDepartureAt"] == nil || t["plannedArrivalAt"] == nil {
		return nil, problem(422, "Rencana Berangkat & Rencana Tiba wajib diisi sebelum transport berangkat - lengkapi jadwal transport terlebih dahulu.")
	}
	if t["trackingMode"] != "ACTIVITY" {
		cps := checkpoints(t, false)
		checked := false
		records, _ := t["checkpointRecords"].([]map[string]any)
		for _, cr := range records {
			checked = checked || len(cps) > 0 && cr["checkpointId"] == cps[0]["id"]
		}
		if !checked {
			return nil, problem(422, "Transport hanya dapat berangkat setelah check-in di checkpoint pertama.")
		}
	}
	updated, err := r.updateRow("Transport", idValue(t["id"]), map[string]any{"status": "DEPARTED", "departedAt": time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	if err = r.departLoadedShipments(t, nil); err != nil {
		return nil, err
	}
	b.audit(r, "status_change", "transport", t["id"], textValue(t["transportCode"])+" → DEPARTED", nil, nil)
	return updated, nil
}
func (r *request) destinationWarehouse(s map[string]any) (any, error) {
	if s["destinationWarehouseId"] != nil {
		return s["destinationWarehouseId"], nil
	}
	w, err := r.one(`SELECT jsonb_build_object('id',id) FROM "Warehouse" WHERE "isActive" AND lower(trim(city))=lower(trim($1)) ORDER BY id LIMIT 1`, s["destination"])
	if err != nil {
		return nil, err
	}
	return object(w)["id"], nil
}
func (r *request) receiveTransportShipment(t, ts map[string]any, direct, forceDirect bool, description string) error {
	s := object(ts["master"])
	// Reload under the shipment lock; receipt/undo may have run while we waited.
	fresh, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s WHERE id=$1 FOR UPDATE`, s["id"])
	if err != nil {
		return err
	}
	if fresh == nil {
		return problem(409, "Shipment berubah. Muat ulang data.")
	}
	s = fresh
	warehouse, err := r.destinationWarehouse(s)
	if err != nil {
		return err
	}
	event := "AT_DEST_GUDANG"
	if direct {
		event = "ARRIVED_AT_GUDANG"
	}
	if direct && (forceDirect || s["status"] == "IN_TRANSPORT") {
		if _, err = r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": event, "arrivedWarehouseId": warehouse, "destReceivedAt": time.Now().UTC()}); err != nil {
			return err
		}
		exists, e := r.one(`SELECT jsonb_build_object('id',id) FROM "Delivery" WHERE "masterId"=$1 LIMIT 1`, s["id"])
		if e != nil {
			return e
		}
		if exists == nil && t["driverId"] != nil {
			code, e := r.nextDeliveryCode()
			if e != nil {
				return e
			}
			if _, e = r.insertRow("Delivery", map[string]any{"deliveryCode": code, "masterId": s["id"], "kurirId": t["driverId"], "status": "ASSIGNED", "notes": "Auto-assigned dari transport " + textValue(t["transportCode"]) + " (DIRECT)"}); e != nil {
				return e
			}
		}
	} else if !direct && s["status"] == "IN_TRANSPORT" {
		if _, err = r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": event, "arrivedWarehouseId": warehouse, "destReceivedAt": nil}); err != nil {
			return err
		}
	}
	return r.tracking(s, event, description)
}
func (r *request) markTransportArrived(t map[string]any, final map[string]any) (map[string]any, error) {
	now := time.Now().UTC()
	updated, err := r.updateRow("Transport", idValue(t["id"]), map[string]any{"status": "ARRIVED", "arrivedAt": now, "statusBeforeArrival": t["status"]})
	if err != nil {
		return nil, err
	}
	for _, ts := range transportShips(t) {
		if ts["dropStatus"] != "LOADED" && ts["dropStatus"] != "AT_DROP_POINT" {
			continue
		}
		direct := object(ts["master"])["fulfillmentMode"] == "DIRECT" && (final != nil || t["trackingMode"] == "ACTIVITY")
		desc := fmt.Sprintf("Transport %s tiba di gudang tujuan - paket menunggu scan penerimaan Admin Gudang sebelum berstatus Arrived", t["transportCode"])
		if direct {
			desc = fmt.Sprintf("Transport %s tiba di tujuan", t["transportCode"])
			if final != nil {
				desc = fmt.Sprintf("Driver tiba di '%s'", final["name"])
			}
		}
		if err = r.receiveTransportShipment(t, ts, direct, true, desc); err != nil {
			return nil, err
		}
	}
	if err = r.exec(`UPDATE "TransportShipment" SET "dropStatus"='DROPPED',"droppedAt"=$2,"droppedById"=$3 WHERE "transportId"=$1 AND "dropStatus" IN ('LOADED','AT_DROP_POINT')`, t["id"], now, r.actor["id"]); err != nil {
		return nil, err
	}
	return updated, r.syncReturnTransport(t["id"], "ARRIVED")
}
func (b *business) arriveTransport(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if t["trackingMode"] == "ACTIVITY" && t["status"] != "DEPARTED" {
		return nil, problem(422, "Transport ACTIVITY harus berangkat sebelum diselesaikan.")
	}
	if t["status"] != "PLANNED" && t["status"] != "DEPARTED" {
		return nil, problem(422, fmt.Sprintf("Transport berstatus %s, hanya PLANNED atau DEPARTED yang bisa diselesaikan.", t["status"]))
	}
	without := t["trackingMode"] == "ACTIVITY" && hasPermission(r.actor, "transport.arrive_without_photo")
	if t["trackingMode"] == "ACTIVITY" && !without {
		evidence, e := r.transportEvidence(t, true, "Bukti tiba di tujuan")
		if e != nil {
			return nil, e
		}
		if _, e = r.saveTransportActivity(t, evidence); e != nil {
			return nil, e
		}
	}
	updated, err := r.markTransportArrived(t, nil)
	if err != nil {
		return nil, err
	}
	action := "status_change"
	if t["status"] == "PLANNED" {
		action = "status_change_bypass"
	}
	b.audit(r, action, "transport", t["id"], textValue(t["transportCode"])+" → ARRIVED", nil, map[string]any{"status": "ARRIVED", "withoutPhoto": without})
	return updated, nil
}
func actorRole(user map[string]any) string {
	if truthy(user["isOwner"]) {
		return "Owner"
	}
	names := []string{}
	for _, role := range objects(user["roles"]) {
		names = append(names, textValue(role["name"]))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
func (b *business) completeTransport(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if t["status"] != "ARRIVED" {
		return nil, problem(409, "Hanya transport ARRIVED yang dapat diaudit dan dikonfirmasi Completed.")
	}
	if t["completedAt"] != nil {
		return nil, problem(409, "Status transport telah berubah. Muat ulang sebelum mengonfirmasi audit.")
	}
	data := map[string]any{"status": "COMPLETED", "completedAt": time.Now().UTC(), "completedById": r.actor["id"], "completedByUsername": r.actor["username"], "completedByRole": actorRole(r.actor)}
	updated, err := r.updateRow("Transport", idValue(t["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "complete", "transport", t["id"], textValue(t["transportCode"])+" → COMPLETED", map[string]any{"status": "ARRIVED"}, data)
	out := map[string]any{"id": t["id"]}
	for key := range data {
		out[key] = updated[key]
	}
	return out, nil
}
func (b *business) undoTransportArrival(r *request) (any, error) {
	if !truthy(r.actor["isOwner"]) {
		return nil, problem(403, "Hanya Owner yang boleh membatalkan status tiba.")
	}
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if t["status"] != "ARRIVED" {
		return nil, problem(422, "Hanya transport berstatus ARRIVED yang dapat dibatalkan tiba.")
	}
	previous := t["statusBeforeArrival"]
	if previous == nil {
		previous = "PLANNED"
		if t["departedAt"] != nil || t["trackingMode"] == "ACTIVITY" {
			previous = "DEPARTED"
		}
	}
	if previous != "PLANNED" && previous != "DEPARTED" {
		return nil, problem(409, "Status sebelum tiba tidak valid.")
	}
	updated, err := r.updateRow("Transport", idValue(t["id"]), map[string]any{"status": previous, "arrivedAt": nil, "statusBeforeArrival": nil})
	if err != nil {
		return nil, err
	}
	if err = r.syncReturnTransport(t["id"], textValue(previous)); err != nil {
		return nil, err
	}
	b.audit(r, "undo_arrival", "transport", t["id"], textValue(t["transportCode"]), map[string]any{"status": t["status"], "arrivedAt": t["arrivedAt"]}, map[string]any{"status": previous, "arrivedAt": nil})
	return updated, nil
}
func (b *business) printTransport(r *request) (any, error) {
	t, err := r.transport(false)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, role := range objects(r.actor["roles"]) {
		names = append(names, textValue(role["name"]))
	}
	if truthy(r.actor["isOwner"]) {
		names = []string{"Owner"}
	}
	b.audit(r, "printed_manifest", "transport", t["id"], textValue(t["transportCode"]), nil, map[string]any{"printedAt": time.Now().UTC(), "accountName": r.actor["name"], "roles": names})
	return map[string]any{"logged": true}, nil
}
func (r *request) transportEvidence(t map[string]any, required bool, label string) (map[string]any, error) {
	if required && r.body["capturedAt"] == nil {
		return nil, problem(422, "Waktu pengambilan foto wajib berupa timestamp dengan zona waktu.")
	}
	meta, hasLocation, err := parseHandoverEvidence(r.body, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if required && !hasLocation {
		return nil, problem(422, "Koordinat GPS tidak valid.")
	}
	photo, err := optionalProof(r.body["photo"])
	if err != nil {
		return nil, err
	}
	if photo == nil {
		return nil, problem(422, "Foto aktivitas wajib disertakan.")
	}
	if hasLocation && textValue(meta["locationLabel"]) == "" {
		meta["locationLabel"] = nullable(reverseEvidenceAddress(r.ctx, number(r.body["latitude"]), number(r.body["longitude"])))
	}
	driver, err := r.employeeBrief(t["driverId"], false)
	if err != nil {
		return nil, err
	}
	route := object(t["route"])
	v := object(t["vehicle"])
	url, err := processEvidencePhoto(r.ctx, textValue(photo), hasLocation, meta, evidenceCaption{Company: r.company, Label: label + " · " + textValue(t["transportCode"]), Origin: textValue(fallback(t["origin"], route["origin"])), Destination: textValue(fallback(t["destination"], route["destination"])), Driver: textValue(object(driver)["name"]), RecordedBy: textValue(r.actor["name"]), VehiclePlate: textValue(v["vehicleNumber"]), VehicleDescription: textValue(v["name"]), ShipmentCount: len(transportShips(t))})
	if err != nil {
		return nil, problem(422, "Foto bukti tidak dapat diproses. Ambil ulang foto.")
	}
	for _, key := range []string{"captureLatitude", "captureLongitude", "captureAccuracy"} {
		delete(meta, key)
	}
	meta["photoUrl"], meta["latitude"], meta["longitude"], meta["locationAccuracy"] = url, r.body["latitude"], r.body["longitude"], r.body["locationAccuracy"]
	return meta, nil
}
func (r *request) saveTransportActivity(t, data map[string]any) (map[string]any, error) {
	activity, err := r.one(`INSERT INTO "TransportActivity" AS a ("transportId","photoUrl",latitude,longitude,"locationAccuracy","capturedAt","locationLabel","timeZone","mockLocation","recordedById","recordedByUsername") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING to_jsonb(a)`, t["id"], data["photoUrl"], data["latitude"], data["longitude"], data["locationAccuracy"], data["capturedAt"], data["locationLabel"], data["timeZone"], data["mockLocation"], r.actor["id"], r.actor["username"])
	if err != nil {
		return nil, err
	}
	err = r.exec(`UPDATE "Transport" SET "currentLatitude"=$2,"currentLongitude"=$3,"lastLocationAt"=$4,"updatedAt"=NOW() WHERE id=$1 AND ("lastLocationAt" IS NULL OR "lastLocationAt"<=$4)`, t["id"], data["latitude"], data["longitude"], data["capturedAt"])
	return activity, err
}
func (b *business) transportActivities(r *request) (any, error) {
	t, err := r.transport(false)
	if err != nil {
		return nil, err
	}
	if t["trackingMode"] != "ACTIVITY" {
		return nil, problem(422, "Transport ini tidak menggunakan activity tracking.")
	}
	return r.many(`SELECT to_jsonb(a) FROM "TransportActivity" a WHERE "transportId"=$1 ORDER BY "capturedAt" DESC,id DESC`, t["id"])
}
func (b *business) createTransportActivity(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if !crew(r.actor, t) {
		return nil, problem(403, "Hanya driver/kenek yang ditugaskan yang boleh mencatat aktivitas.")
	}
	if t["trackingMode"] != "ACTIVITY" {
		return nil, problem(422, "Transport ini tidak mendukung activity tracking.")
	}
	if t["status"] != "PLANNED" && t["status"] != "DEPARTED" {
		return nil, problem(422, "Aktivitas hanya untuk transport PLANNED atau DEPARTED.")
	}
	data, err := r.transportEvidence(t, true, "Aktivitas transport")
	if err != nil {
		return nil, err
	}
	activity, err := r.saveTransportActivity(t, data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "activity", "transport", t["id"], textValue(t["transportCode"])+" · aktivitas", nil, map[string]any{"activityId": activity["id"], "capturedAt": data["capturedAt"], "hasPhoto": true})
	return activity, nil
}
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	a := math.Pow(math.Sin((lat2-lat1)*rad/2), 2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Pow(math.Sin((lon2-lon1)*rad/2), 2)
	return math.Round(2 * 6371000 * math.Asin(math.Sqrt(math.Min(1, a))))
}
func kmDisplay(meters float64) float64 { return math.Round(meters/10) / 100 }
func (b *business) transportCheckin(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if t["trackingMode"] == "ACTIVITY" {
		return nil, problem(422, "Gunakan activity tracking untuk transport ini, bukan checkpoint check-in.")
	}
	if !hasPermission(r.actor, "*") && !crew(r.actor, t) {
		return nil, problem(403, "Hanya driver/kenek yang ditugaskan pada transport ini yang boleh check-in.")
	}
	if t["status"] != "PLANNED" && t["status"] != "DEPARTED" {
		return nil, problem(422, fmt.Sprintf("Check-in hanya untuk transport PLANNED atau DEPARTED (saat ini: %s).", t["status"]))
	}
	cps := checkpoints(t, false)
	var cp map[string]any
	for _, c := range cps {
		if idValue(c["id"]) == idValue(r.body["checkpointId"]) && c["isActive"] == true {
			cp = c
		}
	}
	if cp == nil {
		return nil, problem(422, "Checkpoint tidak ditemukan pada rute transport ini.")
	}
	data, err := r.transportEvidence(t, false, "Check-in "+textValue(cp["name"]))
	if err != nil {
		return nil, err
	}
	if data["latitude"] == nil || data["longitude"] == nil {
		return nil, problem(422, "Koordinat GPS tidak valid.")
	}
	distance := haversineMeters(number(data["latitude"]), number(data["longitude"]), number(cp["latitude"]), number(cp["longitude"]))
	if distance > number(cp["radiusMeters"]) {
		return nil, fieldProblem(fmt.Sprintf("Jarak Anda %g KM dari checkpoint - radius validasi hanya %g KM. Mendekatlah ke checkpoint lalu ulangi check-in.", kmDisplay(distance), kmDisplay(number(cp["radiusMeters"]))), "distance", fmt.Sprintf("distance %g m > radius %g m", distance, number(cp["radiusMeters"])))
	}
	var recent bool
	if err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "CheckpointRecord" WHERE "transportId"=$1 AND "checkpointId"=$2 AND "recordedById"=$3 AND "recordedAt">=NOW()-INTERVAL '2 minutes')`, t["id"], cp["id"], r.actor["id"]).Scan(&recent); err != nil {
		return nil, err
	}
	if recent {
		return nil, problem(422, "Anda baru saja check-in di checkpoint ini - tunggu sebentar sebelum check-in ulang.")
	}
	record, err := r.one(`INSERT INTO "CheckpointRecord" AS cr ("transportId","checkpointId",latitude,longitude,"locationAccuracy","withinRadius","distanceMeters","photoUrl","locationLabel","timeZone","capturedAt","mockLocation","recordedById") VALUES ($1,$2,$3,$4,$5,true,$6,$7,$8,$9,$10,$11,$12) RETURNING to_jsonb(cr)`, t["id"], cp["id"], data["latitude"], data["longitude"], data["locationAccuracy"], distance, data["photoUrl"], data["locationLabel"], data["timeZone"], data["capturedAt"], data["mockLocation"], r.actor["id"])
	if err != nil {
		return nil, err
	}
	if err = r.exec(`UPDATE "TransportShipment" SET "dropStatus"='AT_DROP_POINT' WHERE "transportId"=$1 AND "dropCheckpointId"=$2 AND "dropStatus"='LOADED' AND $3='MULTI_DROP'`, t["id"], cp["id"], t["transportMode"]); err != nil {
		return nil, err
	}
	if _, err = r.updateRow("Transport", idValue(t["id"]), map[string]any{"currentLatitude": data["latitude"], "currentLongitude": data["longitude"], "lastLocationAt": time.Now().UTC()}); err != nil {
		return nil, err
	}
	if len(cps) > 0 && cp["id"] == cps[0]["id"] && t["status"] == "PLANNED" {
		if _, err = r.updateRow("Transport", idValue(t["id"]), map[string]any{"status": "DEPARTED", "departedAt": time.Now().UTC()}); err != nil {
			return nil, err
		}
		if err = r.departLoadedShipments(t, cp); err != nil {
			return nil, err
		}
		t["status"] = "DEPARTED"
	}
	auto := len(cps) > 0 && cp["id"] == cps[len(cps)-1]["id"] && t["status"] == "DEPARTED"
	if auto {
		if _, err = r.markTransportArrived(t, cp); err != nil {
			return nil, err
		}
		b.audit(r, "status_change", "transport", t["id"], textValue(t["transportCode"])+" → ARRIVED (auto: check-in checkpoint akhir)", nil, nil)
	}
	b.audit(r, "checkin", "transport", t["id"], textValue(t["transportCode"])+" · "+textValue(cp["name"]), nil, map[string]any{"checkpoint": cp["name"], "distanceMeters": distance, "withinRadius": true, "hasPhoto": true})
	rec := map[string]any{}
	for _, key := range []string{"id", "checkpointId", "latitude", "longitude", "distanceMeters", "withinRadius", "recordedAt"} {
		rec[key] = record[key]
	}
	rec["checkpointName"] = cp["name"]
	message := fmt.Sprintf("Check-in berhasil di %s (jarak %g KM, radius %g KM).", cp["name"], kmDisplay(distance), kmDisplay(number(cp["radiusMeters"])))
	if auto {
		message = fmt.Sprintf("Check-in berhasil di %s - transport %s OTOMATIS ditandai ARRIVED (checkpoint akhir tercapai).", cp["name"], t["transportCode"])
	}
	return map[string]any{"record": rec, "distanceKm": kmDisplay(distance), "radiusKm": kmDisplay(number(cp["radiusMeters"])), "autoArrived": auto, "message": message}, nil
}
