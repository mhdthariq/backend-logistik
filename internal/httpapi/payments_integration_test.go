package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestPaymentReviewPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "priceAmount"=100,"finalPriceAmount"=80 WHERE id=$1`, s["id"])
	create := func(amount int) map[string]any {
		t.Helper()
		return apiCall(t, app, "POST", fmt.Sprintf("/api/v1/shipments/%.0f/payments", s["id"]), fmt.Sprintf(`{"method":"CASH","amount":%d}`, amount), owner, "", 200)["data"].(map[string]any)
	}
	p := create(30)
	path := fmt.Sprintf("/api/v1/payments/%.0f", p["id"])
	for _, action := range []string{"verify", "reject"} {
		apiCall(t, app, "POST", path+"/"+action, `{}`, "", "", 401)
		apiCall(t, app, "POST", path+"/"+action, `{}`, worker, "", 403)
		apiCall(t, app, "POST", "/api/v1/payments/99999/"+action, `{}`, owner, "", 404)
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,'payment.verify','Payment','verify'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120)`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "originWarehouseId"=11,origin='Bandung' WHERE id=$1`, s["id"])
	for _, action := range []string{"verify", "reject"} {
		apiCall(t, app, "POST", path+"/"+action, `{}`, worker, "", 403)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "originWarehouseId"=10,origin='Jakarta' WHERE id=$1`, s["id"])
	for _, tc := range []struct{ name, sql, message string }{
		{"b2b", `UPDATE "Customer" SET type='b2b'`, "Pembayaran B2B melalui invoice, bukan per shipment."},
		{"cancelled", `UPDATE "MasterShipment" SET status='CANCELLED'`, "Shipment dibatalkan; pembayaran tidak dapat diverifikasi."},
		{"unpriced", `UPDATE "MasterShipment" SET "priceAmount"=NULL,"finalPriceAmount"=NULL`, "Pembayaran melebihi sisa tagihan atau harga belum dihitung."},
		{"excess", `UPDATE "MasterShipment" SET "finalPriceAmount"=29`, "Pembayaran melebihi sisa tagihan atau harga belum dihitung."},
		{"zero", `UPDATE "Payment" SET amount=0`, "Pembayaran melebihi sisa tagihan atau harga belum dihitung."},
		{"negative", `UPDATE "Payment" SET amount=-1`, "Pembayaran melebihi sisa tagihan atau harga belum dihitung."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shipmentSQL(t, db, tc.sql)
			out := apiCall(t, app, "POST", path+"/verify", `{}`, owner, "", 422)
			if out["message"] != tc.message {
				t.Fatal(out)
			}
			shipmentSQL(t, db, `UPDATE "Customer" SET type='b2c'; UPDATE "MasterShipment" SET status='CREATED',"priceAmount"=100,"finalPriceAmount"=80; UPDATE "Payment" SET amount=30`)
		})
	}
	shipmentSQL(t, db, `CREATE FUNCTION fail_verification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='VERIFIED' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_verification BEFORE UPDATE ON "Payment" FOR EACH ROW EXECUTE FUNCTION fail_verification()`)
	apiCall(t, app, "POST", path+"/verify", `{}`, owner, "", 500)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Payment" WHERE status='RECORDED'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_verification ON "Payment"`)
	verified := apiCall(t, app, "POST", path+"/verify", `{"status":"REJECTED","verifiedById":999}`, worker, "", 200)["data"].(map[string]any)
	if verified["status"] != "VERIFIED" || verified["verifiedById"] != float64(2) || verified["verifiedAt"] == nil || len(verified) != len(p) {
		t.Fatal(verified)
	}
	if stamp, ok := verified["verifiedAt"].(string); !ok || len(stamp) != 24 {
		t.Fatal(verified)
	}
	for _, action := range []string{"verify", "reject"} {
		apiCall(t, app, "POST", path+"/"+action, `{}`, owner, "", 422)
	}
	p = create(50)
	path = fmt.Sprintf("/api/v1/payments/%.0f", p["id"])
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "finalPriceAmount"=70 WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/verify", `{}`, owner, "", 422)
	// Rejection is allowed even if the shipment was cancelled or changed to B2B.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CANCELLED'; UPDATE "Customer" SET type='b2b'`)
	rejected := apiCall(t, app, "POST", path+"/reject", `{"reason":" Duplicate ","verifiedById":999}`, worker, "", 200)["data"].(map[string]any)
	if rejected["status"] != "REJECTED" || rejected["verifiedById"] != float64(2) {
		t.Fatal(rejected)
	}
	apiCall(t, app, "POST", path+"/reject", `{}`, owner, "", 422)
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityType"='payment' AND action='status_change' AND ("entityLabel" LIKE '% → VERIFIED' OR "afterData"::jsonb->>'reason'='Duplicate')`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestPaymentReviewConcurrentPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, "")
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "priceAmount"=100 WHERE id=$1`, s["id"])
	paths := []string{}
	for range 2 {
		p := apiCall(t, app, "POST", fmt.Sprintf("/api/v1/shipments/%.0f/payments", s["id"]), `{"method":"CASH","amount":60}`, owner, "", 200)["data"].(map[string]any)
		paths = append(paths, fmt.Sprintf("/api/v1/payments/%.0f/verify", p["id"]))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for _, path := range paths {
		wg.Go(func() {
			<-start
			req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+owner)
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
			if response.StatusCode == http.StatusConflict {
				var body map[string]any
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["message"] != "Pembayaran berubah bersamaan. Muat ulang lalu coba lagi." {
					t.Error(body, err)
				}
			}
			statuses <- response.StatusCode
		})
	}
	close(start)
	wg.Wait()
	close(statuses)
	successes := 0
	for status := range statuses {
		if status == 200 {
			successes++
		} else if status != 409 && status != 422 {
			t.Fatal(status)
		}
	}
	var paid float64
	if err := db.pool.QueryRow(t.Context(), `SELECT sum(amount) FROM "Payment" WHERE status='VERIFIED'`).Scan(&paid); err != nil || paid != 60 || successes != 1 {
		t.Fatal(paid, successes, err)
	}
	// Concurrent reviews of the same payment can credit the remaining balance once.
	shipmentSQL(t, db, `UPDATE "Payment" SET amount=40 WHERE status='RECORDED'`)
	var id int
	if err := db.pool.QueryRow(t.Context(), `SELECT id FROM "Payment" WHERE status='RECORDED'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/payments/%d/verify", id)
	results := concurrentPosts(t, app, path, `{}`, owner, 2)
	slices.Sort(results)
	if len(results) != 2 || results[0] != 200 || (results[1] != 409 && results[1] != 422) {
		t.Fatal(results)
	}
	apiCall(t, app, "POST", path, `{}`, owner, "", 422)
	if err := db.pool.QueryRow(t.Context(), `SELECT sum(amount) FROM "Payment" WHERE status='VERIFIED'`).Scan(&paid); err != nil || paid != 100 {
		t.Fatal(paid, err)
	}
}

func TestUnpaidPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	apiCall(t, app, "GET", "/api/v1/unpaid", "", "", "", 401)
	apiCall(t, app, "GET", "/api/v1/unpaid", "", worker, "", 403)
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,'payment.unpaid.view','Payment','unpaid'),(121,'shipment.direct.manage','Shipment','direct'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120),(2,121)`)
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11`)
	other := newShipment(t, app, owner, cid, `,"originWarehouseId":11`)
	unpriced := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "priceAmount"=100,"discountAmount"=20,"finalPriceAmount"=NULL WHERE id<>$1; UPDATE "MasterShipment" SET "fulfillmentMode"='DIRECT',origin='Bandung' WHERE id=$2`, unpriced["id"], other["id"])
	for _, p := range []struct {
		status string
		amount int
	}{{"VERIFIED", 30}, {"RECORDED", 20}, {"REJECTED", 99}} {
		shipmentSQL(t, db, `INSERT INTO "Payment" ("masterId",method,amount,status,reference,"recordedById","createdAt") VALUES ($1,'CASH',$2,$3,'Ref',2,'2026-11-01T00:00:00Z')`, s["id"], p.amount, p.status)
	}
	rows := apiCall(t, app, "GET", "/api/v1/unpaid", "", worker, "", 200)["data"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	row := rows[0].(map[string]any)
	if len(row) != 13 || row["id"] != s["id"] || row["finalPriceAmount"] != float64(80) || row["paidAmount"] != float64(30) || row["pendingAmount"] != float64(20) || row["remainingAmount"] != float64(30) || row["customerName"] != "Sender" || row["customerPhone"] != "123" {
		t.Fatal(row)
	}
	payments := row["payments"].([]any)
	if len(payments) != 3 {
		t.Fatal(payments)
	}
	p := payments[0].(map[string]any)
	if len(p) != 7 || p["recordedByName"] != "Worker" || p["reference"] != "Ref" || p["createdAt"] != "2026-11-01T00:00:00.000Z" {
		t.Fatal(p)
	}
	if rows = apiCall(t, app, "GET", "/api/v1/unpaid", "", owner, "", 200)["data"].([]any); len(rows) != 2 {
		t.Fatal(rows)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "finalPriceAmount"=40 WHERE id=$1`, s["id"])
	rows = apiCall(t, app, "GET", "/api/v1/unpaid", "", worker, "", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["remainingAmount"] != float64(0) {
		t.Fatal(rows)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='AT_DEST_GUDANG',"arrivedWarehouseId"=11 WHERE id=$1`, s["id"])
	if rows = apiCall(t, app, "GET", "/api/v1/unpaid", "", worker, "", 200)["data"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CANCELLED' WHERE id=$1; UPDATE "User" SET "employeeId"=NULL WHERE id=2`, s["id"])
	if rows = apiCall(t, app, "GET", "/api/v1/unpaid", "", worker, "", 200)["data"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b'`)
	if rows = apiCall(t, app, "GET", "/api/v1/unpaid", "", owner, "", 200)["data"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
}
