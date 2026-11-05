package httpapi_test

import (
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

func seedPickupStaff(t *testing.T, db testDatabase) {
	t.Helper()
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH-P','Origin','Jakarta',NOW()),(11,'WH-D','Destination','Bandung',NOW());
	 INSERT INTO "Employee" (id,"employeeNumber",name,"warehouseId","updatedAt") VALUES (10,'EMP-P','Courier',10,NOW()),(11,'EMP-OTHER','Other courier',11,NOW());
	 UPDATE "User" SET "employeeId"=10 WHERE id=2;
	 INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'pickup.view','Pickup','view'),(101,'pickup.scan','Pickup','scan'),(102,'pickup.assign_kurir','Pickup','assign'),(103,'pickup.create','Pickup','create'),(104,'proof_photo.view','Proof','view');
	 INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100),(2,101)`)
}

func TestPickupCRUDPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/pickups"}, {"POST", "/api/v1/pickups"}, {"GET", "/api/v1/pickups/99999"}, {"PUT", "/api/v1/pickups/99999"}, {"DELETE", "/api/v1/pickups/99999"}, {"POST", "/api/v1/pickups/99999/scans"}} {
		apiCall(t, app, route.method, route.path, `{}`, "", "", 401)
		if route.method == "POST" && route.path == "/api/v1/pickups" || route.method == "PUT" || route.method == "DELETE" {
			apiCall(t, app, route.method, route.path, `{}`, worker, "", 403)
		} else if route.path != "/api/v1/pickups" {
			apiCall(t, app, route.method, route.path, `{}`, owner, "", 404)
		}
	}
	for _, body := range []string{`null`, `{`, `{}`, `{"masterId":999999}`} {
		apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 422)
	}
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box"}]`)
	body := fmt.Sprintf(`{"masterId":%.0f,"kurirId":10,"notes":" collect ","latitude":-6.2,"longitude":106.8}`, s["id"])
	apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='READY_FOR_PICKUP' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "priceAmount"=100,"discountAmount"=20,"finalPriceAmount"=80 WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", "/api/v1/pickups", fmt.Sprintf(`{"masterId":%.0f}`, s["id"]), owner, "", 422)
	apiCall(t, app, "POST", "/api/v1/pickups", fmt.Sprintf(`{"masterId":%.0f,"kurirId":99999}`, s["id"]), owner, "", 422)
	p := apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 200)["data"].(map[string]any)
	if p["pickupCode"] != "PICK-2026-000001" || p["status"] != "ASSIGNED" || p["notes"] != "collect" || p["latitude"] != -6.2 {
		t.Fatal(p)
	}
	path := fmt.Sprintf("/api/v1/pickups/%.0f", p["id"])
	// Known proof fields and device metadata are visible only with permission.
	shipmentSQL(t, db, `UPDATE "Pickup" SET "photoUrl"='data:image/jpeg;base64,test',"captureLatitude"=-6.3,"captureLongitude"=106.7,"captureAccuracy"=10,"locationLabel"='Secret',"timeZone"='Asia/Jakarta',"capturedAt"=NOW(),"mockLocation"=false WHERE id=$1`, p["id"])
	detail := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if detail["photoUrl"] != nil || detail["latitude"] != -6.2 || detail["progress"].(map[string]any)["total"] != float64(1) || detail["paymentSummary"].(map[string]any)["remainingAmount"] != float64(80) {
		t.Fatal(detail)
	}
	for _, key := range []string{"captureLatitude", "captureLongitude", "captureAccuracy", "locationLabel", "timeZone", "capturedAt", "mockLocation"} {
		if _, exists := detail[key]; exists {
			t.Fatalf("capture field %s leaked", key)
		}
	}
	full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	if full["photoUrl"] == nil || full["captureLatitude"] == nil {
		t.Fatal(full)
	}
	list := apiCall(t, app, "GET", "/api/v1/pickups?search=pick-2026-&status=ASSIGNED", "", worker, "", 200)["data"].([]any)
	if len(list) != 1 {
		t.Fatal(list)
	}
	row := list[0].(map[string]any)
	if row["masterId"] != s["id"] || row["detailsCount"] != float64(1) || row["scannedCount"] != float64(0) || row["photoUrl"] != nil || row["pickedUpFromCheckpoint1"] != false || row["pickupSenderName"] != "Sender" {
		t.Fatal(row)
	}
	apiCall(t, app, "PUT", path, `{"kurirId":99999}`, owner, "", 422)
	updated := apiCall(t, app, "PUT", path, `{"notes":"next","status":"COMPLETED","latitude":null}`, owner, "", 200)["data"].(map[string]any)
	if updated["status"] != "ASSIGNED" || updated["notes"] != "next" || updated["latitude"] != nil {
		t.Fatal(updated)
	}
	apiCall(t, app, "PUT", path, `{"status":"IN_PROGRESS","kurirId":11}`, owner, "", 200)
	apiCall(t, app, "GET", path, "", worker, "", 403)
	apiCall(t, app, "POST", path+"/scans", `{"payload":"unknown"}`, worker, "", 403)
	if got := apiCall(t, app, "GET", "/api/v1/pickups", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	shipmentSQL(t, db, `UPDATE "Pickup" SET status='PICKED_UP' WHERE id=$1`, p["id"])
	apiCall(t, app, "PUT", path, `{"status":"ASSIGNED"}`, owner, "", 422)
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Pickup" SET status='COMPLETED' WHERE id=$1`, p["id"])
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Pickup" SET status='ASSIGNED' WHERE id=$1; UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$2`, p["id"], s["id"])
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 200)
	apiCall(t, app, "PUT", path, `{"status":"ASSIGNED"}`, owner, "", 422)
	full = apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	if full["status"] != "CANCELLED" || full["master"].(map[string]any)["status"] != "READY_FOR_PICKUP" {
		t.Fatal(full)
	}
	var events, audits int
	if err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM "TrackingEvent" WHERE event IN ('PICKUP_ASSIGNED','PICKUP_CANCELLED')),(SELECT count(*) FROM "AuditLog" WHERE "entityType"='pickup')`).Scan(&events, &audits); err != nil || events != 2 || audits < 4 {
		t.Fatal(events, audits, err)
	}
}

func TestPickupScanningPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"details":[{"description":"A"},{"description":"B"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='READY_FOR_PICKUP',"priceAmount"=100 WHERE id=$1`, s["id"])
	p := apiCall(t, app, "POST", "/api/v1/pickups", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"]), owner, "", 200)["data"].(map[string]any)
	path := fmt.Sprintf("/api/v1/pickups/%.0f", p["id"])
	scan := func(payload string) map[string]any {
		t.Helper()
		return apiCall(t, app, "POST", path+"/scans", fmt.Sprintf(`{"payload":%q,"method":"SCANNED"}`, payload), worker, "", 200)["data"].(map[string]any)
	}
	for _, body := range []string{`null`, `{`, `{}`, `{"payload":" "}`, `{"payload":"https://example.test/tracking-paket?corporateId=OTHER&resi=x"}`} {
		apiCall(t, app, "POST", path+"/scans", body, worker, "", 422)
	}
	unknown := scan("not-this-shipment")
	if unknown["scan"].(map[string]any)["result"] != "unexpected" || unknown["progress"].(map[string]any)["scanned"] != float64(0) {
		t.Fatal(unknown)
	}
	master := scan(p["pickupCode"].(string))
	if master["progress"].(map[string]any)["allScanned"] != false || master["progress"].(map[string]any)["masterScanned"] != true {
		t.Fatal(master)
	}
	progress := master["progress"].(map[string]any)
	code := progress["details"].([]any)[0].(map[string]any)["detailCode"].(string)
	first := scan("https://example.test/tracking-paket?corporateId=LOCAL&resi=" + code)
	if first["scan"].(map[string]any)["result"] != "ok" || first["progress"].(map[string]any)["scanned"] != float64(1) {
		t.Fatal(first)
	}
	duplicate := scan(code)
	if duplicate["scan"].(map[string]any)["result"] != "duplicate" || duplicate["message"] != "Paket ini sudah pernah discan." {
		t.Fatal(duplicate)
	}
	details := duplicate["progress"].(map[string]any)["details"].([]any)
	if details[0].(map[string]any)["scannedByName"] != "Worker" || details[0].(map[string]any)["scanMethod"] != "SCANNED" {
		t.Fatal(details)
	}
	last := scan(details[1].(map[string]any)["detailCode"].(string))
	if last["progress"].(map[string]any)["allScanned"] != true {
		t.Fatal(last)
	}
	full := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if full["status"] != "ASSIGNED" || full["master"].(map[string]any)["status"] != "READY_FOR_PICKUP" {
		t.Fatal("scanning advanced custody state", full)
	}
	for _, raw := range full["scans"].([]any) {
		by := raw.(map[string]any)["scannedBy"].(map[string]any)
		if by["passwordHash"] != nil || by["name"] != "Worker" {
			t.Fatal(by)
		}
	}
	// Arrival scans are independent of pickup scans.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1`, s["id"])
	arrival := apiCall(t, app, "GET", fmt.Sprintf("/api/v1/shipments/%.0f/arrival-scans", s["id"]), "", owner, "", 200)["data"].(map[string]any)["progress"].(map[string]any)
	if arrival["scanned"] != float64(0) {
		t.Fatal(arrival)
	}
	// A B2B master read covers all packages; progress remains scoped to this task.
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b' WHERE id=$1; DELETE FROM "HandoverScan" WHERE "pickupId"=$2`, cid, p["id"])
	b2b := scan(s["masterCode"].(string))["progress"].(map[string]any)
	if b2b["isB2B"] != true || b2b["allScanned"] != true || b2b["scanned"] != float64(2) {
		t.Fatal(b2b)
	}
	for _, status := range []string{"COMPLETED", "CANCELLED"} {
		shipmentSQL(t, db, `UPDATE "Pickup" SET status=$1 WHERE id=$2`, status, p["id"])
		apiCall(t, app, "POST", path+"/scans", `{"payload":"x"}`, worker, "", 422)
	}
}

func TestPickupScopeAndGatesPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	shipmentSQL(t, db, `INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,102),(2,103)`)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='READY_FOR_PICKUP',"priceAmount"=100 WHERE id=$1; UPDATE "Customer" SET type='b2b' WHERE id=$2`, s["id"], cid)
	body := fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"])
	apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 422)
	shipmentSQL(t, db, `INSERT INTO "Invoice" (id,"invoiceNumber","customerId","updatedAt") VALUES (10,'INV-PICKUP',$1,NOW()); INSERT INTO "InvoiceLine" ("invoiceId","shipmentId",description,"unitPrice") VALUES (10,$2,'Pickup',100)`, cid, s["id"])
	p := apiCall(t, app, "POST", "/api/v1/pickups", body, worker, "", 200)["data"].(map[string]any)
	path := fmt.Sprintf("/api/v1/pickups/%.0f", p["id"])
	shipmentSQL(t, db, `UPDATE "Employee" SET "warehouseId"=11 WHERE id=10`)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		apiCall(t, app, method, path, `{}`, worker, "", 403)
	}
	apiCall(t, app, "POST", path+"/scans", `{"payload":"x"}`, worker, "", 403)
	apiCall(t, app, "POST", "/api/v1/pickups", body, worker, "", 403)
	if got := apiCall(t, app, "GET", "/api/v1/pickups", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	shipmentSQL(t, db, `UPDATE "Employee" SET "warehouseId"=10 WHERE id=10; UPDATE "MasterShipment" SET status='DELIVERED',"arrivedWarehouseId"=11 WHERE id=$1`, s["id"])
	// Historical pickup list stays with the origin, rather than following the parcel.
	if got := apiCall(t, app, "GET", "/api/v1/pickups", "", worker, "", 200)["data"].([]any); len(got) != 1 {
		t.Fatal(got)
	}
	// A courier account without an employee must never fall back to all tasks.
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=NULL WHERE id=2; DELETE FROM "UserPermission" WHERE "userId"=2 AND "permissionId" IN (102,103)`)
	if got := apiCall(t, app, "GET", "/api/v1/pickups", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPickupRollbackAndConcurrencyPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='READY_FOR_PICKUP',"priceAmount"=100 WHERE id=$1;
	 CREATE FUNCTION fail_pickup_tracking() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event IN ('PICKUP_ASSIGNED','PICKUP_CANCELLED') THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER fail_pickup_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_pickup_tracking()`, s["id"])
	body := fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"])
	apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 500)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Pickup"`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_pickup_tracking ON "TrackingEvent"`)
	p := apiCall(t, app, "POST", "/api/v1/pickups", body, owner, "", 200)["data"].(map[string]any)
	path := fmt.Sprintf("/api/v1/pickups/%.0f", p["id"])
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1; CREATE TRIGGER fail_pickup_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_pickup_tracking()`, s["id"])
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 500)
	full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	if full["status"] != "ASSIGNED" || full["master"].(map[string]any)["status"] != "PICKED_UP" {
		t.Fatal(full)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_pickup_tracking ON "TrackingEvent"`)
	code := full["progress"].(map[string]any)["details"].([]any)[0].(map[string]any)["detailCode"]
	results := make(chan string, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			req := httptest.NewRequest("POST", path+"/scans", strings.NewReader(fmt.Sprintf(`{"payload":%q}`, code)))
			req.Header.Set("Authorization", "Bearer "+owner)
			req.Header.Set("Content-Type", "application/json")
			response, err := app.Test(req)
			if err != nil {
				results <- err.Error()
				return
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var out map[string]any
			if err := json.NewDecoder(response.Body).Decode(&out); err != nil || response.StatusCode != 200 {
				results <- fmt.Sprint(response.StatusCode, err, out)
				return
			}
			results <- out["data"].(map[string]any)["scan"].(map[string]any)["result"].(string)
		})
	}
	wg.Wait()
	close(results)
	ok, duplicate := 0, 0
	for result := range results {
		switch result {
		case "ok":
			ok++
		case "duplicate":
			duplicate++
		default:
			t.Fatal(result)
		}
	}
	if ok != 1 || duplicate != 7 {
		t.Fatal(ok, duplicate)
	}
	shipmentSQL(t, db, `CREATE FUNCTION fail_pickup_scan() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected scan failure'; END $$; CREATE TRIGGER fail_pickup_scan BEFORE INSERT ON "HandoverScan" FOR EACH ROW EXECUTE FUNCTION fail_pickup_scan()`)
	apiCall(t, app, "POST", path+"/scans", fmt.Sprintf(`{"payload":%q}`, code), owner, "", 500)
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "HandoverScan" WHERE "pickupId"=$1`, p["id"]).Scan(&count); err != nil || count != 8 {
		t.Fatal(count, err)
	}
}

func TestPickupDirectCrewPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	standard := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	direct := newShipment(t, app, owner, cid, `,"fulfillmentMode":"DIRECT","originWarehouseId":10`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='READY_FOR_PICKUP',"priceAmount"=100;
	 INSERT INTO "Role" (id,slug,name) VALUES (10,'driver','Driver'); INSERT INTO "UserRole" ("userId","roleId") VALUES (2,10);
	 INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'PICKUP-TEST',1000,10,NOW());
	 INSERT INTO "Route" (id,name,"updatedAt") VALUES (10,'Pickup route',NOW());
	 INSERT INTO "Checkpoint" (id,"routeId",name,sequence,latitude,longitude,"updatedAt") VALUES (10,10,'First',1,0,0,NOW()),(11,10,'Last',2,1,1,NOW());
	 INSERT INTO "Transport" (id,"transportCode","vehicleId","routeId","updatedAt") VALUES (10,'T-PICK',10,10,NOW());
	 INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,$1)`, direct["id"])
	for _, s := range []map[string]any{standard, direct} {
		apiCall(t, app, "POST", "/api/v1/pickups", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"]), owner, "", 200)
	}
	worker := loginAs(t, app, "worker", "")
	list := apiCall(t, app, "GET", "/api/v1/pickups?fulfillmentMode=STANDARD", "", worker, "", 200)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["fulfillmentMode"] != "DIRECT" || list[0].(map[string]any)["pickedUpFromCheckpoint1"] != false {
		t.Fatal(list)
	}
	// A later checkpoint is not sufficient for the first-checkpoint badge.
	shipmentSQL(t, db, `INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius") VALUES (10,11,1,1,true)`)
	list = apiCall(t, app, "GET", "/api/v1/pickups", "", worker, "", 200)["data"].([]any)
	if list[0].(map[string]any)["pickedUpFromCheckpoint1"] != false {
		t.Fatal(list)
	}
	shipmentSQL(t, db, `INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius") VALUES (10,10,0,0,true)`)
	list = apiCall(t, app, "GET", "/api/v1/pickups?fulfillmentMode=DIRECT", "", worker, "", 200)["data"].([]any)
	if list[0].(map[string]any)["pickedUpFromCheckpoint1"] != true {
		t.Fatal(list)
	}
	// Preserve the first-gap numbering rule even when legacy codes are sparse.
	shipmentSQL(t, db, `UPDATE "Pickup" SET "pickupCode"='PICK-2026-000003' WHERE "masterId"=$1`, direct["id"])
	p := apiCall(t, app, "POST", "/api/v1/pickups", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, standard["id"]), owner, "", 200)["data"].(map[string]any)
	if p["pickupCode"] != "PICK-2026-000002" {
		t.Fatal(p)
	}
}

func TestPickupTenantIsolationPostgres(t *testing.T) {
	a, other := disposableDB(t), disposableDB(t)
	for _, db := range []testDatabase{a, other} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Employee" (id,"employeeNumber",name,"updatedAt") VALUES (10,'COURIER','Courier',NOW()); INSERT INTO "Customer" (id,code,name,"updatedAt") VALUES (10,'C','Sender',NOW()); INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"priceAmount","updatedAt") VALUES (10,'SHARED',10,'Jakarta','Bandung','READY_FOR_PICKUP',100,NOW()); INSERT INTO "DetailShipment" ("masterId","detailCode",description,"updatedAt") VALUES (10,'SHARED-PACKAGE','Box',NOW()); INSERT INTO "Pickup" (id,"pickupCode","masterId","kurirId","updatedAt") VALUES (10,'PICK-SHARED',10,10,NOW())`)
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
	apiCall(t, app, "GET", "/api/v1/pickups/10", "", tokenA, "B", 401)
	apiCall(t, app, "POST", "/api/v1/pickups/10/scans", `{"payload":"https://example.test/tracking-paket?corporateId=B&resi=SHARED-PACKAGE"}`, tokenA, "A", 422)
	apiCall(t, app, "POST", "/api/v1/pickups/10/scans", `{"payload":"SHARED-PACKAGE"}`, tokenA, "A", 200)
	full := apiCall(t, app, "GET", "/api/v1/pickups/10", "", tokenB, "B", 200)["data"].(map[string]any)
	if full["progress"].(map[string]any)["scanned"] != float64(0) {
		t.Fatal(full)
	}
	apiCall(t, app, "PUT", "/api/v1/pickups/10", `{"notes":"Tenant A only"}`, tokenA, "A", 200)
	apiCall(t, app, "DELETE", "/api/v1/pickups/10", `{}`, tokenA, "A", 200)
	var status string
	var scans, events int
	if err := other.pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM "HandoverScan"),(SELECT count(*) FROM "TrackingEvent") FROM "Pickup" WHERE id=10`).Scan(&status, &scans, &events); err != nil || status != "ASSIGNED" || scans != 0 || events != 0 {
		t.Fatal(status, scans, events, err)
	}
}
