package httpapi

import (
	"fmt"
	"strings"

	"novalogistik/backend/internal/pricing"
)

func optionalProof(v any) (any, error) {
	if v == nil || v == "" {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, problem(422, "Format foto tidak valid.")
	}
	if !strings.HasPrefix(s, "data:image/jpeg;base64,") && !strings.HasPrefix(s, "data:image/png;base64,") {
		return nil, problem(422, "Format foto tidak didukung - gunakan JPEG atau PNG.")
	}
	if len(s) > 2_500_000 {
		return nil, problem(422, "Foto terlalu besar (maks ±2.5 MB setelah kompresi).")
	}
	return s, nil
}

func (b *business) shipmentPayments(r *request) (any, error) {
	s, err := r.shipment(false)
	if err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(p)||jsonb_build_object('recordedBy',CASE WHEN u.id IS NULL THEN NULL ELSE jsonb_build_object('name',u.name) END,'verifiedBy',CASE WHEN v.id IS NULL THEN NULL ELSE jsonb_build_object('name',v.name) END) FROM "Payment" p LEFT JOIN "User" u ON u.id=p."recordedById" LEFT JOIN "User" v ON v.id=p."verifiedById" WHERE p."masterId"=$1 ORDER BY p."createdAt" DESC,p.id DESC`, s["id"])
}

func (b *business) recordShipmentPayment(r *request) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	canRecord := hasPermission(r.actor, "payment.record")
	if !canRecord && (r.actor["partnerType"] == "MARKETING" || !hasRole(r.actor, "admin-gudang")) {
		return nil, problem(403, "Missing permission: payment.record")
	}
	if idValue(r.c.Params("id")) == 0 {
		return nil, problem(422, "ID shipment tidak valid.")
	}
	method := r.body["method"]
	if method != "CASH" && method != "TRANSFER" {
		return nil, problem(422, "Metode pembayaran harus CASH atau TRANSFER.")
	}
	amount := numeric(r.body["amount"])
	if amount == nil || number(amount) < 1 {
		return nil, fieldProblem("The amount field must be a valid number ≥ 1.", "amount", "The amount field must be a valid number >= 1.")
	}
	proof, err := optionalProof(r.body["proof"])
	if err != nil {
		return nil, err
	}
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	c, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	if !canRecord && c["type"] != "b2c" {
		return nil, problem(403, "Admin Gudang hanya dapat mencatat pembayaran B2C.")
	}
	if c["type"] == "b2b" {
		return nil, problem(422, "Pembayaran B2B melalui invoice, bukan per shipment.")
	}
	if s["status"] == "CANCELLED" {
		return nil, problem(422, "Shipment dibatalkan; pembayaran tidak dapat dicatat.")
	}
	total := s["finalPriceAmount"]
	if total == nil {
		total = s["priceAmount"]
	}
	if s["priceAmount"] == nil || number(total) <= 0 {
		return nil, problem(422, "Hitung harga shipment terlebih dahulu sebelum mencatat pembayaran.")
	}
	var paid float64
	if err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(amount),0) FROM "Payment" WHERE "masterId"=$1 AND status='VERIFIED'`, s["id"]).Scan(&paid); err != nil {
		return nil, err
	}
	if number(amount) > number(total)-paid {
		return nil, problem(422, "Pembayaran melebihi sisa tagihan.")
	}
	p, err := r.one(`INSERT INTO "Payment" AS p ("masterId",method,amount,status,reference,"proofUrl","recordedById") VALUES ($1,$2,$3,'RECORDED',$4,$5,$6) RETURNING to_jsonb(p)`, s["id"], method, amount, nullable(r.body["reference"]), proof, r.actor["id"])
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "payment", p["id"], fmt.Sprintf("%s · Rp%s", s["masterCode"], rupiahNumber(number(amount))), nil, p)
	return p, nil
}

func (b *business) notifyMarketing(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	summary, err := r.paymentSummary(s)
	if err != nil {
		return nil, err
	}
	remaining := number(summary["remainingAmount"])
	if remaining <= 0 {
		return nil, problem(422, "Shipment ini sudah lunas - tidak perlu notifikasi.")
	}
	note := textValue(r.body["note"])
	if note == "" {
		note = "Mohon follow-up ke customer apakah kiriman tetap diproses."
	}
	if err = r.tracking(s, "GUDANG_NOTIFY_MARKETING", fmt.Sprintf("Gudang memberi tahu marketing: sisa pembayaran Rp%s - %s", rupiahNumber(pricing.Round(remaining)), note)); err != nil {
		return nil, err
	}
	b.audit(r, "notified_marketing", "shipment", s["id"], fmt.Sprintf("%s · sisa Rp%.0f", s["masterCode"], pricing.Round(remaining)), nil, map[string]any{"note": note})
	return map[string]any{"notified": true, "remainingAmount": remaining}, nil
}
