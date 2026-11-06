package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func TestShipmentPaymentsAndNotificationPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f", s["id"])
	for _, route := range []struct{ method, suffix string }{{"GET", "payments"}, {"POST", "payments"}, {"POST", "notify-marketing"}} {
		apiCall(t, app, route.method, path+"/"+route.suffix, `{}`, "", "", 401)
		apiCall(t, app, route.method, path+"/"+route.suffix, `{}`, worker, "", 403)
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,'payment.view','Payment','view'),(121,'payment.record','Payment','record'),(122,'shipment.notify_marketing','Shipment','notify'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120),(2,122)`)
	if got := apiCall(t, app, "GET", path+"/payments", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	apiCall(t, app, "POST", path+"/notify-marketing", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/payments", `{"method":"CASH","amount":10}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "priceAmount"=100,"finalPriceAmount"=80 WHERE id=$1`, s["id"])
	for _, body := range []string{`null`, `{`, `{}`, `{"method":"CARD","amount":1}`, `{"method":"CASH","amount":0}`, `{"method":"CASH","amount":"NaN"}`, `{"method":"CASH","amount":81}`, `{"method":"CASH","amount":1,"proof":true}`, `{"method":"CASH","amount":1,"proof":"javascript:alert(1)"}`} {
		apiCall(t, app, "POST", path+"/payments", body, owner, "", 422)
	}
	shipmentSQL(t, db, `INSERT INTO "Role" (id,slug,name) VALUES (10,'admin-gudang','Admin Gudang'); INSERT INTO "UserRole" ("userId","roleId") VALUES (2,10)`)
	p := apiCall(t, app, "POST", path+"/payments", `{"method":"TRANSFER","amount":"30","status":"VERIFIED","recordedById":999,"proof":"data:image/jpeg;base64,QUJD","reference":" REF "}`, worker, "", 200)["data"].(map[string]any)
	if p["status"] != "RECORDED" || p["recordedById"] != float64(2) || p["reference"] != "REF" || p["proofUrl"] != "data:image/jpeg;base64,QUJD" {
		t.Fatal(p)
	}
	list := apiCall(t, app, "GET", path+"/payments", "", worker, "", 200)["data"].([]any)
	row := list[0].(map[string]any)
	if row["recordedBy"].(map[string]any)["name"] != "Worker" || len(row["recordedBy"].(map[string]any)) != 1 || row["verifiedBy"] != nil {
		t.Fatal(row)
	}
	shipmentSQL(t, db, `UPDATE "Payment" SET status='VERIFIED',"verifiedById"=1 WHERE id=$1`, p["id"])
	apiCall(t, app, "POST", path+"/payments", `{"method":"CASH","amount":51}`, owner, "", 422)
	notice := apiCall(t, app, "POST", path+"/notify-marketing", `{"note":" Call sender "}`, worker, "", 200)["data"].(map[string]any)
	if notice["remainingAmount"] != float64(50) || notice["notified"] != true {
		t.Fatal(notice)
	}
	var description string
	if err := db.pool.QueryRow(t.Context(), `SELECT description FROM "TrackingEvent" WHERE "masterId"=$1 AND event='GUDANG_NOTIFY_MARKETING'`, s["id"]).Scan(&description); err != nil || description != "Gudang memberi tahu marketing: sisa pembayaran Rp50 - Call sender" {
		t.Fatal(description, err)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "originWarehouseId"=11,origin='Bandung' WHERE id=$1`, s["id"])
	apiCall(t, app, "GET", path+"/payments", "", worker, "", 403)
	apiCall(t, app, "POST", path+"/payments", `{"method":"CASH","amount":1}`, worker, "", 403)
	apiCall(t, app, "POST", path+"/notify-marketing", `{}`, worker, "", 403)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "originWarehouseId"=10,origin='Jakarta',status='CANCELLED' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", path+"/payments", `{"method":"CASH","amount":1}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CREATED' WHERE id=$1; UPDATE "Customer" SET type='b2b' WHERE id=$2`, s["id"], cid)
	apiCall(t, app, "POST", path+"/payments", `{"method":"CASH","amount":1}`, worker, "", 403)
	apiCall(t, app, "POST", path+"/payments", `{"method":"CASH","amount":1}`, owner, "", 422)
	apiCall(t, app, "GET", "/api/v1/shipments/9999/payments", "", owner, "", 404)
	apiCall(t, app, "POST", "/api/v1/shipments/no/payments", `{"method":"CASH","amount":1}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Payment" SET amount=80 WHERE id=$1`, p["id"])
	apiCall(t, app, "POST", path+"/notify-marketing", `{}`, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Payment" SET amount=30 WHERE id=$1; CREATE FUNCTION fail_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event='GUDANG_NOTIFY_MARKETING' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_notification BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_notification()`, p["id"])
	apiCall(t, app, "POST", path+"/notify-marketing", `{}`, owner, "", 500)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='notified_marketing'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestShipmentExpenseLifecyclePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10`)
	path := fmt.Sprintf("/api/v1/shipments/%.0f/operational-expenses", s["id"])
	for _, method := range []string{"GET", "POST"} {
		apiCall(t, app, method, path, `{}`, "", "", 401)
		apiCall(t, app, method, path, `{}`, worker, "", 403)
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,'shipment.view','Shipment','view'),(121,'transport.expense.view','Expense','view'),(122,'shipment.expense.create','Expense','create'),(123,'transport.expense.approve','Expense','approve'),(124,'transport.expense.delete','Expense','delete'),(125,'transport.expense.delete-approved','Expense','remove'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120),(2,121),(2,122)`)
	apiCall(t, app, "POST", path, `{"type":"BBM","amount":10}`, worker, "", 409)
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b' WHERE id=$1`, cid)
	for _, status := range []string{"CREATED", "PICKED_UP", "IN_TRANSPORT", "AT_DEST_GUDANG", "ARRIVED_AT_GUDANG", "DELIVERED", "CANCELLED"} {
		shipmentSQL(t, db, `UPDATE "MasterShipment" SET status=$1 WHERE id=$2`, status, s["id"])
		apiCall(t, app, "POST", path, `{"type":"BBM","amount":10}`, owner, "", 409)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	for _, body := range []string{`{}`, `null`, `{"type":"NO","amount":10}`, `{"type":"BBM","amount":"NaN"}`, `{"type":"BBM","amount":0}`, `{"type":"BBM","amount":-1}`} {
		apiCall(t, app, "POST", path, body, owner, "", 422)
	}
	e := apiCall(t, app, "POST", path, `{"type":"BBM","amount":"12.5","description":" fuel ","status":"APPROVED","createdById":999}`, worker, "", 200)["data"].(map[string]any)
	if e["status"] != "PENDING" || e["createdById"] != float64(2) || e["expenseCode"] != "EXP-2026-000001" || e["description"] != "fuel" {
		t.Fatal(e)
	}
	ep := fmt.Sprintf("%s/%.0f", path, e["id"])
	apiCall(t, app, "POST", ep, `{"action":"approve"}`, worker, "", 403)
	apiCall(t, app, "DELETE", ep, `{}`, worker, "", 403)
	report := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if report["pendingTotal"] != 12.5 || report["approvedTotal"] != float64(0) || report["canCreate"] != true || len(report["rows"].([]any)[0].(map[string]any)) != 13 {
		t.Fatal(report)
	}
	apiCall(t, app, "POST", ep, `{"action":"invalid"}`, owner, "", 422)
	apiCall(t, app, "POST", ep, `{"action":"cancel","reason":"bad"}`, owner, "", 409)
	approval := apiCall(t, app, "POST", ep, `{"action":"approve"}`, owner, "", 200)["data"].(map[string]any)
	if approval["status"] != "APPROVED" || approval["approvedByRole"] != "Owner" || approval["approvedByUsername"] != "owner" {
		t.Fatal(approval)
	}
	if stamp, ok := approval["approvedAt"].(string); !ok || len(stamp) != 24 {
		t.Fatal("Prisma millisecond timestamp", approval)
	}
	apiCall(t, app, "POST", ep, `{"action":"approve"}`, owner, "", 409)
	apiCall(t, app, "POST", ep, `{"action":"cancel"}`, owner, "", 422)
	apiCall(t, app, "DELETE", ep, `{"reason":"remove"}`, worker, "", 403)
	shipmentSQL(t, db, `CREATE FUNCTION fail_expense_reversal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW."entityType"='transport_expense' AND NEW.action IN ('cancel','remove') THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_expense_reversal BEFORE INSERT ON "AuditLog" FOR EACH ROW EXECUTE FUNCTION fail_expense_reversal()`)
	apiCall(t, app, "POST", ep, `{"action":"cancel","reason":"wrong"}`, owner, "", 500)
	apiCall(t, app, "DELETE", ep, `{"reason":"wrong"}`, owner, "", 500)
	var status string
	if err := db.pool.QueryRow(t.Context(), `SELECT status FROM "TransportExpense" WHERE id=$1`, e["id"]).Scan(&status); err != nil || status != "APPROVED" {
		t.Fatal(status, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_expense_reversal ON "AuditLog"`)
	apiCall(t, app, "POST", ep, `{"action":"cancel","reason":" Duplicate charge "}`, owner, "", 200)
	report = apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if report["approvedTotal"] != float64(0) || report["pendingTotal"] != float64(0) {
		t.Fatal(report)
	}
	apiCall(t, app, "DELETE", ep, `{"reason":" Remove duplicate "}`, owner, "", 200)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityType"='transport_expense' AND action IN ('cancel','remove') AND "afterData"::jsonb->>'reason' IN ('Duplicate charge','Remove duplicate')`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	apiCall(t, app, "DELETE", ep, `{}`, owner, "", 404)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='ARRIVED_AT_GUDANG',"destReceivedAt"=NOW(),"arrivedWarehouseId"=11 WHERE id=$1`, s["id"])
	apiCall(t, app, "GET", path, "", worker, "", 403)
	apiCall(t, app, "POST", path, `{"type":"TOL","amount":1}`, worker, "", 403)
	created := apiCall(t, app, "POST", path, `{"type":"TOL","amount":1}`, owner, "", 200)["data"].(map[string]any)
	apiCall(t, app, "DELETE", fmt.Sprintf("%s/%.0f", path, created["id"]), `{}`, owner, "", 200)
	apiCall(t, app, "POST", "/api/v1/shipments/9999/operational-expenses", `{"type":"BBM","amount":10}`, owner, "", 404)
	apiCall(t, app, "GET", "/api/v1/shipments/no/operational-expenses", "", owner, "", 422)
}

// Collect actual HTTP statuses so concurrent mutation failures cannot pass silently.
func concurrentPosts(t *testing.T, app *fiber.App, path, body, token string, n int) []int {
	t.Helper()
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			if err != nil {
				t.Error(err)
				statuses <- 0
				return
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			statuses <- resp.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	result := []int{}
	for code := range statuses {
		result = append(result, code)
	}
	return result
}

func TestShipmentFinanceConcurrencyPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, "")
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b' WHERE id=$1; UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$2`, cid, s["id"])
	path := fmt.Sprintf("/api/v1/shipments/%.0f/operational-expenses", s["id"])
	for _, status := range concurrentPosts(t, app, path, `{"type":"BBM","amount":10}`, owner, 2) {
		if status != 200 {
			t.Fatal(status)
		}
	}
	var count, distinct int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT "expenseCode") FROM "TransportExpense"`).Scan(&count, &distinct); err != nil || count != 2 || distinct != 2 {
		t.Fatal(count, distinct, err)
	}
	var eid int
	if err := db.pool.QueryRow(t.Context(), `SELECT min(id) FROM "TransportExpense"`).Scan(&eid); err != nil {
		t.Fatal(err)
	}
	statuses := concurrentPosts(t, app, fmt.Sprintf("%s/%d", path, eid), `{"action":"approve"}`, owner, 2)
	slices.Sort(statuses)
	if statuses[0] != 200 || statuses[1] != 409 {
		t.Fatal(statuses)
	}
}

func TestShipmentFinanceTenantIsolationPostgres(t *testing.T) {
	a, b := disposableDB(t), disposableDB(t)
	for _, db := range []testDatabase{a, b} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Customer" (id,code,name,type,"updatedAt") VALUES (10,'C','Customer','b2b',NOW()); INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"priceAmount","updatedAt") VALUES (10,'S',10,'A','B','RECEIVED_AT_GUDANG',100,NOW())`)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		db := a
		if r.URL.Query().Get("KdCustomer") == "B" {
			db = b
		}
		u, err := url.Parse(db.dsn)
		if err != nil {
			t.Error(err)
			return
		}
		if err = json.NewEncoder(w).Encode(map[string]any{"Status": "Ok", "Data": map[string]any{"KdCustomer": r.URL.Query().Get("KdCustomer"), "NamaCustomer": db.name, "Server": u.Host, "Db": db.name, "Username": u.User.Username()}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	registry := tenant.New(tenant.Config{Mode: "production", LicenseURL: server.URL, LicenseKey: "test", LicenseToken: "test", LicenseApplication: "test", MaxPools: 2})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	ownerA, ownerB := loginAs(t, app, "owner", "A"), loginAs(t, app, "owner", "B")
	apiCall(t, app, "POST", "/api/v1/shipments/10/operational-expenses", `{"type":"BBM","amount":10}`, ownerA, "A", 200)
	apiCall(t, app, "POST", "/api/v1/shipments/10/notify-marketing", `{}`, ownerA, "A", 200)
	for _, route := range []struct {
		path  string
		total float64
	}{{"operational-expenses", 0}} {
		out := apiCall(t, app, "GET", "/api/v1/shipments/10/"+route.path, "", ownerB, "B", 200)["data"].(map[string]any)
		if out["pendingTotal"] != route.total || len(out["rows"].([]any)) != 0 {
			t.Fatal(out)
		}
	}
	var count int
	if err := b.pool.QueryRow(t.Context(), `SELECT count(*) FROM "TrackingEvent"`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	apiCall(t, app, "GET", "/api/v1/shipments/10/payments", "", ownerA, "B", 401)
	for _, db := range []testDatabase{a, b} {
		shipmentSQL(t, db, `UPDATE "Customer" SET type='b2c'; INSERT INTO "Payment" (id,"masterId",method,amount,status,"recordedById") VALUES (10,10,'CASH',30,'RECORDED',1)`)
	}
	apiCall(t, app, "POST", "/api/v1/payments/10/verify", `{}`, ownerA, "A", 200)
	rows := apiCall(t, app, "GET", "/api/v1/unpaid", "", ownerB, "B", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["paidAmount"] != float64(0) || rows[0].(map[string]any)["pendingAmount"] != float64(30) {
		t.Fatal(rows)
	}
	apiCall(t, app, "POST", "/api/v1/payments/10/reject", `{"reason":"Wrong transfer"}`, ownerB, "B", 200)
	rows = apiCall(t, app, "GET", "/api/v1/unpaid", "", ownerA, "A", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["paidAmount"] != float64(30) || rows[0].(map[string]any)["pendingAmount"] != float64(0) {
		t.Fatal(rows)
	}
	apiCall(t, app, "POST", "/api/v1/payments/10/reject", `{}`, ownerA, "B", 401)
	for _, db := range []testDatabase{a, b} {
		shipmentSQL(t, db, `INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,2,'MARKETING',NOW())`)
	}
	apiCall(t, app, "PUT", "/api/v1/partners/10", `{"companyPercent":60,"partnerPercent":40}`, ownerA, "A", 200)
	apiCall(t, app, "PUT", "/api/v1/partners/10/profile", `{"name":"Tenant A Partner"}`, ownerA, "A", 200)
	apiCall(t, app, "DELETE", "/api/v1/partners/10/profile", `{}`, ownerA, "A", 200)
	partners := apiCall(t, app, "GET", "/api/v1/partners", "", ownerB, "B", 200)["data"].([]any)
	if len(partners) != 1 {
		t.Fatal(partners)
	}
	partner := partners[0].(map[string]any)
	if partner["name"] != "Worker" || partner["userActive"] != true || partner["profitShare"].(map[string]any)["partner"] != float64(20) {
		t.Fatal(partner)
	}
	var wallets int
	if err := a.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Wallet"`).Scan(&wallets); err != nil || wallets != 0 {
		t.Fatal("tenant B listing created a wallet in A", wallets, err)
	}
}
