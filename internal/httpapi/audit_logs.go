package httpapi

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

const auditSelect = `SELECT to_jsonb(a)||jsonb_build_object('actorName',COALESCE(u.name,'System'),'actorUsername',u.username,
 'master',to_jsonb(s),'transport',CASE WHEN t.id IS NULL THEN NULL ELSE to_jsonb(t)||jsonb_build_object('route',to_jsonb(rt),'shipments',COALESCE((SELECT jsonb_agg(jsonb_build_object('master',to_jsonb(ms))) FROM "TransportShipment" ts JOIN "MasterShipment" ms ON ms.id=ts."shipmentId" WHERE ts."transportId"=t.id),'[]'::jsonb)) END)
 FROM "AuditLog" a LEFT JOIN "User" u ON u.id=a."actorId"
 LEFT JOIN "Pickup" p ON a."entityType"='pickup' AND p.id=a."entityId"
 LEFT JOIN "Delivery" d ON a."entityType"='delivery' AND d.id=a."entityId"
 LEFT JOIN "DetailShipment" ds ON a."entityType"='shipment_detail' AND ds.id=a."entityId"
 LEFT JOIN "Payment" pay ON a."entityType"='payment' AND pay.id=a."entityId"
 LEFT JOIN "MasterShipment" s ON s.id=CASE WHEN a."entityType"='shipment' THEN a."entityId" ELSE COALESCE(p."masterId",d."masterId",ds."masterId",pay."masterId") END
 LEFT JOIN "Transport" t ON a."entityType"='transport' AND t.id=a."entityId" LEFT JOIN "Route" rt ON rt.id=t."routeId" `

func warehouseIDsVisible(actor map[string]any, ids []int) bool {
	unscoped, wid := warehouseScope(actor)
	return unscoped || wid != nil && slices.Contains(ids, idValue(wid))
}

func (r *request) visibleAudits(rows, warehouses []map[string]any) []map[string]any {
	unscoped, _ := warehouseScope(r.actor)
	out := []map[string]any{}
	for _, row := range rows {
		visible := unscoped
		if !unscoped {
			switch row["entityType"] {
			case "shipment", "shipment_detail", "pickup", "delivery", "payment":
				if row["master"] != nil {
					visible = warehouseIDsVisible(r.actor, shipmentWarehouseIDs(object(row["master"]), warehouses))
				}
			case "transport":
				if row["transport"] != nil {
					t := object(row["transport"])
					t["shipments"] = objects(t["shipments"])
					visible = warehouseIDsVisible(r.actor, transportGudangIDs(t, warehouses))
				}
			default:
				visible = true
			}
		}
		if visible {
			out = append(out, row)
		}
	}
	return out
}

func auditJSON(v any) any {
	var parsed any
	if s, ok := v.(string); ok && json.Unmarshal([]byte(s), &parsed) == nil {
		return parsed
	}
	return nil
}

func auditInteger(r *request, key string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(r.c.Query(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, problem(422, "Parameter audit tidak valid.")
	}
	return min(maximum, max(minimum, n)), nil
}

func (b *business) auditLogs(r *request) (any, error) {
	limit, err := auditInteger(r, "limit", 100, 1, 500)
	if err != nil {
		return nil, err
	}
	offset, err := auditInteger(r, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	var entityID, actorID any
	for _, item := range []struct {
		key    string
		target *any
	}{{"entityId", &entityID}, {"actorId", &actorID}} {
		if raw := strings.TrimSpace(r.c.Query(item.key)); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 32)
			if err != nil {
				return nil, problem(422, "Parameter audit tidak valid.")
			}
			*item.target = n
		}
	}
	where := `WHERE ($1='' OR a."entityType"=$1) AND ($2::int IS NULL OR a."entityId"=$2) AND ($3='' OR a.action=$3) AND ($4::int IS NULL OR $4=0 OR a."actorId"=$4) AND ($5='' OR strpos(lower(a."entityLabel"),lower($5))>0 OR strpos(lower(a.action),lower($5))>0 OR strpos(lower(u.name),lower($5))>0)`
	args := []any{strings.TrimSpace(r.c.Query("entityType")), entityID, strings.TrimSpace(r.c.Query("action")), actorID, strings.TrimSpace(r.c.Query("search"))}
	unscoped, _ := warehouseScope(r.actor)
	take, skip := limit, offset
	if !unscoped {
		take, skip = limit+min(offset, 500), 0
		take = min(500, take)
	}
	rows, err := r.many(auditSelect+where+` ORDER BY a."createdAt" DESC,a.id DESC LIMIT $6 OFFSET $7`, append(args, take, skip)...)
	if err != nil {
		return nil, err
	}
	if !unscoped {
		warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
		if err != nil {
			return nil, err
		}
		rows = r.visibleAudits(rows, warehouses)
		start := min(offset, len(rows))
		rows = rows[start:min(start+limit, len(rows))]
	}
	total := len(rows)
	if unscoped {
		if err = r.db.QueryRow(r.ctx, `SELECT count(*) FROM "AuditLog" a LEFT JOIN "User" u ON u.id=a."actorId" `+where, args...).Scan(&total); err != nil {
			return nil, err
		}
	}
	groups, err := r.one(`SELECT jsonb_build_object('entityTypes',COALESCE((SELECT jsonb_agg(x ORDER BY x) FROM (SELECT DISTINCT "entityType" x FROM "AuditLog" WHERE "entityType"<>'') t),'[]'::jsonb),'actions',COALESCE((SELECT jsonb_agg(x ORDER BY x) FROM (SELECT DISTINCT action x FROM "AuditLog") t),'[]'::jsonb))`)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, row := range rows {
		item := map[string]any{}
		for _, key := range []string{"id", "action", "entityType", "entityId", "entityLabel", "actorName", "actorUsername", "createdAt"} {
			item[key] = row[key]
		}
		item["beforeData"], item["afterData"] = auditJSON(row["beforeData"]), auditJSON(row["afterData"])
		out = append(out, item)
	}
	return Envelope{Data: out, Meta: map[string]any{"total": total, "limit": limit, "offset": offset, "entityTypes": groups["entityTypes"], "actions": groups["actions"]}}, nil
}
