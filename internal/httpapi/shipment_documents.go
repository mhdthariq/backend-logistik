package httpapi

import "strings"

func (b *business) shipmentDocuments(r *request) (any, error) {
	s, err := r.shipment(false)
	if err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(d) FROM "ShipmentDocument" d WHERE "masterId"=$1 ORDER BY "createdAt" DESC`, s["id"])
}

func (b *business) createShipmentDocument(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	if s["status"] == "CANCELLED" {
		return nil, problem(422, "Shipment yang dibatalkan tidak dapat diberi dokumen.")
	}
	name, mime, data := textValue(r.body["fileName"]), textValue(r.body["mimeType"]), textValue(r.body["dataUrl"])
	if name == "" || mime == "" || data == "" {
		return nil, problem(422, "Nama file, tipe file, dan isi dokumen wajib diisi.")
	}
	allowed := mime == "application/pdf" || mime == "image/jpeg" || mime == "image/png" || mime == "image/webp"
	if !allowed || !strings.HasPrefix(data, "data:"+mime+";base64,") {
		return nil, problem(422, "Dokumen harus berupa PDF, JPEG, PNG, atau WebP.")
	}
	if len(data) > 12_000_000 {
		return nil, problem(422, "Dokumen terlalu besar (maksimal 8 MB).")
	}
	// ShipmentDocument has no updatedAt column, unlike most Prisma models.
	d, err := r.one(`INSERT INTO "ShipmentDocument" AS d ("masterId","fileName","mimeType","dataUrl") VALUES ($1,$2,$3,$4) RETURNING to_jsonb(d)`, s["id"], name, mime, data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "shipment_document", d["id"], textValue(s["masterCode"])+" · "+name, nil, map[string]any{"fileName": name, "mimeType": mime})
	return d, nil
}

func (b *business) deleteShipmentDocument(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	d, err := r.one(`DELETE FROM "ShipmentDocument" AS d WHERE id=$1 AND "masterId"=$2 RETURNING to_jsonb(d)`, idValue(r.c.Params("documentId")), s["id"])
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, problem(404, "Dokumen tidak ditemukan.")
	}
	b.audit(r, "deleted", "shipment_document", d["id"], textValue(s["masterCode"])+" · "+textValue(d["fileName"]), map[string]any{"fileName": d["fileName"], "mimeType": d["mimeType"]}, nil)
	return map[string]any{"deleted": true}, nil
}
