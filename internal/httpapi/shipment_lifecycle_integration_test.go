package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestShipmentReadyAndCancelPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, "")
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	apiCall(t, app, "POST", path+"/ready", `{}`, "", "", 401)
	apiCall(t, app, "POST", path+"/ready", `{}`, worker, "", 403)
	apiCall(t, app, "POST", path+"/cancel", `{}`, worker, "", 403)
	apiCall(t, app, "POST", "/api/v1/shipments/99999/ready", `{}`, owner, "", 404)
	apiCall(t, app, "POST", "/api/v1/shipments/99999/cancel", `{}`, owner, "", 404)
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/details", `{"description":"Box"}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "priceAmount"=10000,"penerimaName"=NULL WHERE id=$1`, s["id"])
	invalid := apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 422)
	if invalid["errors"].(map[string]any)["penerimaName"] == nil {
		t.Fatal(invalid)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "penerimaName"='Receiver' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/ready", `null`, owner, "", 200)
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 422)
	for _, status := range []string{"CREATED", "READY_FOR_PICKUP", "PICKED_UP", "RECEIVED_AT_GUDANG", "IN_TRANSPORT", "AT_DEST_GUDANG", "ARRIVED_AT_GUDANG", "DELIVERED", "CANCELLED", "UNKNOWN"} {
		t.Run(status, func(t *testing.T) {
			shipmentSQL(t, db, `UPDATE "MasterShipment" SET status=$2 WHERE id=$1`, s["id"], status)
			expected := 422
			if status == "CREATED" {
				expected = 200
			}
			apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", expected)
			shipmentSQL(t, db, `UPDATE "MasterShipment" SET status=$2 WHERE id=$1`, s["id"], status)
			expected = 200
			if status == "DELIVERED" || status == "CANCELLED" || status == "UNKNOWN" {
				expected = 422
			}
			apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", expected)
		})
	}
	// Pickup creators need view + pickup.create; update is not required.
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'shipment.view','Shipment','view'),(101,'pickup.create','Pickup','create'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100),(2,101); UPDATE "MasterShipment" SET status='CREATED' WHERE id=$1`, s["id"])
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH-LIFECYCLE','Origin','Jakarta',NOW()); INSERT INTO "Employee" (id,"employeeNumber",name,"warehouseId","updatedAt") VALUES (10,'E-W','Worker',10,NOW()); UPDATE "User" SET "employeeId"=10 WHERE id=2`)
	worker = loginAs(t, app, "worker", "")
	apiCall(t, app, "POST", path+"/ready", `{}`, worker, "", 200)
	shipmentSQL(t, db, `UPDATE "Warehouse" SET city='Other' WHERE id=10; UPDATE "MasterShipment" SET status='CREATED' WHERE id=$1; INSERT INTO "Permission" (id,slug,module,description) VALUES (102,'shipment.cancel','Shipment','cancel'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,102)`, s["id"])
	for _, action := range []string{"ready", "cancel"} {
		apiCall(t, app, "POST", path+"/"+action, `{}`, worker, "", 403)
	}
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW())`)
	worker = loginAs(t, app, "worker", "")
	for _, action := range []string{"ready", "cancel"} {
		apiCall(t, app, "POST", path+"/"+action, `{}`, worker, "", 403)
	}
	// Owning Marketing partners pass scope even when attached to another warehouse.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdByPartnerId"=10 WHERE id=$1; UPDATE "Customer" SET type='b2b' WHERE id=$2`, s["id"], cid)
	apiCall(t, app, "POST", path+"/ready", `{}`, worker, "", 200)
	apiCall(t, app, "POST", path+"/cancel", `{}`, worker, "", 200)
}

func TestCancellationGuardsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, "")
	path := fmt.Sprintf("/api/v1/shipments/%.0f/cancel", s["id"])
	shipmentSQL(t, db, `INSERT INTO "Invoice" (id,"invoiceNumber","customerId","updatedAt") VALUES (10,'INV-CANCEL',$1,NOW()); INSERT INTO "InvoiceLine" ("invoiceId",description,"unitPrice","shipmentId") VALUES (10,'Shipment',100,$2)`, cid, s["id"])
	for _, status := range []string{"DRAFT", "SENT", "PARTIALLY_SETTLED", "SETTLED", "UNKNOWN"} {
		t.Run("invoice "+status, func(t *testing.T) {
			shipmentSQL(t, db, `UPDATE "Invoice" SET status=$1 WHERE id=10`, status)
			response := apiCall(t, app, "POST", path, `{}`, owner, "", 422)
			if !strings.Contains(response["message"].(string), "invoice aktif INV-CANCEL") {
				t.Fatal(response)
			}
		})
	}
	shipmentSQL(t, db, `UPDATE "Invoice" SET status='CANCELLED' WHERE id=10; INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'CANCEL-TRUCK',1000,10,NOW()); INSERT INTO "Transport" (id,"transportCode","vehicleId","updatedAt") VALUES (10,'TR-CANCEL',10,NOW()); INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,$1)`, s["id"])
	for _, status := range []string{"PLANNED", "DEPARTED"} {
		shipmentSQL(t, db, `UPDATE "Transport" SET status=$1 WHERE id=10`, status)
		response := apiCall(t, app, "POST", path, `{}`, owner, "", 422)
		if !strings.Contains(response["message"].(string), "transport aktif TR-CANCEL") {
			t.Fatal(response)
		}
	}
	shipmentSQL(t, db, `UPDATE "Transport" SET status='COMPLETED' WHERE id=10; INSERT INTO "Delivery" (id,"deliveryCode","masterId","updatedAt") VALUES (10,'DL-CANCEL',$1,NOW())`, s["id"])
	for _, status := range []string{"ASSIGNED", "IN_PROGRESS"} {
		shipmentSQL(t, db, `UPDATE "Delivery" SET status=$1 WHERE id=10`, status)
		response := apiCall(t, app, "POST", path, `{}`, owner, "", 422)
		if !strings.Contains(response["message"].(string), "delivery aktif (DL-CANCEL") {
			t.Fatal(response)
		}
	}
	shipmentSQL(t, db, `UPDATE "Delivery" SET status='CANCELLED' WHERE id=10; INSERT INTO "Payment" ("masterId",method,amount,status) VALUES ($1,'CASH',100,'VERIFIED'); INSERT INTO "Pickup" ("pickupCode","masterId","updatedAt") VALUES ('PK-CANCEL',$1,NOW())`, s["id"])
	apiCall(t, app, "POST", path, `{}`, owner, "", 200)
	var warnings string
	if err := db.pool.QueryRow(context.Background(), `SELECT "afterData" FROM "AuditLog" WHERE "entityId"=$1 AND "entityLabel" LIKE '%(warnings)'`, s["id"]).Scan(&warnings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings, "1 pembayaran terverifikasi") || !strings.Contains(warnings, "PK-CANCEL") {
		t.Fatal(warnings)
	}
	var count int
	if err := db.pool.QueryRow(context.Background(), `SELECT count(*) FROM "TrackingEvent" WHERE "masterId"=$1 AND event='CANCELLED'`, s["id"]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed guards wrote events: %d %v", count, err)
	}
}

func TestShipmentEscrowPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW()); INSERT INTO "Wallet" ("partnerId",balance,"updatedAt") VALUES (10,10000,NOW()); UPDATE "Customer" SET "marketingPartnerId"=10 WHERE id=$1`, cid)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdByPartnerId"=10,"discountFundedBy"='MARKETING',"priceAmount"=10000,"finalPriceAmount"=9000 WHERE id=$1; INSERT INTO "WithdrawalRequest" ("requestCode","partnerId",amount,"requestedById","updatedAt") VALUES ('WD-ESCROW',10,4000,2,NOW())`, s["id"])
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 422)
	check := func(balance float64, entries int, status string) {
		t.Helper()
		var gotBalance float64
		var gotCount int
		var gotStatus string
		if err := db.pool.QueryRow(context.Background(), `SELECT balance,(SELECT count(*) FROM "WalletTransaction"),(SELECT status FROM "MasterShipment" WHERE id=$1) FROM "Wallet" WHERE "partnerId"=10`, s["id"]).Scan(&gotBalance, &gotCount, &gotStatus); err != nil {
			t.Fatal(err)
		}
		if gotBalance != balance || gotCount != entries || gotStatus != status {
			t.Fatalf("balance=%v count=%d status=%s", gotBalance, gotCount, gotStatus)
		}
	}
	check(10000, 0, "CREATED")
	shipmentSQL(t, db, `UPDATE "WithdrawalRequest" SET status='REJECTED' WHERE "requestCode"='WD-ESCROW'; CREATE FUNCTION fail_lifecycle_tracking() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event IN ('READY_FOR_PICKUP','CANCELLED') THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_lifecycle_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_lifecycle_tracking()`)
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 500)
	check(10000, 0, "CREATED")
	shipmentSQL(t, db, `DROP TRIGGER fail_lifecycle_tracking ON "TrackingEvent"`)
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 200)
	check(2800, 1, "READY_FOR_PICKUP")
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 422)
	shipmentSQL(t, db, `CREATE TRIGGER fail_lifecycle_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_lifecycle_tracking()`)
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 500)
	check(2800, 1, "READY_FOR_PICKUP")
	shipmentSQL(t, db, `DROP TRIGGER fail_lifecycle_tracking ON "TrackingEvent"`)
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 200)
	check(10000, 2, "CANCELLED")
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 422)
	check(10000, 2, "CANCELLED")
	var delta float64
	if err := db.pool.QueryRow(context.Background(), `SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END) FROM "WalletTransaction"`).Scan(&delta); err != nil || delta != 0 {
		t.Fatalf("ledger not conserved: %v %v", delta, err)
	}
	// B2B never fronts the company share, even at zero wallet balance.
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b' WHERE id=$1; UPDATE "Wallet" SET balance=0 WHERE "partnerId"=10`, cid)
	b2b := newShipment(t, app, owner, cid, `,"details":[{"description":"B2B"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdByPartnerId"=10,"discountFundedBy"='MARKETING',"priceAmount"=10000 WHERE id=$1`, b2b["id"])
	apiCall(t, app, "POST", fmt.Sprintf("/api/v1/shipments/%.0f/ready", b2b["id"]), `{}`, owner, "", 200)
	check(0, 2, "CANCELLED")
}

func TestConcurrentShipmentEscrowPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW()); INSERT INTO "Wallet" ("partnerId",balance,"updatedAt") VALUES (10,10000,NOW())`)
	paths := []string{}
	for range 2 {
		s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
		shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdByPartnerId"=10,"discountFundedBy"='MARKETING',"priceAmount"=10000 WHERE id=$1`, s["id"])
		paths = append(paths, fmt.Sprintf("/api/v1/shipments/%.0f/ready", s["id"]))
	}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, path := range paths {
		wg.Go(func() {
			req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+owner)
			resp, err := app.Test(req)
			if err != nil {
				results <- 0
				return
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			results <- resp.StatusCode
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 200 {
			success++
		} else if code != 409 && code != 422 {
			t.Fatalf("unexpected result %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("expected one affordable escrow, got %d", success)
	}
	var balance float64
	var count int
	if err := db.pool.QueryRow(context.Background(), `SELECT balance,(SELECT count(*) FROM "WalletTransaction") FROM "Wallet" WHERE "partnerId"=10`).Scan(&balance, &count); err != nil || balance != 2000 || count != 1 {
		t.Fatalf("balance=%v count=%d err=%v", balance, count, err)
	}
	// Persisted ledger records reconcile before/after amounts with the wallet.
	var raw []byte
	if err := db.pool.QueryRow(context.Background(), `SELECT to_jsonb(t) FROM "WalletTransaction" t`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	if ledger["balanceBefore"] != float64(10000) || ledger["balanceAfter"] != float64(2000) || ledger["direction"] != "DEBIT" {
		t.Fatal(ledger)
	}
}

func TestLegacyPartialEscrowRecoveryPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW()); INSERT INTO "Wallet" (id,"partnerId",balance,"updatedAt") VALUES (10,10,2000,NOW())`)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	// Legacy ready debited before changing status; Go must reuse its hold even
	// when the remaining balance would not cover a second company-share debit.
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdByPartnerId"=10,"discountFundedBy"='MARKETING',"priceAmount"=10000 WHERE id=$1;
 INSERT INTO "WalletTransaction" ("walletId",type,amount,direction,"balanceBefore","balanceAfter","referenceType","referenceId","businessRef") VALUES (10,'SHIPMENT_ESCROW',8000,'DEBIT',10000,2000,'shipment',$1,$2)`, s["id"], fmt.Sprintf("ESCROW-%.0f", s["id"]))
	apiCall(t, app, "POST", path+"/ready", `{}`, owner, "", 200)
	var balance float64
	var count int
	if err := db.pool.QueryRow(context.Background(), `SELECT balance,(SELECT count(*) FROM "WalletTransaction") FROM "Wallet" WHERE id=10`).Scan(&balance, &count); err != nil || balance != 2000 || count != 1 {
		t.Fatalf("duplicate hold: %v %d %v", balance, count, err)
	}
	// A pre-existing refund is also an exactly-once ledger operation.
	shipmentSQL(t, db, `UPDATE "Wallet" SET balance=10000 WHERE id=10; INSERT INTO "WalletTransaction" ("walletId",type,amount,direction,"balanceBefore","balanceAfter","referenceType","referenceId","businessRef") VALUES (10,'SHIPMENT_ESCROW',8000,'CREDIT',2000,10000,'shipment',$1,$2)`, s["id"], fmt.Sprintf("ESCROW-REFUND-%.0f", s["id"]))
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 200)
	if err := db.pool.QueryRow(context.Background(), `SELECT balance,(SELECT count(*) FROM "WalletTransaction") FROM "Wallet" WHERE id=10`).Scan(&balance, &count); err != nil || balance != 10000 || count != 2 {
		t.Fatalf("duplicate refund: %v %d %v", balance, count, err)
	}
}
