package httpapi

import (
	"fmt"
	"math"
	"slices"

	"github.com/gofiber/fiber/v3"
)

func (b *business) registerPayments(app *fiber.App) {
	app.Post("/api/v1/payments/:id/verify", b.handle("payment.verify", true, true, b.verifyPayment))
	app.Post("/api/v1/payments/:id/reject", b.handle("payment.verify", true, true, b.rejectPayment))
	app.Get("/api/v1/unpaid", b.handle("payment.unpaid.view", true, false, b.unpaid))
}

func (b *business) verifyPayment(r *request) (any, error) {
	return b.reviewPayment(r, true)
}

func (b *business) rejectPayment(r *request) (any, error) {
	return b.reviewPayment(r, false)
}

func (b *business) reviewPayment(r *request, verify bool) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	p, err := r.find("Payment", "Payment tidak ditemukan.", false)
	if err != nil {
		return nil, err
	}
	// Match other Go payment writers: lock the shipment before its payment.
	s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s WHERE id=$1 FOR UPDATE`, p["masterId"])
	if err != nil {
		return nil, err
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, err
	}
	status, verb := "REJECTED", "ditolak"
	if verify {
		status, verb = "VERIFIED", "diverifikasi"
	}
	if p["status"] != "RECORDED" {
		return nil, problem(422, fmt.Sprintf("Payment berstatus %s, hanya RECORDED yang bisa %s.", p["status"], verb))
	}
	if verify {
		customer, queryErr := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
		if queryErr != nil {
			return nil, queryErr
		}
		if customer["type"] == "b2b" {
			return nil, problem(422, "Pembayaran B2B melalui invoice, bukan per shipment.")
		}
		if s["status"] == "CANCELLED" {
			return nil, problem(422, "Shipment dibatalkan; pembayaran tidak dapat diverifikasi.")
		}
		total := s["finalPriceAmount"]
		if total == nil {
			total = s["priceAmount"]
		}
		var paid float64
		if err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(amount),0) FROM "Payment" WHERE "masterId"=$1 AND status='VERIFIED'`, s["id"]).Scan(&paid); err != nil {
			return nil, err
		}
		amount := number(p["amount"])
		if total == nil || amount <= 0 || amount > number(total)-paid {
			return nil, problem(422, "Pembayaran melebihi sisa tagihan atau harga belum dihitung.")
		}
	}
	updated, err := r.one(`UPDATE "Payment" p SET status=$2,"verifiedById"=$3,"verifiedAt"=NOW() WHERE id=$1 AND status='RECORDED' RETURNING to_jsonb(p)`, p["id"], status, r.actor["id"])
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, problem(409, "Status pembayaran sudah berubah. Muat ulang.")
	}
	var after any
	if !verify {
		after = map[string]any{"reason": textValue(r.body["reason"])}
	}
	b.audit(r, "status_change", "payment", p["id"], fmt.Sprintf("%s → %s", s["masterCode"], status), nil, after)
	return updated, nil
}

func (b *business) unpaid(r *request) (any, error) {
	shipments, err := r.many(`SELECT to_jsonb(s)||jsonb_build_object('customerName',c.name,'customerPhone',c.phone,'payments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'method',p.method,'amount',p.amount,'status',p.status,'reference',p.reference,'createdAt',p."createdAt",'recordedByName',u.name) ORDER BY p."createdAt" DESC,p.id DESC) FROM "Payment" p LEFT JOIN "User" u ON u.id=p."recordedById" WHERE p."masterId"=s.id),'[]'::jsonb)) FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId" WHERE c.type='b2c' AND s.status<>'CANCELLED' AND s."priceAmount" IS NOT NULL ORDER BY s."createdAt" DESC,s.id DESC`)
	if err != nil {
		return nil, err
	}
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return nil, err
	}
	unscoped, warehouse := warehouseScope(r.actor)
	rows := []map[string]any{}
	for _, s := range shipments {
		if !unscoped && !slices.Contains(shipmentWarehouseIDs(s, warehouses), idValue(warehouse)) {
			continue
		}
		paid, pending := 0.0, 0.0
		for _, value := range s["payments"].([]any) {
			p := value.(map[string]any)
			switch p["status"] {
			case "VERIFIED":
				paid += number(p["amount"])
			case "RECORDED":
				pending += number(p["amount"])
			}
		}
		finalPrice := s["finalPriceAmount"]
		if finalPrice == nil {
			finalPrice = number(s["priceAmount"]) - number(s["discountAmount"])
		}
		rows = append(rows, map[string]any{
			"id": s["id"], "masterCode": s["masterCode"], "status": s["status"], "destination": s["destination"],
			"customerName": s["customerName"], "customerPhone": s["customerPhone"], "priceAmount": number(s["priceAmount"]),
			"discountAmount": number(s["discountAmount"]), "finalPriceAmount": finalPrice, "paidAmount": paid,
			"pendingAmount": pending, "remainingAmount": math.Max(0, number(finalPrice)-paid-pending), "payments": s["payments"],
		})
	}
	return rows, nil
}
