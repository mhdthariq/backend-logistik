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

func seedDeliveryStaff(t *testing.T, db testDatabase) {
	t.Helper()
	seedPickupStaff(t, db)
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (110,'delivery.view','Delivery','view'),(111,'delivery.scan','Delivery','scan'),(112,'delivery.assign_kurir','Delivery','assign'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,110),(2,111)`)
}

func TestDeliveryCRUDPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedDeliveryStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/deliveries"}, {"POST", "/api/v1/deliveries"}, {"GET", "/api/v1/deliveries/99999"}, {"PUT", "/api/v1/deliveries/99999"}, {"DELETE", "/api/v1/deliveries/99999"}, {"POST", "/api/v1/deliveries/99999/scans"}} {
		apiCall(t, app, route.method, route.path, `{}`, "", "", 401)
		if route.method == "POST" && route.path == "/api/v1/deliveries" || route.method == "PUT" || route.method == "DELETE" {
			apiCall(t, app, route.method, route.path, `{}`, worker, "", 403)
		}
		if route.path != "/api/v1/deliveries" {
			apiCall(t, app, route.method, route.path, `{}`, owner, "", 404)
		}
	}
	for _, body := range []string{`null`, `{`, `{}`, `{"masterId":99999}`} {
		apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 422)
	}
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box"}]`)
	body := fmt.Sprintf(`{"masterId":%.0f,"kurirId":10,"notes":" deliver ","latitude":-6.2,"longitude":106.8}`, s["id"])
	for _, status := range []string{"CREATED", "AT_DEST_GUDANG", "ARRIVED_AT_GUDANG"} {
		shipmentSQL(t, db, `UPDATE "MasterShipment" SET status=$1 WHERE id=$2`, status, s["id"])
		apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 422)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"arrivedWarehouseId"=10 WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", "/api/v1/deliveries", fmt.Sprintf(`{"masterId":%.0f}`, s["id"]), owner, "", 422)
	apiCall(t, app, "POST", "/api/v1/deliveries", fmt.Sprintf(`{"masterId":%.0f,"kurirId":99999}`, s["id"]), owner, "", 422)
	d := apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 200)["data"].(map[string]any)
	if d["deliveryCode"] != "DLV-2026-000001" || d["status"] != "ASSIGNED" || d["notes"] != "deliver" || d["latitude"] != -6.2 {
		t.Fatal(d)
	}
	path := fmt.Sprintf("/api/v1/deliveries/%.0f", d["id"])
	shipmentSQL(t, db, `UPDATE "Delivery" SET "photoUrl"='data:image/jpeg;base64,test',"proofOfDelivery"='Receiver',"captureLatitude"=-6.3,"captureLongitude"=106.7,"captureAccuracy"=10,"locationLabel"='Secret',"timeZone"='Asia/Jakarta',"capturedAt"=NOW(),"mockLocation"=false WHERE id=$1`, d["id"])
	full := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if full["photoUrl"] != nil || full["proofOfDelivery"] != "Receiver" || full["latitude"] != -6.2 || full["progress"].(map[string]any)["total"] != float64(1) {
		t.Fatal(full)
	}
	for _, key := range []string{"captureLatitude", "captureLongitude", "captureAccuracy", "locationLabel", "timeZone", "capturedAt", "mockLocation"} {
		if _, exists := full[key]; exists {
			t.Fatalf("capture field %s leaked", key)
		}
	}
	full = apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	if full["photoUrl"] == nil || full["captureLatitude"] == nil {
		t.Fatal(full)
	}
	list := apiCall(t, app, "GET", "/api/v1/deliveries?search=dlv-2026-&status=ASSIGNED&fulfillmentMode=STANDARD", "", worker, "", 200)["data"].([]any)
	if len(list) != 1 {
		t.Fatal(list)
	}
	row := list[0].(map[string]any)
	if row["masterId"] != s["id"] || row["detailsCount"] != float64(1) || row["scannedCount"] != float64(0) || row["allScanned"] != false || row["originWarehouseName"] != "Origin" || row["address"] != "Jakarta" || row["customerPhone"] != "123" || row["photoUrl"] != nil {
		t.Fatal(row)
	}
	updated := apiCall(t, app, "PUT", path, `{"notes":"next","status":"COMPLETED","latitude":null,"kurirId":11}`, owner, "", 200)["data"].(map[string]any)
	if updated["status"] != "ASSIGNED" || updated["notes"] != "next" || updated["latitude"] != nil || updated["kurirId"] != float64(11) {
		t.Fatal(updated)
	}
	apiCall(t, app, "GET", path, "", worker, "", 403)
	apiCall(t, app, "POST", path+"/scans", `{"payload":"x"}`, worker, "", 403)
	if got := apiCall(t, app, "GET", "/api/v1/deliveries", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	// Destination receipt moves warehouse visibility, including supervisor access.
	shipmentSQL(t, db, `INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,112); UPDATE "MasterShipment" SET status='ARRIVED_AT_GUDANG',"arrivedWarehouseId"=11,"destReceivedAt"=NOW() WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", "/api/v1/deliveries", body, worker, "", 403)
	apiCall(t, app, "GET", path, "", worker, "", 403)
	apiCall(t, app, "PUT", path, `{"notes":"forged"}`, worker, "", 403)
	apiCall(t, app, "DELETE", path, `{}`, worker, "", 403)
	if got := apiCall(t, app, "GET", "/api/v1/deliveries?search=DLV", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 200)
	shipmentSQL(t, db, `UPDATE "Delivery" SET status='COMPLETED' WHERE id=$1`, d["id"])
	apiCall(t, app, "PUT", path, `{"notes":"x"}`, owner, "", 422)
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Delivery" SET status='FAILED' WHERE id=$1`, d["id"])
	apiCall(t, app, "PUT", path, `{"notes":"x"}`, owner, "", 422)
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 200)
	apiCall(t, app, "GET", path, "", owner, "", 404)
	var events, audits int
	if err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM "TrackingEvent" WHERE event='DELIVERY_ASSIGNED'),(SELECT count(*) FROM "AuditLog" WHERE "entityType"='delivery')`).Scan(&events, &audits); err != nil || events != 2 || audits != 4 {
		t.Fatal(events, audits, err)
	}
}

func TestDeliveryScanningPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedDeliveryStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"details":[{"description":"A"},{"description":"B"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	body := fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"])
	d := apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 200)["data"].(map[string]any)
	path := fmt.Sprintf("/api/v1/deliveries/%.0f", d["id"])
	scan := func(payload string) map[string]any {
		t.Helper()
		return apiCall(t, app, "POST", path+"/scans", fmt.Sprintf(`{"payload":%q,"method":"SCANNED"}`, payload), worker, "", 200)["data"].(map[string]any)
	}
	for _, body := range []string{`null`, `{`, `{}`, `{"payload":" "}`, `{"payload":"https://example.test/tracking-paket?corporateId=OTHER&resi=x"}`} {
		apiCall(t, app, "POST", path+"/scans", body, worker, "", 422)
	}
	unknown := scan("unknown")
	if unknown["scan"].(map[string]any)["result"] != "unexpected" || unknown["progress"].(map[string]any)["scanned"] != float64(0) {
		t.Fatal(unknown)
	}
	master := scan(d["deliveryCode"].(string))
	progress := master["progress"].(map[string]any)
	if progress["masterScanned"] != true || progress["allScanned"] != false {
		t.Fatal(master)
	}
	details := progress["details"].([]any)
	code := details[0].(map[string]any)["detailCode"].(string)
	first := scan("https://example.test/tracking-paket?corporateId=LOCAL&resi=" + code)
	if first["progress"].(map[string]any)["scanned"] != float64(1) || first["message"] != "Paket OK (1/2) · scan." {
		t.Fatal(first)
	}
	duplicate := scan(code)
	if duplicate["scan"].(map[string]any)["result"] != "duplicate" || duplicate["message"] != "Paket ini sudah pernah discan." {
		t.Fatal(duplicate)
	}
	last := scan(details[1].(map[string]any)["detailCode"].(string))
	if last["progress"].(map[string]any)["allScanned"] != true || last["message"] != "Semua paket sudah discan - silakan konfirmasi serah terima ke customer." {
		t.Fatal(last)
	}
	list := apiCall(t, app, "GET", "/api/v1/deliveries", "", worker, "", 200)["data"].([]any)
	if row := list[0].(map[string]any); row["scannedCount"] != float64(2) || row["allScanned"] != true {
		t.Fatal(row)
	}
	full := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if full["status"] != "ASSIGNED" || full["master"].(map[string]any)["status"] != "RECEIVED_AT_GUDANG" {
		t.Fatal("scan changed custody", full)
	}
	for _, raw := range full["scans"].([]any) {
		by := raw.(map[string]any)["scannedBy"].(map[string]any)
		if len(by) != 2 || by["name"] != "Worker" {
			t.Fatal("scan user leaked account fields", by)
		}
	}
	meta := full["progress"].(map[string]any)["details"].([]any)[0].(map[string]any)
	if meta["scannedByName"] != "Worker" || meta["scanMethod"] != "SCANNED" {
		t.Fatal(meta)
	}
	// Another delivery on the same master must not inherit these scans.
	d2 := apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 200)["data"].(map[string]any)
	other := apiCall(t, app, "GET", fmt.Sprintf("/api/v1/deliveries/%.0f", d2["id"]), "", worker, "", 200)["data"].(map[string]any)
	if other["progress"].(map[string]any)["scanned"] != float64(0) {
		t.Fatal(other)
	}
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b' WHERE id=$1; DELETE FROM "HandoverScan" WHERE "deliveryId"=$2`, cid, d["id"])
	b2b := scan(s["masterCode"].(string))["progress"].(map[string]any)
	if b2b["isB2B"] != true || b2b["allScanned"] != true || b2b["scanned"] != float64(2) {
		t.Fatal(b2b)
	}
	// List counts deliberately reflect explicit package scans, as in Next.js.
	list = apiCall(t, app, "GET", "/api/v1/deliveries", "", worker, "", 200)["data"].([]any)
	for _, row := range list {
		if row.(map[string]any)["scannedCount"] != float64(0) || row.(map[string]any)["allScanned"] != false {
			t.Fatal(row)
		}
	}
	for _, status := range []string{"COMPLETED", "FAILED"} {
		shipmentSQL(t, db, `UPDATE "Delivery" SET status=$1 WHERE id=$2`, status, d["id"])
		apiCall(t, app, "POST", path+"/scans", `{"payload":"x"}`, worker, "", 422)
	}
	shipmentSQL(t, db, `UPDATE "Delivery" SET status='ASSIGNED',"kurirId"=NULL WHERE id=$1`, d["id"])
	apiCall(t, app, "POST", path+"/scans", `{"payload":"x"}`, worker, "", 403)
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=NULL WHERE id=2`)
	if rows := apiCall(t, app, "GET", "/api/v1/deliveries", "", worker, "", 200)["data"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
}

func TestDeliveryRollbackAndConcurrentScansPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedDeliveryStaff(t, db)
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1; CREATE FUNCTION fail_delivery_tracking() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_delivery_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_delivery_tracking()`, s["id"])
	body := fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"])
	apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 500)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Delivery"`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_delivery_tracking ON "TrackingEvent"`)
	d := apiCall(t, app, "POST", "/api/v1/deliveries", body, owner, "", 200)["data"].(map[string]any)
	path := fmt.Sprintf("/api/v1/deliveries/%.0f", d["id"])
	full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
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
	shipmentSQL(t, db, `CREATE FUNCTION fail_delivery_scan() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_delivery_scan BEFORE INSERT ON "HandoverScan" FOR EACH ROW EXECUTE FUNCTION fail_delivery_scan()`)
	apiCall(t, app, "POST", path+"/scans", fmt.Sprintf(`{"payload":%q}`, code), owner, "", 500)
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "HandoverScan" WHERE "deliveryId"=$1`, d["id"]).Scan(&count); err != nil || count != 8 {
		t.Fatal(count, err)
	}
	shipmentSQL(t, db, `CREATE FUNCTION fail_delivery_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_delivery_delete BEFORE DELETE ON "Delivery" FOR EACH ROW EXECUTE FUNCTION fail_delivery_delete()`)
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 500)
	apiCall(t, app, "GET", path, "", owner, "", 200)
	shipmentSQL(t, db, `DROP TRIGGER fail_delivery_delete ON "Delivery"`)
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 200)
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "HandoverScan" WHERE "deliveryId"=$1`, d["id"]).Scan(&count); err != nil || count != 0 {
		t.Fatal("delete did not cascade scans", count, err)
	}
}

func TestDeliveryDirectCrewPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedDeliveryStaff(t, db)
	standard := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	direct := newShipment(t, app, owner, cid, `,"fulfillmentMode":"DIRECT","originWarehouseId":10`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG';
	 INSERT INTO "Role" (id,slug,name) VALUES (10,'driver','Driver'); INSERT INTO "UserRole" ("userId","roleId") VALUES (2,10);
	 INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'DELIVERY-TEST',1000,10,NOW());
	 INSERT INTO "Route" (id,name,"updatedAt") VALUES (10,'Delivery route',NOW());
	 INSERT INTO "Checkpoint" (id,"routeId",name,sequence,latitude,longitude,"updatedAt") VALUES (10,10,'First',1,0,0,NOW()),(11,10,'Last',2,1,1,NOW());
	 INSERT INTO "Transport" (id,"transportCode","vehicleId","routeId","updatedAt") VALUES (10,'T-DELIVERY',10,10,NOW());
	 INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,$1)`, direct["id"])
	for _, s := range []map[string]any{standard, direct} {
		apiCall(t, app, "POST", "/api/v1/deliveries", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"]), owner, "", 200)
	}
	worker := loginAs(t, app, "worker", "")
	list := apiCall(t, app, "GET", "/api/v1/deliveries?fulfillmentMode=STANDARD", "", worker, "", 200)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["fulfillmentMode"] != "DIRECT" || list[0].(map[string]any)["pickedUpFromCheckpoint1"] != false {
		t.Fatal(list)
	}
	shipmentSQL(t, db, `INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius") VALUES (10,11,1,1,true)`)
	list = apiCall(t, app, "GET", "/api/v1/deliveries", "", worker, "", 200)["data"].([]any)
	if list[0].(map[string]any)["pickedUpFromCheckpoint1"] != false {
		t.Fatal(list)
	}
	shipmentSQL(t, db, `INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius") VALUES (10,10,0,0,true)`)
	list = apiCall(t, app, "GET", "/api/v1/deliveries?fulfillmentMode=DIRECT", "", worker, "", 200)["data"].([]any)
	if list[0].(map[string]any)["pickedUpFromCheckpoint1"] != true {
		t.Fatal(list)
	}
	shipmentSQL(t, db, `UPDATE "Delivery" SET "deliveryCode"='DLV-2026-000003' WHERE "masterId"=$1`, direct["id"])
	d := apiCall(t, app, "POST", "/api/v1/deliveries", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, standard["id"]), owner, "", 200)["data"].(map[string]any)
	if d["deliveryCode"] != "DLV-2026-000002" {
		t.Fatal(d)
	}
}

func TestOptionsRouteCheckpointsPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Route" (id,name,"isActive","updatedAt") VALUES (10,'A',true,NOW()),(11,'A',true,NOW()),(12,'Hidden',false,NOW()); INSERT INTO "Checkpoint" ("routeId",name,sequence,latitude,longitude,"radiusMeters","isActive","updatedAt") VALUES (10,'Last',2,1,2,500,true,NOW()),(10,'First',1,3,4,600,true,NOW()),(10,'Hidden',3,5,6,700,false,NOW())`)
	options := apiCall(t, app, "GET", "/api/v1/options", "", owner, "", 200)["data"].(map[string]any)
	routes := options["routes"].([]any)
	if len(routes) != 2 || routes[0].(map[string]any)["id"] != float64(10) || len(routes[1].(map[string]any)["checkpoints"].([]any)) != 0 {
		t.Fatal(routes)
	}
	checkpoints := routes[0].(map[string]any)["checkpoints"].([]any)
	if len(checkpoints) != 2 || checkpoints[0].(map[string]any)["name"] != "First" || checkpoints[0].(map[string]any)["radiusMeters"] != float64(600) || len(checkpoints[0].(map[string]any)) != 5 {
		t.Fatal(checkpoints)
	}
}
func TestDeliveryTenantIsolationPostgres(t *testing.T) {
	a, other := disposableDB(t), disposableDB(t)
	for _, db := range []testDatabase{a, other} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Employee" (id,"employeeNumber",name,"updatedAt") VALUES (10,'COURIER','Courier',NOW()); INSERT INTO "Customer" (id,code,name,"updatedAt") VALUES (10,'C','Sender',NOW()); INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"priceAmount","updatedAt") VALUES (10,'SHARED',10,'Jakarta','Bandung','RECEIVED_AT_GUDANG',100,NOW()); INSERT INTO "DetailShipment" ("masterId","detailCode",description,"updatedAt") VALUES (10,'SHARED-PACKAGE','Box',NOW()); INSERT INTO "Delivery" (id,"deliveryCode","masterId","kurirId","updatedAt") VALUES (10,'DLV-SHARED',10,10,NOW())`)
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
	apiCall(t, app, "GET", "/api/v1/deliveries/10", "", tokenA, "B", 401)
	apiCall(t, app, "POST", "/api/v1/deliveries/10/scans", `{"payload":"https://example.test/tracking-paket?corporateId=B&resi=SHARED-PACKAGE"}`, tokenA, "A", 422)
	apiCall(t, app, "POST", "/api/v1/deliveries/10/scans", `{"payload":"SHARED-PACKAGE"}`, tokenA, "A", 200)
	full := apiCall(t, app, "GET", "/api/v1/deliveries/10", "", tokenB, "B", 200)["data"].(map[string]any)
	if full["progress"].(map[string]any)["scanned"] != float64(0) {
		t.Fatal(full)
	}
	apiCall(t, app, "PUT", "/api/v1/deliveries/10", `{"notes":"Tenant A only"}`, tokenA, "A", 200)
	apiCall(t, app, "DELETE", "/api/v1/deliveries/10", `{}`, tokenA, "A", 200)
	var notes *string
	var scans, events int
	if err := other.pool.QueryRow(t.Context(), `SELECT notes,(SELECT count(*) FROM "HandoverScan"),(SELECT count(*) FROM "TrackingEvent") FROM "Delivery" WHERE id=10`).Scan(&notes, &scans, &events); err != nil || notes != nil || scans != 0 || events != 0 {
		t.Fatal(notes, scans, events, err)
	}
}
