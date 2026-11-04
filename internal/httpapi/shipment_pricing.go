package httpapi

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"novalogistik/backend/internal/pricing"
)

func number(v any) float64 {
	if n := numeric(v); n != nil {
		return n.(float64)
	}
	return 0
}

func pricedDetails(rows []map[string]any) []pricing.Detail {
	result := make([]pricing.Detail, 0, len(rows))
	for _, d := range rows {
		result = append(result, pricing.Detail{LengthCm: number(d["lengthCm"]), WidthCm: number(d["widthCm"]), HeightCm: number(d["heightCm"]), ActualWeightKg: number(d["actualWeightKg"]), VolumeM3: number(d["volumeM3"])})
	}
	return result
}

func pricingTariff(t map[string]any, customerType string) pricing.Tariff {
	// Billing follows the shipment customer, including when a generic tariff
	// has no customerType. B2B /kg never compares weight against volume.
	return pricing.Tariff{Method: textValue(t["pricingMethod"]), CustomerType: customerType, RoundingMode: textValue(t["roundingMode"]), RatePerKg: number(t["ratePerKg"]), MinKg: number(t["minChargeableKg"]), Multiplier: number(t["volumetricMultiplier"]), RoundingUnit: number(t["roundingUnitKg"]), RatePerKoli: number(t["ratePerKoli"]), RatePerCubic: number(t["ratePerCubic"]), MinKoli: numericDefault(t["minChargeableKoli"], float64(1)).(float64), MinM3: number(t["minChargeableM3"])}
}

func shipmentTotals(rows []map[string]any, roundWeight bool) map[string]any {
	kg, volume := 0.0, 0.0
	for _, d := range pricedDetails(rows) {
		kg += d.ActualWeightKg
		volume += pricing.Volume(d)
	}
	if roundWeight {
		kg = pricing.Round(kg*100) / 100
	}
	return map[string]any{"totalPackages": len(rows), "totalActualKg": kg, "totalVolumeM3": pricing.Round(volume*1_000_000) / 1_000_000}
}

func (r *request) resolveShipmentTariff(s, customer map[string]any) (map[string]any, error) {
	if idValue(s["tariffId"]) > 0 {
		t, err := r.one(`SELECT to_jsonb(t) FROM "Tariff" t WHERE id=$1 AND "isActive" AND "effectiveFrom"<=NOW() AND ("effectiveTo" IS NULL OR "effectiveTo">=NOW())`, s["tariffId"])
		if err != nil || t != nil {
			return t, err
		}
	}
	return r.one(`SELECT to_jsonb(t) FROM "Tariff" t WHERE origin=$1 AND destination=$2 AND "isActive" AND "effectiveFrom"<=NOW() AND ("effectiveTo" IS NULL OR "effectiveTo">=NOW()) AND ("customerType"=$3 OR "customerType" IS NULL) AND ("customerId" IS NULL OR "customerId"=$4) ORDER BY "customerType" DESC,"effectiveFrom" DESC LIMIT 1`, s["origin"], s["destination"], customer["type"], s["customerId"])
}

func (r *request) pricingPreview(s, customer map[string]any, details []map[string]any) (any, error) {
	t, err := r.resolveShipmentTariff(s, customer)
	if err != nil || t == nil {
		return nil, err
	}
	p := pricing.Compute(pricedDetails(details), pricingTariff(t, textValue(customer["type"])))
	estimate := s["priceAmount"]
	if estimate == nil {
		estimate = p.Price
	}
	return map[string]any{"tariffId": t["id"], "ratePerKg": t["ratePerKg"], "volumetricMultiplier": p.Multiplier, "minChargeableKg": t["minChargeableKg"], "roundingMode": t["roundingMode"], "roundingUnitKg": t["roundingUnitKg"], "actualKg": p.ActualKg, "volumetricKg": p.VolumetricKg, "chargeableKg": p.ChargeableKg, "estimatedPrice": estimate, "method": p.Method, "ratePerKoli": t["ratePerKoli"], "ratePerCubic": t["ratePerCubic"], "totalVolumeM3": p.TotalVolumeM3, "koliCount": p.KoliCount, "chargeableKoli": p.ChargeableKoli, "chargeableVolumeM3": p.ChargeableVolumeM3, "rateApplied": p.RateApplied, "simpleKg": p.SimpleKg}, nil
}

func (b *business) shipment(r *request) (any, error) {
	s, err := r.shipment(false)
	if err != nil {
		return nil, err
	}
	id := s["id"]
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	details, err := r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	preview, err := r.pricingPreview(s, customer, details)
	if err != nil {
		return nil, err
	}
	// Each relation uses the Prisma-owned keys and returns [] for empty lists.
	relations, err := r.one(`SELECT jsonb_build_object('invoiceLines',`+invoiceLinesJSON+`,
	 'tariff',(SELECT to_jsonb(t) FROM "Tariff" t WHERE t.id=s."tariffId"),
	 'pickups',COALESCE((SELECT jsonb_agg(to_jsonb(p)||jsonb_build_object('scans',COALESCE((SELECT jsonb_agg(to_jsonb(h)) FROM "HandoverScan" h WHERE h."pickupId"=p.id),'[]'::jsonb))) FROM "Pickup" p WHERE p."masterId"=s.id),'[]'::jsonb),
	 'deliveries',COALESCE((SELECT jsonb_agg(to_jsonb(d)) FROM "Delivery" d WHERE d."masterId"=s.id),'[]'::jsonb),
	 'payments',COALESCE((SELECT jsonb_agg((CASE WHEN $2 THEN to_jsonb(p) ELSE to_jsonb(p)-'proofUrl' END)||jsonb_build_object('recordedBy',(SELECT jsonb_build_object('name',u.name) FROM "User" u WHERE u.id=p."recordedById"),'verifiedBy',(SELECT jsonb_build_object('name',u.name) FROM "User" u WHERE u.id=p."verifiedById"))) FROM "Payment" p WHERE p."masterId"=s.id),'[]'::jsonb),
	 'transportItems',COALESCE((SELECT jsonb_agg(to_jsonb(i)||jsonb_build_object('transport',to_jsonb(t))) FROM "TransportShipment" i JOIN "Transport" t ON t.id=i."transportId" WHERE i."shipmentId"=s.id),'[]'::jsonb)) FROM "MasterShipment" s WHERE s.id=$1`, id, hasPermission(r.actor, "payment.view"))
	if err != nil {
		return nil, err
	}
	for k, v := range relations {
		s[k] = v
	}
	s["customer"], s["details"], s["pricingPreview"], s["totals"] = customer, details, preview, shipmentTotals(details, false)
	events, err := r.trackingEvents(id)
	if err != nil {
		return nil, err
	}
	s["trackingEvents"] = events
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w`)
	if err != nil {
		return nil, err
	}
	shipmentWarehouseNames(s, warehouses)
	var paid float64
	if err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(amount),0) FROM "Payment" WHERE "masterId"=$1 AND status='VERIFIED'`, id).Scan(&paid); err != nil {
		return nil, err
	}
	final := s["finalPriceAmount"]
	if final == nil && s["priceAmount"] != nil {
		final = pricing.Round((number(s["priceAmount"])-number(s["discountAmount"]))*100) / 100
	}
	status := "UNPRICED"
	remaining := 0.0
	if final != nil && number(final) > 0 {
		remaining = math.Max(0, number(final)-paid)
		switch {
		case paid >= number(final)-0.01:
			status = "PAID"
		case paid > 0:
			status = "DP"
		default:
			status = "UNPAID"
		}
	}
	s["paymentSummary"] = map[string]any{"priceAmount": s["priceAmount"], "discountAmount": s["discountAmount"], "finalPriceAmount": final, "paidAmount": paid, "remainingAmount": remaining, "dpRequirement": 0, "dpOk": true, "status": status}
	return s, nil
}

// rupiahNumber matches the decimal/group separators used by id-ID toLocaleString.
func rupiahNumber(n float64) string {
	s := strconv.FormatFloat(pricing.Round(n*1000)/1000, 'f', -1, 64)
	parts := strings.SplitN(s, ".", 2)
	integer := parts[0]
	sign := ""
	if strings.HasPrefix(integer, "-") {
		sign = "-"
		integer = integer[1:]
	}
	for i := len(integer) - 3; i > 0; i -= 3 {
		integer = integer[:i] + "." + integer[i:]
	}
	if len(parts) > 1 {
		return sign + integer + "," + parts[1]
	}
	return sign + integer
}

func (b *business) priceShipment(r *request) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	if !hasPermission(r.actor, "shipment.update") && !hasPermission(r.actor, "pickup.create") {
		return nil, problem(403, "Missing permission: shipment.update")
	}
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	if s["status"] != "CREATED" && s["status"] != "READY_FOR_PICKUP" && s["status"] != "PICKED_UP" {
		return nil, problem(422, "Harga hanya bisa dihitung sebelum shipment masuk gudang.")
	}
	var verified int
	if err = r.db.QueryRow(r.ctx, `SELECT count(*) FROM "Payment" WHERE "masterId"=$1 AND status='VERIFIED'`, s["id"]).Scan(&verified); err != nil {
		return nil, err
	}
	if verified > 0 {
		return nil, problem(422, fmt.Sprintf("Harga tidak bisa diubah lagi — shipment sudah memiliki %d pembayaran terverifikasi. Gunakan adjustment berwenang (dengan alasan + audit) untuk koreksi harga.", verified))
	}
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	details, err := r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=$1`, s["id"])
	if err != nil {
		return nil, err
	}
	var tariff map[string]any
	if tid := idValue(r.body["tariffId"]); tid > 0 {
		// The legacy override intentionally does not require effectiveFrom <= now.
		tariff, err = r.one(`SELECT to_jsonb(t) FROM "Tariff" t WHERE id=$1 AND "isActive" AND ("effectiveTo" IS NULL OR "effectiveTo">=NOW())`, tid)
	} else {
		tariff, err = r.resolveShipmentTariff(s, customer)
	}
	if err != nil {
		return nil, err
	}
	if tariff == nil {
		return nil, problem(422, fmt.Sprintf("Tidak ada tarif aktif untuk %s → %s (%s).", s["origin"], s["destination"], customer["type"]))
	}
	p := pricing.Compute(pricedDetails(details), pricingTariff(tariff, textValue(customer["type"])))
	discount := 0.0
	if r.body["discountType"] == "PERCENTAGE" {
		pct, err := percentage(r.body)
		if err != nil {
			return nil, err
		}
		discount = p.Price * pct.(float64) / 100
	} else if raw, exists := r.body["discountAmount"]; exists {
		discount = number(raw)
	} else if number(s["discountAmount"]) > 0 {
		discount = number(s["discountAmount"])
	} else if s["discountPercentage"] != nil {
		discount = p.Price * number(s["discountPercentage"]) / 100
	}
	discount = math.Max(0, discount)
	var partner map[string]any
	if s["createdByPartnerId"] != nil {
		partner, err = r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE id=$1`, s["createdByPartnerId"])
		if err != nil {
			return nil, err
		}
	}
	if discount > 0 {
		if discount > p.Price+0.001 {
			return nil, fieldProblem(fmt.Sprintf("Discount (Rp%s) tidak boleh melebihi harga shipment (Rp%s).", rupiahNumber(discount), rupiahNumber(p.Price)), "discountAmount", "Discount melebihi harga shipment.")
		}
		if partner != nil && (s["discountFundedBy"] == "MARKETING" || s["createdByPartnerId"] != nil) {
			share := p.Price * number(partner["partnerPercent"]) / 100
			if discount > share+0.001 {
				return nil, fieldProblem(fmt.Sprintf("Discount Rp%s melebihi bagian Marketing (Rp%s = %s%% dari harga) - discount akan membuat bagian Marketing negatif (§6.2).", rupiahNumber(discount), rupiahNumber(pricing.Round(share)), textValue(partner["partnerPercent"])), "discountAmount", "Discount melebihi bagian Marketing.")
			}
		}
	}
	final := math.Max(0, pricing.Round((p.Price-discount)*100)/100)
	if customer["type"] == "b2c" && partner != nil && (s["discountFundedBy"] == "MARKETING" || s["createdByPartnerId"] != nil) {
		share := pricing.Round(final*number(partner["companyPercent"])/100*100) / 100
		if share > 0 {
			if err = r.exec(`INSERT INTO "Wallet" ("partnerId","updatedAt") VALUES ($1,NOW()) ON CONFLICT ("partnerId") DO NOTHING`, partner["id"]); err != nil {
				return nil, err
			}
			var balance, reserved float64
			err = r.db.QueryRow(r.ctx, `SELECT balance,COALESCE((SELECT sum(amount) FROM "WithdrawalRequest" WHERE "partnerId"=$1 AND status IN ('PENDING','APPROVED','PROCESSING')),0) FROM "Wallet" WHERE "partnerId"=$1 FOR UPDATE`, partner["id"]).Scan(&balance, &reserved)
			if err != nil {
				return nil, err
			}
			available := math.Max(0, balance-reserved)
			if available < share-0.001 {
				return nil, fieldProblem(fmt.Sprintf("Saldo wallet Marketing tidak mencukupi untuk membuka shipment B2C ini. Diperlukan minimal Rp%s (%s%% bagian company dari total Rp%s). Saldo tersedia: Rp%s (dari Rp%s, Rp%s terreserve withdrawal aktif). Silakan top-up wallet terlebih dahulu sebelum menghitung harga shipment.", rupiahNumber(pricing.Round(share)), textValue(partner["companyPercent"]), rupiahNumber(final), rupiahNumber(available), rupiahNumber(balance), rupiahNumber(reserved)), "wallet", "Saldo wallet tidak mencukupi untuk bagian company.")
			}
		}
	}
	data := map[string]any{"tariffId": tariff["id"], "chargeableWeightKg": p.ChargeableKg, "ratePerKg": tariff["ratePerKg"], "pricingMethod": p.Method, "chargeableKoli": nil, "chargeableVolumeM3": nil, "priceAmount": p.Price, "pricedAt": time.Now().UTC(), "discountAmount": discount, "discountPercentage": nil, "finalPriceAmount": final}
	if p.Method == "PER_KOLI" {
		data["chargeableKoli"] = p.ChargeableKoli
	}
	if p.Method == "PER_CUBIC" {
		data["chargeableVolumeM3"] = p.ChargeableVolumeM3
	}
	if discount > 0 {
		data["discountPercentage"] = pricing.Round(discount/p.Price*10000) / 100
	}
	if raw, exists := r.body["insuranceAmount"]; exists {
		data["insuranceAmount"] = math.Max(0, number(raw))
	}
	after, err := r.updateRow("MasterShipment", idValue(s["id"]), data)
	if err != nil {
		return nil, err
	}
	description := ""
	switch {
	case p.Method == "PER_KOLI":
		description = fmt.Sprintf("Harga dihitung: %.0f koli × Rp%s/koli = Rp%s", p.ChargeableKoli, rupiahNumber(p.RateApplied), rupiahNumber(p.Price))
	case p.Method == "PER_CUBIC":
		description = fmt.Sprintf("Harga dihitung: %.3f m³ × Rp%s/m³ = Rp%s", p.ChargeableVolumeM3, rupiahNumber(p.RateApplied), rupiahNumber(p.Price))
	case p.SimpleKg:
		description = fmt.Sprintf("Harga dihitung: %.2f kg (berat yang diinput, min %s kg) → ditagih %.2f kg × Rp%s/kg = Rp%s", p.ActualKg, textValue(tariff["minChargeableKg"]), p.ChargeableKg, rupiahNumber(p.RateApplied), rupiahNumber(p.Price))
	default:
		description = fmt.Sprintf("Harga dihitung: chargeable %.1f kg (aktual %.1f kg, volumetrik %.2f kg = L×W×H/1.000.000 × %s) × Rp%s = Rp%s", p.ChargeableKg, p.ActualKg, p.VolumetricKg, rupiahNumber(p.Multiplier), rupiahNumber(p.RateApplied), rupiahNumber(p.Price))
	}
	if err = r.tracking(s, "PRICED", description); err != nil {
		return nil, err
	}
	b.audit(r, "priced", "shipment", s["id"], textValue(s["masterCode"]), nil, map[string]any{"chargeableWeightKg": p.ChargeableKg, "ratePerKg": tariff["ratePerKg"], "priceAmount": p.Price, "volumetricMultiplier": p.Multiplier})
	after["pricing"] = p
	return after, nil
}
