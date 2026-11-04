package httpapi

import (
	"fmt"
	"math"

	"novalogistik/backend/internal/pricing"
)

func (b *business) readyShipment(r *request) (any, error) {
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
	if s["status"] != "CREATED" {
		return nil, problem(422, fmt.Sprintf("Shipment dengan status %s tidak bisa di-submit untuk pickup.", s["status"]))
	}
	var count int
	if err = r.db.QueryRow(r.ctx, `SELECT count(*) FROM "DetailShipment" WHERE "masterId"=$1`, s["id"]).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, problem(422, "Tambahkan minimal satu detail barang sebelum submit untuk pickup.")
	}
	if number(s["priceAmount"]) <= 0 {
		return nil, problem(422, "Harga belum dihitung - klik “Hitung Harga” dan simpan harga shipment sebelum submit untuk pickup.")
	}
	if !truthy(s["penerimaName"]) {
		return nil, fieldProblem("Isi data Penerima (nama penerima) sebelum submit untuk pickup - Penerima dicetak pada resi.", "penerimaName", "Penerima wajib diisi (dicetak pada Resi).")
	}
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	var escrow any
	amount := 0.0
	owned := s["createdByPartnerId"] != nil && (s["discountFundedBy"] == "MARKETING" || s["createdByPartnerId"] == r.actor["partnerId"])
	if customer["type"] == "b2c" && owned {
		partner, err := r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE id=$1`, s["createdByPartnerId"])
		if err != nil {
			return nil, err
		}
		if partner != nil {
			final := s["finalPriceAmount"]
			if final == nil {
				final = s["priceAmount"]
			}
			amount = pricing.Round(number(final)*(number(partner["companyPercent"])/100)*100) / 100
			if amount > 0 {
				wallet, reserved, err := r.escrowWallet(partner["id"])
				if err != nil {
					return nil, err
				}
				ref := fmt.Sprintf("ESCROW-%d", idValue(s["id"]))
				existing, err := r.one(`SELECT to_jsonb(t) FROM "WalletTransaction" t WHERE "businessRef"=$1`, ref)
				if err != nil {
					return nil, err
				}
				if existing != nil {
					escrow = existing["id"]
					amount = number(existing["amount"])
				} else {
					balance := number(wallet["balance"])
					available := math.Max(0, balance-reserved)
					if available < amount-0.001 {
						message := fmt.Sprintf("Saldo wallet Marketing tidak mencukupi untuk request pickup shipment B2C ini. Diperlukan minimal Rp%s (%s%% bagian company dari total Rp%s). Saldo tersedia: Rp%s (dari Rp%s, Rp%s terreserve withdrawal aktif). Silakan top-up wallet terlebih dahulu.", rupiahNumber(pricing.Round(amount)), textValue(partner["companyPercent"]), rupiahNumber(pricing.Round(number(final))), rupiahNumber(available), rupiahNumber(balance), rupiahNumber(reserved))
						return nil, fieldProblem(message, "wallet", "Saldo wallet tidak mencukupi untuk escrow pickup.")
					}
					escrow, err = r.appendEscrow(wallet, s, amount, "DEBIT", ref, fmt.Sprintf("Escrow hold - bagian company (%s%%) untuk shipment B2C %s", textValue(partner["companyPercent"]), s["masterCode"]))
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	description := fmt.Sprintf("Shipment siap dijemput (%d paket) - harga Rp%s", count, rupiahNumber(pricing.Round(number(s["priceAmount"]))))
	var after any
	if escrow != nil {
		description += fmt.Sprintf(". Escrow Rp%s di-hold dari wallet Marketing.", rupiahNumber(pricing.Round(amount)))
		after = map[string]any{"escrowAmount": amount, "escrowWalletTransactionId": escrow}
	}
	return b.shipmentStatus(r, s, "READY_FOR_PICKUP", description, after)
}

// escrowWallet locks the balance before reading reservations or appending ledger
// entries. The caller's transaction also owns the shipment transition.
func (r *request) escrowWallet(partner any) (map[string]any, float64, error) {
	if err := r.exec(`INSERT INTO "Wallet" ("partnerId","updatedAt") VALUES ($1,NOW()) ON CONFLICT ("partnerId") DO NOTHING`, partner); err != nil {
		return nil, 0, err
	}
	wallet, err := r.one(`SELECT to_jsonb(w) FROM "Wallet" w WHERE "partnerId"=$1 FOR UPDATE`, partner)
	if err != nil {
		return nil, 0, err
	}
	var reserved float64
	err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(amount),0) FROM "WithdrawalRequest" WHERE "partnerId"=$1 AND status IN ('PENDING','APPROVED','PROCESSING')`, partner).Scan(&reserved)
	return wallet, reserved, err
}

func (r *request) appendEscrow(wallet, shipment map[string]any, amount float64, direction, ref, description string) (any, error) {
	balance := number(wallet["balance"])
	after := pricing.Round((balance+amount)*100) / 100
	if direction == "DEBIT" {
		after = pricing.Round((balance-amount)*100) / 100
	}
	if after < -0.001 {
		return nil, problem(422, "Saldo wallet tidak mencukupi untuk operasi debit ini.")
	}
	tx, err := r.one(`INSERT INTO "WalletTransaction" AS t ("walletId",type,amount,direction,"balanceBefore","balanceAfter","referenceType","referenceId","businessRef",status,description,"createdById") VALUES ($1,'SHIPMENT_ESCROW',$2,$3,$4,$5,'shipment',$6,$7,'COMPLETED',$8,$9) RETURNING to_jsonb(t)`, wallet["id"], pricing.Round(amount*100)/100, direction, balance, after, shipment["id"], ref, description, r.actor["id"])
	if err != nil {
		return nil, err
	}
	if err = r.exec(`UPDATE "Wallet" SET balance=$2,"updatedAt"=NOW() WHERE id=$1`, wallet["id"], after); err != nil {
		return nil, err
	}
	return tx["id"], nil
}

func (b *business) shipmentStatus(r *request, s map[string]any, status, description string, after any) (any, error) {
	updated, err := r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": status})
	if err != nil {
		return nil, err
	}
	if err = r.tracking(s, status, description); err != nil {
		return nil, err
	}
	b.audit(r, "status_change", "shipment", s["id"], textValue(s["masterCode"])+" → "+status, nil, after)
	return updated, nil
}

func (b *business) cancelShipment(r *request) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	switch s["status"] {
	case "CREATED", "READY_FOR_PICKUP", "PICKED_UP", "RECEIVED_AT_GUDANG", "IN_TRANSPORT", "AT_DEST_GUDANG", "ARRIVED_AT_GUDANG":
	default:
		return nil, problem(422, fmt.Sprintf("Shipment dengan status %s tidak bisa dibatalkan.", s["status"]))
	}
	warnings, err := r.cancellationGuards(s["id"])
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		b.audit(r, "status_change", "shipment", s["id"], textValue(s["masterCode"])+" → CANCELLED (warnings)", nil, map[string]any{"warnings": warnings})
	}
	hold, err := r.one(`SELECT to_jsonb(t) FROM "WalletTransaction" t WHERE "businessRef"=$1`, fmt.Sprintf("ESCROW-%d", idValue(s["id"])))
	if err != nil {
		return nil, err
	}
	description := "Shipment dibatalkan oleh " + textValue(r.actor["name"])
	var after any
	if hold != nil {
		description += fmt.Sprintf(". Escrow Rp%s dikembalikan ke wallet Marketing.", rupiahNumber(pricing.Round(number(hold["amount"]))))
		if s["createdByPartnerId"] != nil {
			wallet, _, err := r.escrowWallet(s["createdByPartnerId"])
			if err != nil {
				return nil, err
			}
			ref := fmt.Sprintf("ESCROW-REFUND-%d", idValue(s["id"]))
			existing, err := r.one(`SELECT to_jsonb(t) FROM "WalletTransaction" t WHERE "businessRef"=$1`, ref)
			if err != nil {
				return nil, err
			}
			var txID any
			if existing != nil {
				txID = existing["id"]
			} else {
				txID, err = r.appendEscrow(wallet, s, number(hold["amount"]), "CREDIT", ref, fmt.Sprintf("Refund escrow - shipment %s dibatalkan", s["masterCode"]))
				if err != nil {
					return nil, err
				}
			}
			after = map[string]any{"escrowRefundAmount": hold["amount"], "escrowRefundWalletTransactionId": txID}
		}
	}
	return b.shipmentStatus(r, s, "CANCELLED", description, after)
}

func (r *request) cancellationGuards(id any) ([]string, error) {
	transport, err := r.one(`SELECT to_jsonb(t) FROM "TransportShipment" l JOIN "Transport" t ON t.id=l."transportId" WHERE l."shipmentId"=$1 AND t.status IN ('PLANNED','DEPARTED') LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	if transport != nil {
		return nil, problem(422, fmt.Sprintf("Shipment masih dimuat di transport aktif %s (%s). Hapus shipment dari transport tersebut sebelum membatalkan.", transport["transportCode"], transport["status"]))
	}
	delivery, err := r.one(`SELECT to_jsonb(d) FROM "Delivery" d WHERE "masterId"=$1 AND status IN ('ASSIGNED','IN_PROGRESS') LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	if delivery != nil {
		return nil, problem(422, fmt.Sprintf("Masih ada tugas delivery aktif (%s — %s). Batalkan delivery tersebut sebelum membatalkan shipment.", delivery["deliveryCode"], delivery["status"]))
	}
	invoice, err := r.one(`SELECT to_jsonb(i) FROM "InvoiceLine" l JOIN "Invoice" i ON i.id=l."invoiceId" WHERE l."shipmentId"=$1 AND i.status<>'CANCELLED' LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	if invoice != nil {
		return nil, problem(422, fmt.Sprintf("Shipment sudah termasuk dalam invoice aktif %s (%s). Batalkan invoice tersebut terlebih dahulu sebelum membatalkan shipment.", invoice["invoiceNumber"], invoice["status"]))
	}
	var payments int
	if err = r.db.QueryRow(r.ctx, `SELECT count(*) FROM "Payment" WHERE "masterId"=$1 AND status='VERIFIED'`, id).Scan(&payments); err != nil {
		return nil, err
	}
	warnings := []string{}
	if payments > 0 {
		warnings = append(warnings, fmt.Sprintf("Shipment memiliki %d pembayaran terverifikasi. Pembatalan shipment tidak membatalkan pembayaran otomatis — proses refund/adjustment secara terpisah.", payments))
	}
	pickup, err := r.one(`SELECT to_jsonb(p) FROM "Pickup" p WHERE "masterId"=$1 AND status IN ('ASSIGNED','IN_PROGRESS') LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	if pickup != nil {
		warnings = append(warnings, fmt.Sprintf("Shipment memiliki tugas pickup aktif (%s — %s). Pickup akan menjadi yatim setelah pembatalan — batalkan pickup secara terpisah jika perlu.", pickup["pickupCode"], pickup["status"]))
	}
	return warnings, nil
}
