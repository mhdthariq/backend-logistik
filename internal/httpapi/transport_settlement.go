package httpapi

import (
	"fmt"
	"math"
	"time"

	"novalogistik/backend/internal/pricing"
)

func (b *business) settleTransport(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	existing, err := r.one(`SELECT to_jsonb(s) FROM "TransportSettlement" s WHERE "transportId"=$1`, t["id"])
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, problem(422, fmt.Sprintf("Transport ini sudah disettle (%s).", existing["settlementCode"]))
	}
	if t["status"] != "ARRIVED" && t["status"] != "COMPLETED" {
		return nil, problem(422, fmt.Sprintf("Transport berstatus %s belum bisa disettle - harus ARRIVED atau COMPLETED.", t["status"]))
	}
	owner, err := r.one(`SELECT to_jsonb(p)||jsonb_build_object('user',jsonb_build_object('name',u.name)) FROM "Partner" p JOIN "User" u ON u.id=p."userId" WHERE p.id=$1`, object(t["vehicle"])["ownerId"])
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, problem(422, "Kendaraan ini milik perusahaan (bukan Vehicle Owner) - tidak ada profit share yang perlu dibayarkan.")
	}
	value := 0.0
	for _, ts := range transportShips(t) {
		value += number(object(ts["master"])["priceAmount"])
	}
	if n := numeric(r.body["transportValue"]); n != nil {
		value = number(n)
	}
	if value <= 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return nil, fieldProblem("Nilai transport harus lebih besar dari nol - shipment yang dimuat belum diharga dan tidak ada nilai yang diinput.", "transportValue", "Nilai transport wajib diisi.")
	}
	companyAmount := pricing.Round(value*(number(owner["companyPercent"])/100)*100) / 100
	ownerAmount := pricing.Round(value*(number(owner["partnerPercent"])/100)*100) / 100
	if math.IsInf(companyAmount, 0) || math.IsInf(ownerAmount, 0) || math.IsNaN(companyAmount) || math.IsNaN(ownerAmount) || companyAmount < 0 || ownerAmount < 0 {
		return nil, problem(422, "Nilai profit share tidak valid.")
	}
	code, err := r.nextCode("TransportSettlement", "settlementCode", "TST-", 731033)
	if err != nil {
		return nil, err
	}
	ref := fmt.Sprintf("TST-TRP-%d", idValue(t["id"]))
	ledger, err := r.one(`SELECT to_jsonb(w) FROM "WalletTransaction" w WHERE "businessRef"=$1`, ref)
	if err != nil {
		return nil, err
	}
	if ledger == nil {
		if err = r.exec(`INSERT INTO "Wallet" ("partnerId","updatedAt") VALUES ($1,NOW()) ON CONFLICT ("partnerId") DO NOTHING`, owner["id"]); err != nil {
			return nil, err
		}
		wallet, e := r.one(`SELECT to_jsonb(w) FROM "Wallet" w WHERE "partnerId"=$1 FOR UPDATE`, owner["id"])
		if e != nil {
			return nil, e
		}
		before := number(wallet["balance"])
		after := pricing.Round((before+ownerAmount)*100) / 100
		if math.IsInf(after, 0) {
			return nil, problem(422, "Saldo melebihi batas.")
		}
		ledger, err = r.one(`INSERT INTO "WalletTransaction" AS wt ("walletId",type,amount,direction,"balanceBefore","balanceAfter","referenceType","referenceId","businessRef",status,description,"createdById") VALUES ($1,'TRANSPORT_PROFIT_SHARE',$2,'CREDIT',$3,$4,'transport_settlement',$5,$6,'COMPLETED',$7,$8) RETURNING to_jsonb(wt)`, wallet["id"], ownerAmount, before, after, t["id"], ref, fmt.Sprintf("Profit share transport %s (%g%% dari Rp%g)", t["transportCode"], number(owner["partnerPercent"]), value), r.actor["id"])
		if err != nil {
			return nil, err
		}
		if _, err = r.updateRow("Wallet", idValue(wallet["id"]), map[string]any{"balance": after}); err != nil {
			return nil, err
		}
	}
	settlement, err := r.insertRow("TransportSettlement", map[string]any{"settlementCode": code, "transportId": t["id"], "vehicleId": t["vehicleId"], "ownerId": owner["id"], "transportValue": value, "companyPercent": owner["companyPercent"], "ownerPercent": owner["partnerPercent"], "companyAmount": companyAmount, "ownerAmount": ownerAmount, "status": "FINALIZED", "finalizedById": r.actor["id"], "finalizedAt": time.Now().UTC(), "walletTransactionId": ledger["id"]})
	if err != nil {
		return nil, err
	}
	settlement["transport"], settlement["vehicle"], settlement["owner"] = map[string]any{"transportCode": t["transportCode"]}, map[string]any{"vehicleNumber": object(t["vehicle"])["vehicleNumber"]}, owner
	b.audit(r, "settled", "transport_settlement", settlement["id"], code, nil, map[string]any{"transportCode": t["transportCode"], "transportValue": value, "ownerPercent": owner["partnerPercent"], "ownerAmount": ownerAmount, "owner": object(owner["user"])["name"]})
	return settlement, nil
}
