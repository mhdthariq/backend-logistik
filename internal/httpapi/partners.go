package httpapi

import (
	"fmt"
	"math"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/identity"
	"novalogistik/backend/internal/pricing"
)

func (b *business) registerPartners(app *fiber.App) {
	app.Get("/api/v1/partners", b.handle("partner.view", true, true, b.partners))
	app.Put("/api/v1/partners/:id", b.handle("partner.update", true, true, b.updatePartner))
	app.Put("/api/v1/partners/:id/profile", b.handle("user.update", true, true, b.updatePartnerProfile))
	app.Delete("/api/v1/partners/:id/profile", b.handle("user.disable", true, true, b.disablePartnerProfile))
}

func (b *business) partners(r *request) (any, error) {
	typeName := textValue(r.c.Query("type"))
	// Legacy partner listing lazily creates missing wallets, including inactive partners.
	if err := r.exec(`INSERT INTO "Wallet" ("partnerId","updatedAt") SELECT id,NOW() FROM "Partner" WHERE ($1='' OR type=$1) ON CONFLICT ("partnerId") DO NOTHING`, typeName); err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT jsonb_build_object(
 'id',p.id,'userId',u.id,'name',u.name,'username',u.username,'userActive',u."isActive",'type',p.type,
 'profitShare',jsonb_build_object('company',p."companyPercent",'partner',p."partnerPercent"),
 'bank',jsonb_build_object('bankName',p."bankName",'bankAccountName',p."bankAccountName",'bankAccountNumber',p."bankAccountNumber"),
 'isActive',p."isActive",'notes',p.notes,'warehouseId',p."warehouseId",'warehouseName',wh.name,
 'wallet',jsonb_build_object('partnerId',p.id,'walletId',w.id,'balance',w.balance,'reserved',res.amount,'available',GREATEST(0,w.balance-res.amount)),
 'totals',jsonb_build_object('transportEarnings',ledger.transport,'commissions',ledger.commission,'repairDeductions',ledger.repair,'withdrawals',ledger.withdrawal),
 'counts',jsonb_build_object(
  'vehicles',(SELECT count(*) FROM "Vehicle" WHERE "ownerId"=p.id),
  'commissions',(SELECT count(*) FROM "MarketingCommission" WHERE "partnerId"=p.id),
  'settlements',(SELECT count(*) FROM "TransportSettlement" WHERE "ownerId"=p.id),
  'topUps',(SELECT count(*) FROM "TopUpRequest" WHERE "partnerId"=p.id),
  'withdrawals',(SELECT count(*) FROM "WithdrawalRequest" WHERE "partnerId"=p.id),
  'repairs',(SELECT count(*) FROM "VehicleRepair" WHERE "ownerId"=p.id)))
 FROM "Partner" p JOIN "User" u ON u.id=p."userId" JOIN "Wallet" w ON w."partnerId"=p.id
 LEFT JOIN "Warehouse" wh ON wh.id=p."warehouseId"
 CROSS JOIN LATERAL (SELECT COALESCE(sum(amount),0) amount FROM "WithdrawalRequest" WHERE "partnerId"=p.id AND status IN ('PENDING','APPROVED','PROCESSING')) res
 CROSS JOIN LATERAL (SELECT
  COALESCE(sum(amount) FILTER (WHERE type='TRANSPORT_PROFIT_SHARE' AND direction='CREDIT'),0) transport,
  COALESCE(sum(amount) FILTER (WHERE type='COMMISSION' AND direction='CREDIT'),0) commission,
  COALESCE(sum(amount) FILTER (WHERE type='REPAIR_DEDUCTION' AND direction='DEBIT'),0) repair,
  COALESCE(sum(amount) FILTER (WHERE type='WITHDRAWAL' AND direction='DEBIT'),0) withdrawal
  FROM "WalletTransaction" WHERE "walletId"=w.id) ledger
 WHERE ($1='' OR p.type=$1) ORDER BY p."createdAt",p.id`, typeName)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		totals := object(row["totals"])
		shipment, transport := number(totals["commissions"]), number(totals["transportEarnings"])
		row["earningsSummary"] = map[string]any{"shipment": shipment, "transport": transport, "total": pricing.Round((shipment+transport)*100) / 100}
	}
	return rows, nil
}

func (r *request) partnerAccount() (map[string]any, map[string]any, error) {
	p, err := r.find("Partner", "Partner tidak ditemukan.", false)
	if err != nil {
		return nil, nil, err
	}
	// Lock the account first, matching the existing user-administration writers.
	u, err := r.one(`SELECT to_jsonb(u)-'passwordHash' FROM "User" u WHERE id=$1 FOR UPDATE`, p["userId"])
	if err != nil {
		return nil, nil, err
	}
	p, err = r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE id=$1 FOR UPDATE`, p["id"])
	return p, u, err
}

func (b *business) updatePartner(r *request) (any, error) {
	p, u, err := r.partnerAccount()
	if err != nil {
		return nil, err
	}
	company, partner := p["companyPercent"], p["partnerPercent"]
	if n := numeric(r.body["companyPercent"]); n != nil {
		company = n
	}
	if n := numeric(r.body["partnerPercent"]); n != nil {
		partner = n
	}
	if number(company) <= 0 || number(partner) <= 0 {
		return nil, problem(422, "Persentase company dan partner harus lebih besar dari 0.")
	}
	if math.Abs(number(company)+number(partner)-100) > 0.001 {
		return nil, problem(422, "Persentase company + partner harus tepat 100%.")
	}
	warehouse := p["warehouseId"]
	if raw, ok := r.body["warehouseId"]; ok {
		switch raw {
		case nil, "", "general", "none":
			warehouse = nil
		default:
			id := idValue(raw)
			if id == 0 {
				return nil, fieldProblem("Gudang tidak valid.", "warehouseId", "Gudang tidak valid.")
			}
			w, queryErr := r.one(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE id=$1 AND "isActive"`, id)
			if queryErr != nil {
				return nil, queryErr
			}
			if w == nil {
				return nil, fieldProblem("Gudang tidak ditemukan / tidak aktif.", "warehouseId", "Gudang tidak ditemukan / tidak aktif.")
			}
			warehouse = w["id"]
		}
	}
	notes := nullable(r.body["notes"])
	if notes == nil {
		notes = p["notes"]
	}
	updated, err := r.updateRow("Partner", idValue(p["id"]), map[string]any{"companyPercent": company, "partnerPercent": partner, "warehouseId": warehouse, "notes": notes, "isActive": boolValue(r.body["isActive"], p["isActive"] == true)})
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "partner", p["id"], textValue(u["username"]), nil, map[string]any{
		"before": map[string]any{"companyPercent": p["companyPercent"], "partnerPercent": p["partnerPercent"], "warehouseId": p["warehouseId"]},
		"after":  map[string]any{"companyPercent": company, "partnerPercent": partner, "warehouseId": warehouse},
	})
	return updated, nil
}

func partnerProfile(p, u map[string]any) map[string]any {
	return map[string]any{"id": p["id"], "userId": u["id"], "name": u["name"], "username": u["username"], "isActive": u["isActive"]}
}

func (b *business) updatePartnerProfile(r *request) (any, error) {
	p, u, err := r.partnerAccount()
	if err != nil {
		return nil, err
	}
	data, changes := map[string]any{}, map[string]any{}
	if raw, ok := r.body["name"]; ok {
		name := textValue(raw)
		if name == "" {
			return nil, fieldProblem("Nama tidak boleh kosong.", "name", "Nama tidak boleh kosong.")
		}
		if name != u["name"] {
			data["name"] = name
			changes["name"] = map[string]any{"before": u["name"], "after": name}
		}
	}
	if textValue(r.body["password"]) != "" {
		password := fmt.Sprint(r.body["password"])
		if jsLength(password) < 8 {
			return nil, fieldProblem("Password minimal 8 karakter.", "password", "Password minimal 8 karakter.")
		}
		hash, hashErr := identity.HashPassword(password)
		if hashErr != nil {
			return nil, hashErr
		}
		data["passwordHash"] = hash
		changes["password"] = map[string]any{"before": "[hidden]", "after": "[changed]"}
	}
	if raw, ok := r.body["isActive"]; ok && truthy(raw) != u["isActive"] {
		data["isActive"] = truthy(raw)
		changes["isActive"] = map[string]any{"before": u["isActive"], "after": truthy(raw)}
	}
	if len(data) == 0 {
		profile := partnerProfile(p, u)
		profile["unchanged"] = true
		return profile, nil
	}
	updated, err := r.updateRow("User", idValue(u["id"]), data)
	if err != nil {
		return nil, err
	}
	if r.body["isActive"] == false {
		if err = r.exec(`DELETE FROM "SessionToken" WHERE "userId"=$1`, u["id"]); err != nil {
			return nil, err
		}
	}
	b.audit(r, "updated_profile", "partner", p["id"], textValue(u["username"]), map[string]any{"name": u["name"], "isActive": u["isActive"]}, changes)
	if changes["password"] != nil {
		b.audit(r, "change_password", "partner", p["id"], textValue(r.actor["name"])+" change password "+textValue(u["username"]), nil, map[string]any{"changedBy": r.actor["name"], "changedByUsername": r.actor["username"], "targetUser": u["username"]})
	}
	return partnerProfile(p, updated), nil
}

func (b *business) disablePartnerProfile(r *request) (any, error) {
	p, u, err := r.partnerAccount()
	if err != nil {
		return nil, err
	}
	if u["isOwner"] == true {
		return nil, problem(422, "Akun Owner tidak bisa dinonaktifkan.")
	}
	if _, err = r.updateRow("User", idValue(u["id"]), map[string]any{"isActive": false}); err != nil {
		return nil, err
	}
	if err = r.exec(`DELETE FROM "SessionToken" WHERE "userId"=$1`, u["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "disabled", "partner", p["id"], textValue(u["username"]), map[string]any{"name": u["name"], "isActive": u["isActive"]}, map[string]any{"isActive": false})
	return map[string]any{"disabled": true, "userId": u["id"]}, nil
}
