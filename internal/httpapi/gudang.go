package httpapi

import (
	"math"
	"sort"
	"strings"

	"novalogistik/backend/internal/pricing"
)

// Physical warehouse membership gives explicit receipt/branch IDs precedence over city.
func warehouseHolds(s, w map[string]any) bool {
	if s["arrivedWarehouseId"] != nil {
		return s["arrivedWarehouseId"] == w["id"]
	}
	branch, city := s["destinationWarehouseId"], s["destination"]
	if s["status"] == "RECEIVED_AT_GUDANG" {
		branch, city = s["originWarehouseId"], s["origin"]
	}
	if branch != nil {
		return branch == w["id"]
	}
	cityText, _ := city.(string)
	warehouseCity, _ := w["city"].(string)
	return warehouseCity != "" && strings.EqualFold(cityText, warehouseCity)
}

func (b *business) gudang(r *request) (any, error) {
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive" ORDER BY code,id`)
	if err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT to_jsonb(s)||jsonb_build_object('customer',jsonb_build_object('name',c.name,'companyName',c."companyName",'phone',c.phone),
	 'paid',(SELECT COALESCE(sum(p.amount),0) FROM "Payment" p WHERE p."masterId"=s.id AND p.status='VERIFIED'),
	 'pickup',(SELECT jsonb_build_object('pickupCode',p."pickupCode",'kurirName',e.name) FROM "Pickup" p LEFT JOIN "Employee" e ON e.id=p."kurirId" AND e."isActive" WHERE p."masterId"=s.id ORDER BY p."completedAt" DESC,p.id DESC LIMIT 1),
	 'transport',(SELECT jsonb_build_object('transportCode',t."transportCode",'driverName',d.name,'kenekName',k.name) FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId" LEFT JOIN "Employee" d ON d.id=t."driverId" LEFT JOIN "Employee" k ON k.id=t."kenekId" WHERE ts."shipmentId"=s.id ORDER BY t."createdAt" DESC,t.id DESC LIMIT 1))
	 FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId"
	 WHERE s.status IN ('PICKED_UP','CREATED','READY_FOR_PICKUP','RECEIVED_AT_GUDANG','AT_DEST_GUDANG','ARRIVED_AT_GUDANG') ORDER BY s."updatedAt",s.id`)
	if err != nil {
		return nil, err
	}
	// The legacy workspace selects dimensions only, so direct volume is omitted here too.
	details, err := r.many(`SELECT jsonb_build_object('id',d.id,'masterId',d."masterId",'actualWeightKg',d."actualWeightKg",'lengthCm',d."lengthCm",'widthCm',d."widthCm",'heightCm',d."heightCm") FROM "DetailShipment" d JOIN "MasterShipment" s ON s.id=d."masterId" WHERE s.status IN ('PICKED_UP','CREATED','READY_FOR_PICKUP','RECEIVED_AT_GUDANG','AT_DEST_GUDANG','ARRIVED_AT_GUDANG') ORDER BY d.id`)
	if err != nil {
		return nil, err
	}
	byMaster := map[int][]map[string]any{}
	detailMaster := map[int]int{}
	for _, d := range details {
		id := idValue(d["masterId"])
		byMaster[id] = append(byMaster[id], d)
		detailMaster[idValue(d["id"])] = id
	}
	unscoped, warehouseID := warehouseScope(r.actor)
	var scopeWarehouse map[string]any
	names := map[int]any{}
	for _, w := range warehouses {
		names[idValue(w["id"])] = w["name"]
		if w["id"] == warehouseID {
			scopeWarehouse = w
		}
	}
	queue := map[string]map[int]bool{"gudang_arrival": {}, "transport_arrival": {}}
	visible := map[int]bool{}
	sources := map[int]map[string]any{}
	for _, s := range rows {
		id := idValue(s["id"])
		sources[id] = s
		visible[id] = unscoped
		for _, wid := range shipmentWarehouseIDs(s, warehouses) {
			visible[id] = visible[id] || wid == idValue(warehouseID)
		}
		if s["status"] == "PICKED_UP" && visible[id] {
			queue["gudang_arrival"][id] = true
		}
		if s["status"] == "AT_DEST_GUDANG" || s["status"] == "ARRIVED_AT_GUDANG" && s["destReceivedAt"] == nil {
			// Explicit IDs still scope queue rows when the warehouse has been deactivated.
			w := map[string]any{"id": warehouseID}
			if scopeWarehouse != nil {
				w = scopeWarehouse
			}
			queue["transport_arrival"][id] = unscoped || warehouseID != nil && warehouseHolds(s, w)
		}
	}
	scans, err := r.many(`SELECT jsonb_build_object('masterId',h."masterId",'detailId',h."detailId",'context',h.context,'method',h.method) FROM "HandoverScan" h JOIN "MasterShipment" s ON s.id=h."masterId" WHERE h.context IN ('gudang_arrival','transport_arrival') AND h.result IN ('ok','duplicate') AND s.status IN ('PICKED_UP','AT_DEST_GUDANG','ARRIVED_AT_GUDANG')`)
	if err != nil {
		return nil, err
	}
	counts := map[string]map[int]int{"gudang_arrival": {}, "transport_arrival": {}}
	methods := map[int]map[string]int{}
	for _, scan := range scans {
		context := textValue(scan["context"])
		master := detailMaster[idValue(scan["detailId"])]
		if !queue[context][idValue(scan["masterId"])] || !queue[context][master] {
			continue
		}
		counts[context][master]++
		if context == "gudang_arrival" {
			if methods[master] == nil {
				methods[master] = map[string]int{"SCANNED": 0, "TYPED": 0}
			}
			method := "TYPED"
			if scan["method"] == "SCANNED" {
				method = "SCANNED"
			}
			methods[master][method]++
		}
	}
	arrivals, transportArrivals, walkIns := []map[string]any{}, []map[string]any{}, []map[string]any{}
	held := []map[string]any{}
	for _, s := range rows {
		id := idValue(s["id"])
		customer := s["customer"].(map[string]any)
		name := customer["companyName"]
		if !truthy(name) {
			name = customer["name"]
		}
		totals := shipmentTotals(byMaster[id], false)
		final := s["finalPriceAmount"]
		if final == nil && s["priceAmount"] != nil {
			final = number(s["priceAmount"]) - number(s["discountAmount"])
		}
		var remaining any
		if final != nil {
			remaining = math.Max(0, number(final)-number(s["paid"]))
		}
		base := map[string]any{"id": s["id"], "masterCode": s["masterCode"], "customerName": name, "origin": s["origin"], "destination": s["destination"], "originWarehouseId": s["originWarehouseId"], "destinationWarehouseId": s["destinationWarehouseId"], "penerimaName": s["penerimaName"], "detailsCount": totals["totalPackages"], "totalWeightKg": totals["totalActualKg"], "totalVolumeM3": totals["totalVolumeM3"]}
		if queue["gudang_arrival"][id] {
			base["customerPhone"], base["priceAmount"], base["paidAmount"], base["remainingAmount"], base["dpOk"] = customer["phone"], s["priceAmount"], s["paid"], remaining, true
			base["scannedCount"], base["updatedAt"] = counts["gudang_arrival"][id], s["updatedAt"]
			m := methods[id]
			if m == nil {
				m = map[string]int{"SCANNED": 0, "TYPED": 0}
			}
			base["scannedByMethod"], base["pickupCode"], base["kurirName"] = m, nil, nil
			if p, ok := s["pickup"].(map[string]any); ok {
				base["pickupCode"], base["kurirName"] = p["pickupCode"], p["kurirName"]
			}
			arrivals = append(arrivals, base)
		}
		if (s["status"] == "CREATED" || s["status"] == "READY_FOR_PICKUP") && visible[id] {
			base["status"], base["priceAmount"], base["finalPriceAmount"] = s["status"], s["priceAmount"], final
			walkIns = append(walkIns, base)
		}
		if queue["transport_arrival"][id] {
			base["customerPhone"], base["arrivedWarehouseId"], base["originWarehouseName"] = customer["phone"], s["arrivedWarehouseId"], names[idValue(s["originWarehouseId"])]
			base["scannedCount"], base["updatedAt"] = counts["transport_arrival"][id], s["updatedAt"]
			base["transportCode"], base["driverName"], base["kenekName"] = nil, nil, nil
			if t, ok := s["transport"].(map[string]any); ok {
				base["transportCode"], base["driverName"], base["kenekName"] = t["transportCode"], t["driverName"], t["kenekName"]
			}
			transportArrivals = append(transportArrivals, base)
		}
		if s["status"] == "RECEIVED_AT_GUDANG" || s["status"] == "AT_DEST_GUDANG" || s["status"] == "ARRIVED_AT_GUDANG" {
			stage, originName := "destination", names[idValue(s["originWarehouseId"])]
			if s["status"] == "RECEIVED_AT_GUDANG" {
				stage, originName = "origin", nil
			}
			held = append(held, map[string]any{"id": s["id"], "masterCode": s["masterCode"], "customerName": name, "status": s["status"], "stage": stage, "originWarehouseName": originName, "destReceivedAt": s["destReceivedAt"], "packages": totals["totalPackages"], "weightKg": totals["totalActualKg"], "volumeM3": totals["totalVolumeM3"], "priceAmount": s["priceAmount"], "remainingAmount": remaining, "updatedAt": s["updatedAt"]})
		}
	}
	sort.SliceStable(walkIns, func(i, j int) bool {
		return textValue(sources[idValue(walkIns[i]["id"])]["createdAt"]) > textValue(sources[idValue(walkIns[j]["id"])]["createdAt"])
	})
	contents := []map[string]any{}
	for _, w := range warehouses {
		if !unscoped && w["id"] != warehouseID {
			continue
		}
		shipments := []map[string]any{}
		packages, weight := 0, 0.0
		for i := len(held) - 1; i >= 0; i-- {
			h := held[i]
			if !warehouseHolds(sources[idValue(h["id"])], w) {
				continue
			}
			shipments = append(shipments, h)
			packages += h["packages"].(int)
			weight += number(h["weightKg"])
		}
		contents = append(contents, map[string]any{"id": w["id"], "code": w["code"], "name": w["name"], "city": w["city"], "customerSupportContact": w["customerSupportContact"], "heldShipments": len(shipments), "heldPackages": packages, "heldWeightKg": pricing.Round(weight*100) / 100, "unpaidCount": 0, "shipments": shipments})
	}
	scope := map[string]any{"warehouseId": nil, "warehouseName": nil, "scoped": false}
	if !unscoped && scopeWarehouse != nil {
		scope = map[string]any{"warehouseId": scopeWarehouse["id"], "warehouseName": scopeWarehouse["name"], "scoped": true}
	}
	return map[string]any{"scope": scope, "arrivals": arrivals, "transportArrivals": transportArrivals, "walkIns": walkIns, "warehouses": contents}, nil
}
