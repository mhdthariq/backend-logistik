package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"novalogistik/backend/internal/pricing"
)

// This counter uses the same CodeSequence key as Node. Allocation and all package
// inserts share a transaction so failures never leave partially created packages.
func (r *request) detailCodes(count int) ([]string, error) {
	now := time.Now()
	date := now.Format("20060102")
	var next int
	err := r.db.QueryRow(r.ctx, `INSERT INTO "CodeSequence" (key,"nextValue","updatedAt")
	 VALUES ($1, GREATEST(COALESCE((SELECT max(substring("detailCode" from '-([0-9]+)$')::int) FROM "DetailShipment" WHERE "detailCode" LIKE $2),0)+$3+1,$3+1),NOW())
	 ON CONFLICT (key) DO UPDATE SET "nextValue"="CodeSequence"."nextValue"+$3,"updatedAt"=NOW() RETURNING "nextValue"`, "detailShipment:daily:"+date, "DTL-"+date+"-%", count).Scan(&next)
	if err != nil {
		return nil, err
	}
	codes := make([]string, count)
	for i := range codes {
		codes[i] = fmt.Sprintf("DTL-%s-%s-%03d", date, now.Format("150405"), next-count+i)
	}
	return codes, nil
}

func quantity(raw any) (int, error) {
	n := pricing.Round(numericDefault(raw, float64(1)).(float64))
	// Bound expansion to keep requests within the server's resource budget.
	if n < 1 {
		return 0, fieldProblem("Jumlah paket harus minimal 1.", "quantity", "Jumlah paket harus minimal 1.")
	}
	if n > 10000 {
		return 0, fieldProblem("Jumlah paket harus antara 1 dan 10000.", "quantity", "Jumlah paket harus antara 1 dan 10000.")
	}
	return int(n), nil
}

func (r *request) insertDetails(master map[string]any, input map[string]any) ([]map[string]any, error) {
	description, err := required(input, "description")
	if err != nil {
		return nil, err
	}
	count, err := quantity(input["quantity"])
	if err != nil {
		return nil, err
	}
	codes, err := r.detailCodes(count)
	if err != nil {
		return nil, err
	}
	// One SQL insert avoids a round trip per physical package.
	return r.many(`INSERT INTO "DetailShipment" AS d ("detailCode","masterId",description,"lengthCm","widthCm","heightCm","volumeM3","actualWeightKg","updatedAt")
	 SELECT code,$2,$3,$4,$5,$6,$7,$8,NOW() FROM unnest($1::text[]) code RETURNING to_jsonb(d)`, codes, master["id"], description, numeric(input["lengthCm"]), numeric(input["widthCm"]), numeric(input["heightCm"]), numeric(input["volumeM3"]), numericDefault(input["actualWeightKg"], float64(0)))
}

func (b *business) shipmentDetails(r *request) (any, error) {
	s, err := r.shipment(false)
	if err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=$1 ORDER BY id`, s["id"])
}

func (b *business) createShipmentDetails(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	if !detailEditable(s) {
		return nil, problem(422, "Detail hanya bisa ditambah saat status CREATED atau READY_FOR_PICKUP.")
	}
	rows, err := r.insertDetails(s, r.body)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "shipment_detail", rows[0]["id"], textValue(s["masterCode"]), nil, map[string]any{"master": s["masterCode"], "description": rows[0]["description"], "packages": len(rows), "codes": []any{rows[0]["detailCode"], rows[len(rows)-1]["detailCode"]}})
	return map[string]any{"created": len(rows), "details": rows}, nil
}

func detailChanges(body map[string]any, strict bool) (map[string]any, error) {
	data := map[string]any{}
	if v, exists := body["description"]; exists {
		if strict {
			if _, ok := v.(string); !ok {
				return nil, fieldProblem("Description wajib diisi.", "description", "Description wajib diisi.")
			}
		}
		value, err := required(body, "description")
		if err != nil {
			return nil, err
		}
		data["description"] = value
	}
	for _, field := range []string{"lengthCm", "widthCm", "heightCm", "volumeM3", "actualWeightKg"} {
		v, exists := body[field]
		if !exists {
			continue
		}
		if strict && (v != nil || field == "actualWeightKg") {
			n, ok := v.(float64)
			if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n < 0 {
				msg := field + " harus berupa angka finite nonnegatif."
				return nil, fieldProblem(msg, field, msg)
			}
		}
		data[field] = numeric(v)
		if field == "actualWeightKg" && data[field] == nil {
			data[field] = float64(0)
		}
	}
	if strict && len(data) == 0 {
		return nil, fieldProblem("Tidak ada field detail untuk diubah.", "body", "Tidak ada field detail untuk diubah.")
	}
	return data, nil
}

func (b *business) mutateShipmentDetail(r *request, deleting bool) (any, error) {
	d, err := r.find("DetailShipment", "Detail shipment tidak ditemukan.", false)
	if err != nil {
		return nil, err
	}
	s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s WHERE id=$1 FOR UPDATE`, d["masterId"])
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, problem(404, "Detail shipment tidak ditemukan.")
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, err
	}
	verb := "diubah"
	if deleting {
		verb = "dihapus"
	}
	if !detailEditable(s) {
		return nil, problem(422, "Detail hanya bisa "+verb+" saat master masih CREATED atau READY_FOR_PICKUP.")
	}
	d, err = r.find("DetailShipment", "Detail shipment tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	before := map[string]any{}
	for key, value := range d {
		before[key] = value
	}
	before["master"] = s
	if deleting {
		if err = r.exec(`DELETE FROM "DetailShipment" WHERE id=$1`, d["id"]); err != nil {
			return nil, err
		}
		b.audit(r, "deleted", "shipment_detail", d["id"], textValue(d["detailCode"]), before, nil)
		return map[string]any{"deleted": true}, nil
	}
	data, err := detailChanges(r.body, false)
	if err != nil {
		return nil, err
	}
	after, err := r.updateRow("DetailShipment", idValue(d["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "shipment_detail", d["id"], textValue(d["detailCode"]), before, after)
	return after, nil
}

func (b *business) updateShipmentDetail(r *request) (any, error) {
	return b.mutateShipmentDetail(r, false)
}
func (b *business) deleteShipmentDetail(r *request) (any, error) {
	return b.mutateShipmentDetail(r, true)
}
func (b *business) updateShipmentDetails(r *request) (any, error) {
	return b.batchShipmentDetails(r, false)
}
func (b *business) deleteShipmentDetails(r *request) (any, error) {
	return b.batchShipmentDetails(r, true)
}

func (b *business) batchShipmentDetails(r *request, deleting bool) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	if idValue(r.c.Params("id")) == 0 {
		return nil, fieldProblem("Shipment ID tidak valid.", "id", "Shipment ID tidak valid.")
	}
	var body map[string]any
	if err := json.Unmarshal(r.c.Body(), &body); err != nil || body == nil {
		return nil, fieldProblem("Body harus berupa object JSON.", "body", "Body harus berupa object JSON.")
	}
	list, ok := body["detailIds"].([]any)
	valid := ok && len(list) > 0
	seen := map[int]bool{}
	detailIDs := []int{}
	for _, raw := range list {
		_, number := raw.(float64)
		id := idValue(raw)
		if !number || id == 0 || seen[id] {
			valid = false
		}
		seen[id] = true
		detailIDs = append(detailIDs, id)
	}
	if !valid {
		msg := "detailIds harus berisi ID integer positif yang unik dan tidak kosong."
		return nil, fieldProblem(msg, "detailIds", msg)
	}
	data := map[string]any{}
	var err error
	if !deleting {
		data, err = detailChanges(body, true)
		if err != nil {
			return nil, err
		}
	}
	// Every Go shipment mutation locks the parent first, serializing state checks,
	// detail edits and deletes. Row locks also protect against legacy child writes.
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	if !detailEditable(s) {
		return nil, problem(422, "Detail hanya bisa diubah atau dihapus saat master masih CREATED atau READY_FOR_PICKUP.")
	}
	before, err := r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=$1 AND id=ANY($2::int[]) ORDER BY id FOR UPDATE`, s["id"], detailIDs)
	if err != nil {
		return nil, err
	}
	if len(before) != len(list) {
		msg := "Semua detailIds harus milik shipment ini dan masih tersedia."
		return nil, fieldProblem(msg, "detailIds", msg)
	}
	after := []map[string]any{}
	for _, d := range before {
		if deleting {
			tag, err := r.db.Exec(r.ctx, `DELETE FROM "DetailShipment" WHERE id=$1`, d["id"])
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() != 1 {
				return nil, problem(409, "Detail berubah bersamaan. Silakan coba lagi.")
			}
			b.audit(r, "deleted", "shipment_detail", d["id"], textValue(d["detailCode"]), d, nil)
		} else {
			row, err := r.updateRow("DetailShipment", idValue(d["id"]), data)
			if err != nil {
				return nil, err
			}
			if row == nil {
				return nil, problem(409, "Detail berubah bersamaan. Silakan coba lagi.")
			}
			after = append(after, row)
			b.audit(r, "updated", "shipment_detail", d["id"], textValue(d["detailCode"]), d, row)
		}
	}
	if deleting {
		return map[string]any{"deleted": len(before)}, nil
	}
	return map[string]any{"updated": len(before), "details": after}, nil
}
