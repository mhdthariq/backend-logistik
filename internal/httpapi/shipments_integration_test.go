package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func shipmentApp(t *testing.T) (testDatabase, *fiber.App, string, int) {
	t.Helper()
	db := disposableDB(t)
	seedIdentity(t, db)
	registry := tenant.New(tenant.Config{DatabaseURL: db.dsn, Mode: "development"})
	t.Cleanup(registry.Close)
	app := httpapi.NewWithTenants(nil, registry)
	owner := loginAs(t, app, "owner", "")
	c := apiCall(t, app, "POST", "/api/v1/customers", `{"name":"Sender","phone":"123","address":"Jakarta","type":"b2c"}`, owner, "", 200)["data"].(map[string]any)
	return db, app, owner, int(c["id"].(float64))
}

func shipmentSQL(t *testing.T, db testDatabase, sql string, args ...any) {
	t.Helper()
	if _, err := db.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func newShipment(t *testing.T, app *fiber.App, token string, cid int, extra string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"customerId":%d,"origin":"Jakarta","destination":"Bandung","penerimaName":"Receiver"%s}`, cid, extra)
	return apiCall(t, app, "POST", "/api/v1/shipments", body, token, "", 200)["data"].(map[string]any)
}

func TestShipmentCRUDPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	apiCall(t, app, "GET", "/api/v1/shipments", "", "", "", 401)
	apiCall(t, app, "POST", "/api/v1/shipments", `{}`, worker, "", 403)
	for _, body := range []string{`{}`, `null`, `{`, `{"customerId":99999}`, fmt.Sprintf(`{"customerId":%d}`, cid), fmt.Sprintf(`{"customerId":%d,"tariffId":99999}`, cid), fmt.Sprintf(`{"customerId":%d,"origin":"A","destination":"B","discountType":"PERCENTAGE","discountPercentage":101}`, cid)} {
		apiCall(t, app, "POST", "/api/v1/shipments", body, owner, "", 422)
	}
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box","quantity":2,"actualWeightKg":2.1,"volumeM3":0.01}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	if s["masterCode"] != "MKT-000007" || s["pengirimName"] != "Sender" || len(s["details"].([]any)) != 2 || s["paymentTrackingEnabled"] != false {
		t.Fatalf("creation differs: %v", s)
	}
	t.Run("detail aggregates and update", func(t *testing.T) {
		full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
		if full["totals"].(map[string]any)["totalActualKg"] != 4.2 || full["pricingPreview"] != nil || full["paymentSummary"].(map[string]any)["status"] != "UNPRICED" {
			t.Fatalf("detail contract differs: %v", full)
		}
		for _, key := range []string{"invoiceLines", "payments", "pickups", "deliveries", "transportItems", "trackingEvents", "details"} {
			if _, ok := full[key].([]any); !ok {
				t.Errorf("%s must be an array", key)
			}
		}
		updated := apiCall(t, app, "PUT", path, `{"penerimaName":"Updated","insuranceAmount":-2,"discountType":"PERCENTAGE","discountPercentage":10}`, owner, "", 200)["data"].(map[string]any)
		if updated["penerimaName"] != "Updated" || updated["discountPercentage"] != float64(10) || updated["insuranceAmount"] != float64(0) {
			t.Fatal(updated)
		}
		events := apiCall(t, app, "GET", path+"/tracking", "", owner, "", 200)["data"].([]any)
		if len(events) != 1 || events[0].(map[string]any)["actor"].(map[string]any)["passwordHash"] != nil {
			t.Fatal("tracking actor leaked credentials")
		}
		apiCall(t, app, "POST", path+"/print-log", `{}`, owner, "", 200)
		var count int
		if err := db.pool.QueryRow(context.Background(), `SELECT count(*) FROM "AuditLog" WHERE action='printed_resi' AND "entityId"=$1`, s["id"]).Scan(&count); err != nil || count != 1 {
			t.Fatalf("print audit missing: %d %v", count, err)
		}
	})
	t.Run("filters", func(t *testing.T) {
		for _, query := range []string{"?search=MKT-000007", "?status=CREATED&customerType=b2c&fulfillmentMode=STANDARD", "?invoice=not_invoice"} {
			rows := apiCall(t, app, "GET", "/api/v1/shipments"+query, "", owner, "", 200)["data"].([]any)
			if len(rows) != 1 {
				t.Fatal(rows)
			}
		}
		apiCall(t, app, "GET", "/api/v1/shipments?invoice=garbage", "", owner, "", 422)
		rows := apiCall(t, app, "GET", "/api/v1/shipments?invoice=on_invoice", "", owner, "", 200)["data"].([]any)
		if len(rows) != 0 {
			t.Fatal(rows)
		}
	})
	t.Run("delete preserves history", func(t *testing.T) {
		shipmentSQL(t, db, `INSERT INTO "Payment" ("masterId",method,amount) VALUES ($1,'CASH',100)`, s["id"])
		apiCall(t, app, "DELETE", path, "", owner, "", 422)
		shipmentSQL(t, db, `DELETE FROM "Payment" WHERE "masterId"=$1`, s["id"])
		shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1`, s["id"])
		apiCall(t, app, "DELETE", path, "", owner, "", 422)
		apiCall(t, app, "PUT", path, `{"origin":"Elsewhere"}`, owner, "", 422)
		shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CANCELLED' WHERE id=$1`, s["id"])
		apiCall(t, app, "DELETE", path, "", owner, "", 200)
		apiCall(t, app, "GET", path, "", owner, "", 404)
		var count int
		if err := db.pool.QueryRow(context.Background(), `SELECT count(*) FROM "DetailShipment" WHERE "masterId"=$1`, s["id"]).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade failed: %d %v", count, err)
		}
	})
}

func TestShipmentDetailsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, "")
	path := fmt.Sprintf("/api/v1/shipments/%.0f/details", s["id"])
	apiCall(t, app, "POST", path, `{"description":"Box","quantity":0}`, owner, "", 422)
	apiCall(t, app, "POST", path, `{"description":"Box","quantity":10001}`, owner, "", 422)
	created := apiCall(t, app, "POST", path, `{"description":"Box","quantity":3,"actualWeightKg":2,"lengthCm":100,"widthCm":100,"heightCm":100,"volumeM3":0.2}`, owner, "", 200)["data"].(map[string]any)
	details := created["details"].([]any)
	d1 := details[0].(map[string]any)
	d2 := details[1].(map[string]any)
	batch := path + "/batch"
	for _, body := range []string{`null`, `[]`, `{`, `{}`, `{"detailIds":[]}`, `{"detailIds":[1,1]}`, `{"detailIds":["1"]}`, `{"detailIds":[0]}`, fmt.Sprintf(`{"detailIds":[%.0f,999999],"description":"wrong"}`, d1["id"]), fmt.Sprintf(`{"detailIds":[%.0f],"actualWeightKg":null}`, d1["id"]), fmt.Sprintf(`{"detailIds":[%.0f],"volumeM3":-1}`, d1["id"]), fmt.Sprintf(`{"detailIds":[%.0f],"description":false}`, d1["id"])} {
		apiCall(t, app, "PUT", batch, body, owner, "", 422)
	}
	changed := apiCall(t, app, "PUT", batch, fmt.Sprintf(`{"detailIds":[%.0f,%.0f],"description":" New ","volumeM3":null,"actualWeightKg":0}`, d1["id"], d2["id"]), owner, "", 200)["data"].(map[string]any)
	if changed["updated"] != float64(2) || changed["details"].([]any)[0].(map[string]any)["volumeM3"] != nil {
		t.Fatal(changed)
	}
	single := fmt.Sprintf("/api/v1/shipment-details/%.0f", d1["id"])
	apiCall(t, app, "PUT", single, `{"actualWeightKg":"3.25","volumeM3":0.1}`, owner, "", 200)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='PICKED_UP' WHERE id=$1`, s["id"])
	for _, call := range []struct{ method, path, body string }{{"POST", path, `{"description":"x"}`}, {"PUT", batch, fmt.Sprintf(`{"detailIds":[%.0f],"description":"x"}`, d1["id"])}, {"DELETE", single, ""}} {
		apiCall(t, app, call.method, call.path, call.body, owner, "", 422)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CREATED' WHERE id=$1`, s["id"])
	// A trigger failure on the second update proves the entire batch rolls back.
	shipmentSQL(t, db, fmt.Sprintf(`CREATE FUNCTION fail_detail_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%.0f AND NEW.description='rollback' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_rollback BEFORE UPDATE ON "DetailShipment" FOR EACH ROW EXECUTE FUNCTION fail_detail_update()`, d2["id"]))
	apiCall(t, app, "PUT", batch, fmt.Sprintf(`{"detailIds":[%.0f,%.0f],"description":"rollback"}`, d1["id"], d2["id"]), owner, "", 500)
	rows := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].([]any)
	if rows[0].(map[string]any)["description"] != "New" {
		t.Fatal("partial batch committed")
	}
	apiCall(t, app, "DELETE", batch, fmt.Sprintf(`{"detailIds":[%.0f,%.0f]}`, d1["id"], d2["id"]), owner, "", 200)
	apiCall(t, app, "DELETE", fmt.Sprintf("/api/v1/shipment-details/%.0f", details[2].(map[string]any)["id"]), "", owner, "", 200)
}

func TestShipmentPricingPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	tariff := apiCall(t, app, "POST", "/api/v1/tariffs", `{"origin":"Jakarta","destination":"Bandung","customerType":"b2c","ratePerKg":1000,"effectiveFrom":"2020-01-01"}`, owner, "", 200)["data"].(map[string]any)
	s := newShipment(t, app, owner, cid, fmt.Sprintf(`,"tariffId":%.0f,"details":[{"description":"Box","actualWeightKg":2.1,"volumeM3":0.01}]`, tariff["id"]))
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	preview := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)["pricingPreview"].(map[string]any)
	if preview["estimatedPrice"] != float64(2500) || preview["chargeableKg"] != 2.5 {
		t.Fatal(preview)
	}
	price := apiCall(t, app, "POST", path+"/price", `{"discountType":"PERCENTAGE","discountPercentage":10,"insuranceAmount":100}`, owner, "", 200)["data"].(map[string]any)
	if price["priceAmount"] != float64(2500) || price["discountAmount"] != float64(250) || price["finalPriceAmount"] != float64(2250) || price["pricingMethod"] != "PER_KG" {
		t.Fatal(price)
	}
	apiCall(t, app, "POST", path+"/price", `{"discountAmount":2501}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/price", `{"discountType":"PERCENTAGE","discountPercentage":-1}`, owner, "", 422)
	// Tracking failure rolls back the price snapshot too.
	shipmentSQL(t, db, `CREATE FUNCTION fail_priced_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event='PRICED' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_pricing_rollback BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_priced_event()`)
	apiCall(t, app, "POST", path+"/price", `{"discountAmount":0}`, owner, "", 500)
	full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	if full["finalPriceAmount"] != float64(2250) {
		t.Fatal("pricing snapshot committed without event")
	}
	shipmentSQL(t, db, `DROP TRIGGER test_pricing_rollback ON "TrackingEvent"; INSERT INTO "Payment" ("masterId",method,amount,status,"proofUrl","recordedById") VALUES ($1,'CASH',250,'VERIFIED','private-proof',1)`, s["id"])
	apiCall(t, app, "POST", path+"/price", `{}`, owner, "", 422)
	full = apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	summary := full["paymentSummary"].(map[string]any)
	if summary["paidAmount"] != float64(250) || summary["remainingAmount"] != float64(2000) || summary["status"] != "DP" {
		t.Fatal(summary)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/price", `{}`, owner, "", 422)
}

func TestShipmentScopePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH-JKT','Jakarta Warehouse','Jakarta',NOW()),(11,'WH-BDG','Bandung Warehouse','Bandung',NOW()); INSERT INTO "Employee" (id,"employeeNumber",name,"warehouseId","updatedAt") VALUES (10,'EMP-JKT','Worker',10,NOW()); UPDATE "User" SET "employeeId"=10 WHERE id=2;
	 INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'shipment.view','Shipment','view'),(101,'shipment_detail.update','Shipment','update'),(102,'shipment.view_tracking','Shipment','tracking'),(103,'shipment.create','Shipment','create'),(104,'shipment.update','Shipment','edit'); INSERT INTO "UserPermission" ("userId","permissionId") SELECT 2,id FROM "Permission" WHERE id>=100;`)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	apiCall(t, app, "GET", path, "", worker, "", 200)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='DELIVERED',"arrivedWarehouseId"=11 WHERE id=$1`, s["id"])
	apiCall(t, app, "GET", path, "", worker, "", 403)
	apiCall(t, app, "GET", path+"/tracking", "", worker, "", 403)
	apiCall(t, app, "PUT", path+"/details/batch", fmt.Sprintf(`{"detailIds":[%.0f],"description":"Other"}`, s["details"].([]any)[0].(map[string]any)["id"]), worker, "", 403)
	rows := apiCall(t, app, "GET", "/api/v1/shipments?search=MKT", "", worker, "", 200)["data"].([]any)
	if len(rows) != 0 {
		t.Fatal("list leaked destination warehouse shipment")
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='IN_TRANSPORT' WHERE id=$1; INSERT INTO "Payment" ("masterId",method,amount,"proofUrl") VALUES ($1,'CASH',100,'secret-proof')`, s["id"])
	visible := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if _, exists := visible["payments"].([]any)[0].(map[string]any)["proofUrl"]; exists {
		t.Fatal("payment proof leaked")
	}
	apiCall(t, app, "POST", "/api/v1/shipments", fmt.Sprintf(`{"customerId":%d,"origin":"A","destination":"B","fulfillmentMode":"DIRECT"}`, cid), worker, "", 403)
}

func TestShipmentDocumentsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, "")
	path := fmt.Sprintf("/api/v1/shipments/%.0f/documents", s["id"])
	for _, body := range []string{`{}`, `{"fileName":"x","mimeType":"text/html","dataUrl":"data:text/html;base64,aGVsbG8="}`, `{"fileName":"x","mimeType":"image/png","dataUrl":"data:application/pdf;base64,aGVsbG8="}`} {
		apiCall(t, app, "POST", path, body, owner, "", 422)
	}
	d := apiCall(t, app, "POST", path, `{"fileName":" Proof.pdf ","mimeType":"application/pdf","dataUrl":"data:application/pdf;base64,aGVsbG8="}`, owner, "", 200)["data"].(map[string]any)
	if d["fileName"] != "Proof.pdf" {
		t.Fatal(d)
	}
	rows := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	other := newShipment(t, app, owner, cid, "")
	apiCall(t, app, "DELETE", fmt.Sprintf("/api/v1/shipments/%.0f/documents/%.0f", other["id"], d["id"]), "", owner, "", 404)
	apiCall(t, app, "DELETE", fmt.Sprintf("%s/%.0f", path, d["id"]), "", owner, "", 200)
	// Base64 expansion may exceed 8 MiB; the JSON body must reach validation.
	apiCall(t, app, "POST", path, `{"fileName":"large.pdf","mimeType":"application/pdf","dataUrl":"data:application/pdf;base64,`+strings.Repeat("A", 8_500_000)+`"}`, owner, "", 200)
	apiCall(t, app, "POST", path, `{"fileName":"large.pdf","mimeType":"application/pdf","dataUrl":"data:application/pdf;base64,`+strings.Repeat("A", 12_000_000)+`"}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CANCELLED' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path, `{}`, owner, "", 422)
}

func TestConcurrentShipmentCodesPostgres(t *testing.T) {
	_, app, owner, cid := shipmentApp(t)
	var wg sync.WaitGroup
	results := make(chan map[string]any, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"customerId":%d,"origin":"Jakarta","destination":"Bandung","details":[{"description":"Box","quantity":3}]}`, cid)
			req := httptest.NewRequest("POST", "/api/v1/shipments", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+owner)
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			if err != nil {
				failures <- err
				return
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					failures <- err
				}
			}()
			var payload map[string]any
			if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				failures <- err
				return
			}
			if resp.StatusCode != 200 {
				failures <- fmt.Errorf("create=%d: %v", resp.StatusCode, payload)
				return
			}
			results <- payload["data"].(map[string]any)
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	masters, details := map[string]bool{}, map[string]bool{}
	for s := range results {
		code := s["masterCode"].(string)
		if masters[code] {
			t.Error("duplicate master code")
		}
		masters[code] = true
		for _, raw := range s["details"].([]any) {
			code := raw.(map[string]any)["detailCode"].(string)
			if details[code] {
				t.Error("duplicate package code")
			}
			details[code] = true
		}
	}
	if len(masters) != 8 || len(details) != 24 {
		t.Fatalf("created %d masters and %d details", len(masters), len(details))
	}
}

func TestShipmentMarketingAndTariffsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW());
	 INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'shipment.view','Shipment','view'),(101,'shipment.create','Shipment','create'),(102,'shipment.update','Shipment','update'),(103,'shipment_detail.view','Shipment','details');
	 INSERT INTO "UserPermission" ("userId","permissionId") SELECT 2,id FROM "Permission" WHERE id>=100;
	 UPDATE "Customer" SET "marketingPartnerId"=10 WHERE id=$1;`, cid)
	worker := loginAs(t, app, "worker", "")
	tariff := apiCall(t, app, "POST", "/api/v1/tariffs", `{"origin":"Jakarta","destination":"Bandung","customerType":"b2c","ratePerKg":1000,"effectiveFrom":"2020-01-01"}`, owner, "", 200)["data"].(map[string]any)
	s := newShipment(t, app, worker, cid, fmt.Sprintf(`,"tariffId":%.0f,"discountType":"PERCENTAGE","discountPercentage":10,"details":[{"description":"Box","actualWeightKg":10}]`, tariff["id"]))
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	if s["createdByPartnerId"] != float64(10) || s["discountFundedBy"] != "MARKETING" {
		t.Fatal(s)
	}
	apiCall(t, app, "GET", path, "", worker, "", 200)
	apiCall(t, app, "POST", path+"/price", `{}`, worker, "", 422) // no wallet funds
	shipmentSQL(t, db, `INSERT INTO "Wallet" ("partnerId",balance,"updatedAt") VALUES (10,10000,NOW()) ON CONFLICT ("partnerId") DO UPDATE SET balance=10000`)
	apiCall(t, app, "POST", path+"/price", `{"discountAmount":2001}`, worker, "", 422) // exceeds Marketing's 20%
	shipmentSQL(t, db, `INSERT INTO "WithdrawalRequest" ("requestCode","partnerId",amount,"requestedById","updatedAt") VALUES ('WD-TEST',10,4000,2,NOW())`)
	apiCall(t, app, "POST", path+"/price", `{}`, worker, "", 422) // available 6000 < company share 7200
	shipmentSQL(t, db, `UPDATE "WithdrawalRequest" SET status='REJECTED' WHERE "requestCode"='WD-TEST'`)
	priced := apiCall(t, app, "POST", path+"/price", `{}`, worker, "", 200)["data"].(map[string]any)
	if priced["priceAmount"] != float64(10000) || priced["discountAmount"] != float64(1000) || priced["finalPriceAmount"] != float64(9000) {
		t.Fatal(priced)
	}
	other := newShipment(t, app, owner, cid, "")
	apiCall(t, app, "GET", fmt.Sprintf("/api/v1/shipments/%.0f", other["id"]), "", worker, "", 403)
	foreign := apiCall(t, app, "POST", "/api/v1/customers", `{"name":"Foreign"}`, owner, "", 200)["data"].(map[string]any)
	apiCall(t, app, "POST", "/api/v1/shipments", fmt.Sprintf(`{"customerId":%.0f,"origin":"A","destination":"B"}`, foreign["id"]), worker, "", 403)
	t.Run("fallback excludes another customer's contract", func(t *testing.T) {
		// Invalid selected ID is ignored only during tariff resolution, not creation.
		shipmentSQL(t, db, `UPDATE "MasterShipment" SET "tariffId"=NULL WHERE id=$1; UPDATE "Tariff" SET "isActive"=false WHERE id=$2;
		 INSERT INTO "Tariff" (origin,destination,"customerId","ratePerKg","effectiveFrom","updatedAt") VALUES ('Jakarta','Bandung',$3,99999,'2020-01-01',NOW())`, s["id"], tariff["id"], foreign["id"])
		full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
		if full["pricingPreview"] != nil {
			t.Fatal("used another customer's tariff")
		}
		generic := apiCall(t, app, "POST", "/api/v1/tariffs", `{"origin":"Jakarta","destination":"Bandung","ratePerKg":2000,"effectiveFrom":"2020-01-01"}`, owner, "", 200)["data"].(map[string]any)
		full = apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
		preview := full["pricingPreview"].(map[string]any)
		if preview["tariffId"] != generic["id"] || preview["estimatedPrice"] != float64(10000) || preview["ratePerKg"] != float64(2000) {
			t.Fatal(preview)
		}
	})
	t.Run("B2B kg pricing ignores volume and wallet coverage", func(t *testing.T) {
		c := apiCall(t, app, "POST", "/api/v1/customers", `{"name":"PIC","companyName":"B2B Company","type":"b2b","marketingPartnerId":10}`, owner, "", 200)["data"].(map[string]any)
		contract := apiCall(t, app, "POST", "/api/v1/tariffs", fmt.Sprintf(`{"origin":"A","destination":"B","customerType":"b2b","customerId":%.0f,"ratePerKg":8000,"effectiveFrom":"2020-01-01"}`, c["id"]), owner, "", 200)["data"].(map[string]any)
		apiCall(t, app, "POST", "/api/v1/shipments", fmt.Sprintf(`{"customerId":%d,"tariffId":%.0f}`, cid, contract["id"]), owner, "", 422)
		shipmentSQL(t, db, `UPDATE "Wallet" SET balance=0 WHERE "partnerId"=10`)
		x := newShipment(t, app, worker, int(c["id"].(float64)), fmt.Sprintf(`,"tariffId":%.0f,"details":[{"description":"Big light box","actualWeightKg":3.3,"volumeM3":1}]`, contract["id"]))
		result := apiCall(t, app, "POST", fmt.Sprintf("/api/v1/shipments/%.0f/price", x["id"]), `{}`, worker, "", 200)["data"].(map[string]any)
		if result["chargeableWeightKg"] != 3.3 || result["priceAmount"] != float64(26400) {
			t.Fatal(result)
		}
	})
}

func TestShipmentInvoiceAndHistoryPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	shipmentSQL(t, db, `INSERT INTO "Invoice" (id,"invoiceNumber","customerId","updatedAt") VALUES (10,'INV-TEST',$1,NOW()); INSERT INTO "InvoiceLine" ("invoiceId",description,"unitPrice","shipmentId") VALUES (10,'Shipment',100,$2)`, cid, s["id"])
	apiCall(t, app, "DELETE", path, "", owner, "", 422)
	rows := apiCall(t, app, "GET", "/api/v1/shipments?invoice=on_invoice", "", owner, "", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["invoiceLines"].([]any)[0].(map[string]any)["invoice"].(map[string]any)["invoiceNumber"] != "INV-TEST" {
		t.Fatal(rows)
	}
	shipmentSQL(t, db, `DELETE FROM "InvoiceLine" WHERE "shipmentId"=$1`, s["id"])
	for _, sql := range []string{
		`INSERT INTO "Discrepancy" ("masterId",type,description) VALUES ($1,'MISSING','missing')`,
		`INSERT INTO "HandoverScan" ("detailId","scanLevel",payload,result) SELECT id,'detail','test','ok' FROM "DetailShipment" WHERE "masterId"=$1`,
	} {
		shipmentSQL(t, db, sql, s["id"])
		apiCall(t, app, "DELETE", path, "", owner, "", 422)
		shipmentSQL(t, db, `DELETE FROM "Discrepancy" WHERE "masterId"=$1; DELETE FROM "HandoverScan" WHERE "detailId" IN (SELECT id FROM "DetailShipment" WHERE "masterId"=$1)`, s["id"])
	}
	apiCall(t, app, "DELETE", path, "", owner, "", 200)
}

func TestB2BKgWithGenericTariffPostgres(t *testing.T) {
	_, app, owner, b2cID := shipmentApp(t)
	customer := apiCall(t, app, "POST", "/api/v1/customers", `{"name":"PIC","companyName":"B2B Company","type":"b2b"}`, owner, "", 200)["data"].(map[string]any)
	tariff := apiCall(t, app, "POST", "/api/v1/tariffs", `{"origin":"Jakarta","destination":"Bandung","ratePerKg":8000,"minChargeableKg":1,"roundingUnitKg":2,"volumetricMultiplier":250,"effectiveFrom":"2020-01-01"}`, owner, "", 200)["data"].(map[string]any)
	for _, tt := range []struct {
		name       string
		customerID int
		extra      string
		kg, price  float64
		simple     bool
	}{
		{"B2B selected generic tariff", int(customer["id"].(float64)), fmt.Sprintf(`,"tariffId":%.0f`, tariff["id"]), 3.3, 26400, true},
		{"B2B fallback generic tariff", int(customer["id"].(float64)), "", 3.3, 26400, true},
		{"B2C retains volumetric billing", b2cID, "", 250, 2000000, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newShipment(t, app, owner, tt.customerID, tt.extra+`,"details":[{"description":"Large box","actualWeightKg":3.3,"lengthCm":100,"widthCm":100,"heightCm":100}]`)
			path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
			full := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
			preview := full["pricingPreview"].(map[string]any)
			if preview["simpleKg"] != tt.simple || preview["chargeableKg"] != tt.kg || preview["estimatedPrice"] != tt.price {
				t.Fatalf("preview: %v", preview)
			}
			for _, body := range []string{`{}`, fmt.Sprintf(`{"tariffId":%.0f}`, tariff["id"])} {
				priced := apiCall(t, app, "POST", path+"/price", body, owner, "", 200)["data"].(map[string]any)
				if priced["chargeableWeightKg"] != tt.kg || priced["priceAmount"] != tt.price || priced["pricing"].(map[string]any)["simpleKg"] != tt.simple {
					t.Fatalf("snapshot: %v", priced)
				}
			}
		})
	}
}
