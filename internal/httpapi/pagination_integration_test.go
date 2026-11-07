package httpapi_test

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestListPaginationPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (110,'delivery.view','Delivery','view'),(111,'shipment.view','Shipment','view'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,110),(2,111)`)
	shipmentSQL(t, db, `INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","destinationWarehouseId","arrivedWarehouseId","fulfillmentMode","createdAt","updatedAt")
 SELECT g,'PAGE-'||g,$1,CASE WHEN g%3=0 THEN 'Other' ELSE ' Jakarta ' END,'Bandung',CASE g%4 WHEN 0 THEN 'CREATED' WHEN 1 THEN 'IN_TRANSPORT' WHEN 2 THEN 'RECEIVED_AT_GUDANG' ELSE 'DELIVERED' END,NULL,11,CASE WHEN g%4=3 THEN 11 ELSE NULL END,CASE WHEN g%5=0 THEN 'DIRECT' ELSE 'STANDARD' END,'2026-11-01',NOW() FROM generate_series(100,160) g`, cid)
	shipmentSQL(t, db, `INSERT INTO "DetailShipment" ("masterId","detailCode",description,"actualWeightKg","updatedAt") SELECT g,'PKG-'||g,'Box',2,NOW() FROM generate_series(100,160) g;
 INSERT INTO "Pickup" (id,"pickupCode","masterId","kurirId",status,"createdAt","updatedAt") SELECT g,'PICK-'||g,g,CASE WHEN g%2=0 THEN 10 ELSE 11 END,'ASSIGNED','2026-11-01',NOW() FROM generate_series(100,160) g;
 INSERT INTO "Delivery" (id,"deliveryCode","masterId","kurirId",status,"createdAt","updatedAt") SELECT g,'DLV-'||g,g,CASE WHEN g%2=0 THEN 10 ELSE 11 END,'ASSIGNED','2026-11-01',NOW() FROM generate_series(100,160) g;
 INSERT INTO "Transport" (id,"transportCode","routeId","vehicleId","driverId",status,"createdAt","updatedAt") SELECT g,'TRP-'||g,CASE WHEN g%3=0 THEN 11 ELSE 10 END,10,10,CASE WHEN g%2=0 THEN 'ARRIVED' ELSE 'COMPLETED' END,'2026-11-01',NOW() FROM generate_series(100,160) g;
 INSERT INTO "TransportShipment" ("transportId","shipmentId") SELECT g,g FROM generate_series(100,160) g;`)
	shipmentSQL(t, db, `INSERT INTO "Invoice" (id,"invoiceNumber","customerId",status,"createdAt","updatedAt") SELECT g,'INV-'||g,$1,'DRAFT','2026-11-01',NOW() FROM generate_series(100,160) g`, cid)
	worker := loginAs(t, app, "worker", "")
	for _, endpoint := range []string{"shipments", "pickups", "deliveries", "transports", "invoices"} {
		for _, token := range []string{owner, worker} {
			if endpoint == "invoices" && token == worker {
				continue
			}
			for _, filter := range []string{"", "&warehouseId=10", "&warehouseId=11", "&search=PAGE-100", "&status=COMPLETED"} {
				path := "/api/v1/" + endpoint + "?" + filter
				t.Run(endpoint+filter+fmt.Sprint(token == owner), func(t *testing.T) {
					full := apiCall(t, app, "GET", path, "", token, "", 200)["data"].([]any)
					if token == owner && endpoint != "invoices" && (filter == "&warehouseId=10" || filter == "&warehouseId=11") {
						all := apiCall(t, app, "GET", "/api/v1/"+endpoint, "", owner, "", 200)["data"].([]any)
						wid := float64(10)
						if filter == "&warehouseId=11" {
							wid = 11
						}
						var expected []any
						for _, row := range all {
							for _, id := range row.(map[string]any)["gudangIds"].([]any) {
								if id == wid {
									expected = append(expected, row)
									break
								}
							}
						}
						if len(expected) != len(full) || (len(full) > 0 && !reflect.DeepEqual(expected, full)) {
							t.Fatal("SQL warehouse filtering differs from existing warehouse membership")
						}
					}
					var combined []any
					for page := 1; page <= max(1, (len(full)+16)/17); page++ {
						payload := apiCall(t, app, "GET", path+fmt.Sprintf("&page=%d&pageSize=17", page), "", token, "", 200)
						rows, meta := payload["data"].([]any), payload["meta"].(map[string]any)
						if len(rows) > 17 || meta["total"] != float64(len(full)) {
							t.Fatalf("wrong limit/count: rows=%d meta=%v expected=%d", len(rows), meta, len(full))
						}
						combined = append(combined, rows...)
					}
					if len(combined) != len(full) || (len(full) > 0 && !reflect.DeepEqual(combined, full)) {
						t.Fatal("pages lost, duplicated, or changed records")
					}
				})
			}
		}
		for _, query := range []string{"page=0", "page=-1", "page=x", "page=1000001", "pageSize=0", "pageSize=101", "pageSize=999999999999999999999", "page=1&warehouseId=-1"} {
			if endpoint == "invoices" && query == "page=1&warehouseId=-1" {
				continue
			}
			apiCall(t, app, "GET", "/api/v1/"+endpoint+"?"+query, "", owner, "", 422)
		}
		last := apiCall(t, app, "GET", "/api/v1/"+endpoint+"?page=999&pageSize=25", "", owner, "", 200)
		if last["meta"].(map[string]any)["page"] != float64(3) || len(last["data"].([]any)) != 11 {
			t.Fatal("last-page clamp failed", last)
		}
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (112,'shipment.direct.manage','Shipment','direct'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,112)`)
	direct := apiCall(t, app, "GET", "/api/v1/shipments?page=1&fulfillmentMode=DIRECT", "", worker, "", 200)
	if direct["meta"].(map[string]any)["total"] != float64(13) {
		t.Fatal("direct manager lost shipments outside their warehouse", direct)
	}
	history := apiCall(t, app, "GET", "/api/v1/transports?page=1&history=true&status=COMPLETED", "", owner, "", 200)
	if history["meta"].(map[string]any)["total"] != float64(30) {
		t.Fatal("paginated history ignored status", history)
	}
	// No employee assignment must still return the page envelope, never another courier's tasks.
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=NULL WHERE id=2`)
	for _, endpoint := range []string{"pickups", "deliveries"} {
		response := apiCall(t, app, "GET", "/api/v1/"+endpoint+"?mine=true&page=1", "", worker, "", 200)
		if response["meta"].(map[string]any)["total"] != float64(0) {
			t.Fatal(response)
		}
	}
	options := apiCall(t, app, "GET", "/api/v1/options?include=warehouses", "", owner, "", 200)["data"].(map[string]any)
	if len(options) != 2 || options["warehouses"] == nil || options["company"] == nil {
		t.Fatal("options loaded unwanted groups", options)
	}
	apiCall(t, app, "GET", "/api/v1/options?include=unknown", "", owner, "", 422)
	// Search must match company/receiver fields across the entire data set.
	shipmentSQL(t, db, `UPDATE "Customer" SET "companyName"='Pagination Company' WHERE id=$1`, cid)
	response := apiCall(t, app, "GET", "/api/v1/shipments?page=1&search="+url.QueryEscape("Pagination Company"), "", owner, "", 200)
	if response["meta"].(map[string]any)["total"] != float64(61) {
		t.Fatal(response)
	}
}

func TestPaginationQueryPlanPostgres(t *testing.T) {
	db, _, _, cid := shipmentApp(t)
	var valid bool
	if err := db.pool.QueryRow(context.Background(), `SELECT indisvalid FROM pg_index WHERE indexrelid='"DetailShipment_masterId_idx"'::regclass`).Scan(&valid); err != nil || !valid {
		t.Fatalf("package lookup migration did not create a valid index: %v", err)
	}
	// Reproduce the pre-migration baseline only in this disposable database.
	shipmentSQL(t, db, `DROP INDEX "DetailShipment_masterId_idx"`)
	shipmentSQL(t, db, `INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,"updatedAt") SELECT g,'PERF-'||g,$1,'Jakarta','Bandung',NOW() FROM generate_series(100,1099) g`, cid)
	shipmentSQL(t, db, `INSERT INTO "DetailShipment" ("masterId","detailCode",description,"updatedAt") SELECT g,'PERF-PKG-'||g||'-'||n,'Box',NOW() FROM generate_series(100,1099) g CROSS JOIN generate_series(1,5) n; ANALYZE "MasterShipment"; ANALYZE "DetailShipment";`)
	projection := `SELECT to_jsonb(s)||jsonb_build_object('details',(SELECT jsonb_agg(to_jsonb(d)) FROM "DetailShipment" d WHERE d."masterId"=s.id)) FROM "MasterShipment" s`
	for _, item := range []struct{ name, sql string }{
		{"unbounded baseline", projection + ` ORDER BY s."createdAt" DESC,s.id DESC`},
		{"page of 25", `WITH page_ids AS MATERIALIZED (SELECT id FROM "MasterShipment" ORDER BY "createdAt" DESC,id DESC LIMIT 25) ` + projection + ` WHERE s.id IN (SELECT id FROM page_ids) ORDER BY s."createdAt" DESC,s.id DESC`},
		{"page of 25 with package index", `WITH page_ids AS MATERIALIZED (SELECT id FROM "MasterShipment" ORDER BY "createdAt" DESC,id DESC LIMIT 25) ` + projection + ` WHERE s.id IN (SELECT id FROM page_ids) ORDER BY s."createdAt" DESC,s.id DESC`},
	} {
		if item.name == "page of 25 with package index" {
			shipmentSQL(t, db, `CREATE INDEX CONCURRENTLY "DetailShipment_masterId_idx" ON "DetailShipment" ("masterId")`)
			shipmentSQL(t, db, `ANALYZE "DetailShipment"`)
		}
		started := time.Now()
		rows, err := db.pool.Query(context.Background(), `EXPLAIN (ANALYZE,BUFFERS) `+item.sql)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan += line + "\n"
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s (%s):\n%s", item.name, time.Since(started), plan)
	}
}
