package httpapi

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func canEnterShipmentExpense(s, c map[string]any) bool {
	return strings.EqualFold(textValue(c["type"]), "b2b") && s["createdByPartnerId"] == nil && (s["status"] == "RECEIVED_AT_GUDANG" || s["status"] == "ARRIVED_AT_GUDANG" && s["destReceivedAt"] != nil)
}

func (b *business) shipmentExpenses(r *request) (any, error) {
	if !hasPermission(r.actor, "transport.expense.view") {
		return nil, problem(403, "Missing permission: transport.expense.view")
	}
	if idValue(r.c.Params("id")) == 0 {
		return nil, problem(422, "Shipment tidak valid.")
	}
	s, err := r.shipment(false)
	if err != nil {
		return nil, err
	}
	c, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT jsonb_build_object('id',e.id,'expenseCode',e."expenseCode",'type',e.type,'amount',e.amount,'description',e.description,'status',e.status,'createdAt',e."createdAt",'approvedAt',e."approvedAt",'approvedByUsername',e."approvedByUsername",'approvedByRole',e."approvedByRole",'cancellationReason',e."cancellationReason",'shipment',jsonb_build_object('id',s.id,'masterCode',s."masterCode",'origin',s.origin,'destination',s.destination),'transport',CASE WHEN t.id IS NULL THEN NULL ELSE jsonb_build_object('id',t.id,'transportCode',t."transportCode",'origin',t.origin,'destination',t.destination) END) FROM "TransportExpense" e JOIN "MasterShipment" s ON s.id=e."shipmentId" LEFT JOIN "Transport" t ON t.id=e."transportId" WHERE e."shipmentId"=$1 ORDER BY e."createdAt" DESC,e.id DESC`, s["id"])
	if err != nil {
		return nil, err
	}
	approved, pending := 0.0, 0.0
	for _, row := range rows {
		if row["status"] == "APPROVED" {
			approved += number(row["amount"])
		}
		if row["status"] == "PENDING" {
			pending += number(row["amount"])
		}
	}
	return map[string]any{"customerType": c["type"], "canCreate": hasPermission(r.actor, "shipment.expense.create") && canEnterShipmentExpense(s, c), "rows": rows, "approvedTotal": approved, "pendingTotal": pending}, nil
}

func (b *business) createShipmentExpense(r *request) (any, error) {
	if idValue(r.c.Params("id")) == 0 {
		return nil, problem(422, "Shipment tidak valid.")
	}
	switch r.body["type"] {
	case "BONGKAR", "PARKIR", "MAKAN", "BBM", "ANGKUT", "TOL":
	default:
		return nil, problem(422, "Jenis biaya harus salah satu dari: BONGKAR, PARKIR, MAKAN, BBM, ANGKUT, TOL.")
	}
	amount := numeric(r.body["amount"])
	if amount == nil {
		return nil, problem(422, "Nominal biaya wajib diisi.")
	}
	if number(amount) <= 0 {
		return nil, problem(422, "Nominal biaya harus lebih dari 0.")
	}
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	c, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	if !canEnterShipmentExpense(s, c) {
		return nil, problem(409, "Biaya hanya untuk B2B non-Marketing setelah konfirmasi tiba di gudang.")
	}
	code, err := r.nextCode("TransportExpense", "expenseCode", "EXP-2026-", 731027)
	if err != nil {
		return nil, err
	}
	var description any
	if v, ok := r.body["description"].(string); ok {
		runes := []rune(strings.TrimSpace(v))
		description = string(runes[:min(1000, len(runes))])
	}
	e, err := r.insertRow("TransportExpense", map[string]any{"shipmentId": s["id"], "expenseCode": code, "type": r.body["type"], "amount": amount, "description": description, "createdById": r.actor["id"], "status": "PENDING"})
	if err != nil {
		return nil, err
	}
	b.audit(r, "create", "transport_expense", e["id"], "", nil, e)
	return e, nil
}

func (r *request) shipmentExpense() (map[string]any, error) {
	if idValue(r.c.Params("id")) == 0 || idValue(r.c.Params("expenseId")) == 0 {
		return nil, problem(422, "ID biaya atau shipment tidak valid.")
	}
	s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s JOIN "TransportExpense" e ON e."shipmentId"=s.id WHERE s.id=$1 AND e.id=$2 FOR UPDATE OF s`, idValue(r.c.Params("id")), idValue(r.c.Params("expenseId")))
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, problem(404, "Biaya tidak ditemukan.")
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, err
	}
	e, err := r.one(`SELECT to_jsonb(e) FROM "TransportExpense" e WHERE id=$1 AND "shipmentId"=$2 FOR UPDATE`, idValue(r.c.Params("expenseId")), s["id"])
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, problem(404, "Biaya tidak ditemukan.")
	}
	e["shipment"] = s
	return e, nil
}

func (b *business) approveShipmentExpense(r *request) (any, error) {
	e, err := r.shipmentExpense()
	if err != nil {
		return nil, err
	}
	if r.body["action"] == "cancel" {
		return r.reverseShipmentExpense(e, "cancel")
	}
	if r.body["action"] != "approve" {
		return nil, problem(422, "Tindakan tidak valid.")
	}
	if e["status"] != "PENDING" {
		return nil, problem(409, "Hanya biaya PENDING yang dapat disetujui.")
	}
	roles := []string{}
	for _, role := range r.actor["roles"].([]any) {
		roles = append(roles, textValue(role.(map[string]any)["name"]))
	}
	sort.Strings(roles)
	role := strings.Join(roles, ", ")
	if truthy(r.actor["isOwner"]) {
		role = "Owner"
	}
	approval := map[string]any{"status": "APPROVED", "approvedAt": time.Now().UTC(), "approvedById": r.actor["id"], "approvedByUsername": r.actor["username"], "approvedByRole": role}
	updated, err := r.updateRow("TransportExpense", idValue(e["id"]), approval)
	if err != nil {
		return nil, err
	}
	approval["approvedAt"] = updated["approvedAt"]
	b.audit(r, "approve", "transport_expense", e["id"], "", nil, approval)
	return approval, nil
}

func (b *business) deleteShipmentExpense(r *request) (any, error) {
	e, err := r.shipmentExpense()
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
	b.audit(r, "delete", "transport_expense", e["id"], "", e, nil)
	return map[string]any{"deleted": true}, nil
}

func (r *request) reverseShipmentExpense(e map[string]any, action string) (any, error) {
	permission, status := "transport.expense.approve", "CANCELLED"
	if action == "remove" {
		permission, status = "transport.expense.delete-approved", "REMOVED"
	}
	if !hasPermission(r.actor, permission) {
		return nil, problem(403, "Missing permission: "+permission)
	}
	reason, _ := r.body["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" || jsStringLength(reason) > 1000 {
		return nil, problem(422, "Alasan wajib diisi (maksimal 1000 karakter).")
	}
	if action == "cancel" && e["status"] != "APPROVED" {
		return nil, problem(409, "Hanya biaya APPROVED yang dapat dibatalkan.")
	}
	if action == "remove" && e["status"] != "APPROVED" && e["status"] != "CANCELLED" {
		return nil, problem(409, "Status biaya tidak dapat dihapus melalui pembatalan approval.")
	}
	if action == "cancel" {
		if _, err := r.updateRow("TransportExpense", idValue(e["id"]), map[string]any{"status": status, "cancelledAt": time.Now().UTC(), "cancelledById": r.actor["id"], "cancellationReason": reason}); err != nil {
			return nil, err
		}
	} else if err := r.exec(`DELETE FROM "TransportExpense" WHERE id=$1`, e["id"]); err != nil {
		return nil, err
	}
	before, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	after, err := json.Marshal(map[string]any{"reason": reason, "status": status})
	if err != nil {
		return nil, err
	}
	// Reversals require a durable reason, so this audit shares the transaction.
	if err = r.exec(`INSERT INTO "AuditLog" (action,"entityType","entityId","entityLabel","actorId","beforeData","afterData") VALUES ($1,'transport_expense',$2,$3,$4,$5,$6)`, action, e["id"], e["expenseCode"], r.actor["id"], string(before), string(after)); err != nil {
		return nil, err
	}
	return map[string]any{"status": status}, nil
}
