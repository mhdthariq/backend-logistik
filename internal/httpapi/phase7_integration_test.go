package httpapi_test

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestPhase7RouteAuthenticationPostgres(t *testing.T) {
	_, app, _, _ := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	f, err := os.Open("../../docs/route-inventory.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	parameter := regexp.MustCompile(`\[[^]]+\]`)
	for _, row := range rows[1:] {
		if row[0] != "7" {
			continue
		}
		t.Run(row[1]+" "+row[2], func(t *testing.T) {
			path := parameter.ReplaceAllString(row[2], "99999")
			apiCall(t, app, row[1], path, `{}`, "", "", 401)
			apiCall(t, app, row[1], path, `{}`, worker, "", 403)
		})
	}
}

func grantFinance(t *testing.T, db testDatabase, user any, permissions ...string) {
	t.Helper()
	for _, slug := range permissions {
		shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) SELECT COALESCE(max(id),0)+1,$1,'Finance',$1 FROM "Permission" ON CONFLICT (slug) DO NOTHING`, slug)
		shipmentSQL(t, db, `INSERT INTO "UserPermission" ("userId","permissionId") SELECT $1,id FROM "Permission" WHERE slug=$2 ON CONFLICT DO NOTHING`, user, slug)
	}
}
func ownFinancePartner(t *testing.T, db testDatabase, app *fiber.App, owner, name, kind string) (map[string]any, string) {
	t.Helper()
	p := newTestPartner(t, app, owner, name)
	shipmentSQL(t, db, `UPDATE "Partner" SET type=$2,"bankName"='Bank',"bankAccountName"='Partner',"bankAccountNumber"='12345' WHERE id=$1`, p["id"], kind)
	grantFinance(t, db, p["userId"], "wallet.view_own", "wallet.transaction.view_own", "wallet.withdrawal.create", "vehicle.view_own", "transport.view_own_vehicles")
	return p, loginAs(t, app, name, "")
}
func assertWalletBalance(t *testing.T, db testDatabase, id any, want float64) {
	t.Helper()
	var balance float64
	if err := db.pool.QueryRow(t.Context(), `SELECT balance FROM "Wallet" WHERE "partnerId"=$1`, id).Scan(&balance); err != nil || balance != want {
		t.Fatalf("wallet balance %g, want %g: %v", balance, want, err)
	}
}
func TestPhase7WalletWorkflowsPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	p, partner := ownFinancePartner(t, db, app, owner, "marketing", "MARKETING")
	_, other := ownFinancePartner(t, db, app, owner, "other", "MARKETING")
	pid := p["id"]
	apiCall(t, app, "GET", "/api/v1/wallet", "", owner, "", 403)
	apiCall(t, app, "GET", "/api/v1/wallet", "", "", "", 401)
	apiCall(t, app, "POST", "/api/v1/wallet/adjustments", `{}`, worker, "", 403)
	apiCall(t, app, "GET", "/api/v1/topups", "", worker, "", 403)
	apiCall(t, app, "GET", "/api/v1/withdrawals", "", worker, "", 403)
	summary := apiCall(t, app, "GET", "/api/v1/wallet", "", partner, "", 200)["data"].(map[string]any)
	if summary["balance"] != float64(0) || summary["partnerType"] != "MARKETING" {
		t.Fatal(summary)
	}
	for _, body := range []string{`{}`, fmt.Sprintf(`{"partnerId":%.0f,"amount":9999,"proofUrl":"data:image/png;base64,AAA="}`, pid), fmt.Sprintf(`{"partnerId":%.0f,"amount":10000}`, pid)} {
		apiCall(t, app, "POST", "/api/v1/topups", body, owner, "", 422)
	}
	create := func() map[string]any {
		return apiCall(t, app, "POST", "/api/v1/topups", fmt.Sprintf(`{"partnerId":%.0f,"amount":10000,"proofUrl":"data:image/png;base64,AAA="}`, pid), owner, "", 200)["data"].(map[string]any)["topUp"].(map[string]any)
	}
	top := create()
	path := fmt.Sprintf("/api/v1/topups/%.0f", top["id"])
	apiCall(t, app, "POST", path+"/verify", `{}`, partner, "", 403)
	rows := apiCall(t, app, "GET", "/api/v1/topups?status=PENDING_VERIFICATION", "", other, "", 200)["data"].(map[string]any)["topUps"].([]any)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	rows = apiCall(t, app, "GET", "/api/v1/topups", "", partner, "", 200)["data"].(map[string]any)["topUps"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	apiCall(t, app, "POST", path+"/verify", `{}`, owner, "", 200)
	assertWalletBalance(t, db, pid, 10000)
	apiCall(t, app, "POST", path+"/verify", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/cancel", `{}`, owner, "", 422)
	for _, action := range []string{"reject", "cancel"} {
		top = create()
		apiCall(t, app, "POST", fmt.Sprintf("/api/v1/topups/%.0f/%s", top["id"], action), `{"reason":"Wrong"}`, owner, "", 200)
	}
	assertWalletBalance(t, db, pid, 10000)
	apiCall(t, app, "POST", "/api/v1/wallet/adjustments", fmt.Sprintf(`{"partnerId":%.0f,"amount":1,"reason":"Correction"}`, pid), owner, "", 200)
	assertWalletBalance(t, db, pid, 10001)
	w := apiCall(t, app, "POST", "/api/v1/withdrawals", `{"amount":6000}`, partner, "", 200)["data"].(map[string]any)
	wp := fmt.Sprintf("/api/v1/withdrawals/%.0f", w["id"])
	apiCall(t, app, "POST", "/api/v1/withdrawals", `{"amount":5000}`, partner, "", 422)
	apiCall(t, app, "POST", wp+"/cancel", `{}`, other, "", 403)
	apiCall(t, app, "POST", wp+"/process", `{}`, owner, "", 422)
	apiCall(t, app, "POST", "/api/v1/wallet/adjustments", fmt.Sprintf(`{"partnerId":%.0f,"amount":5000,"direction":"DEBIT","reason":"Correction","respectReservation":true}`, pid), owner, "", 422)
	apiCall(t, app, "POST", wp+"/approve", `{}`, owner, "", 200)
	apiCall(t, app, "POST", wp+"/process", `{"proofUrl":"proof"}`, owner, "", 200)
	apiCall(t, app, "POST", wp+"/cancel", `{}`, partner, "", 422)
	apiCall(t, app, "POST", wp+"/complete", `{}`, owner, "", 200)
	apiCall(t, app, "POST", wp+"/complete", `{}`, owner, "", 422)
	assertWalletBalance(t, db, pid, 4001)
	for _, action := range []string{"cancel", "reject", "fail"} {
		w = apiCall(t, app, "POST", "/api/v1/withdrawals", `{"amount":100}`, partner, "", 200)["data"].(map[string]any)
		token := owner
		if action == "cancel" {
			token = partner
		}
		apiCall(t, app, "POST", fmt.Sprintf("/api/v1/withdrawals/%.0f/%s", w["id"], action), `{}`, token, "", 200)
	}
	assertWalletBalance(t, db, pid, 4001)
	rows = apiCall(t, app, "GET", "/api/v1/wallet/transactions?type=WITHDRAWAL&direction=DEBIT", "", partner, "", 200)["data"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	rows = apiCall(t, app, "GET", "/api/v1/withdrawals", "", other, "", 200)["data"].([]any)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	apiCall(t, app, "GET", "/api/v1/topups?partnerType=MARKETING", "", owner, "", 200)
	apiCall(t, app, "GET", "/api/v1/withdrawals?status=COMPLETED&partnerType=MARKETING", "", owner, "", 200)
	// A failing ledger insert must roll back both workflow status and balance.
	top = create()
	shipmentSQL(t, db, `CREATE FUNCTION reject_finance_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger unavailable'; END $$; CREATE TRIGGER reject_finance_ledger BEFORE INSERT ON "WalletTransaction" FOR EACH ROW EXECUTE FUNCTION reject_finance_ledger()`)
	apiCall(t, app, "POST", fmt.Sprintf("/api/v1/topups/%.0f/verify", top["id"]), `{}`, owner, "", 500)
	assertWalletBalance(t, db, pid, 4001)
	var status string
	if err := db.pool.QueryRow(t.Context(), `SELECT status FROM "TopUpRequest" WHERE id=$1`, top["id"]).Scan(&status); err != nil || status != "PENDING_VERIFICATION" {
		t.Fatal(status, err)
	}
}

func concurrentFinance(t *testing.T, app *fiber.App, token, path, body string) []int {
	t.Helper()
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 2 {
		wg.Go(func() {
			<-start
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			if err != nil {
				t.Error(err)
				return
			}
			statuses[i] = resp.StatusCode
			if err := resp.Body.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	return statuses
}
func TestPhase7WalletConcurrencyPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	p, partner := ownFinancePartner(t, db, app, owner, "marketing", "MARKETING")
	apiCall(t, app, "POST", "/api/v1/wallet/adjustments", fmt.Sprintf(`{"partnerId":%.0f,"amount":10000,"reason":"Seed"}`, p["id"]), owner, "", 200)
	statuses := concurrentFinance(t, app, partner, "/api/v1/withdrawals", `{"amount":6000}`)
	slices.Sort(statuses)
	if statuses[0] != 200 || statuses[1] != 422 {
		t.Fatal(statuses)
	}
	top := apiCall(t, app, "POST", "/api/v1/topups", fmt.Sprintf(`{"partnerId":%.0f,"amount":10000,"proofUrl":"data:image/png;base64,AA=="}`, p["id"]), owner, "", 200)["data"].(map[string]any)["topUp"].(map[string]any)
	statuses = concurrentFinance(t, app, owner, fmt.Sprintf("/api/v1/topups/%.0f/verify", top["id"]), `{}`)
	slices.Sort(statuses)
	if statuses[0] != 200 || statuses[1] != 422 {
		t.Fatal(statuses)
	}
	assertWalletBalance(t, db, p["id"], 20000)
	statuses = concurrentFinance(t, app, owner, "/api/v1/wallet/adjustments", fmt.Sprintf(`{"partnerId":%.0f,"amount":500,"reason":"Concurrent credit"}`, p["id"]))
	if statuses[0] != 200 || statuses[1] != 200 {
		t.Fatal(statuses)
	}
	assertWalletBalance(t, db, p["id"], 21000)
	rows := apiCall(t, app, "GET", "/api/v1/withdrawals?status=PENDING", ``, partner, "", 200)["data"].([]any)
	wp := fmt.Sprintf("/api/v1/withdrawals/%.0f", rows[0].(map[string]any)["id"])
	apiCall(t, app, "POST", wp+"/approve", `{}`, owner, "", 200)
	statuses = concurrentFinance(t, app, owner, wp+"/complete", `{}`)
	slices.Sort(statuses)
	if statuses[0] != 200 || statuses[1] != 422 {
		t.Fatal(statuses)
	}
	assertWalletBalance(t, db, p["id"], 15000)
}

func TestPhase7InvoicesPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	p, partner := ownFinancePartner(t, db, app, owner, "marketing", "MARKETING")
	worker := loginAs(t, app, "worker", "")
	grantFinance(t, db, 1, "invoice.settlement.update", "invoice.settlement.delete")
	apiCall(t, app, "POST", "/api/v1/invoices", `{}`, worker, "", 403)
	apiCall(t, app, "POST", "/api/v1/invoices", fmt.Sprintf(`{"customerId":%d,"lines":[{"description":"Item","unitPrice":100}]}`, cid), owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b',"companyName"='B2B' WHERE id=$1`, cid)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"createdByPartnerId"=$2 WHERE id=$1`, s["id"], p["id"])
	body := fmt.Sprintf(`{"customerId":%d,"lines":[{"description":"Shipment","quantity":1,"unitPrice":100,"shipmentId":%.0f}]}`, cid, s["id"])
	apiCall(t, app, "POST", "/api/v1/invoices", body, owner, "", 422)
	tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, ``), owner, "", 200)["data"].(map[string]any)
	shipmentSQL(t, db, `UPDATE "Transport" SET status='ARRIVED' WHERE id=$1`, tr["id"])
	apiCall(t, app, "POST", "/api/v1/invoices", strings.Replace(body, `"unitPrice":100`, `"unitPrice":1e308`, 1), owner, "", 422)
	i := apiCall(t, app, "POST", "/api/v1/invoices", body, owner, "", 200)["data"].(map[string]any)
	ip := fmt.Sprintf("/api/v1/invoices/%.0f", i["id"])
	commission := i["commission"].(map[string]any)
	if commission["commissionAmount"] != float64(20) {
		t.Fatal(commission)
	}
	apiCall(t, app, "POST", "/api/v1/invoices", body, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "Partner" SET "companyPercent"=10,"partnerPercent"=90 WHERE id=$1`, p["id"])
	apiCall(t, app, "PUT", ip, `{"discount":10,"notes":"Terms"}`, owner, "", 200)
	line := apiCall(t, app, "POST", ip+"/lines", `{"description":"Extra","unitPrice":10}`, owner, "", 200)["data"].(map[string]any)
	apiCall(t, app, "DELETE", fmt.Sprintf("/api/v1/invoice-lines/%.0f", line["id"]), ``, owner, "", 200)
	detail := apiCall(t, app, "GET", ip, ``, owner, "", 200)["data"].(map[string]any)
	if detail["totalAmount"] != float64(90) || detail["commission"].(map[string]any)["commissionAmount"] != float64(18) {
		t.Fatal(detail)
	}
	list := apiCall(t, app, "GET", "/api/v1/invoices?status=DRAFT&search=B2B", ``, owner, "", 200)["data"].([]any)
	if len(list) != 1 {
		t.Fatal(list)
	}
	apiCall(t, app, "POST", ip+"/print-log", `{}`, owner, "", 200)
	apiCall(t, app, "POST", ip+"/settlements", `{"amount":90,"method":"TRANSFER"}`, owner, "", 422)
	apiCall(t, app, "POST", ip+"/send", `{}`, owner, "", 200)
	apiCall(t, app, "PUT", ip, `{"discount":0}`, owner, "", 422)
	for _, bad := range []string{`{"amount":0,"method":"CASH"}`, `{"amount":100,"method":"CASH"}`, `{"amount":1,"method":"BAD"}`, `{"amount":1,"pph":2,"method":"CASH"}`, `{"amount":1,"method":"CASH","proofUrl":"https://bad"}`, `{"amount":1,"method":"CASH","settledAt":"garbage"}`} {
		apiCall(t, app, "POST", ip+"/settlements", bad, owner, "", 422)
	}
	first := apiCall(t, app, "POST", ip+"/settlements", `{"amount":30,"pph":5,"method":"TRANSFER"}`, owner, "", 200)["data"].(map[string]any)
	if first["invoice"].(map[string]any)["status"] != "PARTIALLY_SETTLED" || first["commissionReleased"] != false {
		t.Fatal(first)
	}
	finance := apiCall(t, app, "GET", "/api/v1/finance/summary", ``, owner, "", 200)["data"].(map[string]any)
	if finance["invoicePayments"].(map[string]any)["partialTotal"] != float64(30) || finance["customerIncome"].(map[string]any)["b2bOutstanding"] != float64(60) {
		t.Fatal(finance)
	}
	full := apiCall(t, app, "POST", ip+"/settlements", `{"amount":60,"pph":10,"method":"TRANSFER","proofUrl":"data:application/pdf;base64,AA=="}`, owner, "", 200)["data"].(map[string]any)
	if full["commissionReleased"] != true {
		t.Fatal(full)
	}
	assertWalletBalance(t, db, p["id"], 18)
	sp := fmt.Sprintf("%s/settlements/%.0f", ip, full["settlement"].(map[string]any)["id"])
	apiCall(t, app, "PUT", sp, `{"amount":50,"method":"CASH"}`, owner, "", 200)
	assertWalletBalance(t, db, p["id"], 0)
	apiCall(t, app, "PUT", sp, `{"amount":60,"method":"CASH"}`, owner, "", 200)
	assertWalletBalance(t, db, p["id"], 18)
	apiCall(t, app, "POST", "/api/v1/withdrawals", `{"amount":18}`, partner, "", 200)
	apiCall(t, app, "DELETE", sp, ``, owner, "", 422)
	assertWalletBalance(t, db, p["id"], 18)
	shipmentSQL(t, db, `UPDATE "WithdrawalRequest" SET status='CANCELLED' WHERE "partnerId"=$1`, p["id"])
	apiCall(t, app, "DELETE", sp, ``, owner, "", 200)
	assertWalletBalance(t, db, p["id"], 0)
	rows := apiCall(t, app, "GET", "/api/v1/commissions?status=PENDING", ``, partner, "", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["paidAmount"] != float64(30) {
		t.Fatal(rows)
	}
	apiCall(t, app, "GET", "/api/v1/commissions", ``, owner, "", 403)
	apiCall(t, app, "DELETE", ip, ``, owner, "", 422)
	apiCall(t, app, "DELETE", fmt.Sprintf("%s/settlements/%.0f", ip, first["settlement"].(map[string]any)["id"]), ``, owner, "", 200)
	apiCall(t, app, "DELETE", ip, ``, owner, "", 200)
	apiCall(t, app, "GET", ip, ``, owner, "", 404)
	// Unlinked and zero-total invoices carry no partner commission.
	z := apiCall(t, app, "POST", "/api/v1/invoices", fmt.Sprintf(`{"customerId":%d,"lines":[{"description":"Free","unitPrice":0}]}`, cid), owner, "", 200)["data"].(map[string]any)
	if z["commission"] != nil {
		t.Fatal(z)
	}
	zp := fmt.Sprintf("/api/v1/invoices/%.0f", z["id"])
	apiCall(t, app, "PUT", zp, `{"lines":[{"description":"Free replacement","unitPrice":0}]}`, owner, "", 200)
	apiCall(t, app, "POST", zp+"/send", `{}`, owner, "", 200)
	apiCall(t, app, "POST", zp+"/settlements", `{"amount":0,"method":"CASH"}`, owner, "", 200)
	apiCall(t, app, "POST", zp+"/settlements", `{"amount":0,"method":"CASH"}`, owner, "", 422)
	account := apiCall(t, app, "POST", "/api/v1/bank-accounts", `{"bankName":"Bank","accountNumber":"1234","accountName":"Company"}`, owner, "", 200)["data"].(map[string]any)
	bankInvoice := apiCall(t, app, "POST", "/api/v1/invoices", fmt.Sprintf(`{"customerId":%d,"bankAccountId":%.0f,"lines":[{"description":"Item","unitPrice":100}]}`, cid, account["id"]), owner, "", 200)["data"].(map[string]any)
	shipmentSQL(t, db, `UPDATE "BankAccount" SET "accountNumber"='CHANGED' WHERE id=$1`, account["id"])
	bi := fmt.Sprintf("/api/v1/invoices/%.0f", bankInvoice["id"])
	bankInvoice = apiCall(t, app, "GET", bi, ``, owner, "", 200)["data"].(map[string]any)
	if bankInvoice["paymentAccountNumber"] != "1234" {
		t.Fatal(bankInvoice)
	}
	apiCall(t, app, "POST", bi+"/send", `{}`, owner, "", 200)
	statuses := concurrentFinance(t, app, owner, bi+"/settlements", `{"amount":75,"method":"TRANSFER"}`)
	success := 0
	for _, status := range statuses {
		if status == 200 {
			success++
		} else if status != 409 && status != 422 {
			t.Fatal(statuses)
		}
	}
	if success != 1 {
		t.Fatal(statuses)
	}
	bankInvoice = apiCall(t, app, "GET", bi, ``, owner, "", 200)["data"].(map[string]any)
	if bankInvoice["settledAmount"] != float64(75) {
		t.Fatal(bankInvoice)
	}
}

func TestPhase7RepairsAndReportsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	p, partner := ownFinancePartner(t, db, app, owner, "vehicle-owner", "VEHICLE_OWNER")
	_, other := ownFinancePartner(t, db, app, owner, "other-owner", "VEHICLE_OWNER")
	shipmentSQL(t, db, `UPDATE "Vehicle" SET "ownerId"=$1 WHERE id=10`, p["id"])
	apiCall(t, app, "POST", "/api/v1/wallet/adjustments", fmt.Sprintf(`{"partnerId":%.0f,"amount":1000,"reason":"Seed"}`, p["id"]), owner, "", 200)
	worker := loginAs(t, app, "worker", "")
	for _, path := range []string{"/api/v1/repairs", "/api/v1/repairs/logs", "/api/v1/repairs/vehicles", "/api/v1/finance/summary", "/api/v1/finance/operational-expenses"} {
		apiCall(t, app, "GET", path, ``, worker, "", 403)
	}
	apiCall(t, app, "GET", "/api/v1/repairs/vehicles", ``, owner, "", 200)
	apiCall(t, app, "POST", "/api/v1/repairs", `{"vehicleId":10,"description":"Tyres","amount":100}`, owner, "", 422)
	repair := apiCall(t, app, "POST", "/api/v1/repairs", `{"vehicleId":10,"description":"Tyres","amount":100,"proofUrl":"proof","repairDate":"2026-11-01"}`, owner, "", 200)["data"].(map[string]any)
	rp := fmt.Sprintf("/api/v1/repairs/%.0f", repair["id"])
	assertWalletBalance(t, db, p["id"], 900)
	apiCall(t, app, "PUT", rp, `{"description":"Tyres","amount":100,"proofUrl":"proof","repairDate":"2026-11-01"}`, owner, "", 422)
	apiCall(t, app, "PUT", rp, `{"description":"TYRES","amount":200}`, owner, "", 200)
	assertWalletBalance(t, db, p["id"], 800)
	apiCall(t, app, "PUT", rp, `{"description":"TYRES","amount":50}`, owner, "", 200)
	assertWalletBalance(t, db, p["id"], 950)
	apiCall(t, app, "PUT", rp, `{"description":"TYRES","amount":2000}`, owner, "", 422)
	assertWalletBalance(t, db, p["id"], 950)
	apiCall(t, app, "POST", "/api/v1/repairs", `{"vehicleId":11,"description":"Company","amount":9999,"proofUrl":"proof"}`, owner, "", 200)
	rows := apiCall(t, app, "GET", "/api/v1/repairs", ``, other, "", 200)["data"].([]any)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	rows = apiCall(t, app, "GET", "/api/v1/repairs?status=VERIFIED", ``, partner, "", 200)["data"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box","actualWeightKg":2}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"priceAmount"=100 WHERE id=$1`, s["id"])
	tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, ``), owner, "", 200)["data"].(map[string]any)
	apiCall(t, app, "POST", transportPath(tr)+"/expenses", `{"type":"BBM","amount":25,"description":"Fuel"}`, owner, "", 200)
	rows = apiCall(t, app, "GET", "/api/v1/partner/transports", ``, partner, "", 200)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["settlement"] != nil || rows[0].(map[string]any)["transportValue"] != float64(100) {
		t.Fatal(rows)
	}
	rows = apiCall(t, app, "GET", "/api/v1/partner/transports", ``, other, "", 200)["data"].([]any)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	rows = apiCall(t, app, "GET", "/api/v1/partner/vehicles", ``, partner, "", 200)["data"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	dashboard := apiCall(t, app, "GET", "/api/v1/partner/dashboard", ``, partner, "", 200)["data"].(map[string]any)
	if len(dashboard["repairs"].([]any)) != 1 || dashboard["totals"].(map[string]any)["repairDeductions"] != float64(50) {
		t.Fatal(dashboard)
	}
	apiCall(t, app, "GET", "/api/v1/partner/vehicles", ``, owner, "", 403)
	for _, query := range []string{"?page=0", "?type=OTHER", "?status=BAD"} {
		apiCall(t, app, "GET", "/api/v1/finance/operational-expenses"+query, ``, owner, "", 422)
	}
	report := apiCall(t, app, "GET", "/api/v1/finance/operational-expenses?type=BBM&search=Fuel", ``, owner, "", 200)["data"].(map[string]any)
	if report["pendingTotal"] != float64(25) || report["count"] != float64(1) {
		t.Fatal(report)
	}
	summary := apiCall(t, app, "GET", "/api/v1/finance/summary", ``, owner, "", 200)["data"].(map[string]any)
	if summary["totals"].(map[string]any)["repairDeductions"] != float64(50) || len(summary["monthlyLedger"].([]any)) != 1 {
		t.Fatal(summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil || strings.Contains(string(encoded), "passwordHash") {
		t.Fatal(string(encoded), err)
	}
	apiCall(t, app, "DELETE", rp, ``, owner, "", 200)
	assertWalletBalance(t, db, p["id"], 1000)
	logs := apiCall(t, app, "GET", fmt.Sprintf("/api/v1/repairs/logs?repairId=%.0f&limit=2", repair["id"]), ``, partner, "", 200)
	if logs["meta"].(map[string]any)["total"] != float64(4) || len(logs["data"].([]any)) != 2 {
		t.Fatal(logs)
	}
	rows = apiCall(t, app, "GET", "/api/v1/repairs/logs?action=DELETED", ``, other, "", 200)["data"].([]any)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
}
