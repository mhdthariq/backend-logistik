package httpapi

import (
	"fmt"
	"reflect"
	"time"

	"github.com/gofiber/fiber/v3"
)

func (b *business) registerReturnTasks(app *fiber.App) {
	app.Get("/api/v1/return-tasks", b.handle("return-task.view", true, false, b.returnTasks))
	app.Get("/api/v1/return-tasks/:id", b.handle("return-task.view", true, false, b.returnTask))
	app.Post("/api/v1/return-tasks/:id/approve", b.handle("return-task.approve", true, true, b.approveReturnTask))
	app.Post("/api/v1/return-tasks/:id/create-transport", b.handle("return-task.create", true, true, b.createReturnTransport))
}
func (b *business) createReturnTask(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	var returning, active bool
	if err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "ReturnTask" WHERE "returnTransportId"=$1),EXISTS(SELECT 1 FROM "ReturnTask" WHERE "originalTransportId"=$1 AND status<>'CANCELLED')`, t["id"]).Scan(&returning, &active); err != nil {
		return nil, err
	}
	if returning {
		return nil, problem(422, "Ini sudah merupakan transport balik.")
	}
	if t["deliveryApprovedAt"] == nil {
		return nil, problem(422, "Transport belum disetujui (delivery approval) - kendaraan belum bisa dianggap kosong.")
	}
	if len(transportShips(t)) == 0 {
		return nil, problem(422, "Transport tanpa resi tidak membutuhkan resi tugas balik.")
	}
	if dropProgress(transportShips(t))["allApproved"] != true {
		return nil, problem(422, "Masih ada resi yang belum DELIVERY_APPROVED - kendaraan belum kosong.")
	}
	if active {
		return nil, problem(422, "Resi tugas balik untuk transport ini sudah ada.")
	}
	route := object(t["route"])
	origin, dest := fallback(t["destination"], route["destination"]), fallback(t["origin"], route["origin"])
	if textValue(origin) == "" || textValue(dest) == "" {
		return nil, problem(422, "Asal/tujuan transport tidak lengkap - tidak bisa membuat rute balik.")
	}
	code, err := r.nextCode("ReturnTask", "returnTaskCode", "RTN-2026-", 731032)
	if err != nil {
		return nil, err
	}
	task, err := r.insertRow("ReturnTask", map[string]any{"returnTaskCode": code, "originalTransportId": t["id"], "vehicleId": t["vehicleId"], "driverId": t["driverId"], "origin": origin, "destination": dest, "status": "CREATED", "createdById": r.actor["id"]})
	if err != nil {
		return nil, err
	}
	b.audit(r, "create", "return_task", task["id"], fmt.Sprintf("%s ← %s (%s → %s)", code, t["transportCode"], origin, dest), nil, nil)
	return task, nil
}
func (b *business) returnTasks(r *request) (any, error) {
	return r.many(`SELECT jsonb_build_object('id',rt.id,'returnTaskCode',rt."returnTaskCode",'status',rt.status,'origin',rt.origin,'destination',rt.destination,'vehicleNumber',v."vehicleNumber",'driverName',e.name,'originalTransportId',rt."originalTransportId",'originalTransportCode',t."transportCode",'returnTransportId',rt."returnTransportId",'returnTransportCode',back."transportCode",'approvedAt',rt."approvedAt",'createdAt',rt."createdAt") FROM "ReturnTask" rt JOIN "Transport" t ON t.id=rt."originalTransportId" JOIN "Vehicle" v ON v.id=t."vehicleId" LEFT JOIN "Employee" e ON e.id=t."driverId" LEFT JOIN "Transport" back ON back.id=rt."returnTransportId" WHERE ($1='' OR rt.status=$1) AND ($2='' OR strpos(lower(rt."returnTaskCode"),lower($2))>0 OR strpos(lower(rt.origin),lower($2))>0 OR strpos(lower(rt.destination),lower($2))>0) AND ($3 OR EXISTS(SELECT 1 FROM "Employee" me JOIN "Warehouse" w ON w.id=me."warehouseId" WHERE me.id=$4 AND (lower(rt.origin)=lower(w.city) OR lower(rt.destination)=lower(w.city)))) ORDER BY rt."createdAt" DESC,rt.id DESC`, r.c.Query("status"), r.c.Query("search"), hasPermission(r.actor, "*"), r.actor["employeeId"])
}
func (r *request) loadReturnTask(lock bool) (map[string]any, error) {
	return r.find("ReturnTask", "Resi tugas balik tidak ditemukan.", lock)
}
func (b *business) returnTask(r *request) (any, error) {
	task, err := r.loadReturnTask(false)
	if err != nil {
		return nil, err
	}
	t, err := r.loadTransport(idValue(task["originalTransportId"]), false)
	if err != nil {
		return nil, err
	}
	original := rawTransport(t)
	original["vehicle"], original["route"] = t["vehicle"], t["route"]
	original["driver"], err = r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1`, t["driverId"])
	if err != nil {
		return nil, err
	}
	task["originalTransport"] = original
	task["returnTransport"], err = r.one(`SELECT jsonb_build_object('id',id,'transportCode',"transportCode",'status',status) FROM "Transport" WHERE id=$1`, task["returnTransportId"])
	if err != nil {
		return nil, err
	}
	preview := []map[string]any{}
	if t["trackingMode"] != "ACTIVITY" {
		cps := checkpoints(t, false)
		for i := len(cps) - 1; i >= 0; i-- {
			preview = append(preview, map[string]any{"sequence": len(cps) - i, "name": cps[i]["name"]})
		}
	}
	task["returnCheckpoints"] = preview
	return task, nil
}
func (b *business) approveReturnTask(r *request) (any, error) {
	task, err := r.loadReturnTask(true)
	if err != nil {
		return nil, err
	}
	if task["status"] != "CREATED" {
		return nil, problem(422, fmt.Sprintf("Resi tugas balik berstatus %s - hanya CREATED yang bisa disetujui.", task["status"]))
	}
	updated, err := r.updateRow("ReturnTask", idValue(task["id"]), map[string]any{"status": "APPROVED", "approvedById": r.actor["id"], "approvedAt": time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	b.audit(r, "approve", "return_task", task["id"], textValue(task["returnTaskCode"])+" → APPROVED", nil, nil)
	return updated, nil
}
func checkpointPlan(cps []map[string]any) []map[string]any {
	plan := []map[string]any{}
	for _, cp := range cps {
		row := map[string]any{}
		for _, key := range []string{"name", "sequence", "latitude", "longitude", "radiusMeters"} {
			row[key] = cp[key]
		}
		plan = append(plan, row)
	}
	return plan
}
func (b *business) createReturnTransport(r *request) (any, error) {
	if err := r.exec(`SELECT pg_advisory_xact_lock(731030)`); err != nil {
		return nil, err
	}
	task, err := r.loadReturnTask(true)
	if err != nil {
		return nil, err
	}
	if task["returnTransportId"] != nil {
		return nil, problem(422, "Transport balik sudah dibuat untuk tugas ini.")
	}
	if task["status"] != "APPROVED" {
		return nil, problem(422, fmt.Sprintf("Resi tugas balik berstatus %s - harus APPROVED dulu.", task["status"]))
	}
	original, err := r.loadTransport(idValue(task["originalTransportId"]), false)
	if err != nil {
		return nil, err
	}
	cps := checkpoints(original, true)
	if original["trackingMode"] == "ACTIVITY" {
		cps = []map[string]any{}
	} else if len(cps) < 3 {
		return nil, problem(422, "Rute asal punya kurang dari 3 checkpoint - rute balik tidak bisa dibuat.")
	}
	depart := dateValue(r.body["plannedDepartureAt"], false)
	if depart == nil {
		depart = time.Now().UTC()
	}
	arrive := dateValue(r.body["plannedArrivalAt"], false)
	if arrive == nil {
		arrive = depart.(time.Time).Add(6 * time.Hour)
	}
	if arrive.(time.Time).Before(depart.(time.Time)) {
		return nil, problem(422, "Rencana tiba tidak boleh lebih awal dari rencana berangkat.")
	}
	data := map[string]any{"vehicleId": task["vehicleId"], "driverId": task["driverId"], "kenekId": original["kenekId"]}
	if err = r.availableTransportResources(data, 0); err != nil {
		return nil, err
	}
	reversed := []map[string]any{}
	for i := len(cps) - 1; i >= 0; i-- {
		cp := checkpointPlan([]map[string]any{cps[i]})[0]
		cp["sequence"] = float64(len(cps) - i)
		reversed = append(reversed, cp)
	}
	routeData := map[string]any{"name": textValue(fallback(object(original["route"])["name"], "Rute")) + " (Balik)", "origin": task["origin"], "destination": task["destination"], "isActive": true}
	candidates, err := r.many(`SELECT to_jsonb(r)||jsonb_build_object('checkpoints',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.sequence) FROM "Checkpoint" c WHERE c."routeId"=r.id AND c."isActive"),'[]'::jsonb)) FROM "Route" r WHERE name=$1 AND origin=$2 AND destination=$3 AND "isActive" ORDER BY id`, routeData["name"], task["origin"], task["destination"])
	if err != nil {
		return nil, err
	}
	var route map[string]any
	for _, candidate := range candidates {
		if reflect.DeepEqual(checkpointPlan(objects(candidate["checkpoints"])), reversed) {
			route = candidate
			break
		}
	}
	if route == nil {
		route, err = r.insertRow("Route", routeData)
		if err != nil {
			return nil, err
		}
		for _, cp := range reversed {
			cp["routeId"], cp["isActive"] = route["id"], true
			if _, err = r.insertRow("Checkpoint", cp); err != nil {
				return nil, err
			}
		}
	}
	code, err := r.nextCode("Transport", "transportCode", "TRP-2026-", 731031)
	if err != nil {
		return nil, err
	}
	data["transportCode"], data["routeId"], data["origin"], data["destination"], data["plannedDepartureAt"], data["plannedArrivalAt"], data["status"], data["transportMode"], data["trackingMode"] = code, route["id"], task["origin"], task["destination"], depart, arrive, "PLANNED", "DIRECT", original["trackingMode"]
	t, err := r.insertRow("Transport", data)
	if err != nil {
		return nil, err
	}
	updated, err := r.updateRow("ReturnTask", idValue(task["id"]), map[string]any{"returnTransportId": t["id"], "status": "PLANNED"})
	if err != nil {
		return nil, err
	}
	b.audit(r, "create", "transport", t["id"], code+" (balik) ← "+textValue(task["returnTaskCode"]), nil, nil)
	return map[string]any{"returnTask": updated, "transport": map[string]any{"id": t["id"], "transportCode": code, "status": t["status"]}}, nil
}
