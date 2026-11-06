package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

func changedValues(before, after map[string]any) map[string]any {
	changes := map[string]any{}
	for k, v := range after {
		if before[k] != v {
			changes[k] = map[string]any{"before": before[k], "after": v}
		}
	}
	return changes
}

func (b *business) registerRepairs(app *fiber.App) {
	app.Get("/api/v1/repairs", b.handle("", true, false, b.repairs))
	app.Post("/api/v1/repairs", b.handle("repair.create", true, true, b.createRepair))
	app.Get("/api/v1/repairs/logs", b.handle("", true, false, b.repairLogs))
	app.Get("/api/v1/repairs/vehicles", b.handle("repair.create", true, false, b.repairVehicles))
	app.Put("/api/v1/repairs/:id", b.handle("repair.update", true, true, b.updateRepair))
	app.Delete("/api/v1/repairs/:id", b.handle("repair.delete", true, true, b.deleteRepair))
}

const repairSelect = `SELECT to_jsonb(r)||jsonb_build_object('vehicle',jsonb_build_object('id',v.id,'vehicleNumber',v."vehicleNumber",'name',v.name),'owner',CASE WHEN p.id IS NULL THEN NULL ELSE ` + partnerJoin + ` END) FROM "VehicleRepair" r JOIN "Vehicle" v ON v.id=r."vehicleId" LEFT JOIN "Partner" p ON p.id=r."ownerId" LEFT JOIN "User" u ON u.id=p."userId" `

func (r *request) repairScope() (int, error) {
	if r.actor["partnerType"] == "VEHICLE_OWNER" && idValue(r.actor["partnerId"]) > 0 {
		return idValue(r.actor["partnerId"]), nil
	}
	if !hasPermission(r.actor, "repair.view") {
		return 0, problem(403, "Missing permission: repair.view")
	}
	return 0, nil
}
func (b *business) repairs(r *request) (any, error) {
	id, err := r.repairScope()
	if err != nil {
		return nil, err
	}
	return r.many(repairSelect+`WHERE ($1=0 OR r."ownerId"=$1) AND ($2='' OR r.status=$2) ORDER BY r."createdAt" DESC,r.id DESC`, id, r.c.Query("status"))
}
func (b *business) repairVehicles(r *request) (any, error) {
	return r.many(`SELECT jsonb_build_object('id',id,'vehicleNumber',"vehicleNumber",'ownerId',"ownerId") FROM "Vehicle" ORDER BY "vehicleNumber",id`)
}
func (b *business) repairLogs(r *request) (any, error) {
	id, err := r.repairScope()
	if err != nil {
		return nil, err
	}
	limit := 50
	if n := numeric(r.c.Query("limit")); n != nil {
		limit = int(math.Min(200, math.Max(1, number(n))))
	}
	rid := idValue(r.c.Query("repairId"))
	action := r.c.Query("action")
	where := ` FROM "RepairActionLog" l WHERE ($1=0 OR "ownerId"=$1) AND ($2=0 OR "repairId"=$2) AND ($3='' OR action=$3)`
	var count int
	if err = r.db.QueryRow(r.ctx, `SELECT count(*)`+where, id, rid, action).Scan(&count); err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT to_jsonb(l)`+where+` ORDER BY "createdAt" DESC,id DESC LIMIT $4`, id, rid, action, limit)
	if err != nil {
		return nil, err
	}
	for _, l := range rows {
		var changes any
		if s, ok := l["changes"].(string); ok {
			if err := json.Unmarshal([]byte(s), &changes); err != nil {
				changes = nil
			}
		}
		l["changes"] = changes
	}
	return Envelope{Data: rows, Meta: map[string]any{"total": count, "limit": limit}}, nil
}
func (r *request) repairInput(existing map[string]any) (map[string]any, error) {
	desc, err := required(r.body, "description")
	if err != nil {
		return nil, err
	}
	amount, err := minimum(r.body, "amount", 1)
	if err != nil {
		return nil, err
	}
	proof := nullable(r.body["proofUrl"])
	if proof == nil && existing != nil {
		proof = existing["proofUrl"]
	}
	if !truthy(proof) {
		return nil, fieldProblem("Bukti repair (invoice/nota/foto) wajib dilampirkan.", "proofUrl", "Bukti wajib dilampirkan.")
	}
	at := dateValue(r.body["repairDate"], false)
	if at == nil && existing != nil {
		at = dateValue(existing["repairDate"], false)
	}
	if at == nil {
		at = time.Now().UTC()
	}
	var transport any
	if raw := r.body["relatedTransportId"]; raw != nil && raw != "" {
		id := idValue(raw)
		if id == 0 {
			return nil, problem(422, "Transport tidak valid.")
		}
		transport = id
	}
	vehicle := r.body["vehicleId"]
	if existing != nil {
		vehicle = existing["vehicleId"]
	}
	if transport != nil {
		t, err := r.one(`SELECT to_jsonb(t) FROM "Transport" t WHERE id=$1`, transport)
		if err != nil {
			return nil, err
		}
		if t == nil || idValue(t["vehicleId"]) != idValue(vehicle) {
			return nil, fieldProblem("Transport terkait tidak cocok dengan kendaraan repair ini.", "relatedTransportId", "Transport tidak cocok.")
		}
	}
	return map[string]any{"description": desc, "amount": amount, "repairDate": at, "workshopVendor": nullable(r.body["workshopVendor"]), "proofUrl": proof, "notes": nullable(r.body["notes"]), "relatedTransportId": transport}, nil
}
func (r *request) logRepair(repair map[string]any, vehicle, action, detail string, changes any) error {
	var encoded any
	if changes != nil {
		raw, err := json.Marshal(changes)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	return r.exec(`INSERT INTO "RepairActionLog" ("repairId","repairCode","ownerId","vehicleNumber",action,detail,changes,amount,"actorId","actorName") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, repair["id"], repair["repairCode"], repair["ownerId"], vehicle, action, detail, encoded, repair["amount"], r.actor["id"], r.actor["name"])
}
func (b *business) createRepair(r *request) (any, error) {
	vehicle := idValue(r.body["vehicleId"])
	v, err := r.one(`SELECT to_jsonb(v) FROM "Vehicle" v WHERE id=$1 FOR UPDATE`, vehicle)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, problem(404, "Kendaraan tidak ditemukan.")
	}
	data, err := r.repairInput(nil)
	if err != nil {
		return nil, err
	}
	code, err := r.nextCode("VehicleRepair", "repairCode", "REP-", 731044)
	if err != nil {
		return nil, err
	}
	data["repairCode"], data["vehicleId"], data["ownerId"], data["status"], data["createdById"], data["verifiedAt"] = code, vehicle, v["ownerId"], "VERIFIED", r.actor["id"], time.Now().UTC()
	repair, err := r.insertRow("VehicleRepair", data)
	if err != nil {
		return nil, err
	}
	var ledger any
	deducted := 0.0
	if v["ownerId"] != nil {
		t, e := r.ledger(v["ownerId"], "REPAIR_DEDUCTION", "DEBIT", number(data["amount"]), "repair", repair["id"], fmt.Sprintf("REP-%d", idValue(repair["id"])), "Deduction repair "+code, false)
		if e != nil {
			return nil, e
		}
		ledger = t["id"]
		deducted = number(data["amount"])
	}
	repair, err = r.updateRow("VehicleRepair", idValue(repair["id"]), map[string]any{"walletTransactionId": ledger, "deductedAmount": deducted})
	if err != nil {
		return nil, err
	}
	if err = r.logRepair(repair, textValue(v["vehicleNumber"]), "CREATED", "Repair "+code+" dibuat dan langsung terverifikasi.", nil); err != nil {
		return nil, err
	}
	b.audit(r, "created", "repair", repair["id"], code, nil, map[string]any{"vehicle": v["vehicleNumber"], "amount": data["amount"], "status": "VERIFIED", "immediatelyDeducted": v["ownerId"] != nil})
	return r.one(repairSelect+`WHERE r.id=$1`, repair["id"])
}
func (b *business) updateRepair(r *request) (any, error) {
	repair, err := r.find("VehicleRepair", "Repair tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	data, err := r.repairInput(repair)
	if err != nil {
		return nil, err
	}
	before := map[string]any{}
	after := map[string]any{}
	for k, v := range data {
		before[k] = repair[k]
		after[k] = v
		if k == "repairDate" {
			before[k] = textValue(repair[k])[:10]
			after[k] = v.(time.Time).Format("2006-01-02")
		}
		if k == "relatedTransportId" && v != nil {
			after[k] = number(v)
		}
		if k == "notes" || k == "workshopVendor" || k == "proofUrl" {
			before[k] = textValue(before[k])
			after[k] = textValue(after[k])
		}
	}
	changes := changedValues(before, after)
	if len(changes) == 0 {
		return nil, problem(422, "Tidak ada perubahan yang terdeteksi - tidak ada yang perlu disimpan.")
	}
	delta := money(number(data["amount"]) - number(repair["amount"]))
	deducted := 0.0
	if repair["ownerId"] != nil {
		deducted = number(repair["deductedAmount"])
		if delta != 0 {
			direction := "DEBIT"
			amount := delta
			if delta < 0 {
				direction = "CREDIT"
				amount = -delta
			}
			if _, err = r.ledger(repair["ownerId"], "REPAIR_DEDUCTION", direction, amount, "repair", repair["id"], fmt.Sprintf("REP-ADJ-%d-%d", idValue(repair["id"]), time.Now().UnixNano()), "Penyesuaian repair "+textValue(repair["repairCode"]), false); err != nil {
				return nil, err
			}
			deducted = money(deducted + delta)
		}
	}
	data["deductedAmount"] = deducted
	updated, err := r.updateRow("VehicleRepair", idValue(repair["id"]), data)
	if err != nil {
		return nil, err
	}
	v, err := r.one(`SELECT to_jsonb(v) FROM "Vehicle" v WHERE id=$1`, repair["vehicleId"])
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for k := range changes {
		if k != "proofUrl" {
			keys = append(keys, k)
		}
	}
	if err = r.logRepair(updated, textValue(v["vehicleNumber"]), "UPDATED", "Repair diubah oleh "+textValue(r.actor["name"])+" - field: "+strings.Join(keys, ", "), changes); err != nil {
		return nil, err
	}
	b.audit(r, "updated", "repair", repair["id"], textValue(repair["repairCode"]), nil, map[string]any{"changes": changes, "walletAdjustment": delta})
	return r.one(repairSelect+`WHERE r.id=$1`, repair["id"])
}
func (b *business) deleteRepair(r *request) (any, error) {
	repair, err := r.find("VehicleRepair", "Repair tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	if repair["ownerId"] != nil && number(repair["deductedAmount"]) > 0 {
		if _, err = r.ledger(repair["ownerId"], "REPAIR_DEDUCTION", "CREDIT", number(repair["deductedAmount"]), "repair", repair["id"], fmt.Sprintf("REP-DEL-%d", idValue(repair["id"])), "Pengembalian penuh deduction repair "+textValue(repair["repairCode"]), false); err != nil {
			return nil, err
		}
	}
	v, err := r.one(`SELECT to_jsonb(v) FROM "Vehicle" v WHERE id=$1`, repair["vehicleId"])
	if err != nil {
		return nil, err
	}
	if err = r.logRepair(repair, textValue(v["vehicleNumber"]), "DELETED", "Repair dihapus oleh "+textValue(r.actor["name"]), nil); err != nil {
		return nil, err
	}
	if err = r.exec(`DELETE FROM "VehicleRepair" WHERE id=$1`, repair["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "repair", repair["id"], textValue(repair["repairCode"]), nil, map[string]any{"vehicle": v["vehicleNumber"], "amount": repair["amount"], "refunded": repair["deductedAmount"]})
	return map[string]any{"id": repair["id"], "deleted": true}, nil
}
