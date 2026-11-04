package httpapi

import "strings"

func shipmentWarehouseIDs(s map[string]any, warehouses []map[string]any) []int {
	idsFor := func(city, explicit any) []int {
		out := []int{}
		if id := idValue(explicit); id > 0 {
			out = append(out, id)
		}
		key := strings.ToLower(textValue(city))
		for _, w := range warehouses {
			if key != "" && w["isActive"] == true && strings.ToLower(textValue(w["city"])) == key {
				out = append(out, idValue(w["id"]))
			}
		}
		return out
	}
	origin := idsFor(s["origin"], s["originWarehouseId"])
	destination := idsFor(s["destination"], s["destinationWarehouseId"])
	var result []int
	switch s["status"] {
	case "IN_TRANSPORT":
		result = append(origin, destination...)
	case "AT_DEST_GUDANG", "ARRIVED_AT_GUDANG", "DELIVERED":
		if id := idValue(s["arrivedWarehouseId"]); id > 0 {
			result = []int{id}
		} else {
			result = destination
		}
	case "RECEIVED_AT_GUDANG":
		if id := idValue(s["arrivedWarehouseId"]); id > 0 {
			result = []int{id}
		} else {
			result = origin
		}
	default:
		result = origin
	}
	out := []int{}
	seen := map[int]bool{}
	for _, id := range result {
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

func shipmentVisible(user, s map[string]any, warehouses []map[string]any) bool {
	if s["fulfillmentMode"] == "DIRECT" && hasPermission(user, "shipment.direct.manage") {
		return true
	}
	unscoped, warehouse := warehouseScope(user)
	if unscoped {
		return true
	}
	for _, id := range shipmentWarehouseIDs(s, warehouses) {
		if idValue(warehouse) == id {
			return true
		}
	}
	return false
}

func (r *request) assertShipmentScope(s map[string]any) error {
	if hasPermission(r.actor, "*") || s["fulfillmentMode"] == "DIRECT" && hasPermission(r.actor, "shipment.direct.manage") {
		return nil
	}
	if partner := marketingID(r.actor); partner != nil {
		if s["createdByPartnerId"] != partner {
			return problem(403, "Shipment ini bukan milik partner Anda.")
		}
		return nil
	}
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return err
	}
	if !shipmentVisible(r.actor, s, warehouses) {
		return problem(403, "Shipment ini berada di gudang lain - data terpisah antar gudang.")
	}
	return nil
}

func (r *request) shipment(lock bool) (map[string]any, error) {
	s, err := r.find("MasterShipment", "Shipment tidak ditemukan.", lock)
	if err != nil {
		return nil, err
	}
	if err = r.assertShipmentScope(s); err != nil {
		return nil, err
	}
	return s, nil
}

func detailEditable(s map[string]any) bool {
	return s["status"] == "CREATED" || s["status"] == "READY_FOR_PICKUP"
}
