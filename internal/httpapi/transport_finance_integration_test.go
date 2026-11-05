package httpapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTransportExpenseLifecyclePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, `,"trackingMode":"ACTIVITY"`), owner, "", 200)["data"].(map[string]any)
	path := transportPath(tr)
	ep := path + "/expenses"
	for _, bad := range []string{`{}`, `{"type":"BBM","amount":0}`, `{"type":"BBM","amount":-1}`, `{"type":"OTHER","amount":10}`, `{"type":"BBM","amount":10,"photos":[null]}`, `{"type":"BBM","amount":10,"latitude":null,"longitude":0,"locationAccuracy":1}`, `{"type":"BBM","amount":10,"photo":"data:image/png;base64,AAAA"}`, `{"type":"BBM","amount":10,"photos":["data:image/png;base64,AAAA"]}`} {
		apiCall(t, app, "POST", ep, bad, owner, "", 422)
	}
	photo := handoverPhoto(t)
	raw, _ := json.Marshal(map[string]any{"type": "BBM", "amount": 100.25, "description": "Receipt", "photos": []string{photo, photo}, "locationLabel": "Original capture"})
	e := apiCall(t, app, "POST", ep, string(raw), owner, "", 200)["data"].(map[string]any)
	expensePath := fmt.Sprintf("%s/%.0f", ep, e["id"])
	if e["hasPhoto"] != true || e["photoCount"] != float64(1) || e["photoUrls"] != nil || e["photoUrl"] != nil {
		t.Fatal(e)
	}
	list := apiCall(t, app, "GET", ep, "", worker, "", 200)
	if list["meta"].(map[string]any)["summary"].(map[string]any)["total"] != float64(0) {
		t.Fatal(list)
	}
	detail := apiCall(t, app, "GET", expensePath, "", owner, "", 200)["data"].(map[string]any)
	if detail["photoUrls"].([]any)[0] != photo || detail["locationLabel"] != "Original capture" {
		t.Fatal(detail)
	}
	// Correcting text/amount preserves original evidence; changing photos clears capture metadata.
	for _, bad := range []string{`{"amount":null}`, `{"type":null}`, `{"amount":0}`} {
		apiCall(t, app, "PUT", expensePath, bad, owner, "", 422)
	}
	apiCall(t, app, "PUT", expensePath, `{"amount":120.5,"description":"Corrected"}`, owner, "", 200)
	detail = apiCall(t, app, "GET", expensePath, "", owner, "", 200)["data"].(map[string]any)
	if detail["locationLabel"] != "Original capture" {
		t.Fatal(detail)
	}
	apiCall(t, app, "PUT", expensePath, `{"removePhoto":true}`, owner, "", 200)
	detail = apiCall(t, app, "GET", expensePath, "", owner, "", 200)["data"].(map[string]any)
	if detail["locationLabel"] != nil || len(detail["photoUrls"].([]any)) != 0 {
		t.Fatal(detail)
	}
	raw, _ = json.Marshal(map[string]any{"photos": []string{photo}})
	apiCall(t, app, "PUT", expensePath, string(raw), owner, "", 200)
	approved := apiCall(t, app, "POST", expensePath+"/approve", `{}`, owner, "", 200)["data"].(map[string]any)
	if approved["approvedByUsername"] != "owner" || approved["approvedByRole"] != "Owner" || approved["status"] != "APPROVED" || approved["photoCount"] != float64(1) {
		t.Fatal(approved)
	}
	apiCall(t, app, "POST", expensePath+"/approve", `{}`, owner, "", 409)
	apiCall(t, app, "PUT", expensePath, `{"amount":1}`, owner, "", 409)
	apiCall(t, app, "DELETE", expensePath, `{}`, worker, "", 403)
	apiCall(t, app, "POST", expensePath+"/cancel", `{}`, owner, "", 422)
	list = apiCall(t, app, "GET", ep, "", worker, "", 200)
	if list["meta"].(map[string]any)["summary"].(map[string]any)["total"] != 120.5 {
		t.Fatal(list)
	}
	// Reversal audit must fail the whole transaction when persistence fails.
	shipmentSQL(t, db, `CREATE FUNCTION reject_transport_reversal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='cancel' AND NEW."entityType"='transport_expense' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_transport_reversal BEFORE INSERT ON "AuditLog" FOR EACH ROW EXECUTE FUNCTION reject_transport_reversal()`)
	apiCall(t, app, "POST", expensePath+"/cancel", `{"reason":"Wrong receipt"}`, owner, "", 500)
	detail = apiCall(t, app, "GET", expensePath, "", owner, "", 200)["data"].(map[string]any)
	if detail["status"] != "APPROVED" {
		t.Fatal("failed reversal committed", detail)
	}
	shipmentSQL(t, db, `DROP TRIGGER reject_transport_reversal ON "AuditLog"`)
	apiCall(t, app, "POST", expensePath+"/cancel", `{"reason":"Wrong receipt"}`, owner, "", 200)
	apiCall(t, app, "POST", expensePath+"/cancel", `{"reason":"Again"}`, owner, "", 409)
	apiCall(t, app, "DELETE", expensePath, `{"reason":"Remove corrected receipt"}`, owner, "", 200)
	apiCall(t, app, "GET", expensePath, "", owner, "", 404)
	e = apiCall(t, app, "POST", ep, `{"type":"TOL","amount":10}`, owner, "", 200)["data"].(map[string]any)
	expensePath = fmt.Sprintf("%s/%.0f", ep, e["id"])
	apiCall(t, app, "DELETE", expensePath, `{}`, owner, "", 200)
	e = apiCall(t, app, "POST", ep, `{"type":"MAKAN","amount":20}`, owner, "", 200)["data"].(map[string]any)
	expensePath = fmt.Sprintf("%s/%.0f", ep, e["id"])
	shipmentSQL(t, db, `UPDATE "Transport" SET status='COMPLETED' WHERE id=$1`, tr["id"])
	apiCall(t, app, "POST", ep, `{"type":"TOL","amount":10}`, owner, "", 409)
	apiCall(t, app, "PUT", expensePath, `{"amount":30}`, owner, "", 409)
	// Approval remains available for pre-completion pending costs.
	apiCall(t, app, "POST", expensePath+"/approve", `{}`, owner, "", 200)
	var reasons int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityType"='transport_expense' AND action IN ('cancel','remove') AND "afterData" LIKE '%reason%'`).Scan(&reasons); err != nil || reasons != 2 {
		t.Fatal(reasons, err)
	}
}
func TestTransportSettlementAtomicPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"companyPercent","partnerPercent","updatedAt") VALUES (10,2,'VEHICLE_OWNER',30,70,NOW()); UPDATE "Vehicle" SET "ownerId"=10 WHERE id IN (10,11)`)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"priceAmount"=1000 WHERE id=$1`, s["id"])
	tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, `,"trackingMode":"ACTIVITY"`), owner, "", 200)["data"].(map[string]any)
	path := transportPath(tr)
	apiCall(t, app, "POST", path+"/settle", `{}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Transport" SET status='ARRIVED',"arrivedAt"=NOW() WHERE id=$1`, tr["id"])
	apiCall(t, app, "POST", path+"/settle", `{"transportValue":-1}`, owner, "", 422)
	shipmentSQL(t, db, `CREATE FUNCTION reject_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'settlement unavailable'; END $$; CREATE TRIGGER reject_settlement BEFORE INSERT ON "TransportSettlement" FOR EACH ROW EXECUTE FUNCTION reject_settlement()`)
	apiCall(t, app, "POST", path+"/settle", `{}`, owner, "", 500)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "WalletTransaction"`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed settlement left a credit", count, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER reject_settlement ON "TransportSettlement"`)
	statuses := concurrentTransportCalls(t, app, owner, []string{path + "/settle", path + "/settle"}, `{}`)
	if (statuses[0] != 200 || statuses[1] != 422) && (statuses[1] != 200 || statuses[0] != 422) {
		t.Fatal(statuses)
	}
	var balance, amount float64
	if err := db.pool.QueryRow(t.Context(), `SELECT w.balance,s."ownerAmount",(SELECT count(*) FROM "WalletTransaction") FROM "Wallet" w JOIN "TransportSettlement" s ON s."ownerId"=w."partnerId"`).Scan(&balance, &amount, &count); err != nil || balance != 700 || amount != 700 || count != 1 {
		t.Fatal(balance, amount, count, err)
	}
	shipmentSQL(t, db, `UPDATE "Transport" SET status='COMPLETED' WHERE id=$1`, tr["id"])
	// Concurrent different transports credit the same wallet without losing either amount.
	shipmentSQL(t, db, `INSERT INTO "Transport" (id,"transportCode","vehicleId",status,"updatedAt") VALUES (90,'SETTLE-A',10,'ARRIVED',NOW()),(91,'SETTLE-B',11,'ARRIVED',NOW())`)
	statuses = concurrentTransportCalls(t, app, owner, []string{"/api/v1/transports/90/settle", "/api/v1/transports/91/settle"}, `{"transportValue":100}`)
	if statuses[0] != 200 || statuses[1] != 200 {
		t.Fatal(statuses)
	}
	if err := db.pool.QueryRow(t.Context(), `SELECT balance FROM "Wallet" WHERE "partnerId"=10`).Scan(&balance); err != nil || balance != 840 {
		t.Fatal(balance, err)
	}
}
func TestTransportPlanningConcurrencyAndScopePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	body := transportBody(s, `,"trackingMode":"ACTIVITY"`)
	statuses := concurrentTransportCalls(t, app, owner, []string{"/api/v1/transports", "/api/v1/transports"}, body)
	if (statuses[0] != 200 || statuses[1] != 422) && (statuses[1] != 200 || statuses[0] != 422) {
		t.Fatal(statuses)
	}
	list := apiCall(t, app, "GET", "/api/v1/transports", "", owner, "", 200)["data"].([]any)
	if len(list) != 1 {
		t.Fatal(list)
	}
	tr := list[0].(map[string]any)
	path := transportPath(tr)
	shipmentSQL(t, db, `UPDATE "Transport" SET "driverId"=11 WHERE id=$1`, tr["id"])
	apiCall(t, app, "GET", path, "", worker, "", 403)
	if got := apiCall(t, app, "GET", "/api/v1/transports", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	shipmentSQL(t, db, `INSERT INTO "Role" (id,name,slug) VALUES (10,'Driver','driver'); INSERT INTO "UserRole" ("userId","roleId") VALUES (2,10)`)
	apiCall(t, app, "GET", path+"/expenses", "", worker, "", 403)
	shipmentSQL(t, db, `DELETE FROM "UserRole" WHERE "roleId"=10; INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,202)`)
	apiCall(t, app, "GET", path, "", worker, "", 200)
	apiCall(t, app, "PUT", path, `{"warehouseStaffId":11}`, worker, "", 403)
	apiCall(t, app, "PUT", path, `{"warehouseStaffId":10}`, worker, "", 200)
	shipmentSQL(t, db, `UPDATE "Route" SET origin='Other',destination='Elsewhere' WHERE id=10; UPDATE "MasterShipment" SET origin='Other',destination='Elsewhere',"originWarehouseId"=NULL,"destinationWarehouseId"=NULL WHERE id=$1`, s["id"])
	apiCall(t, app, "GET", path, "", worker, "", 403)
	apiCall(t, app, "GET", path+"/expenses", "", worker, "", 403)
	if got := apiCall(t, app, "GET", "/api/v1/transports", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 200)
	var status string
	if err := db.pool.QueryRow(t.Context(), `SELECT status FROM "MasterShipment" WHERE id=$1`, s["id"]).Scan(&status); err != nil || status != "RECEIVED_AT_GUDANG" {
		t.Fatal(status, err)
	}
	apiCall(t, app, "GET", path, "", owner, "", 404)
	apiCall(t, app, "POST", "/api/v1/transports", strings.Replace(body, `"vehicleId":10`, `"vehicleId":11`, 1), worker, "", 403)
}
