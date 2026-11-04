package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func TestMasterPostgres(t *testing.T) {
	db := disposableDB(t)
	seedIdentity(t, db)
	registry := tenant.New(tenant.Config{DatabaseURL: db.dsn, Mode: "development"})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	owner := loginAs(t, app, "owner", "")
	t.Run("warehouse lifecycle and reference protection", func(t *testing.T) {
		w := apiCall(t, app, "POST", "/api/v1/warehouses", `{"name":"Central","city":"Jakarta","latitude":-6.2,"longitude":106.8}`, owner, "", 200)["data"].(map[string]any)
		path := fmt.Sprintf("/api/v1/warehouses/%d", int(w["id"].(float64)))
		if w["code"] != "WH-000001" {
			t.Fatal("warehouse code differs")
		}
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", w["createdAt"].(string)); err != nil {
			t.Fatalf("Prisma timestamp format: %v", err)
		}
		apiCall(t, app, "PUT", path, `{"name":"Updated","notes":"Test"}`, owner, "", 200)
		list := apiCall(t, app, "GET", "/api/v1/warehouses?search=Updated&include_inactive=false", "", owner, "", 200)["data"].([]any)
		if len(list) != 1 {
			t.Fatal("warehouse search failed")
		}
		if _, err := db.pool.Exec(context.Background(), `INSERT INTO "Customer" (code,name,"updatedAt") VALUES ('TEST-CUSTOMER','Customer',NOW()); INSERT INTO "MasterShipment" ("masterCode","customerId",origin,destination,"originWarehouseId","updatedAt") SELECT 'TEST-SHIPMENT',c.id,'Jakarta','Bandung',$1,NOW() FROM "Customer" c WHERE code='TEST-CUSTOMER'`, w["id"]); err != nil {
			t.Fatal(err)
		}
		result := apiCall(t, app, "DELETE", path, "", owner, "", 200)["data"].(map[string]any)
		if result["deactivated"] != true {
			t.Fatal("referenced warehouse must be deactivated")
		}
		newWarehouse := apiCall(t, app, "POST", "/api/v1/warehouses", `{"name":"Unused"}`, owner, "", 200)["data"].(map[string]any)
		apiCall(t, app, "DELETE", fmt.Sprintf("/api/v1/warehouses/%d", int(newWarehouse["id"].(float64))), "", owner, "", 200)
		apiCall(t, app, "PUT", "/api/v1/warehouses/999999", `{}`, owner, "", 404)
	})
	t.Run("bank accounts and invoice permission", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"bankName":"Bank","accountNumber":"abc","accountName":"Name"}`, `{"bankName":"Bank","accountNumber":"1234","accountName":"Name","isActive":"true"}`} {
			apiCall(t, app, "POST", "/api/v1/bank-accounts", body, owner, "", 422)
		}
		account := apiCall(t, app, "POST", "/api/v1/bank-accounts", `{"bankName":" Bank ","accountNumber":"12-34 56","accountName":" Company "}`, owner, "", 200)["data"].(map[string]any)
		if account["accountNumber"] != "123456" || account["accountName"] != "Company" {
			t.Fatal("account normalization differs")
		}
		path := fmt.Sprintf("/api/v1/bank-accounts/%d", int(account["id"].(float64)))
		apiCall(t, app, "PUT", path, `{"bankName":"Bank 2","accountNumber":"987654","accountName":"Company"}`, owner, "", 200)
		apiCall(t, app, "DELETE", path, "", owner, "", 200)
		active := apiCall(t, app, "GET", "/api/v1/bank-accounts?active=true", "", owner, "", 200)["data"].([]any)
		if len(active) != 0 {
			t.Fatal("inactive account in active list")
		}
		worker := loginAs(t, app, "worker", "")
		apiCall(t, app, "GET", "/api/v1/bank-accounts", "", worker, "", 403)
		if _, err := db.pool.Exec(context.Background(), `INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'invoice.create','Invoices','Create invoice'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100)`); err != nil {
			t.Fatal(err)
		}
		active = apiCall(t, app, "GET", "/api/v1/bank-accounts", "", worker, "", 200)["data"].([]any)
		if len(active) != 0 {
			t.Fatal("invoice-only user saw inactive bank accounts")
		}
		apiCall(t, app, "PUT", path, `{"bankName":"Bank 2","accountNumber":"987654","accountName":"Company","isActive":true}`, owner, "", 200)
		active = apiCall(t, app, "GET", "/api/v1/bank-accounts", "", worker, "", 200)["data"].([]any)
		if len(active) != 1 {
			t.Fatal("invoice user cannot select active bank account")
		}
	})
	t.Run("checkpoint reorder isolation and rollback", func(t *testing.T) {
		route := apiCall(t, app, "POST", "/api/v1/routes", `{"name":"Jakarta-Bandung","origin":"Jakarta","destination":"Bandung"}`, owner, "", 200)["data"].(map[string]any)
		path := fmt.Sprintf("/api/v1/routes/%d", int(route["id"].(float64)))
		cp1 := apiCall(t, app, "POST", path+"/checkpoints", `{"name":"Origin","latitude":-6.2,"longitude":106.8,"radiusKm":0.5}`, owner, "", 200)["data"].(map[string]any)
		cp2 := apiCall(t, app, "POST", path+"/checkpoints", `{"name":"Middle","latitude":-6.3,"longitude":107}`, owner, "", 200)["data"].(map[string]any)
		cp3 := apiCall(t, app, "POST", path+"/checkpoints", `{"name":"Destination","latitude":-6.9,"longitude":107.6}`, owner, "", 200)["data"].(map[string]any)
		if cp1["radiusMeters"] != float64(500) {
			t.Fatal("radius conversion differs")
		}
		apiCall(t, app, "GET", "/api/v1/routes?search=Jakarta", "", owner, "", 200)
		apiCall(t, app, "GET", path, "", owner, "", 200)
		apiCall(t, app, "PUT", path, `{"name":"Route Updated"}`, owner, "", 200)
		apiCall(t, app, "PUT", path+"/checkpoints", `{"checkpoints":[]}`, owner, "", 422)
		apiCall(t, app, "PUT", path+"/checkpoints", `{"checkpoints":[{"id":999,"name":"Forged","latitude":1,"longitude":2},{"name":"B","latitude":1,"longitude":2},{"name":"C","latitude":1,"longitude":2}]}`, owner, "", 422)
		raw, err := json.Marshal(map[string]any{"checkpoints": []any{cp3, cp1, cp2}})
		if err != nil {
			t.Fatal(err)
		}
		saved := apiCall(t, app, "PUT", path+"/checkpoints", string(raw), owner, "", 200)["data"].(map[string]any)
		if saved["updated"] != float64(3) || saved["checkpoints"].([]any)[0].(map[string]any)["id"] != cp3["id"] {
			t.Fatal("checkpoint reorder failed")
		}
		cpPath := fmt.Sprintf("/api/v1/checkpoints/%d", int(cp1["id"].(float64)))
		apiCall(t, app, "PUT", cpPath, `{"name":"Changed Origin","radiusMeters":300}`, owner, "", 200)
		apiCall(t, app, "DELETE", cpPath, "", owner, "", 422)
		cp4 := apiCall(t, app, "POST", path+"/checkpoints", `{"name":"Extra","latitude":0,"longitude":0}`, owner, "", 200)["data"].(map[string]any)
		apiCall(t, app, "DELETE", cpPath, "", owner, "", 200)
		// Keep two existing IDs and create one new item, deleting exactly one.
		raw, err = json.Marshal(map[string]any{"checkpoints": []any{cp3, cp4, map[string]any{"name": "Replacement", "latitude": -7, "longitude": 108}}})
		if err != nil {
			t.Fatal(err)
		}
		saved = apiCall(t, app, "PUT", path+"/checkpoints", string(raw), owner, "", 200)["data"].(map[string]any)
		if saved["created"] != float64(1) || saved["deleted"] != float64(1) {
			t.Fatal("bulk replace counts differ")
		}
		apiCall(t, app, "DELETE", path, "", owner, "", 200)
		apiCall(t, app, "GET", path, "", owner, "", 404)
	})
	t.Run("concurrent warehouse codes", func(t *testing.T) {
		var wg sync.WaitGroup
		codes := make(chan string, 8)
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				w := apiCall(t, app, "POST", "/api/v1/warehouses", `{"name":"Concurrent"}`, owner, "", 200)["data"].(map[string]any)
				codes <- w["code"].(string)
			})
		}
		wg.Wait()
		close(codes)
		seen := map[string]bool{}
		for code := range codes {
			if seen[code] || !strings.HasPrefix(code, "WH-") {
				t.Fatalf("duplicate/invalid code %s", code)
			}
			seen[code] = true
		}
		if len(seen) != 8 {
			t.Fatal("missing concurrent writes")
		}
	})
}
