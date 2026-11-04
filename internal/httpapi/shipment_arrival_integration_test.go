package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func TestArrivalScanningPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box A"},{"description":"Box B"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	worker := loginAs(t, app, "worker", "")
	for _, route := range []struct{ method, suffix string }{{"GET", "arrival-scans"}, {"POST", "arrival-scans"}, {"POST", "arrival-scan-all"}, {"POST", "arrive"}, {"POST", "undo-arrival"}} {
		apiCall(t, app, route.method, path+"/"+route.suffix, `{}`, "", "", 401)
		apiCall(t, app, route.method, path+"/"+route.suffix, `{}`, worker, "", 403)
		apiCall(t, app, route.method, "/api/v1/shipments/999999/"+route.suffix, `{"reason":"wrong receipt"}`, owner, "", 404)
	}
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrival-scans", `{"payload":"x"}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1`, s["id"])
	for _, body := range []string{`null`, `{`, `{}`, `{"payload":" "}`} {
		apiCall(t, app, "POST", path+"/arrival-scans", body, owner, "", 422)
	}
	checkProgress := func(response map[string]any, scanned float64, all, master bool) map[string]any {
		t.Helper()
		p := response["data"].(map[string]any)["progress"].(map[string]any)
		if p["total"] != float64(2) || p["scanned"] != scanned || p["allScanned"] != all || p["masterScanned"] != master {
			t.Fatal(p)
		}
		return p
	}
	p := checkProgress(apiCall(t, app, "GET", path+"/arrival-scans", "", owner, "", 200), 0, false, false)
	details := p["details"].([]any)
	code := details[0].(map[string]any)["detailCode"].(string)
	master := fmt.Sprintf(`{"payload":%q,"method":"SCANNED"}`, s["masterCode"])
	checkProgress(apiCall(t, app, "POST", path+"/arrival-scans", master, owner, "", 200), 0, false, true)
	unknown := apiCall(t, app, "POST", path+"/arrival-scans", `{"payload":"unrecognized"}`, owner, "", 200)["data"].(map[string]any)
	if unknown["context"] != nil || unknown["scan"].(map[string]any)["result"] != "unexpected" {
		t.Fatal(unknown)
	}
	apiCall(t, app, "POST", path+"/arrival-scans", `{"payload":"https://example.test/tracking-paket?corporateId=OTHER&resi=x"}`, owner, "", 422)
	body := fmt.Sprintf(`{"payload":"https://example.test/tracking-paket?corporateId=LOCAL&resi=%s","method":"SCANNED"}`, code)
	p = checkProgress(apiCall(t, app, "POST", path+"/arrival-scans", body, owner, "", 200), 1, false, true)
	first := p["details"].([]any)[0].(map[string]any)
	if first["scannedByName"] != "Owner" || first["scanMethod"] != "SCANNED" || first["scannedAt"] == nil {
		t.Fatal(first)
	}
	duplicate := apiCall(t, app, "POST", path+"/arrival-scans", fmt.Sprintf(`{"payload":%q}`, code), owner, "", 200)["data"].(map[string]any)
	if duplicate["scan"].(map[string]any)["result"] != "duplicate" || duplicate["message"] != "Paket ini sudah pernah discan." {
		t.Fatal(duplicate)
	}
	checkProgress(map[string]any{"data": duplicate}, 1, false, true)
	if duplicate["progress"].(map[string]any)["details"].([]any)[0].(map[string]any)["scannedAt"] != first["scannedAt"] {
		t.Fatal("earliest scan was replaced")
	}
	bulk := apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)
	checkProgress(bulk, 2, true, true)
	if bulk["data"].(map[string]any)["created"] != float64(1) {
		t.Fatal(bulk)
	}
	if got := apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)["data"].(map[string]any)["created"]; got != float64(0) {
		t.Fatal(got)
	}
	// Contexts do not share successful scans. B2B needs just one master read.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='AT_DEST_GUDANG' WHERE id=$1; UPDATE "Customer" SET type='b2b' WHERE id=$2`, s["id"], cid)
	checkProgress(apiCall(t, app, "GET", path+"/arrival-scans", "", owner, "", 200), 0, false, false)
	p = checkProgress(apiCall(t, app, "POST", path+"/arrival-scans", master, owner, "", 200), 2, true, true)
	if p["isB2B"] != true || p["details"].([]any)[1].(map[string]any)["scanMethod"] != "SCANNED" {
		t.Fatal(p)
	}
	shipmentSQL(t, db, `DELETE FROM "HandoverScan" WHERE "masterId"=$1 AND context='transport_arrival'`, s["id"])
	bulk = apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)
	checkProgress(bulk, 2, true, true)
	if bulk["data"].(map[string]any)["created"] != float64(1) {
		t.Fatal(bulk)
	}
	// B2B empty consignments can display a master read but cannot be received.
	empty := newShipment(t, app, owner, cid, "")
	emptyPath := fmt.Sprintf("/api/v1/shipments/%.0f", empty["id"])
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1`, empty["id"])
	apiCall(t, app, "POST", emptyPath+"/arrival-scan-all", `{}`, owner, "", 422)
}

func TestArrivalAndUndoPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH-O','Origin','Jakarta',NOW()),(11,'WH-D','Destination','Bandung',NOW())`)
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	apiCall(t, app, "POST", path+"/arrive", `null`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10,"mode":"walk_in","notes":"Counter"}`, owner, "", 200)
	for _, body := range []string{`null`, `{}`, `{"reason":2}`, `{"reason":" "}`, `{"reason":"` + strings.Repeat("x", 1001) + `"}`, `{"reason":"` + strings.Repeat("📦", 501) + `"}`} {
		apiCall(t, app, "POST", path+"/undo-arrival", body, owner, "", 422)
	}
	for _, id := range []string{"0", "bad", "1.5", "-1"} {
		apiCall(t, app, "POST", "/api/v1/shipments/"+id+"/undo-arrival", `{"reason":"wrong"}`, owner, "", 422)
	}
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"wrong counter"}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"again"}`, owner, "", 409)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1; INSERT INTO "Pickup" ("pickupCode","masterId",status,"updatedAt") VALUES ('PU-ARRIVAL',$1,'IN_PROGRESS',NOW())`, s["id"])
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10,"mode":"walk_in"}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10,"notes":"Received"}`, owner, "", 200)
	var pickup string
	var completed bool
	if err := db.pool.QueryRow(context.Background(), `SELECT status,"completedAt" IS NOT NULL FROM "Pickup" WHERE "masterId"=$1`, s["id"]).Scan(&pickup, &completed); err != nil || pickup != "COMPLETED" || !completed {
		t.Fatalf("%s %v %v", pickup, completed, err)
	}
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"wrong scan"}`, owner, "", 200)
	var scans int
	if err := db.pool.QueryRow(context.Background(), `SELECT status,"completedAt" IS NOT NULL,(SELECT count(*) FROM "HandoverScan" WHERE "masterId"=$1 AND context='gudang_arrival') FROM "Pickup" WHERE "masterId"=$1`, s["id"]).Scan(&pickup, &completed, &scans); err != nil || pickup != "PICKED_UP" || completed || scans != 0 {
		t.Fatalf("%s %v %d %v", pickup, completed, scans, err)
	}
	// Legacy origin receipt recovers the previous state from tracking history.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"arrivalPreviousStatus"=NULL WHERE id=$1; INSERT INTO "TrackingEvent" ("masterId",event,description,"occurredAt") VALUES ($1,'READY_FOR_PICKUP','legacy',NOW()+INTERVAL '1 minute')`, s["id"])
	if got := apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"legacy"}`, owner, "", 200)["data"].(map[string]any)["status"]; got != "READY_FOR_PICKUP" {
		t.Fatal(got)
	}
	// Destination reception uses its stored warehouse, and leaves origin scans alone.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='AT_DEST_GUDANG',"arrivedWarehouseId"=11 WHERE id=$1; INSERT INTO "HandoverScan" ("masterId",context,"scanLevel",payload,result) VALUES ($1,'gudang_arrival','master','old','ok')`, s["id"])
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"transport"}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)
	r := apiCall(t, app, "POST", path+"/arrive", `{"mode":"transport","warehouseId":10}`, owner, "", 200)["data"].(map[string]any)
	if r["status"] != "ARRIVED_AT_GUDANG" || r["destReceivedAt"] == nil || r["warehouseName"] != "Destination" {
		t.Fatal(r)
	}
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"transport"}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"wrong destination receipt"}`, owner, "", 200)
	var status string
	var received, warehouse bool
	if err := db.pool.QueryRow(context.Background(), `SELECT status,"destReceivedAt" IS NOT NULL,"arrivedWarehouseId"=11,(SELECT count(*) FROM "HandoverScan" WHERE "masterId"=$1) FROM "MasterShipment" WHERE id=$1`, s["id"]).Scan(&status, &received, &warehouse, &scans); err != nil || status != "AT_DEST_GUDANG" || received || !warehouse || scans != 1 {
		t.Fatalf("%s %v %v %d %v", status, received, warehouse, scans, err)
	}
	// Older destination rows without a receipt timestamp still use transport scans.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='ARRIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"transport"}`, owner, "", 200)
	// Non-arrival lifecycle states reject reception and scan writes.
	for _, state := range []string{"RECEIVED_AT_GUDANG", "IN_TRANSPORT", "DELIVERED", "CANCELLED", "UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			shipmentSQL(t, db, `UPDATE "MasterShipment" SET status=$2,"destReceivedAt"=NULL WHERE id=$1`, s["id"], state)
			for _, body := range []string{`{"warehouseId":10}`, `{"warehouseId":10,"mode":"walk_in"}`, `{"mode":"transport"}`} {
				apiCall(t, app, "POST", path+"/arrive", body, owner, "", 422)
			}
			apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 422)
		})
	}
}

func TestArrivalRollbackAndGuardsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH','Origin','Jakarta',NOW())`)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"A"},{"description":"B"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1; INSERT INTO "Pickup" ("pickupCode","masterId",status,"updatedAt") VALUES ('P-ROLL',$1,'PICKED_UP',NOW()); CREATE FUNCTION fail_scan() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM "HandoverScan")>0 THEN RAISE EXCEPTION 'injected scan failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_scan BEFORE INSERT ON "HandoverScan" FOR EACH ROW EXECUTE FUNCTION fail_scan()`, s["id"])
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 500)
	var count int
	if err := db.pool.QueryRow(context.Background(), `SELECT count(*) FROM "HandoverScan"`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial scan batch %d %v", count, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_scan ON "HandoverScan"`)
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, owner, "", 200)
	shipmentSQL(t, db, `CREATE FUNCTION fail_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event IN ('RECEIVED_AT_GUDANG','ARRIVAL_UNDONE') THEN RAISE EXCEPTION 'injected receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_receipt BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_receipt()`)
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10}`, owner, "", 500)
	check := func(status, pickup string, scans int) {
		t.Helper()
		var gotStatus, gotPickup string
		var gotScans int
		if err := db.pool.QueryRow(context.Background(), `SELECT status,(SELECT status FROM "Pickup" WHERE "masterId"=$1),(SELECT count(*) FROM "HandoverScan" WHERE "masterId"=$1) FROM "MasterShipment" WHERE id=$1`, s["id"]).Scan(&gotStatus, &gotPickup, &gotScans); err != nil || gotStatus != status || gotPickup != pickup || gotScans != scans {
			t.Fatalf("%s %s %d %v", gotStatus, gotPickup, gotScans, err)
		}
	}
	check("PICKED_UP", "PICKED_UP", 2)
	shipmentSQL(t, db, `DROP TRIGGER fail_receipt ON "TrackingEvent"`)
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10}`, owner, "", 200)
	shipmentSQL(t, db, `CREATE TRIGGER fail_receipt BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_receipt()`)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"rollback"}`, owner, "", 500)
	check("RECEIVED_AT_GUDANG", "COMPLETED", 2)
	shipmentSQL(t, db, `DROP TRIGGER fail_receipt ON "TrackingEvent"; CREATE FUNCTION fail_undo_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='undo_arrival' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_undo_audit BEFORE INSERT ON "AuditLog" FOR EACH ROW EXECUTE FUNCTION fail_undo_audit()`)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"audit rollback"}`, owner, "", 500)
	check("RECEIVED_AT_GUDANG", "COMPLETED", 2)
	shipmentSQL(t, db, `DROP TRIGGER fail_undo_audit ON "AuditLog"; INSERT INTO "Delivery" ("deliveryCode","masterId",status,"updatedAt") VALUES ('D-GUARD',$1,'COMPLETED',NOW())`, s["id"])
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"downstream"}`, owner, "", 409)
	shipmentSQL(t, db, `UPDATE "Delivery" SET status='CANCELLED'; INSERT INTO "TransportExpense" ("expenseCode","shipmentId",type,amount,status,"updatedAt") VALUES ('EX-GUARD',$1,'BONGKAR',100,'APPROVED',NOW())`, s["id"])
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"expense"}`, owner, "", 409)
	shipmentSQL(t, db, `UPDATE "TransportExpense" SET status='CANCELLED'; INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'ARR-TRUCK',1000,10,NOW()); INSERT INTO "Transport" (id,"transportCode","vehicleId","updatedAt") VALUES (10,'TR-GUARD',10,NOW()); INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,$1)`, s["id"])
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"transport"}`, owner, "", 409)
	check("RECEIVED_AT_GUDANG", "COMPLETED", 2)
	// Destination undo may retain its inbound transport link.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='ARRIVED_AT_GUDANG',"destReceivedAt"=NOW() WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"destination"}`, owner, "", 200)
}

func TestArrivalConcurrentScansPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f/arrival-scans", s["id"])
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1`, s["id"])
	var code string
	if err := db.pool.QueryRow(context.Background(), `SELECT "detailCode" FROM "DetailShipment" WHERE "masterId"=$1`, s["id"]).Scan(&code); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			req := httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"payload":%q}`, code)))
			req.Header.Set("Authorization", "Bearer "+owner)
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				results <- err.Error()
				return
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 {
				results <- fmt.Sprint(resp.StatusCode, err, body)
				return
			}
			results <- body["data"].(map[string]any)["scan"].(map[string]any)["result"].(string)
		})
	}
	wg.Wait()
	close(results)
	ok, duplicates := 0, 0
	for result := range results {
		switch result {
		case "ok":
			ok++
		case "duplicate":
			duplicates++
		default:
			t.Fatal(result)
		}
	}
	if ok != 1 || duplicates != 7 {
		t.Fatalf("ok %d duplicates %d", ok, duplicates)
	}
}

func TestArrivalWarehouseScopePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH-SCOPE-O','Origin','Jakarta',NOW()),(11,'WH-SCOPE-D','Destination','Bandung',NOW()); INSERT INTO "Employee" (id,"employeeNumber",name,"warehouseId","updatedAt") VALUES (10,'EMP-ARRIVAL','Worker',10,NOW()); UPDATE "User" SET "employeeId"=10 WHERE id=2; INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'shipment.view','Shipment','view'),(101,'shipment.confirm_arrival','Shipment','receive'),(102,'shipment.undo_arrival','Shipment','undo'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100),(2,101),(2,102)`)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"walk_in","warehouseId":11}`, worker, "", 403)
	shipmentSQL(t, db, `UPDATE "Warehouse" SET "isActive"=false WHERE id=10`)
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"walk_in","warehouseId":10}`, worker, "", 422)
	shipmentSQL(t, db, `UPDATE "Warehouse" SET "isActive"=true WHERE id=10`)
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"walk_in","warehouseId":10}`, worker, "", 200)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"counter"}`, worker, "", 200)
	// Destination data is isolated from staff of the origin warehouse.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='AT_DEST_GUDANG',"arrivedWarehouseId"=11 WHERE id=$1`, s["id"])
	apiCall(t, app, "GET", path+"/arrival-scans", "", worker, "", 403)
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, worker, "", 403)
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"transport"}`, worker, "", 403)
	shipmentSQL(t, db, `UPDATE "Employee" SET "warehouseId"=11 WHERE id=10`)
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, worker, "", 200)
	apiCall(t, app, "POST", path+"/arrive", `{"mode":"transport"}`, worker, "", 200)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"destination"}`, worker, "", 200)
	// Marketing scope still requires ownership even with receive permissions.
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW())`)
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, worker, "", 403)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdByPartnerId"=10 WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, worker, "", 200)
}

func TestArrivalTenantIsolationPostgres(t *testing.T) {
	a, other := disposableDB(t), disposableDB(t)
	for _, db := range []testDatabase{a, other} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH','Origin','Jakarta',NOW()); INSERT INTO "Customer" (id,code,name,"updatedAt") VALUES (10,'C','Sender',NOW()); INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"updatedAt") VALUES (10,'SHARED',10,'Jakarta','Bandung','PICKED_UP',NOW()); INSERT INTO "DetailShipment" ("masterId","detailCode",description,"updatedAt") VALUES (10,'SHARED-PACKAGE','Box',NOW())`)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		db := a
		if r.URL.Query().Get("KdCustomer") == "B" {
			db = other
		}
		u, err := url.Parse(db.dsn)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"Status": "Ok", "Data": map[string]any{"KdCustomer": r.URL.Query().Get("KdCustomer"), "NamaCustomer": db.name, "Server": u.Host, "Db": db.name, "Username": u.User.Username()}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	registry := tenant.New(tenant.Config{Mode: "production", LicenseURL: server.URL, LicenseKey: "test", LicenseToken: "test", LicenseApplication: "test", MaxPools: 2})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	tokenA, tokenB := loginAs(t, app, "owner", "A"), loginAs(t, app, "owner", "B")
	path := "/api/v1/shipments/10"
	apiCall(t, app, "POST", path+"/arrival-scan-all", `{}`, tokenA, "B", 401)
	apiCall(t, app, "POST", path+"/arrival-scans", `{"payload":"https://example.test/tracking-paket?corporateId=B&resi=SHARED-PACKAGE"}`, tokenA, "A", 422)
	apiCall(t, app, "POST", path+"/arrival-scans", `{"payload":"https://example.test/tracking-paket?corporateId=A&resi=SHARED-PACKAGE"}`, tokenA, "A", 200)
	p := apiCall(t, app, "GET", path+"/arrival-scans", "", tokenB, "B", 200)["data"].(map[string]any)["progress"].(map[string]any)
	if p["scanned"] != float64(0) {
		t.Fatal("cross-tenant scan leak", p)
	}
	apiCall(t, app, "POST", path+"/arrive", `{"warehouseId":10}`, tokenA, "A", 200)
	apiCall(t, app, "POST", path+"/undo-arrival", `{"reason":"tenant A reversal"}`, tokenA, "A", 200)
	var scans, events int
	var status string
	if err := other.pool.QueryRow(context.Background(), `SELECT status,(SELECT count(*) FROM "HandoverScan"),(SELECT count(*) FROM "TrackingEvent") FROM "MasterShipment" WHERE id=10`).Scan(&status, &scans, &events); err != nil || status != "PICKED_UP" || scans != 0 || events != 0 {
		t.Fatalf("cross-tenant writes: %s %d %d %v", status, scans, events, err)
	}
}
