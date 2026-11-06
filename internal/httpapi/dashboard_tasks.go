package httpapi

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

func (b *business) registerAggregates(app *fiber.App) {
	app.Get("/api/v1/dashboard", b.handle("", true, false, b.dashboard))
	app.Get("/api/v1/dashboard/driver", b.handle("transport.view", true, false, b.driverDashboard))
	app.Get("/api/v1/dashboard/kurir", b.handle("pickup.view", true, false, b.courierDashboard))
	app.Get("/api/v1/audit-logs", b.handle("audit_log.view", true, false, b.auditLogs))
}

func dashboardPeriod(r *request) (map[string]any, time.Time, time.Time, error) {
	from := strings.TrimSpace(r.c.Query("from"))
	if from == "" {
		from = time.Now().UTC().Format("2006-01-02")
	}
	to := strings.TrimSpace(r.c.Query("to"))
	if to == "" || to < from {
		to = from
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, time.Time{}, time.Time{}, problem(422, "Periode tidak valid.")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return nil, time.Time{}, time.Time{}, problem(422, "Periode tidak valid.")
	}
	// Legacy rounds the interval through 23:59:59.999 before adding one day.
	return map[string]any{"from": from, "to": to, "days": int(end.Sub(start).Hours()/24) + 2}, start, end.Add(24*time.Hour - time.Millisecond), nil
}

func inDashboardPeriod(value any, from, to time.Time) bool {
	t, ok := dateValue(value, false).(time.Time)
	return ok && !t.Before(from) && !t.After(to)
}

func (b *business) courierDashboard(r *request) (any, error) {
	period, from, to, err := dashboardPeriod(r)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"pickupsAssigned": 0, "pickupsPickedUp": 0, "pickupsCompleted": 0, "deliveriesAssigned": 0, "deliveriesDelivered": 0}
	out := map[string]any{"period": period, "counts": counts, "pickups": []map[string]any{}, "deliveries": []map[string]any{}}
	if r.actor["employeeId"] == nil {
		out["note"] = "Akun tidak terhubung ke data karyawan - hubungi admin."
		return out, nil
	}
	for _, pickup := range []bool{true, false} {
		table, fk, key := `"Delivery"`, `"deliveryId"`, "deliveries"
		if pickup {
			table, fk, key = `"Pickup"`, `"pickupId"`, "pickups"
		}
		rows, err := r.many(`SELECT to_jsonb(t)||jsonb_build_object('master',to_jsonb(s),'customerName',COALESCE(NULLIF(c."companyName",''),c.name),'customerPhone',c.phone,'detailsCount',(SELECT count(*) FROM "DetailShipment" d WHERE d."masterId"=s.id),'scannedCount',(SELECT count(DISTINCT h."detailId") FROM "HandoverScan" h WHERE h.`+fk+`=t.id AND h.result<>'unexpected')) FROM `+table+` t JOIN "MasterShipment" s ON s.id=t."masterId" JOIN "Customer" c ON c.id=s."customerId" WHERE t."kurirId"=$1 ORDER BY t."createdAt" DESC,t.id DESC`, r.actor["employeeId"])
		if err != nil {
			return nil, err
		}
		active, history := []map[string]any{}, []map[string]any{}
		for _, row := range rows {
			s := object(row["master"])
			item := map[string]any{}
			for _, k := range []string{"id", "status", "customerName", "customerPhone", "detailsCount", "scannedCount", "createdAt", "completedAt", "latitude", "longitude"} {
				item[k] = row[k]
			}
			item["masterCode"], item["masterStatus"], item["destination"] = s["masterCode"], s["status"], s["destination"]
			isActive := row["status"] == "ASSIGNED"
			if pickup {
				item["pickupCode"], item["origin"] = row["pickupCode"], s["origin"]
				item["pickupAddress"], item["pickupContact"], item["pickupSenderName"] = s["pengirimAddress"], s["pengirimPhone"], s["pengirimName"]
				isActive = slices.Contains([]string{"ASSIGNED", "PICKED_UP", "IN_PROGRESS"}, textValue(row["status"]))
				if row["status"] == "ASSIGNED" {
					counts["pickupsAssigned"]++
				}
				if row["status"] == "PICKED_UP" {
					counts["pickupsPickedUp"]++
				}
				if row["status"] == "COMPLETED" && inDashboardPeriod(row["completedAt"], from, to) {
					counts["pickupsCompleted"]++
				}
			} else {
				item["deliveryCode"], item["address"], item["penerimaName"], item["priceAmount"] = row["deliveryCode"], s["penerimaAddress"], s["penerimaName"], s["priceAmount"]
				if isActive {
					counts["deliveriesAssigned"]++
				}
				if row["status"] == "COMPLETED" && inDashboardPeriod(row["completedAt"], from, to) {
					counts["deliveriesDelivered"]++
				}
			}
			if isActive {
				active = append(active, item)
			} else if inDashboardPeriod(row["createdAt"], from, to) || inDashboardPeriod(row["completedAt"], from, to) {
				history = append(history, item)
			}
		}
		items := append(active, history...)
		out[key] = items[:min(30, len(items))]
	}
	return out, nil
}

// One query keeps dashboard aggregation independent of the number of assignments.
const dashboardTransportSelect = `SELECT to_jsonb(t)||jsonb_build_object('route',to_jsonb(rt)||jsonb_build_object('checkpoints',COALESCE((SELECT jsonb_agg(to_jsonb(cp) ORDER BY cp.sequence) FROM "Checkpoint" cp WHERE cp."routeId"=rt.id),'[]'::jsonb)),'vehicle',to_jsonb(v),'driverName',d.name,'kenekName',k.name,
 'checkpointRecords',COALESCE((SELECT jsonb_agg(to_jsonb(cr)||jsonb_build_object('checkpointName',cp.name,'recordedByName',u.name) ORDER BY cr."recordedAt" DESC,cr.id DESC) FROM "CheckpointRecord" cr JOIN "Checkpoint" cp ON cp.id=cr."checkpointId" LEFT JOIN "User" u ON u.id=cr."recordedById" WHERE cr."transportId"=t.id),'[]'::jsonb),
 'shipments',COALESCE((SELECT jsonb_agg(jsonb_build_object('master',to_jsonb(s),'packages',a.packages,'actualWeight',a.weight,'volume',a.volume)) FROM "TransportShipment" ts JOIN "MasterShipment" s ON s.id=ts."shipmentId" CROSS JOIN LATERAL (SELECT count(*) packages,COALESCE(sum(ds."actualWeightKg"),0) weight,COALESCE(sum(COALESCE(ds."lengthCm",0)*COALESCE(ds."widthCm",0)*COALESCE(ds."heightCm",0)/1000000.0),0) volume FROM "DetailShipment" ds WHERE ds."masterId"=s.id) a WHERE ts."transportId"=t.id),'[]'::jsonb)) FROM "Transport" t JOIN "Vehicle" v ON v.id=t."vehicleId" LEFT JOIN "Route" rt ON rt.id=t."routeId" LEFT JOIN "Employee" d ON d.id=t."driverId" LEFT JOIN "Employee" k ON k.id=t."kenekId" `

func driverDashboardRow(t map[string]any) map[string]any {
	ships := objects(t["shipments"])
	totals, _ := loadTotals(ships)
	v, route := object(t["vehicle"]), object(t["route"])
	checked := map[int]bool{}
	records := objects(t["checkpointRecords"])
	for _, cr := range records {
		checked[idValue(cr["checkpointId"])] = true
	}
	var next any
	cps := objects(route["checkpoints"])
	if t["trackingMode"] != "ACTIVITY" {
		for _, cp := range cps {
			if !checked[idValue(cp["id"])] {
				m := map[string]any{}
				for _, k := range []string{"id", "name", "sequence", "latitude", "longitude", "radiusMeters"} {
					m[k] = cp[k]
				}
				next = m
				break
			}
		}
	}
	row := map[string]any{"routeName": route["name"], "origin": fallback(t["origin"], route["origin"]), "destination": fallback(t["destination"], route["destination"]), "vehicleNumber": v["vehicleNumber"], "vehicleName": v["name"], "maxWeightKg": v["maxWeightKg"], "maxVolumeM3": v["maxVolumeM3"], "shipmentCount": len(ships), "packageCount": totals["totalKoli"], "totalWeightKg": totals["totalWeightKg"], "totalVolumeM3": totals["totalVolumeM3"], "totalPrice": totals["totalPrice"], "checkpointsTotal": len(cps), "checkpointsCheckedIn": len(checked), "nextCheckpoint": next, "lastCheckpointName": nil, "lastCheckinAt": nil, "lastCheckinBy": nil}
	if t["trackingMode"] == "ACTIVITY" {
		row["checkpointsTotal"] = 0
	}
	if len(records) > 0 {
		row["lastCheckpointName"], row["lastCheckinAt"], row["lastCheckinBy"] = records[0]["checkpointName"], records[0]["recordedAt"], records[0]["recordedByName"]
	}
	for _, k := range []string{"id", "transportCode", "trackingMode", "status", "plannedDepartureAt", "plannedArrivalAt", "departedAt", "arrivedAt", "driverName", "kenekName", "currentLatitude", "currentLongitude", "lastLocationAt"} {
		row[k] = t[k]
	}
	return row
}

func (b *business) driverDashboard(r *request) (any, error) {
	period, from, to, err := dashboardPeriod(r)
	if err != nil {
		return nil, err
	}
	delete(period, "days")
	out := map[string]any{"period": period, "current": nil, "upcoming": []map[string]any{}, "future": []map[string]any{}, "completedCount": 0, "history": []map[string]any{}}
	if r.actor["employeeId"] == nil {
		out["vehicle"], out["nextCheckpoint"], out["note"] = nil, nil, "Akun tidak terhubung ke data karyawan - hubungi admin."
		return out, nil
	}
	rows, err := r.many(dashboardTransportSelect+`WHERE t."driverId"=$1 OR t."kenekId"=$1 ORDER BY t."createdAt" DESC,t.id DESC`, r.actor["employeeId"])
	if err != nil {
		return nil, err
	}
	planned, history := []map[string]any{}, []map[string]any{}
	completed := 0
	for _, t := range rows {
		row := driverDashboardRow(t)
		if t["status"] == "DEPARTED" && out["current"] == nil {
			out["current"] = row
		}
		if t["status"] == "PLANNED" {
			planned = append(planned, row)
		}
		if (t["status"] == "ARRIVED" || t["status"] == "COMPLETED") && inDashboardPeriod(t["arrivedAt"], from, to) {
			history = append(history, row)
			if t["status"] == "COMPLETED" {
				completed++
			}
		}
	}
	sort.SliceStable(planned, func(i, j int) bool {
		return textValue(fallback(planned[i]["plannedDepartureAt"], "9999")) < textValue(fallback(planned[j]["plannedDepartureAt"], "9999"))
	})
	out["upcoming"], out["future"] = planned[:min(1, len(planned))], planned[min(1, len(planned)):min(6, len(planned))]
	out["history"], out["completedCount"], out["totalTransportsAllTime"] = history[:min(15, len(history))], completed, len(rows)
	return out, nil
}
