package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/identity"
	"novalogistik/backend/internal/tenant"
)

func TestCommercialMasterPostgres(t *testing.T) {
	db := disposableDB(t)
	seedIdentity(t, db)
	registry := tenant.New(tenant.Config{DatabaseURL: db.dsn, Mode: "development"})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	owner := loginAs(t, app, "owner", "")
	customer := apiCall(t, app, "POST", "/api/v1/customers", `{"name":"Company PIC","companyName":"Company","type":"b2b"}`, owner, "", 200)["data"].(map[string]any)
	cid := int(customer["id"].(float64))
	cp := fmt.Sprintf("/api/v1/customers/%d", cid)
	t.Run("customer CRUD", func(t *testing.T) {
		apiCall(t, app, "POST", "/api/v1/customers", `{"name":"No Company","type":"b2b"}`, owner, "", 422)
		apiCall(t, app, "POST", "/api/v1/customers", `{"name":"No Warehouse","warehouseId":99999}`, owner, "", 422)
		apiCall(t, app, "GET", "/api/v1/customers?search=Company", "", owner, "", 200)
		apiCall(t, app, "GET", cp, "", owner, "", 200)
		apiCall(t, app, "PUT", cp, `{"phone":"123","companyName":"Company Updated"}`, owner, "", 200)
		unused := apiCall(t, app, "POST", "/api/v1/customers", `{"name":"Unused"}`, owner, "", 200)["data"].(map[string]any)
		apiCall(t, app, "DELETE", fmt.Sprintf("/api/v1/customers/%d", int(unused["id"].(float64))), "", owner, "", 200)
	})
	t.Run("vehicle capacity and photos", func(t *testing.T) {
		apiCall(t, app, "POST", "/api/v1/vehicles", `{"vehicleNumber":"B-TEST","maxWeightKg":500,"maxVolumeM3":5,"photoDataUrl":"invalid"}`, owner, "", 422)
		v := apiCall(t, app, "POST", "/api/v1/vehicles", `{"vehicleNumber":"B-TEST","maxWeightKg":500,"lengthM":2,"widthM":3,"heightM":4,"maxVolumeM3":1,"maxKoli":10.5,"photoDataUrl":"data:image/png;base64,aGVsbG8="}`, owner, "", 200)["data"].(map[string]any)
		if v["maxVolumeM3"] != float64(24) || v["maxKoli"] != float64(11) {
			t.Fatal("vehicle capacity computation differs")
		}
		path := fmt.Sprintf("/api/v1/vehicles/%d", int(v["id"].(float64)))
		apiCall(t, app, "POST", "/api/v1/vehicles", `{"vehicleNumber":"B-TEST"}`, owner, "", 422)
		apiCall(t, app, "GET", "/api/v1/vehicles?status=ACTIVE&search=B-TEST", "", owner, "", 200)
		apiCall(t, app, "PUT", path, `{"maxKoli":-1}`, owner, "", 422)
		updated := apiCall(t, app, "PUT", path, `{"maxWeightKg":600,"maxKoli":null,"lengthM":3,"widthM":2,"heightM":1}`, owner, "", 200)["data"].(map[string]any)
		if updated["maxVolumeM3"] != float64(6) || updated["maxKoli"] != nil {
			t.Fatal("vehicle update differs")
		}
		apiCall(t, app, "DELETE", path, "", owner, "", 200)
	})
	t.Run("tariff lifecycle", func(t *testing.T) {
		body := fmt.Sprintf(`{"name":"Contract","origin":"Jakarta","destination":"Bandung","customerType":"b2b","customerId":%d,"ratePerKg":1000,"effectiveFrom":"2020-01-01","effectiveTo":"2099-01-02"}`, cid)
		tariff := apiCall(t, app, "POST", "/api/v1/tariffs", body, owner, "", 200)["data"].(map[string]any)
		if tariff["effectiveTo"] != "2099-01-02T23:59:59.999Z" {
			t.Fatalf("date-only expiry lost final day: %v", tariff["effectiveTo"])
		}
		id := int(tariff["id"].(float64))
		path := fmt.Sprintf("/api/v1/tariffs/%d", id)
		apiCall(t, app, "POST", "/api/v1/tariffs", body, owner, "", 422)
		apiCall(t, app, "PUT", path, `{"pricingMethod":"PER_KG_SIMPLE"}`, owner, "", 422)
		apiCall(t, app, "PUT", path, `{"pricingMethod":"PER_KOLI"}`, owner, "", 422)
		apiCall(t, app, "PUT", path, `{"pricingMethod":"PER_KOLI","ratePerKoli":5000}`, owner, "", 200)
		apiCall(t, app, "PUT", path, `{"isActive":"false"}`, owner, "", 422)
		apiCall(t, app, "PUT", path, `{"effectiveTo":"2019-01-01"}`, owner, "", 422)
		apiCall(t, app, "GET", "/api/v1/tariffs?active_only=true&search=Contract", "", owner, "", 200)
		if _, err := db.pool.Exec(context.Background(), `INSERT INTO "MasterShipment" ("masterCode","customerId","tariffId",origin,destination,"updatedAt") VALUES ('TARIFF-SHIPMENT',$1,$2,'Jakarta','Bandung',NOW())`, cid, id); err != nil {
			t.Fatal(err)
		}
		apiCall(t, app, "DELETE", path, "", owner, "", 409)
		apiCall(t, app, "DELETE", cp, "", owner, "", 200)
		if _, err := db.pool.Exec(context.Background(), `UPDATE "MasterShipment" SET "tariffId"=NULL WHERE "masterCode"='TARIFF-SHIPMENT'`); err != nil {
			t.Fatal(err)
		}
		apiCall(t, app, "DELETE", path, "", owner, "", 200)
		expired := apiCall(t, app, "POST", "/api/v1/tariffs", `{"origin":"A","destination":"B","customerType":"b2c","pricingMethod":"PER_KG_SIMPLE","ratePerKg":100,"effectiveFrom":"2000-01-01","effectiveTo":"2001-01-01"}`, owner, "", 200)["data"].(map[string]any)
		if expired["isActive"] != false {
			t.Fatal("expired tariff still active")
		}
		apiCall(t, app, "PUT", fmt.Sprintf("/api/v1/tariffs/%d", int(expired["id"].(float64))), `{"isActive":true}`, owner, "", 422)
	})
	t.Run("settings and dropdowns", func(t *testing.T) {
		t.Setenv("MOBILE_ANDROID_MIN_VERSION_CODE", "3")
		t.Setenv("MOBILE_ANDROID_LATEST_VERSION_CODE", "2")
		mobile := apiCall(t, app, "GET", "/api/v1/mobile/config", "", owner, "", 200)["data"].(map[string]any)
		if mobile["android"].(map[string]any)["latestVersionCode"] != float64(3) {
			t.Fatal("mobile version policy differs")
		}
		settings := apiCall(t, app, "GET", "/api/v1/settings", "", owner, "", 200)["data"].(map[string]any)
		if settings["capacity"].(map[string]any)["warningThresholdPct"] != float64(90) {
			t.Fatal("wrong default threshold")
		}
		apiCall(t, app, "PUT", "/api/v1/settings", `{}`, owner, "", 422)
		apiCall(t, app, "PUT", "/api/v1/settings", `{"warningThresholdPct":100}`, owner, "", 422)
		settings = apiCall(t, app, "PUT", "/api/v1/settings", `{"warningThresholdPct":85}`, owner, "", 200)["data"].(map[string]any)
		if settings["capacity"].(map[string]any)["warningThresholdPct"] != float64(85) {
			t.Fatal("settings did not persist")
		}
		worker := loginAs(t, app, "worker", "")
		apiCall(t, app, "PUT", "/api/v1/settings", `{"warningThresholdPct":80}`, worker, "", 403)
		options := apiCall(t, app, "GET", "/api/v1/options", "", owner, "", 200)["data"].(map[string]any)
		for _, key := range []string{"company", "employees", "vehicles", "routes", "warehouses", "customers", "tariffs", "permissions", "vehicleOwners", "marketingPartners", "b2bShipments"} {
			if options[key] == nil {
				t.Fatalf("missing options field %s", key)
			}
		}
	})
}

func TestCustomerScopesPostgres(t *testing.T) {
	db := disposableDB(t)
	seedIdentity(t, db)
	registry := tenant.New(tenant.Config{DatabaseURL: db.dsn, Mode: "development"})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	owner := loginAs(t, app, "owner", "")
	w1 := apiCall(t, app, "POST", "/api/v1/warehouses", `{"name":"Warehouse A"}`, owner, "", 200)["data"].(map[string]any)
	w2 := apiCall(t, app, "POST", "/api/v1/warehouses", `{"name":"Warehouse B"}`, owner, "", 200)["data"].(map[string]any)
	if _, err := db.pool.Exec(context.Background(), `INSERT INTO "Permission" (id,slug,module,description) VALUES (101,'customer.view','Customers','View customers'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,101); INSERT INTO "Employee" (id,"employeeNumber",name,"warehouseId","updatedAt") VALUES (50,'EMP-TEST','Warehouse Staff',$1,NOW()); UPDATE "User" SET "employeeId"=50 WHERE id=2`, w1["id"]); err != nil {
		t.Fatal(err)
	}
	for _, data := range []map[string]any{{"name": "Shared", "warehouseId": nil}, {"name": "Warehouse A Customer", "warehouseId": w1["id"]}, {"name": "Warehouse B Customer", "warehouseId": w2["id"]}} {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		apiCall(t, app, "POST", "/api/v1/customers", string(raw), owner, "", 200)
	}
	worker := loginAs(t, app, "worker", "")
	for _, path := range []string{"/api/v1/customers", "/api/v1/customers?search=Warehouse"} {
		rows := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].([]any)
		for _, row := range rows {
			if row.(map[string]any)["warehouseId"] == w2["id"] {
				t.Fatal("customer search leaked another warehouse")
			}
		}
	}
	options := apiCall(t, app, "GET", "/api/v1/options", "", worker, "", 200)["data"].(map[string]any)
	if len(options["customers"].([]any)) != 2 {
		t.Fatal("dropdown warehouse scope differs")
	}
	partner := apiCall(t, app, "POST", "/api/v1/users", fmt.Sprintf(`{"username":"marketing","name":"Marketing","password":"test-password","roleIds":[3],"permissionIds":[101],"warehouseId":%d}`, int(w1["id"].(float64))), owner, "", 200)["data"].(map[string]any)
	apiCall(t, app, "POST", "/api/v1/customers", fmt.Sprintf(`{"name":"Marketing Customer","marketingPartnerId":%d,"warehouseId":%d}`, int(partner["partnerId"].(float64)), int(w1["id"].(float64))), owner, "", 200)
	token := "marketing-test-token"
	if _, err := db.pool.Exec(context.Background(), `INSERT INTO "SessionToken" (token,"userId","expiresAt") VALUES ($1,$2,$3)`, identity.TokenHash(token), partner["id"], time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows := apiCall(t, app, "GET", "/api/v1/customers", "", token, "", 200)["data"].([]any)
	// Marketing users without an employee have no warehouse. List visibility
	// follows the legacy list contract; options intentionally bypass warehouse
	// scope for the marketing user's own customers.
	if len(rows) != 0 {
		t.Fatal("customer list warehouse scope changed")
	}
	options = apiCall(t, app, "GET", "/api/v1/options", "", token, "", 200)["data"].(map[string]any)
	if len(options["customers"].([]any)) != 1 {
		t.Fatal("marketing dropdown scope differs")
	}
}
