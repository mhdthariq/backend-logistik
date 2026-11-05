package httpapi

import (
	"fmt"
	"time"
)

var expenseTypes = []string{"BONGKAR", "PARKIR", "MAKAN", "BBM", "ANGKUT", "TOL"}

func validateTransportExpense(kind, amount any) error {
	valid := false
	for _, typ := range expenseTypes {
		valid = valid || kind == typ
	}
	if !valid {
		return fieldProblem("Jenis biaya harus salah satu dari: BONGKAR, PARKIR, MAKAN, BBM, ANGKUT, TOL.", "type", "Jenis biaya tidak valid.")
	}
	if numeric(amount) == nil {
		return fieldProblem("Nominal biaya wajib diisi.", "type", "Nominal biaya wajib diisi.")
	}
	if number(amount) <= 0 {
		return fieldProblem("Nominal biaya harus lebih dari 0.", "type", "Nominal biaya harus lebih dari 0.")
	}
	return nil
}
func expensePhotos(e map[string]any) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	add(textValue(e["photoUrl"]))
	if list, ok := e["photoUrls"].([]any); ok {
		for _, v := range list {
			add(textValue(v))
		}
	}
	if list, ok := e["photoUrls"].([]string); ok {
		for _, v := range list {
			add(v)
		}
	}
	return out
}
func expenseWithoutPhotos(e map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range e {
		if k != "photoUrl" && k != "photoUrls" {
			out[k] = v
		}
	}
	count := len(expensePhotos(e))
	out["hasPhoto"], out["photoCount"] = count > 0, count
	return out
}
func (b *business) transportExpenses(r *request) (any, error) {
	t, err := r.transport(false)
	if err != nil {
		return nil, err
	}
	expenses, err := r.many(`SELECT to_jsonb(e) FROM "TransportExpense" e WHERE "transportId"=$1 ORDER BY "createdAt" DESC,id DESC`, t["id"])
	if err != nil {
		return nil, err
	}
	byType := map[string]any{}
	for _, kind := range expenseTypes {
		byType[kind] = map[string]any{"count": 0, "total": 0.0}
	}
	count, total := 0, 0.0
	rows := []map[string]any{}
	for _, e := range expenses {
		rows = append(rows, expenseWithoutPhotos(e))
		bucket, valid := byType[textValue(e["type"])].(map[string]any)
		if e["status"] == "APPROVED" && valid {
			bucket["count"] = bucket["count"].(int) + 1
			bucket["total"] = bucket["total"].(float64) + number(e["amount"])
			count++
			total += number(e["amount"])
		}
	}
	return Envelope{Data: rows, Meta: map[string]any{"summary": map[string]any{"byType": byType, "count": count, "total": total}}}, nil
}
func (r *request) loadTransportExpense(lock bool) (map[string]any, map[string]any, error) {
	t, err := r.transport(lock)
	if err != nil {
		return nil, nil, err
	}
	id := idValue(r.c.Params("expenseId"))
	if id == 0 {
		return nil, nil, problem(422, "ID biaya atau transport tidak valid.")
	}
	e, err := r.one(`SELECT to_jsonb(e) FROM "TransportExpense" e WHERE id=$1 AND "transportId"=$2`+lockClause(lock, "e"), id, t["id"])
	if err != nil {
		return nil, nil, err
	}
	if e == nil {
		return nil, nil, problem(404, "Biaya tidak ditemukan.")
	}
	return e, t, nil
}
func (b *business) transportExpense(r *request) (any, error) {
	e, _, err := r.loadTransportExpense(false)
	return e, err
}
func validateExpenseGPS(body map[string]any) (bool, error) {
	present := false
	for _, k := range []string{"latitude", "longitude", "locationAccuracy"} {
		if _, ok := body[k]; ok {
			present = true
		}
	}
	if !present {
		return false, nil
	}
	for _, f := range []struct {
		key, message string
		max          float64
	}{{"latitude", "GPS latitude wajib valid (-90 sampai 90).", 90}, {"longitude", "GPS longitude wajib valid (-180 sampai 180).", 180}, {"locationAccuracy", "Akurasi GPS wajib valid dan tidak negatif.", 0}} {
		n, ok := body[f.key].(float64)
		if !ok || numeric(n) == nil || f.max > 0 && (n > f.max || n < -f.max) || f.max == 0 && n < 0 {
			return false, fieldProblem(f.message, "location", f.message)
		}
	}
	return true, nil
}
func (r *request) expensePhotoData(existing map[string]any, stamp bool, meta map[string]any, t map[string]any) (map[string]any, error) {
	data := map[string]any{}
	if r.body["removePhoto"] == true {
		data["photoUrl"], data["photoUrls"] = nil, []string{}
	}
	photo, err := optionalProof(r.body["photo"])
	if err != nil {
		return nil, err
	}
	process := func(url string) (string, error) {
		driver, err := r.employeeBrief(t["driverId"], false)
		if err != nil {
			return "", err
		}
		v := object(t["vehicle"])
		result, err := processEvidencePhoto(r.ctx, url, stamp, meta, evidenceCaption{Company: r.company, Label: "Biaya " + textValue(fallback(r.body["type"], existing["type"])) + " · " + textValue(t["transportCode"]), Origin: textValue(t["origin"]), Destination: textValue(t["destination"]), Driver: textValue(object(driver)["name"]), RecordedBy: textValue(r.actor["name"]), VehiclePlate: textValue(v["vehicleNumber"]), VehicleDescription: textValue(v["name"]), ShipmentCount: len(transportShips(t)), MaxPhotoBytes: 10 * 1024 * 1024 / 8})
		if err != nil {
			return "", problem(422, "Foto bukti tidak dapat diproses. Ambil ulang foto.")
		}
		return result, nil
	}
	if photo != nil {
		data["photoUrl"], err = process(textValue(photo))
		if err != nil {
			return nil, err
		}
		data["photoUrls"] = []string{}
	} else if v, ok := r.body["photo"]; ok && v == nil {
		data["photoUrl"] = nil
		data["photoUrls"] = []string{}
	}
	if raw, ok := r.body["photos"]; ok {
		list, valid := raw.([]any)
		if !valid || len(list) > 8 {
			return nil, problem(422, "Maksimal 8 foto bukti per biaya.")
		}
		all := []string{}
		if r.body["removePhoto"] != true {
			all = expensePhotos(existing)
		}
		seen := map[string]bool{}
		for _, s := range all {
			seen[s] = true
		}
		for _, item := range list {
			photo, e := optionalProof(item)
			if e != nil {
				return nil, e
			}
			if photo == nil {
				return nil, problem(422, "Foto bukti tidak valid.")
			}
			s := textValue(photo)
			if !seen[s] {
				all = append(all, s)
				seen[s] = true
			}
		}
		size := 0
		for _, s := range all {
			size += len(s)
		}
		if len(all) > 8 {
			return nil, problem(422, "Maksimal 8 foto bukti per biaya.")
		}
		if size > 10*1024*1024 {
			return nil, problem(422, "Total ukuran foto bukti maksimal 10 MB.")
		}
		result := []string{}
		size = 0
		for _, s := range all {
			url, e := process(s)
			if e != nil {
				return nil, e
			}
			size += len(url)
			result = append(result, url)
		}
		if size > 10*1024*1024 {
			return nil, problem(422, "Total ukuran foto bukti maksimal 10 MB.")
		}
		data["photoUrls"] = result
		if existing != nil {
			data["photoUrl"] = nil
		}
	}
	return data, nil
}
func (b *business) createTransportExpense(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if t["status"] == "COMPLETED" {
		return nil, problem(409, "Transport sudah COMPLETED. Biaya operasional tidak dapat ditambahkan.")
	}
	if err = validateTransportExpense(r.body["type"], r.body["amount"]); err != nil {
		return nil, err
	}
	gps, err := validateExpenseGPS(r.body)
	if err != nil {
		return nil, err
	}
	meta, _, err := parseHandoverEvidence(r.body, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	data, err := r.expensePhotoData(nil, gps, meta, t)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"locationLabel", "timeZone", "capturedAt", "mockLocation"} {
		data[key] = meta[key]
	}
	for _, key := range []string{"latitude", "longitude", "locationAccuracy"} {
		data[key] = r.body[key]
	}
	code, err := r.nextCode("TransportExpense", "expenseCode", "EXP-2026-", 731027)
	if err != nil {
		return nil, err
	}
	data["expenseCode"], data["transportId"], data["type"], data["amount"], data["description"], data["createdById"], data["status"] = code, t["id"], r.body["type"], number(r.body["amount"]), nullable(r.body["description"]), r.actor["id"], "PENDING"
	e, err := r.insertRow("TransportExpense", data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "create", "transport_expense", e["id"], fmt.Sprintf("%s · %s Rp%g", t["transportCode"], e["type"], number(e["amount"])), nil, expenseWithoutPhotos(e))
	return expenseWithoutPhotos(e), nil
}
func (b *business) updateTransportExpense(r *request) (any, error) {
	e, t, err := r.loadTransportExpense(true)
	if err != nil {
		return nil, err
	}
	if t["status"] == "COMPLETED" {
		return nil, problem(409, "Transport sudah COMPLETED. Biaya operasional dan foto tidak dapat diubah.")
	}
	if e["status"] != "PENDING" {
		return nil, problem(409, "Biaya yang sudah disetujui tidak dapat diubah.")
	}
	kind, amount := e["type"], e["amount"]
	if v, ok := r.body["type"]; ok {
		kind = v
	}
	if v, ok := r.body["amount"]; ok {
		amount = v
	}
	if err = validateTransportExpense(kind, amount); err != nil {
		return nil, err
	}
	gps, err := validateExpenseGPS(r.body)
	if err != nil {
		return nil, err
	}
	data, err := r.expensePhotoData(e, false, nil, t)
	if err != nil {
		return nil, err
	}
	changed := len(data) > 0
	for _, key := range []string{"type", "amount", "description"} {
		if v, ok := r.body[key]; ok {
			switch key {
			case "amount":
				data[key] = number(v)
			case "description":
				data[key] = nullable(v)
			default:
				data[key] = textValue(v)
			}
		}
	}
	if gps {
		for _, key := range []string{"latitude", "longitude", "locationAccuracy"} {
			data[key] = r.body[key]
			changed = changed || r.body[key] != e[key]
		}
	}
	if changed {
		for _, key := range []string{"locationLabel", "timeZone", "capturedAt", "mockLocation"} {
			data[key] = nil
		}
	}
	updated, err := r.updateRow("TransportExpense", idValue(e["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "update", "transport_expense", e["id"], fmt.Sprintf("%s · %s", t["transportCode"], updated["type"]), expenseWithoutPhotos(e), expenseWithoutPhotos(updated))
	return expenseWithoutPhotos(updated), nil
}
func (b *business) approveTransportExpense(r *request) (any, error) {
	e, t, err := r.loadTransportExpense(true)
	if err != nil {
		return nil, err
	}
	if e["status"] != "PENDING" {
		return nil, problem(409, "Biaya sudah disetujui.")
	}
	approval := map[string]any{"status": "APPROVED", "approvedAt": time.Now().UTC(), "approvedById": r.actor["id"], "approvedByUsername": r.actor["username"], "approvedByRole": actorRole(r.actor)}
	updated, err := r.updateRow("TransportExpense", idValue(e["id"]), approval)
	if err != nil {
		return nil, err
	}
	b.audit(r, "approve", "transport_expense", e["id"], fmt.Sprintf("%s · %s → APPROVED", t["transportCode"], e["expenseCode"]), map[string]any{"status": e["status"]}, approval)
	return expenseWithoutPhotos(updated), nil
}
func (b *business) cancelTransportExpense(r *request) (any, error) {
	e, _, err := r.loadTransportExpense(true)
	if err != nil {
		return nil, err
	}
	return r.reverseShipmentExpense(e, "cancel")
}
func (b *business) deleteTransportExpense(r *request) (any, error) {
	e, _, err := r.loadTransportExpense(true)
	if err != nil {
		return nil, err
	}
	if e["status"] != "PENDING" {
		return r.reverseShipmentExpense(e, "remove")
	}
	if !hasPermission(r.actor, "transport.expense.delete") {
		return nil, problem(403, "Missing permission: transport.expense.delete")
	}
	if err = r.exec(`DELETE FROM "TransportExpense" WHERE id=$1`, e["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "delete", "transport_expense", e["id"], textValue(e["expenseCode"]), expenseWithoutPhotos(e), nil)
	return map[string]any{"deleted": true}, nil
}
