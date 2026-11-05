package httpapi

import (
	"strconv"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/capacity"
	"novalogistik/backend/internal/pricing"
)

func (b *business) registerFleetCapacity(app *fiber.App) {
	app.Get("/api/v1/fleet-capacity", b.handle("vehicle.view", true, false, b.fleetCapacity))
	app.Post("/api/v1/fleet-capacity/bulk-configure-koli", b.handle("vehicle.update", true, true, b.bulkConfigureKoli))
}

func (b *business) fleetCapacity(r *request) (any, error) {
	vehicles, err := r.many(`SELECT to_jsonb(v)||jsonb_build_object('ownerName',u.name,
	 'activeTransportCount',(SELECT count(*) FROM "Transport" t WHERE t."vehicleId"=v.id AND t.status IN ('PLANNED','DEPARTED')),
	 'masters',COALESCE((SELECT jsonb_agg(ts."shipmentId" ORDER BY t.id,ts."shipmentId") FROM "Transport" t JOIN "TransportShipment" ts ON ts."transportId"=t.id WHERE t."vehicleId"=v.id AND t.status IN ('PLANNED','DEPARTED')),'[]'::jsonb))
	 FROM "Vehicle" v LEFT JOIN "Partner" p ON p.id=v."ownerId" LEFT JOIN "User" u ON u.id=p."userId" ORDER BY v.status,v."vehicleNumber",v.id`)
	if err != nil {
		return nil, err
	}
	// Match detailAggregates: physical volume currently comes from dimensions only.
	details, err := r.many(`SELECT jsonb_build_object('masterId',d."masterId",'weight',COALESCE(sum(d."actualWeightKg"),0),'volume',COALESCE(sum(COALESCE(d."lengthCm",0)*COALESCE(d."widthCm",0)*COALESCE(d."heightCm",0)/1000000.0),0),'koli',count(*)) FROM "DetailShipment" d WHERE EXISTS (SELECT 1 FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId" WHERE ts."shipmentId"=d."masterId" AND t.status IN ('PLANNED','DEPARTED')) GROUP BY d."masterId"`)
	if err != nil {
		return nil, err
	}
	loads := map[int]capacity.Load{}
	for _, d := range details {
		loads[idValue(d["masterId"])] = capacity.Load{Weight: pricing.Round(number(d["weight"])*100) / 100, Volume: pricing.Round(number(d["volume"])*1_000_000) / 1_000_000, Koli: number(d["koli"])}
	}
	warning := threshold(r)
	rows := []map[string]any{}
	summary := map[string]int{"total": len(vehicles), "active": 0, "idle": 0, "overCapacity": 0, "warning": 0, "partiallyConfigured": 0, "ok": 0, "unconfiguredVehicles": 0}
	for _, v := range vehicles {
		row := map[string]any{"activeLoad": nil, "activeCapacity": nil}
		for _, key := range []string{"id", "vehicleNumber", "name", "status", "maxWeightKg", "maxVolumeM3", "maxKoli", "lengthM", "widthM", "heightM", "ownerId", "ownerName", "activeTransportCount"} {
			row[key] = v[key]
		}
		if number(v["activeTransportCount"]) > 0 {
			load := capacity.Load{}
			for _, master := range v["masters"].([]any) {
				part := loads[idValue(master)]
				load.Weight += part.Weight
				load.Volume += part.Volume
				load.Koli += part.Koli
			}
			load.Weight = pricing.Round(load.Weight*100) / 100
			load.Volume = pricing.Round(load.Volume*1_000_000) / 1_000_000
			status := capacity.Compute(capacity.Limits{Weight: number(v["maxWeightKg"]), Volume: number(v["maxVolumeM3"]), Koli: number(v["maxKoli"])}, load, warning)
			row["activeLoad"], row["activeCapacity"] = load, status
			summary["active"]++
			switch status.Overall {
			case "OVERLIMIT":
				summary["overCapacity"]++
			case "WARNING":
				summary["warning"]++
			case "PARTIALLY_CONFIGURED":
				summary["partiallyConfigured"]++
			case "OK":
				summary["ok"]++
			}
		} else {
			summary["idle"]++
		}
		if v["maxKoli"] == nil {
			summary["unconfiguredVehicles"]++
		}
		rows = append(rows, row)
	}
	return map[string]any{"vehicles": rows, "summary": summary}, nil
}

func (b *business) bulkConfigureKoli(r *request) (any, error) {
	if !truthy(r.actor["isOwner"]) {
		return nil, problem(403, "Hanya Owner yang dapat menjalankan bulk configure.")
	}
	vehicles, err := r.many(`SELECT to_jsonb(v) FROM "Vehicle" v WHERE "maxKoli" IS NULL ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	overrides, _ := r.body["overrides"].(map[string]any)
	updated := []map[string]any{}
	for _, v := range vehicles {
		koli := 150.0
		switch weight := number(v["maxWeightKg"]); {
		case weight <= 1500:
			koli = 40
		case weight <= 3000:
			koli = 60
		case weight <= 6000:
			koli = 100
		}
		raw := overrides[strconv.Itoa(idValue(v["id"]))]
		if n := number(raw); n > 0 {
			koli = pricing.Round(n)
			if koli > 2147483647 {
				return nil, fieldProblem("Kapasitas koli terlalu besar.", "overrides", "Kapasitas koli harus muat dalam bilangan bulat 32-bit.")
			}
		}
		if _, err = r.updateRow("Vehicle", idValue(v["id"]), map[string]any{"maxKoli": koli}); err != nil {
			return nil, err
		}
		updated = append(updated, map[string]any{"id": v["id"], "vehicleNumber": v["vehicleNumber"], "maxKoli": koli, "suggested": raw == nil})
		b.audit(r, "updated", "vehicle", v["id"], textValue(v["vehicleNumber"]), map[string]any{"maxKoli": nil}, map[string]any{"maxKoli": koli, "capacityConfigChanged": true, "capacityChanges": []any{map[string]any{"field": "maxKoli", "before": nil, "after": koli}}})
	}
	return map[string]any{"updated": updated, "count": len(updated)}, nil
}
