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

func TestFleetCapacityPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", "", "", 401)
	apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", worker, "", 403)
	empty := apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", owner, "", 200)["data"].(map[string]any)
	if len(empty["vehicles"].([]any)) != 0 || empty["summary"].(map[string]any)["total"] != float64(0) {
		t.Fatal(empty)
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'vehicle.view','Vehicle','view'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100);
	 INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'VEHICLE_OWNER',NOW());
	 INSERT INTO "Vehicle" (id,"vehicleNumber",status,"maxWeightKg","maxVolumeM3","maxKoli","ownerId","updatedAt") VALUES
	 (10,'A-IDLE','ACTIVE',1000,10,100,NULL,NOW()),(11,'B-WARN','ACTIVE',100,10,10,10,NOW()),
	 (12,'C-OVER','MAINTENANCE',100,10,10,NULL,NOW()),(13,'D-PARTIAL','ACTIVE',1000,10,NULL,NULL,NOW()),
	 (14,'E-UNCONFIGURED','ACTIVE',0,0,NULL,NULL,NOW()),(15,'F-OK','ACTIVE',100,10,10,NULL,NOW()),(16,'G-ZERO','INACTIVE',0,0,0,NULL,NOW());
	 INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,"chargeableWeightKg","updatedAt") VALUES (10,'F-WARN',$1,'A','B',9999,NOW()),(11,'F-OVER',$1,'A','B',1,NOW()),(12,'F-OK',$1,'A','B',9999,NOW());
	 INSERT INTO "DetailShipment" ("masterId","detailCode",description,"actualWeightKg","lengthCm","widthCm","heightCm","volumeM3","updatedAt") VALUES
	 (10,'F-P1','Weight',45.004,100,100,100,99,NOW()),(11,'F-P2','Over',110,100,100,100,NULL,NOW()),(12,'F-P3','Okay',85,100,100,100,NULL,NOW());
	 INSERT INTO "Transport" (id,"transportCode","vehicleId",status,"updatedAt") VALUES
	 (10,'F-T1',11,'PLANNED',NOW()),(11,'F-T2',11,'DEPARTED',NOW()),(12,'F-T3',12,'PLANNED',NOW()),(13,'F-T4',13,'PLANNED',NOW()),(14,'F-T5',14,'PLANNED',NOW()),(15,'F-T6',15,'PLANNED',NOW()),
	 (16,'F-COMPLETE',10,'COMPLETED',NOW()),(17,'F-ARRIVED',10,'ARRIVED',NOW()),(18,'F-CANCEL',10,'CANCELLED',NOW());
	 INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,10),(11,10),(12,11),(15,12),(16,11),(17,11),(18,11)`, cid)
	response := apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", worker, "", 200)["data"].(map[string]any)
	summary := response["summary"].(map[string]any)
	for key, want := range map[string]float64{"total": 7, "active": 5, "idle": 2, "overCapacity": 1, "warning": 1, "partiallyConfigured": 1, "ok": 1, "unconfiguredVehicles": 2} {
		if summary[key] != want {
			t.Fatalf("%s: %v", key, summary)
		}
	}
	rows := response["vehicles"].([]any)
	if rows[0].(map[string]any)["id"] != float64(10) || rows[len(rows)-1].(map[string]any)["id"] != float64(12) {
		t.Fatal("status/vehicle number ordering", rows)
	}
	idle := gudangRow(t, rows, 10)
	if idle["activeLoad"] != nil || idle["activeCapacity"] != nil || idle["activeTransportCount"] != float64(0) || idle["ownerName"] != nil {
		t.Fatal(idle)
	}
	warning := gudangRow(t, rows, 11)
	load := warning["activeLoad"].(map[string]any)
	if warning["ownerName"] != "Worker" || warning["activeTransportCount"] != float64(2) || load["totalActualWeightKg"] != float64(90) || load["totalVolumeM3"] != float64(2) || load["totalKoli"] != float64(2) {
		t.Fatal(warning)
	}
	for _, row := range rows {
		if len(row.(map[string]any)) != 15 {
			t.Fatal("unexpected vehicle fields", row)
		}
	}
	for id, want := range map[float64]string{11: "WARNING", 12: "OVERLIMIT", 13: "PARTIALLY_CONFIGURED", 14: "UNCONFIGURED", 15: "OK"} {
		if row := gudangRow(t, rows, id); row["activeCapacity"].(map[string]any)["overallStatus"] != want {
			t.Fatal(row)
		}
	}
	unconfigured := gudangRow(t, rows, 14)["activeCapacity"].(map[string]any)["weight"].(map[string]any)
	for _, key := range []string{"maximum", "utilizationPercent", "remaining", "overBy"} {
		if value, exists := unconfigured[key]; !exists || value != nil {
			t.Fatal(key, unconfigured)
		}
	}
	apiCall(t, app, "PUT", "/api/v1/settings", `{"warningThresholdPct":80}`, owner, "", 200)
	response = apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", owner, "", 200)["data"].(map[string]any)
	if gudangRow(t, response["vehicles"], 15)["activeCapacity"].(map[string]any)["overallStatus"] != "WARNING" {
		t.Fatal(response)
	}
}

func TestBulkConfigureKoliPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	path := "/api/v1/fleet-capacity/bulk-configure-koli"
	apiCall(t, app, "POST", path, `{}`, "", "", 401)
	apiCall(t, app, "POST", path, `{}`, worker, "", 403)
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'vehicle.update','Vehicle','update'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100)`)
	apiCall(t, app, "POST", path, `{}`, worker, "", 403)
	for id, weight := range []float64{1500, 1500.1, 3000, 3000.1, 6000, 6000.1, 1, 1} {
		shipmentSQL(t, db, `INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES ($1,$2,$3,1,NOW())`, id+10, fmt.Sprintf("BULK-%d", id), weight)
	}
	shipmentSQL(t, db, `UPDATE "Vehicle" SET "maxKoli"=7 WHERE id=17; UPDATE "Vehicle" SET status='INACTIVE' WHERE id=15`)
	// A failure halfway through rolls back every vehicle and schedules no audits.
	shipmentSQL(t, db, `CREATE FUNCTION fail_bulk_koli() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=13 THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_bulk_koli BEFORE UPDATE ON "Vehicle" FOR EACH ROW EXECUTE FUNCTION fail_bulk_koli()`)
	apiCall(t, app, "POST", path, `{}`, owner, "", 500)
	var configured, audits int
	if err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM "Vehicle" WHERE "maxKoli" IS NOT NULL),(SELECT count(*) FROM "AuditLog" WHERE "entityType"='vehicle')`).Scan(&configured, &audits); err != nil || configured != 1 || audits != 0 {
		t.Fatal(configured, audits, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_bulk_koli ON "Vehicle"`)
	apiCall(t, app, "POST", path, `{"overrides":{"13":2147483648}}`, owner, "", 422)
	result := apiCall(t, app, "POST", path, `{"overrides":{"12":"12.5","16":-1,"999":99}}`, owner, "", 200)["data"].(map[string]any)
	if result["count"] != float64(7) {
		t.Fatal(result)
	}
	for id, want := range map[float64]float64{10: 40, 11: 60, 12: 13, 13: 100, 14: 100, 15: 150, 16: 40} {
		row := gudangRow(t, result["updated"], id)
		if row["maxKoli"] != want || row["suggested"] != (id != 12 && id != 16) {
			t.Fatal(row)
		}
	}
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityType"='vehicle' AND "actorId"=1 AND "beforeData"::jsonb='{"maxKoli":null}'::jsonb AND "afterData"::jsonb->>'capacityConfigChanged'='true'`).Scan(&audits); err != nil || audits != 7 {
		t.Fatal(audits, err)
	}
	for _, body := range []string{`null`, `{`, `{"overrides":[]}`} {
		result = apiCall(t, app, "POST", path, body, owner, "", 200)["data"].(map[string]any)
		if result["count"] != float64(0) || len(result["updated"].([]any)) != 0 {
			t.Fatal(result)
		}
	}
	// Preserve positive fractions rounding to zero and malformed-body fallback.
	shipmentSQL(t, db, `UPDATE "Vehicle" SET "maxKoli"=NULL WHERE id=10`)
	result = apiCall(t, app, "POST", path, `{"overrides":{"10":0.1}}`, owner, "", 200)["data"].(map[string]any)
	if gudangRow(t, result["updated"], 10)["maxKoli"] != float64(0) {
		t.Fatal(result)
	}
	shipmentSQL(t, db, `UPDATE "Vehicle" SET "maxKoli"=NULL WHERE id=10`)
	result = apiCall(t, app, "POST", path, `{`, owner, "", 200)["data"].(map[string]any)
	if gudangRow(t, result["updated"], 10)["maxKoli"] != float64(40) {
		t.Fatal(result)
	}
}

func TestBulkConfigureKoliConcurrentPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'CONCURRENT',100,1,NOW())`)
	results := make(chan float64, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			req := httptest.NewRequest("POST", "/api/v1/fleet-capacity/bulk-configure-koli", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+owner)
			req.Header.Set("Content-Type", "application/json")
			response, err := app.Test(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var out map[string]any
			if err := json.NewDecoder(response.Body).Decode(&out); err != nil || response.StatusCode != 200 {
				t.Error(response.StatusCode, err, out)
				return
			}
			results <- out["data"].(map[string]any)["count"].(float64)
		})
	}
	wg.Wait()
	close(results)
	total := 0.0
	for count := range results {
		total += count
	}
	var audits int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityType"='vehicle'`).Scan(&audits); err != nil || audits != 1 || total != 1 {
		t.Fatal("competing requests configured the same row", total, audits, err)
	}
}

func TestFleetCapacityTenantIsolationPostgres(t *testing.T) {
	a, other := disposableDB(t), disposableDB(t)
	for _, db := range []testDatabase{a, other} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'SHARED',100,10,NOW()); INSERT INTO "Transport" ("transportCode","vehicleId","updatedAt") VALUES ('SHARED',10,NOW())`)
	}
	shipmentSQL(t, a, `INSERT INTO "SystemSetting" (key,value,"updatedAt") VALUES ('capacity.warningThresholdPct','80',NOW()); INSERT INTO "Customer" (id,code,name,"updatedAt") VALUES (10,'C','Sender',NOW()); INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,"updatedAt") VALUES (10,'S',10,'A','B',NOW()); INSERT INTO "DetailShipment" ("masterId","detailCode",description,"actualWeightKg","updatedAt") VALUES (10,'D','Box',85,NOW()); INSERT INTO "TransportShipment" ("transportId","shipmentId") SELECT id,10 FROM "Transport"`)
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
	apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", tokenA, "B", 401)
	apiCall(t, app, "POST", "/api/v1/fleet-capacity/bulk-configure-koli", `{}`, tokenA, "A", 200)
	shipmentSQL(t, a, `UPDATE "AuditLog" SET "createdAt"=(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-INTERVAL '1 day' WHERE "entityType"='vehicle'`)
	apiCall(t, app, "GET", "/api/v1/capacity/trend", "", tokenA, "B", 401)
	for _, tc := range []struct {
		token, corp string
		total       float64
	}{{tokenA, "A", 1}, {tokenB, "B", 0}} {
		trend := apiCall(t, app, "GET", "/api/v1/capacity/trend", "", tc.token, tc.corp, 200)["data"].(map[string]any)
		if trend["total"] != tc.total {
			t.Fatal("capacity audit tenant isolation", trend)
		}
	}
	for _, tc := range []struct{ token, corp, status string }{{tokenA, "A", "WARNING"}, {tokenB, "B", "PARTIALLY_CONFIGURED"}} {
		result := apiCall(t, app, "GET", "/api/v1/fleet-capacity", "", tc.token, tc.corp, 200)["data"].(map[string]any)
		row := gudangRow(t, result["vehicles"], 10)
		if row["activeCapacity"].(map[string]any)["overallStatus"] != tc.status {
			t.Fatal(row)
		}
	}
	var koli *int
	var audits int
	if err := other.pool.QueryRow(t.Context(), `SELECT "maxKoli",(SELECT count(*) FROM "AuditLog" WHERE "entityType"='vehicle') FROM "Vehicle" WHERE id=10`).Scan(&koli, &audits); err != nil || koli != nil || audits != 0 {
		t.Fatal(koli, audits, err)
	}
}
