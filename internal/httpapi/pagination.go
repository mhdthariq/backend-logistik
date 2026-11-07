package httpapi

import (
	"fmt"
	"strconv"
)

func bind(args *[]any, value any) string {
	*args = append(*args, value)
	return fmt.Sprintf("$%d", len(*args))
}

// shortcut: legacy callers remain unbounded; remove the fallback after their pickers use paged searches.
func (r *request) listRows(projection, from, order, alias, unpriced string, args []any) ([]map[string]any, map[string]any, error) {
	if r.c.Query("page") == "" && r.c.Query("pageSize") == "" {
		rows, err := r.many(projection+from+order, args...)
		return rows, nil, err
	}
	page, err := strconv.Atoi(r.c.Query("page", "1"))
	if err != nil || page < 1 || page > 1000000 {
		return nil, nil, problem(422, "page harus antara 1 dan 1000000.")
	}
	size, err := strconv.Atoi(r.c.Query("pageSize", "25"))
	if err != nil || size < 1 || size > 100 {
		return nil, nil, problem(422, "pageSize harus antara 1 dan 100.")
	}
	if unpriced == "" {
		unpriced = "false"
	}
	meta, err := r.one(`SELECT jsonb_build_object('total',COALESCE(sum(n),0),'statuses',COALESCE(jsonb_object_agg(status,n),'{}'::jsonb),'unpriced',COALESCE(sum(unpriced),0)) FROM (SELECT `+alias+`.status,count(*) n,count(*) FILTER (WHERE `+unpriced+`) unpriced`+from+` GROUP BY `+alias+`.status) counts`, args...)
	if err != nil {
		return nil, nil, err
	}
	pages := max(1, (int(number(meta["total"]))+size-1)/size)
	page = min(page, pages)
	meta["page"], meta["pageSize"], meta["pages"] = page, size, pages
	limit, offset := bind(&args, size), bind(&args, (page-1)*size)
	// shortcut: OFFSET scans earlier IDs; use cursors when deep-page plans become slow.
	// Select IDs first: JSON relations and package aggregates only run for this page.
	query := `WITH page_ids AS MATERIALIZED (SELECT ` + alias + `.id` + from + order + ` LIMIT ` + limit + ` OFFSET ` + offset + `) ` + projection + from + ` AND ` + alias + `.id IN (SELECT id FROM page_ids)` + order
	rows, err := r.many(query, args...)
	return rows, meta, err
}

func listResult(rows []map[string]any, meta map[string]any) any {
	if meta == nil {
		return rows
	}
	return Envelope{Data: rows, Meta: meta}
}

// This mirrors shipmentWarehouseIDs, including explicit IDs and active city matches.
func shipmentWarehouseMatch(alias, parameter string, originOnly bool) string {
	side := func(name string) string {
		return `(` + alias + `."` + name + `WarehouseId"=` + parameter + ` OR EXISTS(SELECT 1 FROM "Warehouse" scope_w WHERE scope_w.id=` + parameter + ` AND scope_w."isActive" AND trim(` + alias + `.` + name + `)<>'' AND lower(trim(scope_w.city))=lower(trim(` + alias + `.` + name + `))))`
	}
	if originOnly {
		return side("origin")
	}
	return `(CASE WHEN ` + alias + `.status='IN_TRANSPORT' THEN (` + side("origin") + ` OR ` + side("destination") + `) WHEN ` + alias + `.status IN ('AT_DEST_GUDANG','ARRIVED_AT_GUDANG','DELIVERED') THEN CASE WHEN ` + alias + `."arrivedWarehouseId">0 THEN ` + alias + `."arrivedWarehouseId"=` + parameter + ` ELSE ` + side("destination") + ` END WHEN ` + alias + `.status='RECEIVED_AT_GUDANG' THEN CASE WHEN ` + alias + `."arrivedWarehouseId">0 THEN ` + alias + `."arrivedWarehouseId"=` + parameter + ` ELSE ` + side("origin") + ` END ELSE ` + side("origin") + ` END)`
}

func (r *request) warehouseListFilter(args *[]any, match func(string) string, direct bool) (string, error) {
	filter := ""
	unscoped, wid := warehouseScope(r.actor)
	if !unscoped {
		condition := match(bind(args, idValue(wid)))
		if direct && hasPermission(r.actor, "shipment.direct.manage") {
			condition = `(s."fulfillmentMode"='DIRECT' OR ` + condition + `)`
		}
		filter += ` AND ` + condition
	}
	if raw := r.c.Query("warehouseId"); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id < 1 || id > 2147483647 {
			return "", problem(422, "warehouseId tidak valid.")
		}
		filter += ` AND ` + match(bind(args, id))
	}
	return filter, nil
}
