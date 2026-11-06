package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/identity"
)

func newTestPartner(t *testing.T, app *fiber.App, owner, username string) map[string]any {
	t.Helper()
	u := apiCall(t, app, "POST", "/api/v1/users", fmt.Sprintf(`{"username":%q,"name":"Partner","password":"pässword-123","roleIds":[3]}`, username), owner, "", 200)["data"].(map[string]any)
	return u["partner"].(map[string]any)
}

func TestPartnerListPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	apiCall(t, app, "GET", "/api/v1/partners", "", "", "", 401)
	apiCall(t, app, "GET", "/api/v1/partners", "", worker, "", 403)
	first := newTestPartner(t, app, owner, "first-partner")
	second := newTestPartner(t, app, owner, "second-partner")
	shipmentSQL(t, db, `UPDATE "Partner" SET type='VEHICLE_OWNER',"isActive"=false WHERE id=$1; DELETE FROM "Wallet"`, second["id"])
	rows := apiCall(t, app, "GET", "/api/v1/partners?type=MARKETING", "", owner, "", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != first["id"] {
		t.Fatal(rows)
	}
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Wallet"`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// Two list requests can lazily create the same missing wallet safely.
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			req := httptest.NewRequest("GET", "/api/v1/partners", nil)
			req.Header.Set("Authorization", "Bearer "+owner)
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			if err != nil {
				t.Error(err)
				return
			}
			if resp.StatusCode != 200 {
				t.Error(resp.StatusCode)
			}
			if err := resp.Body.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Wallet"`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,"updatedAt") VALUES (10,'WH-P','Partner Warehouse',NOW()); UPDATE "Partner" SET "warehouseId"=10,"bankName"='Bank',notes='Note' WHERE id=$1; UPDATE "Wallet" SET balance=100 WHERE "partnerId"=$1`, first["id"])
	for i, tx := range []struct {
		kind, direction string
		amount          float64
	}{
		{"TRANSPORT_PROFIT_SHARE", "CREDIT", 20.125}, {"COMMISSION", "CREDIT", 10.125},
		{"REPAIR_DEDUCTION", "DEBIT", 3}, {"WITHDRAWAL", "DEBIT", 4},
		{"TOPUP", "CREDIT", 500}, {"COMMISSION", "DEBIT", 9},
	} {
		shipmentSQL(t, db, `INSERT INTO "WalletTransaction" ("walletId",type,amount,direction,"balanceBefore","balanceAfter","businessRef") SELECT id,$2,$3,$4,0,0,$5 FROM "Wallet" WHERE "partnerId"=$1`, first["id"], tx.kind, tx.amount, tx.direction, fmt.Sprintf("LEDGER-%d", i))
	}
	for i, status := range []string{"PENDING", "APPROVED", "PROCESSING", "COMPLETED", "REJECTED", "CANCELLED"} {
		shipmentSQL(t, db, `INSERT INTO "WithdrawalRequest" ("requestCode","partnerId",amount,status,"requestedById","updatedAt") VALUES ($1,$2,10,$3,1,NOW())`, fmt.Sprintf("WD-%d", i), first["id"], status)
	}
	rows = apiCall(t, app, "GET", "/api/v1/partners", "", owner, "", 200)["data"].([]any)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	row := rows[0].(map[string]any)
	wallet := row["wallet"].(map[string]any)
	totals := row["totals"].(map[string]any)
	earnings := row["earningsSummary"].(map[string]any)
	if row["warehouseId"] != float64(10) || row["warehouseName"] != "Partner Warehouse" || row["userActive"] != true || row["username"] != "first-partner" || row["bank"].(map[string]any)["bankName"] != "Bank" {
		t.Fatal(row)
	}
	if wallet["balance"] != float64(100) || wallet["reserved"] != float64(30) || wallet["available"] != float64(70) || wallet["partnerId"] != first["id"] || wallet["walletId"] == nil {
		t.Fatal(wallet)
	}
	if totals["transportEarnings"] != 20.125 || totals["commissions"] != 10.125 || totals["repairDeductions"] != float64(3) || totals["withdrawals"] != float64(4) || earnings["total"] != 30.25 || row["counts"].(map[string]any)["withdrawals"] != float64(6) {
		t.Fatal(row)
	}
	if inactive := rows[1].(map[string]any); inactive["isActive"] != false || inactive["warehouseId"] != nil || inactive["warehouseName"] != nil || inactive["wallet"].(map[string]any)["balance"] != float64(0) {
		t.Fatal(inactive)
	}
	shipmentSQL(t, db, `UPDATE "Wallet" SET balance=5 WHERE "partnerId"=$1`, first["id"])
	rows = apiCall(t, app, "GET", "/api/v1/partners?type=MARKETING", "", owner, "", 200)["data"].([]any)
	if rows[0].(map[string]any)["wallet"].(map[string]any)["available"] != float64(0) {
		t.Fatal(rows)
	}
	if rows = apiCall(t, app, "GET", "/api/v1/partners?type=unknown", "", owner, "", 200)["data"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
	raw, err := json.Marshal(row)
	if err != nil || strings.Contains(string(raw), "passwordHash") || strings.Contains(string(raw), "employeeId") {
		t.Fatal(string(raw), err)
	}
}

func TestPartnerSettingsPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	p := newTestPartner(t, app, owner, "partner")
	worker := loginAs(t, app, "worker", "")
	path := fmt.Sprintf("/api/v1/partners/%.0f", p["id"])
	apiCall(t, app, "PUT", path, `{}`, "", "", 401)
	apiCall(t, app, "PUT", path, `{}`, worker, "", 403)
	apiCall(t, app, "PUT", "/api/v1/partners/99999", `{}`, owner, "", 404)
	shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,"isActive","updatedAt") VALUES (10,'WH-A','Active',true,NOW()),(11,'WH-I','Inactive',false,NOW())`)
	for _, body := range []string{`{"companyPercent":0}`, `{"partnerPercent":-1}`, `{"companyPercent":79}`, `{"warehouseId":"bad"}`, `{"warehouseId":1.5}`, `{"warehouseId":{}}`, `{"warehouseId":99999}`, `{"warehouseId":11}`} {
		apiCall(t, app, "PUT", path, body, owner, "", 422)
	}
	updated := apiCall(t, app, "PUT", path, `{"companyPercent":"70","partnerPercent":"30","warehouseId":"10","notes":" Updated ","isActive":"false","userId":999,"balance":999}`, owner, "", 200)["data"].(map[string]any)
	if updated["companyPercent"] != float64(70) || updated["partnerPercent"] != float64(30) || updated["warehouseId"] != float64(10) || updated["notes"] != "Updated" || updated["isActive"] != false || updated["userId"] != p["userId"] {
		t.Fatal(updated)
	}
	for _, body := range []string{`null`, `{`, `{"companyPercent":"NaN","partnerPercent":null,"notes":""}`} {
		got := apiCall(t, app, "PUT", path, body, owner, "", 200)["data"].(map[string]any)
		if got["companyPercent"] != float64(70) || got["partnerPercent"] != float64(30) || got["notes"] != "Updated" || got["isActive"] != false {
			t.Fatal(got)
		}
	}
	for _, value := range []string{`null`, `""`, `"general"`, `"none"`} {
		got := apiCall(t, app, "PUT", path, `{"warehouseId":`+value+`}`, owner, "", 200)["data"].(map[string]any)
		if got["warehouseId"] != nil {
			t.Fatal(got)
		}
	}
	var after string
	if err := db.pool.QueryRow(t.Context(), `SELECT "afterData" FROM "AuditLog" WHERE "entityType"='partner' AND action='updated' AND "afterData"::jsonb->'after'->>'warehouseId'='10'`).Scan(&after); err != nil || !strings.Contains(after, `"companyPercent":70`) {
		t.Fatal(after, err)
	}
}

func TestPartnerProfilePostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	p := newTestPartner(t, app, owner, "partner")
	worker, partnerToken := loginAs(t, app, "worker", ""), loginAs(t, app, "partner", "")
	path := fmt.Sprintf("/api/v1/partners/%.0f/profile", p["id"])
	for _, method := range []string{"PUT", "DELETE"} {
		apiCall(t, app, method, path, `{}`, "", "", 401)
		apiCall(t, app, method, path, `{}`, worker, "", 403)
		apiCall(t, app, method, "/api/v1/partners/99999/profile", `{}`, owner, "", 404)
	}
	for _, body := range []string{`{}`, `null`, `{`, `{"name":"Partner","password":" "}`} {
		got := apiCall(t, app, "PUT", path, body, owner, "", 200)["data"].(map[string]any)
		if got["unchanged"] != true || got["id"] != p["id"] {
			t.Fatal(got)
		}
	}
	for _, body := range []string{`{"name":null}`, `{"name":" "}`, `{"password":"short"}`} {
		apiCall(t, app, "PUT", path, body, owner, "", 422)
	}
	got := apiCall(t, app, "PUT", path, `{"name":" Updated ","password":" changed-password ","username":"forged","isOwner":true,"employeeId":999}`, owner, "", 200)["data"].(map[string]any)
	if got["name"] != "Updated" || got["username"] != "partner" || got["passwordHash"] != nil || got["unchanged"] != nil {
		t.Fatal(got)
	}
	var hash string
	var isOwner bool
	if err := db.pool.QueryRow(t.Context(), `SELECT "passwordHash","isOwner" FROM "User" WHERE id=$1`, p["userId"]).Scan(&hash, &isOwner); err != nil || isOwner || !identity.VerifyPassword(" changed-password ", hash) {
		t.Fatal("password or trusted fields differ", err)
	}
	logs, err := db.pool.Query(t.Context(), `SELECT "afterData" FROM "AuditLog" WHERE "entityType"='partner' AND action IN ('updated_profile','change_password')`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for logs.Next() {
		var after string
		if err := logs.Scan(&after); err != nil || strings.Contains(after, "changed-password") || strings.Contains(after, hash) {
			t.Fatal("password leaked to audit", err)
		}
		count++
	}
	if err := logs.Err(); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	logs.Close()
	// Disable + session revocation must roll back together on a session failure.
	shipmentSQL(t, db, `CREATE FUNCTION fail_partner_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_partner_revoke BEFORE DELETE ON "SessionToken" FOR EACH ROW EXECUTE FUNCTION fail_partner_revoke()`)
	for _, method := range []string{"PUT", "DELETE"} {
		apiCall(t, app, method, path, `{"isActive":false}`, owner, "", 500)
		apiCall(t, app, "GET", "/api/v1/auth/me", "", partnerToken, "", 200)
	}
	shipmentSQL(t, db, `DROP TRIGGER fail_partner_revoke ON "SessionToken"`)
	apiCall(t, app, "PUT", path, `{"isActive":false}`, owner, "", 200)
	apiCall(t, app, "GET", "/api/v1/auth/me", "", partnerToken, "", 401)
	apiCall(t, app, "PUT", path, `{"isActive":true,"password":"login-password"}`, owner, "", 200)
	partnerToken = apiCall(t, app, "POST", "/api/v1/auth/login", `{"username":"partner","password":"login-password"}`, "", "", 200)["data"].(map[string]any)["token"].(string)
	disabled := apiCall(t, app, "DELETE", path, `{}`, owner, "", 200)["data"].(map[string]any)
	if disabled["disabled"] != true || disabled["userId"] != p["userId"] {
		t.Fatal(disabled)
	}
	apiCall(t, app, "GET", "/api/v1/auth/me", "", partnerToken, "", 401)
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "SessionToken" WHERE "userId"=$1`, p["userId"]).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Partner" p JOIN "Wallet" w ON w."partnerId"=p.id WHERE p.id=$1 AND p."isActive"`, p["id"]).Scan(&count); err != nil || count != 1 {
		t.Fatal("historical partner/wallet changed", count, err)
	}
	shipmentSQL(t, db, `UPDATE "User" SET "isOwner"=true WHERE id=$1`, p["userId"])
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 422)
}
