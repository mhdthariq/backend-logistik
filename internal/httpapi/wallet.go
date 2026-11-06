package httpapi

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/pricing"
)

func (b *business) registerWallet(app *fiber.App) {
	app.Get("/api/v1/wallet", b.handle("wallet.view_own", true, true, b.wallet))
	app.Get("/api/v1/wallet/transactions", b.handle("wallet.transaction.view_own", true, true, b.walletTransactions))
	app.Post("/api/v1/wallet/adjustments", b.handle("wallet.adjustment.create", true, true, b.walletAdjustment))
	app.Get("/api/v1/topups", b.handle("", true, false, b.topups))
	app.Post("/api/v1/topups", b.handle("wallet.topup.create", true, true, b.createTopup))
	app.Post("/api/v1/topups/:id/verify", b.handle("wallet.topup.verify", true, true, b.reviewTopup))
	app.Post("/api/v1/topups/:id/reject", b.handle("wallet.topup.verify", true, true, b.reviewTopup))
	app.Post("/api/v1/topups/:id/cancel", b.handle("wallet.topup.cancel", true, true, b.reviewTopup))
	app.Get("/api/v1/withdrawals", b.handle("", true, false, b.withdrawals))
	app.Post("/api/v1/withdrawals", b.handle("wallet.withdrawal.create", true, true, b.createWithdrawal))
	app.Post("/api/v1/withdrawals/:id/approve", b.handle("wallet.withdrawal.approve", true, true, b.reviewWithdrawal))
	app.Post("/api/v1/withdrawals/:id/reject", b.handle("wallet.withdrawal.reject", true, true, b.reviewWithdrawal))
	app.Post("/api/v1/withdrawals/:id/process", b.handle("wallet.withdrawal.process", true, true, b.reviewWithdrawal))
	app.Post("/api/v1/withdrawals/:id/complete", b.handle("wallet.withdrawal.process", true, true, b.reviewWithdrawal))
	app.Post("/api/v1/withdrawals/:id/fail", b.handle("wallet.withdrawal.process", true, true, b.reviewWithdrawal))
	app.Post("/api/v1/withdrawals/:id/cancel", b.handle("wallet.withdrawal.create", true, true, b.reviewWithdrawal))
}

func money(n float64) float64 { return pricing.Round(n*100) / 100 }
func finite(n float64) bool   { return !math.IsInf(n, 0) && !math.IsNaN(n) }
func minimum(body map[string]any, key string, min float64) (float64, error) {
	n := numeric(body[key])
	if n == nil || number(n) < min {
		return 0, fieldProblem("Jumlah tidak valid.", key, fmt.Sprintf("%s minimal %g.", key, min))
	}
	return number(n), nil
}
func (r *request) ownPartner(kind string) (int, error) {
	id := idValue(r.actor["partnerId"])
	if id == 0 || textValue(r.actor["partnerType"]) == "" {
		return 0, problem(403, "Akses ditolak - akun ini bukan partner.")
	}
	if kind != "" && r.actor["partnerType"] != kind {
		return 0, problem(403, "Akses ditolak - jenis partner tidak sesuai.")
	}
	return id, nil
}
func (r *request) lockWallet(partner any) (map[string]any, error) {
	p, err := r.one(`SELECT jsonb_build_object('id',id) FROM "Partner" WHERE id=$1`, partner)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, problem(404, "Partner tidak ditemukan.")
	}
	if err = r.exec(`INSERT INTO "Wallet" ("partnerId","updatedAt") VALUES ($1,NOW()) ON CONFLICT ("partnerId") DO NOTHING`, partner); err != nil {
		return nil, err
	}
	return r.one(`SELECT to_jsonb(w) FROM "Wallet" w WHERE "partnerId"=$1 FOR UPDATE`, partner)
}
func (r *request) reserved(partner any) (float64, error) {
	var n float64
	err := r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(amount),0) FROM "WithdrawalRequest" WHERE "partnerId"=$1 AND status IN ('PENDING','APPROVED','PROCESSING')`, partner).Scan(&n)
	return n, err
}
func (r *request) walletSummary(partner any) (map[string]any, error) {
	w, err := r.lockWallet(partner)
	if err != nil {
		return nil, err
	}
	reserved, err := r.reserved(partner)
	if err != nil {
		return nil, err
	}
	return map[string]any{"partnerId": partner, "walletId": w["id"], "balance": w["balance"], "reserved": reserved, "available": math.Max(0, number(w["balance"])-reserved)}, nil
}

// All ledger callers run in a transaction; the wallet lock serializes balance writers.
func (r *request) ledger(partner any, kind, direction string, amount float64, refType string, refID any, ref, description string, respectReservation bool) (map[string]any, error) {
	w, err := r.lockWallet(partner)
	if err != nil {
		return nil, err
	}
	existing, err := r.one(`SELECT to_jsonb(t) FROM "WalletTransaction" t WHERE "businessRef"=$1`, ref)
	if err != nil || existing != nil {
		return existing, err
	}
	if !finite(amount) || amount < 0 {
		return nil, problem(422, "Jumlah tidak valid.")
	}
	before := number(w["balance"])
	after := money(before + amount)
	if direction == "DEBIT" {
		after = money(before - amount)
		if respectReservation {
			reserved, e := r.reserved(partner)
			if e != nil {
				return nil, e
			}
			if after < reserved-0.001 {
				return nil, problem(422, "Saldo tersedia tidak mencukupi (sebagian saldo sedang direserve oleh permintaan withdrawal aktif).")
			}
		}
	}
	if !finite(after) || after < -0.001 {
		return nil, problem(422, "Saldo wallet tidak mencukupi untuk operasi debit ini.")
	}
	row, err := r.one(`INSERT INTO "WalletTransaction" AS t ("walletId",type,direction,amount,"balanceBefore","balanceAfter","referenceType","referenceId","businessRef",description,"createdById") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING to_jsonb(t)`, w["id"], kind, direction, money(amount), before, after, refType, refID, ref, description, r.actor["id"])
	if err != nil {
		return nil, err
	}
	_, err = r.updateRow("Wallet", idValue(w["id"]), map[string]any{"balance": after})
	return row, err
}
func (b *business) wallet(r *request) (any, error) {
	id, err := r.ownPartner("")
	if err != nil {
		return nil, err
	}
	summary, err := r.walletSummary(id)
	if err != nil {
		return nil, err
	}
	p, err := r.one(`SELECT to_jsonb(p)||jsonb_build_object('name',u.name) FROM "Partner" p JOIN "User" u ON u.id=p."userId" WHERE p.id=$1`, id)
	if err != nil {
		return nil, err
	}
	summary["partnerType"], summary["partnerName"] = p["type"], p["name"]
	summary["profitShare"] = map[string]any{"company": p["companyPercent"], "partner": p["partnerPercent"]}
	summary["bank"] = map[string]any{"bankName": p["bankName"], "bankAccountName": p["bankAccountName"], "bankAccountNumber": p["bankAccountNumber"]}
	return summary, nil
}
func (b *business) walletTransactions(r *request) (any, error) {
	id, err := r.ownPartner("")
	if err != nil {
		return nil, err
	}
	w, err := r.lockWallet(id)
	if err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(t) FROM "WalletTransaction" t WHERE "walletId"=$1 AND ($2='' OR type=$2) AND ($3='' OR direction=$3) ORDER BY "createdAt" DESC,id DESC LIMIT 200`, w["id"], r.c.Query("type"), r.c.Query("direction"))
}
func (b *business) walletAdjustment(r *request) (any, error) {
	id := idValue(r.body["partnerId"])
	if id == 0 {
		return nil, problem(422, "Partner tidak valid.")
	}
	amount, err := minimum(r.body, "amount", 1)
	if err != nil {
		return nil, err
	}
	reason, err := required(r.body, "reason")
	if err != nil {
		return nil, err
	}
	direction := "CREDIT"
	if r.body["direction"] == "DEBIT" {
		direction = "DEBIT"
	}
	ref := fmt.Sprintf("ADJ-P%d-%d", id, time.Now().UnixNano())
	t, err := r.ledger(id, "ADJUSTMENT", direction, amount, "adjustment", nil, ref, "Penyesuaian oleh Owner: "+reason, boolValue(r.body["respectReservation"], false))
	if err != nil {
		return nil, err
	}
	summary, err := r.walletSummary(id)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "wallet_adjustment", t["id"], ref, nil, map[string]any{"partnerId": id, "amount": amount, "direction": direction, "reason": reason})
	return map[string]any{"transactionId": t["id"], "balanceBefore": t["balanceBefore"], "balanceAfter": t["balanceAfter"], "wallet": summary}, nil
}
func companyBank() map[string]any {
	value := func(key, fallback string) string {
		if s, ok := os.LookupEnv(key); ok {
			return s
		}
		return fallback
	}
	return map[string]any{"bankName": value("NEXT_PUBLIC_COMPANY_BANK_NAME", "Bank Mandiri"), "accountNumber": value("NEXT_PUBLIC_COMPANY_BANK_ACCOUNT", "123-00-4567890"), "accountName": value("NEXT_PUBLIC_COMPANY_BANK_HOLDER", "PT NovaLogistik")}
}

const partnerJoin = `to_jsonb(p)||jsonb_build_object('user',jsonb_build_object('name',u.name,'username',u.username))`

func (b *business) topups(r *request) (any, error) {
	own := 0
	if r.actor["partnerType"] == "MARKETING" {
		own = idValue(r.actor["partnerId"])
	}
	if own == 0 && !hasPermission(r.actor, "wallet.topup.view") {
		return nil, problem(403, "Missing permission: wallet.topup.view")
	}
	rows, err := r.many(`SELECT to_jsonb(t)||jsonb_build_object('verifiedBy',CASE WHEN v.id IS NULL THEN NULL ELSE jsonb_build_object('name',v.name) END)||CASE WHEN $1=0 THEN jsonb_build_object('partner',`+partnerJoin+`) ELSE '{}'::jsonb END FROM "TopUpRequest" t JOIN "Partner" p ON p.id=t."partnerId" JOIN "User" u ON u.id=p."userId" LEFT JOIN "User" v ON v.id=t."verifiedById" WHERE ($1=0 OR t."partnerId"=$1) AND ($2='' OR t.status=$2) AND ($1<>0 OR $3='' OR p.type=$3) ORDER BY t."createdAt" DESC,t.id DESC`, own, r.c.Query("status"), r.c.Query("partnerType"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"topUps": rows, "bankInfo": companyBank()}, nil
}
func (b *business) createTopup(r *request) (any, error) {
	id := idValue(r.body["partnerId"])
	p, err := r.one(`SELECT jsonb_build_object('id',id) FROM "Partner" WHERE id=$1 AND type='MARKETING' AND "isActive"`, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fieldProblem("Partner Marketing aktif wajib dipilih.", "partnerId", "Partner Marketing tidak ditemukan atau tidak aktif.")
	}
	amount, err := minimum(r.body, "amount", 10000)
	if err != nil {
		return nil, err
	}
	proof := textValue(r.body["proofUrl"])
	if !strings.HasPrefix(proof, "data:") || len(proof) > 12000000 {
		return nil, fieldProblem("Bukti transfer wajib dilampirkan (maksimal 8 MB).", "proofUrl", "Bukti tidak valid.")
	}
	code, err := r.nextCode("TopUpRequest", "requestCode", "TOP-", 731040)
	if err != nil {
		return nil, err
	}
	t, err := r.insertRow("TopUpRequest", map[string]any{"requestCode": code, "partnerId": id, "amount": amount, "status": "PENDING_VERIFICATION", "partnerNote": nullable(r.body["note"]), "proofUrl": proof, "requestedById": r.actor["id"], "submittedForVerificationAt": time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "topup", t["id"], code, nil, map[string]any{"amount": amount, "status": "PENDING_VERIFICATION", "proofAttached": true})
	return map[string]any{"topUp": t, "bankInfo": companyBank()}, nil
}
func (b *business) reviewTopup(r *request) (any, error) {
	t, err := r.find("TopUpRequest", "Top up tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	if t["status"] != "PENDING_VERIFICATION" {
		return nil, problem(422, "Top up berstatus "+textValue(t["status"])+" tidak bisa diubah.")
	}
	var data map[string]any
	action := "cancelled"
	switch {
	case strings.HasSuffix(r.c.Path(), "/verify"):
		if idValue(r.actor["partnerId"]) == idValue(t["partnerId"]) {
			return nil, problem(403, "Akses ditolak - tidak boleh memverifikasi top up milik sendiri.")
		}
		ledger, err := r.ledger(t["partnerId"], "TOPUP", "CREDIT", number(t["amount"]), "topup", t["id"], fmt.Sprintf("TOP-%d", idValue(t["id"])), "Top up "+textValue(t["requestCode"])+" terverifikasi", false)
		if err != nil {
			return nil, err
		}
		data = map[string]any{"status": "VERIFIED", "verifiedById": r.actor["id"], "verifiedAt": time.Now().UTC(), "walletTransactionId": ledger["id"]}
		action = "verified"
	case strings.HasSuffix(r.c.Path(), "/reject"):
		data = map[string]any{"status": "REJECTED", "rejectReason": fallback(nullable(r.body["reason"]), "Tidak memenuhi ketentuan")}
		action = "rejected"
	default:
		data = map[string]any{"status": "CANCELLED", "rejectReason": fallback(nullable(r.body["reason"]), "Dibatalkan oleh Admin Kantor")}
	}
	updated, err := r.updateRow("TopUpRequest", idValue(t["id"]), data)
	if err != nil {
		return nil, err
	}
	if action == "verified" {
		updated["partner"], err = r.partnerBrief(t["partnerId"])
		if err != nil {
			return nil, err
		}
	}
	b.audit(r, action, "topup", t["id"], textValue(t["requestCode"]), nil, data)
	return updated, nil
}
func (r *request) partnerBrief(id any) (map[string]any, error) {
	return r.one(`SELECT `+partnerJoin+` FROM "Partner" p JOIN "User" u ON u.id=p."userId" WHERE p.id=$1`, id)
}
func (b *business) withdrawals(r *request) (any, error) {
	own := idValue(r.actor["partnerId"])
	if own == 0 && !hasPermission(r.actor, "wallet.withdrawal.view") {
		return nil, problem(403, "Missing permission: wallet.withdrawal.view")
	}
	return r.many(`SELECT to_jsonb(t)||jsonb_build_object('reviewedBy',CASE WHEN v.id IS NULL THEN NULL ELSE jsonb_build_object('name',v.name) END,'processedBy',CASE WHEN x.id IS NULL THEN NULL ELSE jsonb_build_object('name',x.name) END)||CASE WHEN $1=0 THEN jsonb_build_object('partner',`+partnerJoin+`) ELSE '{}'::jsonb END FROM "WithdrawalRequest" t JOIN "Partner" p ON p.id=t."partnerId" JOIN "User" u ON u.id=p."userId" LEFT JOIN "User" v ON v.id=t."reviewedById" LEFT JOIN "User" x ON x.id=t."processedById" WHERE ($1=0 OR t."partnerId"=$1) AND ($2='' OR t.status=$2) AND ($1<>0 OR $3='' OR p.type=$3) ORDER BY t."createdAt" DESC,t.id DESC`, own, r.c.Query("status"), r.c.Query("partnerType"))
}
func (b *business) createWithdrawal(r *request) (any, error) {
	id, err := r.ownPartner("")
	if err != nil {
		return nil, err
	}
	amount, err := minimum(r.body, "amount", 1)
	if err != nil {
		return nil, err
	}
	p, err := r.partnerBrief(id)
	if err != nil {
		return nil, err
	}
	if !truthy(p["bankAccountName"]) || !truthy(p["bankAccountNumber"]) {
		return nil, fieldProblem("Lengkapi rekening bank terdaftar di Profil sebelum melakukan withdrawal.", "bankAccountNumber", "Rekening bank wajib terdaftar.")
	}
	summary, err := r.walletSummary(id)
	if err != nil {
		return nil, err
	}
	if money(number(summary["available"])-amount) < -0.001 {
		return nil, problem(422, "Jumlah melebihi saldo tersedia.")
	}
	code, err := r.nextCode("WithdrawalRequest", "requestCode", "WDR-", 731041)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"requestCode": code, "partnerId": id, "amount": amount, "status": "PENDING", "partnerNote": nullable(r.body["note"]), "requestedById": r.actor["id"]}
	for _, k := range []string{"bankName", "bankAccountName", "bankAccountNumber"} {
		data[k] = p[k]
	}
	w, err := r.insertRow("WithdrawalRequest", data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "withdrawal", w["id"], code, nil, map[string]any{"amount": amount, "status": "PENDING"})
	return w, nil
}
func (b *business) reviewWithdrawal(r *request) (any, error) {
	w, err := r.find("WithdrawalRequest", "Permintaan withdrawal tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	verb := r.c.Path()[strings.LastIndex(r.c.Path(), "/")+1:]
	status := textValue(w["status"])
	allowed := false
	next := ""
	data := map[string]any{}
	now := time.Now().UTC()
	switch verb {
	case "approve":
		allowed = status == "PENDING"
		next = "APPROVED"
		data["reviewedById"], data["reviewedAt"] = r.actor["id"], now
	case "reject":
		allowed = status == "PENDING" || status == "APPROVED"
		next = "REJECTED"
		data["reviewedById"], data["reviewedAt"] = r.actor["id"], now
		data["rejectReason"] = fallback(nullable(r.body["reason"]), "Tidak disetujui")
	case "cancel":
		id, e := r.ownPartner("")
		if e != nil {
			return nil, e
		}
		if id != idValue(w["partnerId"]) {
			return nil, problem(403, "Akses ditolak - ini bukan permintaan withdrawal milik Anda.")
		}
		allowed = status == "PENDING" || status == "APPROVED"
		next = "CANCELLED"
		data["rejectReason"] = fallback(nullable(r.body["reason"]), "Dibatalkan oleh partner")
	case "fail":
		allowed = status == "PENDING" || status == "APPROVED" || status == "PROCESSING"
		next = "FAILED"
		data["rejectReason"] = fallback(nullable(r.body["reason"]), "Transfer bank gagal")
	case "process":
		allowed = status == "APPROVED"
		next = "PROCESSING"
		data["processedById"], data["processedAt"] = r.actor["id"], now
	case "complete":
		allowed = status == "APPROVED" || status == "PROCESSING"
		next = "COMPLETED"
		data["processedById"], data["processedAt"], data["completedAt"] = r.actor["id"], fallback(w["processedAt"], now), now
	}
	if !allowed {
		return nil, problem(422, "Withdrawal berstatus "+status+" tidak bisa diubah.")
	}
	if verb == "process" || verb == "complete" {
		if proof := nullable(r.body["proofUrl"]); proof != nil {
			data["transferProofUrl"] = proof
		}
	}
	if verb == "complete" {
		t, e := r.ledger(w["partnerId"], "WITHDRAWAL", "DEBIT", number(w["amount"]), "withdrawal", w["id"], fmt.Sprintf("WDR-%d", idValue(w["id"])), "Withdrawal "+textValue(w["requestCode"]), false)
		if e != nil {
			return nil, e
		}
		data["walletTransactionId"] = t["id"]
	}
	data["status"] = next
	updated, err := r.updateRow("WithdrawalRequest", idValue(w["id"]), data)
	if err != nil {
		return nil, err
	}
	if verb != "cancel" {
		updated["partner"], err = r.partnerBrief(w["partnerId"])
		if err != nil {
			return nil, err
		}
	}
	action := map[string]string{"approve": "approved", "reject": "rejected", "cancel": "cancelled", "fail": "failed", "process": "status_change", "complete": "completed"}[verb]
	b.audit(r, action, "withdrawal", w["id"], textValue(w["requestCode"]), nil, map[string]any{"status": next, "amount": w["amount"], "reason": data["rejectReason"]})
	return updated, nil
}
