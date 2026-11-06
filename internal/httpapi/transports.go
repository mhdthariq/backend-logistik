package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/capacity"
	"novalogistik/backend/internal/pricing"
)

func (b *business) registerTransports(app *fiber.App) {
	app.Get("/api/v1/transports", b.handle("transport.view", true, false, b.transports))
	app.Post("/api/v1/transports", b.handle("transport.create", true, true, b.createTransport))
	app.Get("/api/v1/transports/:id", b.handle("", true, false, b.transportDetail))
	app.Put("/api/v1/transports/:id", b.handle("transport.create", true, true, b.updateTransport))
	app.Delete("/api/v1/transports/:id", b.handle("transport.create", true, true, b.deleteTransport))
	app.Get("/api/v1/transports/:id/activities", b.handle("transport.view", true, false, b.transportActivities))
	app.Post("/api/v1/transports/:id/activities", b.handle("transport.checkin", true, true, b.createTransportActivity))
	app.Post("/api/v1/transports/:id/checkins", b.handle("transport.checkin", true, true, b.transportCheckin))
	app.Post("/api/v1/transports/:id/depart", b.handle("transport.depart", true, true, b.departTransport))
	app.Post("/api/v1/transports/:id/arrive", b.handle("transport.arrive", true, true, b.arriveTransport))
	app.Post("/api/v1/transports/:id/complete", b.handle("transport.complete", true, true, b.completeTransport))
	app.Post("/api/v1/transports/:id/undo-arrival", b.handle("", true, true, b.undoTransportArrival))
	app.Post("/api/v1/transports/:id/print-log", b.handle("transport.view", true, false, b.printTransport))
	app.Get("/api/v1/transports/:id/drops", b.handle("transport.view", true, false, b.transportDrops))
	app.Post("/api/v1/transports/:id/drops/:shipmentId", b.handle("transport.view", true, true, b.dropTransportShipment))
	app.Post("/api/v1/transports/:id/drops/:shipmentId/approve", b.handle("transport.delivery.approve", true, true, b.approveTransportDrop))
	app.Post("/api/v1/transports/:id/delivery-approval", b.handle("transport.delivery.approve", true, true, b.approveTransportDelivery))
	app.Get("/api/v1/transports/:id/expenses", b.handle("transport.expense.view", true, false, b.transportExpenses))
	app.Post("/api/v1/transports/:id/expenses", b.handle("transport.expense.create", true, true, b.createTransportExpense))
	app.Get("/api/v1/transports/:id/expenses/:expenseId", b.handle("transport.expense.view", true, false, b.transportExpense))
	app.Put("/api/v1/transports/:id/expenses/:expenseId", b.handle("transport.expense.update", true, true, b.updateTransportExpense))
	app.Delete("/api/v1/transports/:id/expenses/:expenseId", b.handle("", true, true, b.deleteTransportExpense))
	app.Post("/api/v1/transports/:id/expenses/:expenseId/approve", b.handle("transport.expense.approve", true, true, b.approveTransportExpense))
	app.Post("/api/v1/transports/:id/expenses/:expenseId/cancel", b.handle("transport.expense.approve", true, true, b.cancelTransportExpense))
	app.Post("/api/v1/transports/:id/settle", b.handle("transport.settle", true, true, b.settleTransport))
	app.Post("/api/v1/transports/:id/return-task", b.handle("return-task.create", true, true, b.createReturnTask))
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func objects(v any) []map[string]any {
	out := []map[string]any{}
	if list, ok := v.([]any); ok {
		for _, item := range list {
			out = append(out, object(item))
		}
	}
	return out
}
func crew(user, t map[string]any) bool {
	id := idValue(user["employeeId"])
	return id > 0 && (id == idValue(t["driverId"]) || id == idValue(t["kenekId"]))
}
func executorOnly(user map[string]any) bool {
	return !hasPermission(user, "transport.create") && !hasPermission(user, "transport.depart") && !hasPermission(user, "transport.arrive")
}
func (r *request) loadTransport(id int, lock bool) (map[string]any, error) {
	if id == 0 {
		return nil, problem(422, "ID transport tidak valid.")
	}
	t, err := r.one(`SELECT to_jsonb(t) FROM "Transport" t WHERE id=$1`+lockClause(lock, "t"), id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, problem(404, "Transport tidak ditemukan.")
	}
	route, err := r.one(`SELECT to_jsonb(r)||jsonb_build_object('checkpoints',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.sequence) FROM "Checkpoint" c WHERE c."routeId"=r.id),'[]'::jsonb)) FROM "Route" r WHERE id=$1`, t["routeId"])
	if err != nil {
		return nil, err
	}
	ships, err := r.many(`SELECT to_jsonb(ts)||jsonb_build_object('master',to_jsonb(s),'customerName',COALESCE(NULLIF(c."companyName",''),c.name),'packages', (SELECT count(*) FROM "DetailShipment" d WHERE d."masterId"=s.id),'actualWeight',COALESCE((SELECT sum(d."actualWeightKg") FROM "DetailShipment" d WHERE d."masterId"=s.id),0),'volume',COALESCE((SELECT sum(COALESCE(d."lengthCm",0)*COALESCE(d."widthCm",0)*COALESCE(d."heightCm",0)/1000000.0) FROM "DetailShipment" d WHERE d."masterId"=s.id),0)) FROM "TransportShipment" ts JOIN "MasterShipment" s ON s.id=ts."shipmentId" JOIN "Customer" c ON c.id=s."customerId" WHERE ts."transportId"=$1 ORDER BY ts."shipmentId"`, id)
	if err != nil {
		return nil, err
	}
	records, err := r.many(`SELECT to_jsonb(cr)||jsonb_build_object('checkpointName',cp.name,'checkpointSequence',cp.sequence,'recordedBy',CASE WHEN u.id IS NULL THEN NULL ELSE jsonb_build_object('id',u.id,'name',u.name) END) FROM "CheckpointRecord" cr JOIN "Checkpoint" cp ON cp.id=cr."checkpointId" LEFT JOIN "User" u ON u.id=cr."recordedById" WHERE cr."transportId"=$1 ORDER BY cr."recordedAt" DESC,cr.id DESC`, id)
	if err != nil {
		return nil, err
	}
	v, err := r.one(`SELECT to_jsonb(v)||jsonb_build_object('ownerName',u.name) FROM "Vehicle" v LEFT JOIN "Partner" p ON p.id=v."ownerId" LEFT JOIN "User" u ON u.id=p."userId" WHERE v.id=$1`, t["vehicleId"])
	if err != nil {
		return nil, err
	}
	t["route"], t["shipments"], t["checkpointRecords"], t["vehicle"] = route, ships, records, v
	return t, nil
}
func transportShips(t map[string]any) []map[string]any {
	s, _ := t["shipments"].([]map[string]any)
	return s
}
func checkpoints(t map[string]any, active bool) []map[string]any {
	out := []map[string]any{}
	for _, cp := range objects(object(t["route"])["checkpoints"]) {
		if !active || cp["isActive"] == true {
			out = append(out, cp)
		}
	}
	return out
}
func transportGudangIDs(t map[string]any, warehouses []map[string]any) []int {
	out := []int{}
	seen := map[int]bool{}
	add := func(id int) {
		if id > 0 && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	route := object(t["route"])
	for _, w := range warehouses {
		if textValue(w["city"]) != "" && (strings.EqualFold(textValue(w["city"]), textValue(route["origin"])) || strings.EqualFold(textValue(w["city"]), textValue(route["destination"]))) {
			add(idValue(w["id"]))
		}
	}
	for _, s := range transportShips(t) {
		for _, id := range shipmentWarehouseIDs(object(s["master"]), warehouses) {
			add(id)
		}
	}
	return out
}
func (r *request) assertTransportScope(t map[string]any) error {
	unscoped, wid := warehouseScope(r.actor)
	if unscoped {
		return nil
	}
	ws, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return err
	}
	for _, id := range transportGudangIDs(t, ws) {
		if id == idValue(wid) {
			return nil
		}
	}
	return problem(403, "Transport ini berada di gudang lain - data terpisah antar gudang.")
}
func (r *request) assertTransportAccess(t map[string]any) error {
	if hasPermission(r.actor, "*") {
		return nil
	}
	if hasRole(r.actor, "driver") || hasRole(r.actor, "kenek") {
		if !crew(r.actor, t) {
			return problem(403, "Anda hanya boleh mengakses transport yang ditugaskan kepada Anda.")
		}
		return nil
	}
	return r.assertTransportScope(t)
}
func (r *request) transport(lock bool) (map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	t, err := r.loadTransport(id, lock)
	if err != nil {
		return nil, err
	}
	return t, r.assertTransportAccess(t)
}
func rawTransport(t map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range t {
		if k != "route" && k != "shipments" && k != "vehicle" && k != "checkpointRecords" {
			out[k] = v
		}
	}
	return out
}
func loadTotals(ships []map[string]any) (map[string]any, capacity.Load) {
	load := capacity.Load{}
	weight, price, priced := 0.0, 0.0, false
	for _, ts := range ships {
		s := object(ts["master"])
		actual := pricing.Round(number(ts["actualWeight"])*100) / 100
		volume := pricing.Round(number(ts["volume"])*1e6) / 1e6
		load.Weight += actual
		load.Volume += volume
		load.Koli += number(ts["packages"])
		if s["chargeableWeightKg"] != nil {
			weight += number(s["chargeableWeightKg"])
		} else {
			weight += actual
		}
		if s["priceAmount"] != nil {
			priced = true
			price += number(s["priceAmount"])
		}
	}
	load.Weight = pricing.Round(load.Weight*100) / 100
	load.Volume = pricing.Round(load.Volume*1e6) / 1e6
	var totalPrice any
	if priced {
		totalPrice = price
	}
	return map[string]any{"shipmentCount": len(ships), "totalWeightKg": pricing.Round(weight*100) / 100, "totalVolumeM3": load.Volume, "totalActualWeightKg": load.Weight, "totalKoli": load.Koli, "totalPrice": totalPrice}, load
}
func vehicleCapacity(v map[string]any, load capacity.Load, warning float64) capacity.Status {
	return capacity.Compute(capacity.Limits{Weight: number(v["maxWeightKg"]), Volume: number(v["maxVolumeM3"]), Koli: number(v["maxKoli"])}, load, warning)
}
func (r *request) employeeBrief(id any, active bool) (map[string]any, error) {
	return r.one(`SELECT jsonb_build_object('id',e.id,'name',e.name) FROM "Employee" e WHERE id=$1 AND (NOT $2 OR "isActive")`, id, active)
}
func (b *business) transportDetail(r *request) (any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	t, err := r.loadTransport(id, false)
	if err != nil {
		return nil, err
	}
	if hasPermission(r.actor, "transport.view") {
		if err = r.assertTransportScope(t); err != nil {
			return nil, err
		}
		if executorOnly(r.actor) && !crew(r.actor, t) {
			return nil, problem(403, "Transport ini tidak ditugaskan kepada Anda.")
		}
	} else if r.actor["partnerType"] != "VEHICLE_OWNER" || idValue(r.actor["partnerId"]) == 0 || idValue(r.actor["partnerId"]) != idValue(object(t["vehicle"])["ownerId"]) {
		return nil, problem(403, "Missing permission: transport.view")
	}
	out := rawTransport(t)
	for _, key := range []string{"driverId", "kenekId", "warehouseStaffId", "vehicleId", "statusBeforeArrival", "deliveryApprovedAt", "deliveryApprovedById"} {
		delete(out, key)
	}
	route := object(t["route"])
	out["routeName"], out["routeOrigin"], out["routeDestination"] = route["name"], route["origin"], route["destination"]
	out["origin"], out["destination"] = fallback(t["origin"], route["origin"]), fallback(t["destination"], route["destination"])
	out["checkpoints"] = checkpoints(t, false)
	if t["trackingMode"] == "ACTIVITY" {
		out["checkpoints"] = []any{}
	}
	v := object(t["vehicle"])
	vehicle := map[string]any{}
	for _, key := range []string{"id", "vehicleNumber", "name", "status", "maxWeightKg", "maxVolumeM3", "maxKoli"} {
		vehicle[key] = v[key]
	}
	out["vehicle"] = vehicle
	for _, key := range []string{"driver", "kenek", "warehouseStaff"} {
		out[key], err = r.employeeBrief(t[key+"Id"], false)
		if err != nil {
			return nil, err
		}
	}
	records, _ := t["checkpointRecords"].([]map[string]any)
	projected := []map[string]any{}
	for _, record := range records {
		row := map[string]any{}
		for _, key := range []string{"id", "checkpointId", "checkpointName", "checkpointSequence", "latitude", "longitude", "withinRadius", "distanceMeters", "photoUrl", "recordedBy", "recordedAt"} {
			row[key] = record[key]
		}
		projected = append(projected, row)
	}
	out["checkpointRecords"] = projected
	ships := []map[string]any{}
	for _, ts := range transportShips(t) {
		s := object(ts["master"])
		row := map[string]any{}
		for _, key := range []string{"id", "masterCode", "resi", "status", "origin", "destination", "penerimaName", "priceAmount"} {
			row[key] = s[key]
		}
		row["customerName"], row["packages"], row["details"] = ts["customerName"], ts["packages"], []any{}
		row["weightKg"] = pricing.Round(number(fallback(s["chargeableWeightKg"], ts["actualWeight"]))*100) / 100
		row["volumeM3"] = pricing.Round(number(ts["volume"])*1e6) / 1e6
		for _, pair := range [][2]string{{"arrivedWarehouseName", "arrivedWarehouseId"}, {"destinationWarehouseName", "destinationWarehouseId"}} {
			w, e := r.one(`SELECT jsonb_build_object('name',name) FROM "Warehouse" WHERE id=$1`, s[pair[1]])
			if e != nil {
				return nil, e
			}
			row[pair[0]] = object(w)["name"]
		}
		ships = append(ships, row)
	}
	out["shipments"] = ships
	totals, load := loadTotals(transportShips(t))
	for k, v := range totals {
		out[k] = v
	}
	out["capacity"] = vehicleCapacity(v, load, threshold(r))
	return out, nil
}
func fallback(a, b any) any {
	if a != nil {
		return a
	}
	return b
}
func (b *business) transports(r *request) (any, error) {
	// Fetch list relations together so database round trips do not grow with the fleet.
	rows, err := r.many(`SELECT to_jsonb(t)||jsonb_build_object(
 'route',to_jsonb(rt)||jsonb_build_object('checkpoints',COALESCE((SELECT jsonb_agg(to_jsonb(cp) ORDER BY cp.sequence) FROM "Checkpoint" cp WHERE cp."routeId"=rt.id),'[]'::jsonb)),
 'vehicle',jsonb_build_object('vehicleNumber',v."vehicleNumber",'name',v.name,'ownerId',v."ownerId",'ownerName',owner.name,'maxWeightKg',v."maxWeightKg",'maxVolumeM3',v."maxVolumeM3",'maxKoli',v."maxKoli"),
 'driverName',driver.name,'kenekName',kenek.name,
 'settlement',CASE WHEN settlement.id IS NULL THEN NULL ELSE jsonb_build_object('settlementCode',settlement."settlementCode",'status',settlement.status,'transportValue',settlement."transportValue",'companyPercent',settlement."companyPercent",'ownerPercent',settlement."ownerPercent",'companyAmount',settlement."companyAmount",'ownerAmount',settlement."ownerAmount",'finalizedAt',settlement."finalizedAt") END,
 'checkpointRecordsCount',(SELECT count(*) FROM "CheckpointRecord" cr JOIN "Checkpoint" cp ON cp.id=cr."checkpointId" WHERE cr."transportId"=t.id),
 'shipments',COALESCE((SELECT jsonb_agg(jsonb_build_object('master',to_jsonb(s),'packages',a.packages,'actualWeight',a.weight,'volume',a.volume) ORDER BY ts."shipmentId")
 FROM "TransportShipment" ts JOIN "MasterShipment" s ON s.id=ts."shipmentId" JOIN "Customer" c ON c.id=s."customerId"
 CROSS JOIN LATERAL (SELECT count(*) packages,COALESCE(sum(ds."actualWeightKg"),0) weight,COALESCE(sum(COALESCE(ds."lengthCm",0)*COALESCE(ds."widthCm",0)*COALESCE(ds."heightCm",0)/1000000.0),0) volume FROM "DetailShipment" ds WHERE ds."masterId"=s.id) a
 WHERE ts."transportId"=t.id),'[]'::jsonb))
 FROM "Transport" t LEFT JOIN "Route" rt ON rt.id=t."routeId" JOIN "Vehicle" v ON v.id=t."vehicleId"
 LEFT JOIN "Partner" p ON p.id=v."ownerId" LEFT JOIN "User" owner ON owner.id=p."userId"
 LEFT JOIN "Employee" driver ON driver.id=t."driverId" AND driver."isActive"
 LEFT JOIN "Employee" kenek ON kenek.id=t."kenekId" AND kenek."isActive"
 LEFT JOIN "TransportSettlement" settlement ON settlement."transportId"=t.id
 WHERE ($1='' OR t.status=$1 OR $2) AND (NOT $2 OR t.status IN ('ARRIVED','COMPLETED','CANCELLED'))
 AND ($3='' OR strpos(lower(t."transportCode"),lower($3))>0 OR strpos(lower(COALESCE(rt.name,'')),lower($3))>0 OR strpos(lower(v."vehicleNumber"),lower($3))>0)
 AND (NOT $4 OR ($5>0 AND (t."driverId"=$5 OR t."kenekId"=$5)))
 ORDER BY t."createdAt" DESC,t.id DESC`, r.c.Query("status"), r.c.Query("history") == "true", strings.TrimSpace(r.c.Query("search")), executorOnly(r.actor) || r.c.Query("mine") == "true", idValue(r.actor["employeeId"]))
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	ws, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return nil, err
	}
	unscoped, wid := warehouseScope(r.actor)
	warning := threshold(r)
	for _, t := range rows {
		t["shipments"] = objects(t["shipments"])
		gudang := transportGudangIDs(t, ws)
		visible := unscoped
		for _, id := range gudang {
			visible = visible || id == idValue(wid)
		}
		if !visible {
			continue
		}
		v, route := object(t["vehicle"]), object(t["route"])
		item := map[string]any{}
		for _, key := range []string{"id", "transportCode", "status", "trackingMode", "transportMode", "routeId", "vehicleId", "driverId", "kenekId", "warehouseStaffId", "plannedDepartureAt", "plannedArrivalAt", "departedAt", "arrivedAt", "createdAt", "currentLatitude", "currentLongitude", "lastLocationAt"} {
			item[key] = t[key]
		}
		item["routeName"], item["routeCheckpoints"] = route["name"], checkpoints(t, false)
		if t["trackingMode"] == "ACTIVITY" {
			item["routeCheckpoints"] = []any{}
		}
		item["origin"], item["destination"] = fallback(t["origin"], route["origin"]), fallback(t["destination"], route["destination"])
		item["vehicleNumber"], item["vehicleName"], item["vehicleOwnerId"], item["vehicleOwnerName"] = v["vehicleNumber"], v["name"], v["ownerId"], v["ownerName"]
		for _, key := range []string{"driverName", "kenekName", "settlement", "checkpointRecordsCount"} {
			item[key] = t[key]
		}
		ships := []map[string]any{}
		for _, ts := range transportShips(t) {
			s := object(ts["master"])
			ships = append(ships, map[string]any{"id": s["id"], "masterCode": s["masterCode"], "status": s["status"], "packages": ts["packages"]})
		}
		item["shipments"], item["gudangIds"] = ships, gudang
		totals, load := loadTotals(transportShips(t))
		for k, v := range totals {
			item[k] = v
		}
		item["capacity"] = vehicleCapacity(v, load, warning)
		out = append(out, item)
	}
	return out, nil
}

func (r *request) transportPlan(existing map[string]any) (map[string]any, error) {
	raw := strings.TrimSpace(string(r.c.Body()))
	if !strings.HasPrefix(raw, "{") || !json.Valid([]byte(raw)) {
		return nil, problem(422, "Payload transport tidak valid.")
	}
	data := map[string]any{}
	for k, v := range existing {
		data[k] = v
	}
	for _, key := range []string{"routeId", "vehicleId", "driverId", "kenekId"} {
		if v, ok := r.body[key]; ok {
			if v == nil || v == "" {
				data[key] = nil
			} else if idValue(v) == 0 {
				return nil, fieldProblem("ID tidak valid.", key, "Pilih ID yang valid.")
			} else {
				data[key] = idValue(v)
			}
		}
	}
	for _, key := range []string{"origin", "destination"} {
		if v, ok := r.body[key]; ok {
			data[key] = nullable(v)
		}
	}
	for _, key := range []string{"plannedDepartureAt", "plannedArrivalAt"} {
		if v, ok := r.body[key]; ok {
			data[key] = dateValue(v, false)
		} else if existing[key] != nil {
			data[key] = dateValue(existing[key], false)
		}
	}
	mode := fallback(existing["trackingMode"], "CHECKPOINT")
	if v, ok := r.body["trackingMode"]; ok {
		mode = v
	}
	if mode != "ACTIVITY" && mode != "CHECKPOINT" {
		return nil, problem(422, "Mode tracking tidak valid.")
	}
	data["trackingMode"] = mode
	tm := fallback(existing["transportMode"], "DIRECT")
	if v, ok := r.body["transportMode"]; ok {
		tm = v
	}
	data["transportMode"] = "DIRECT"
	if tm == "MULTI_DROP" {
		data["transportMode"] = tm
	}
	for _, f := range []struct{ key, label string }{{"routeId", "Rute"}, {"vehicleId", "Kendaraan"}} {
		if idValue(data[f.key]) == 0 {
			return nil, fieldProblem(f.label+" wajib dipilih.", f.key, f.label+" wajib dipilih.")
		}
	}
	route, err := r.one(`SELECT to_jsonb(r)||jsonb_build_object('count',(SELECT count(*) FROM "Checkpoint" c WHERE c."routeId"=r.id)) FROM "Route" r WHERE id=$1`, data["routeId"])
	if err != nil {
		return nil, err
	}
	if route == nil {
		return nil, problem(422, "Rute tidak ditemukan.")
	}
	if mode == "CHECKPOINT" && number(route["count"]) < 3 {
		return nil, problem(422, "Rute harus memiliki minimal 3 checkpoint.")
	}
	v, err := r.one(`SELECT to_jsonb(v) FROM "Vehicle" v WHERE id=$1`, data["vehicleId"])
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, problem(422, "Kendaraan tidak ditemukan.")
	}
	if len(existing) == 0 && v["status"] != "ACTIVE" {
		return nil, problem(422, fmt.Sprintf("Kendaraan %s tidak aktif (%s).", v["vehicleNumber"], v["status"]))
	}
	if len(existing) == 0 {
		data["origin"], data["destination"] = fallback(data["origin"], route["origin"]), fallback(data["destination"], route["destination"])
	}
	for _, f := range []struct{ key, label string }{{"plannedDepartureAt", "Rencana Berangkat"}, {"plannedArrivalAt", "Rencana Tiba"}} {
		if data[f.key] == nil {
			return nil, fieldProblem(f.label+" wajib diisi.", f.key, f.label+" wajib diisi.")
		}
	}
	if data["plannedArrivalAt"].(time.Time).Before(data["plannedDepartureAt"].(time.Time)) {
		return nil, fieldProblem("Rencana tiba tidak boleh lebih awal dari rencana berangkat.", "plannedArrivalAt", "Rencana tiba harus setelah rencana berangkat.")
	}
	if idValue(data["driverId"]) > 0 && idValue(data["driverId"]) == idValue(data["kenekId"]) {
		return nil, fieldProblem("Driver dan kenek tidak boleh orang yang sama.", "kenekId", "Pilih kenek yang berbeda dari driver.")
	}
	for _, key := range []string{"driverId", "kenekId"} {
		if idValue(data[key]) > 0 {
			emp, e := r.employeeBrief(data[key], false)
			if e != nil {
				return nil, e
			}
			if emp == nil {
				return nil, problem(422, "Kru tidak ditemukan.")
			}
		}
	}
	if value, ok := r.body["warehouseStaffId"]; ok {
		data["warehouseStaffId"] = nil
		if value != nil && value != "" {
			if _, ok := value.(float64); !ok || idValue(value) == 0 {
				return nil, problem(422, "Petugas Gudang tidak valid.")
			}
			staff, e := r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1 AND "isActive"`, idValue(value))
			if e != nil {
				return nil, e
			}
			if staff == nil {
				return nil, problem(422, "Petugas Gudang tidak ditemukan atau tidak aktif.")
			}
			if !hasPermission(r.actor, "*") && (idValue(r.actor["warehouseId"]) == 0 || idValue(staff["warehouseId"]) != idValue(r.actor["warehouseId"])) {
				return nil, problem(403, "Petugas Gudang harus berasal dari gudang Anda.")
			}
			data["warehouseStaffId"] = idValue(value)
		}
	}
	if err = r.assertTransportScope(map[string]any{"route": route, "shipments": transportShips(existing)}); err != nil {
		return nil, err
	}
	if err = r.availableTransportResources(data, idValue(existing["id"])); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, key := range []string{"routeId", "vehicleId", "driverId", "kenekId", "warehouseStaffId", "origin", "destination", "plannedDepartureAt", "plannedArrivalAt", "trackingMode", "transportMode"} {
		out[key] = data[key]
	}
	return out, nil
}
func (r *request) availableTransportResources(data map[string]any, exclude int) error {
	conflict, err := r.one(`SELECT to_jsonb(t) FROM "Transport" t WHERE id<>$1 AND status IN ('PLANNED','DEPARTED') AND ("vehicleId"=$2 OR "driverId"=ANY($3::int[]) OR "kenekId"=ANY($3::int[])) ORDER BY id LIMIT 1`, exclude, data["vehicleId"], []int{idValue(data["driverId"]), idValue(data["kenekId"])})
	if err != nil {
		return err
	}
	if conflict == nil {
		return nil
	}
	if idValue(conflict["vehicleId"]) == idValue(data["vehicleId"]) {
		return problem(422, fmt.Sprintf("Kendaraan sudah ditugaskan di transport lain yang aktif (%s — %s). Satu kendaraan hanya boleh di satu transport aktif pada satu waktu.", conflict["transportCode"], conflict["status"]))
	}
	role := "Kenek"
	if driver := idValue(data["driverId"]); driver > 0 && (driver == idValue(conflict["driverId"]) || driver == idValue(conflict["kenekId"])) {
		role = "Driver"
	}
	return problem(422, fmt.Sprintf("%s masih ditugaskan pada transport %s yang belum selesai.", role, conflict["transportCode"]))
}
func (b *business) createTransport(r *request) (any, error) {
	// Serialize planning checks so different vehicles cannot double-load a shipment.
	if err := r.exec(`SELECT pg_advisory_xact_lock(731030)`); err != nil {
		return nil, err
	}
	data, err := r.transportPlan(map[string]any{})
	if err != nil {
		return nil, err
	}
	shipmentIDs, _ := ids(r.body["shipmentIds"])
	ships, err := r.many(`SELECT to_jsonb(s) FROM "MasterShipment" s WHERE id=ANY($1::int[]) ORDER BY id FOR UPDATE`, shipmentIDs)
	if err != nil {
		return nil, err
	}
	for _, s := range ships {
		if err = r.assertShipmentScope(s); err != nil {
			return nil, err
		}
		direct := s["fulfillmentMode"] == "DIRECT"
		if direct && s["status"] != "CREATED" && s["status"] != "READY_FOR_PICKUP" || !direct && s["status"] != "RECEIVED_AT_GUDANG" {
			if direct {
				return nil, problem(422, fmt.Sprintf("Shipment DIRECT %s harus berstatus CREATED atau READY_FOR_PICKUP (saat ini: %s).", s["masterCode"], s["status"]))
			}
			return nil, problem(422, fmt.Sprintf("Shipment %s harus berstatus RECEIVED_AT_GUDANG (saat ini: %s).", s["masterCode"], s["status"]))
		}
		conflict, e := r.one(`SELECT jsonb_build_object('code',t."transportCode") FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId" WHERE ts."shipmentId"=$1 AND t.status IN ('PLANNED','DEPARTED') LIMIT 1`, s["id"])
		if e != nil {
			return nil, e
		}
		if conflict != nil {
			return nil, problem(422, "Shipment sudah dimuat di transport lain yang aktif ("+textValue(conflict["code"])+").")
		}
	}
	code, err := r.nextCode("Transport", "transportCode", "TRP-2026-", 731031)
	if err != nil {
		return nil, err
	}
	data["transportCode"], data["status"] = code, "PLANNED"
	t, err := r.insertRow("Transport", data)
	if err != nil {
		return nil, err
	}
	for _, s := range ships {
		if err = r.exec(`INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES ($1,$2)`, t["id"], s["id"]); err != nil {
			return nil, err
		}
		if _, err = r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": "IN_TRANSPORT"}); err != nil {
			return nil, err
		}
		if s["fulfillmentMode"] == "DIRECT" && idValue(t["driverId"]) > 0 {
			exists, e := r.one(`SELECT jsonb_build_object('id',id) FROM "Pickup" WHERE "masterId"=$1 LIMIT 1`, s["id"])
			if e != nil {
				return nil, e
			}
			if exists == nil {
				pickCode, e := r.nextPickupCode()
				if e != nil {
					return nil, e
				}
				if _, e = r.insertRow("Pickup", map[string]any{"pickupCode": pickCode, "masterId": s["id"], "kurirId": t["driverId"], "status": "ASSIGNED", "notes": "Auto-assigned dari transport " + code + " (DIRECT - driver pickup di checkpoint 1)"}); e != nil {
					return nil, e
				}
				if e = r.tracking(s, "PICKUP_ASSIGNED", "Driver auto-assigned untuk pickup DIRECT (transport "+code+") - scan paket di checkpoint 1."); e != nil {
					return nil, e
				}
			}
		}
		if err = r.tracking(s, "LOADED_TO_TRANSPORT", "Dimuat ke transport "+code); err != nil {
			return nil, err
		}
	}
	b.audit(r, "created", "transport", t["id"], code, nil, data)
	return t, nil
}
func (b *business) updateTransport(r *request) (any, error) {
	if err := r.exec(`SELECT pg_advisory_xact_lock(731030)`); err != nil {
		return nil, err
	}
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if err = r.assertTransportScope(t); err != nil {
		return nil, err
	}
	if t["status"] == "DEPARTED" || t["status"] == "ARRIVED" || t["status"] == "COMPLETED" {
		return nil, problem(422, fmt.Sprintf("Transport dengan status %s tidak bisa diubah.", t["status"]))
	}
	if mode, ok := r.body["trackingMode"]; ok && mode != t["trackingMode"] {
		var evidence bool
		if err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "TransportActivity" WHERE "transportId"=$1) OR EXISTS(SELECT 1 FROM "CheckpointRecord" WHERE "transportId"=$1)`, t["id"]).Scan(&evidence); err != nil {
			return nil, err
		}
		if evidence {
			return nil, problem(422, "Mode tracking tidak bisa diubah setelah bukti tracking dicatat.")
		}
	}
	data, err := r.transportPlan(t)
	if err != nil {
		return nil, err
	}
	updated, err := r.updateRow("Transport", idValue(t["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "transport", t["id"], textValue(t["transportCode"]), diff(rawTransport(t), updated), nil)
	return updated, nil
}
func (b *business) deleteTransport(r *request) (any, error) {
	t, err := r.transport(true)
	if err != nil {
		return nil, err
	}
	if err = r.assertTransportScope(t); err != nil {
		return nil, err
	}
	if t["status"] != "PLANNED" {
		return nil, problem(422, "Hanya transport PLANNED yang bisa dihapus.")
	}
	if err = r.exec(`UPDATE "MasterShipment" s SET status='RECEIVED_AT_GUDANG',"updatedAt"=NOW() FROM "TransportShipment" ts WHERE ts."shipmentId"=s.id AND ts."transportId"=$1 AND s.status='IN_TRANSPORT'`, t["id"]); err != nil {
		return nil, err
	}
	if err = r.exec(`UPDATE "ReturnTask" SET "returnTransportId"=NULL,status='APPROVED',"updatedAt"=NOW() WHERE "returnTransportId"=$1`, t["id"]); err != nil {
		return nil, err
	}
	if err = r.exec(`DELETE FROM "Transport" WHERE id=$1`, t["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "transport", t["id"], textValue(t["transportCode"]), t, nil)
	return map[string]any{"deleted": true}, nil
}
