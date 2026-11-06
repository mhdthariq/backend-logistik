package httpapi_test

import (
	"fmt"
	"reflect"
	"testing"
)

func TestTopupBankAccountsPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	p, partner := ownFinancePartner(t, db, app, owner, "marketing", "MARKETING")
	response := apiCall(t, app, "GET", "/api/v1/topups", "", partner, "", 200)["data"].(map[string]any)
	if accounts, ok := response["bankAccounts"].([]any); !ok || len(accounts) != 0 {
		t.Fatal(response)
	}
	shipmentSQL(t, db, `INSERT INTO "BankAccount" (id,"bankName","accountNumber","accountName","isActive","updatedAt") VALUES
 (10,'Bank B','00001234','Company B',true,NOW()),(11,'Bank A','00005678','Company A',true,NOW()),(12,'Inactive Bank','99999999','Disabled',false,NOW())`)
	want := []any{
		map[string]any{"id": float64(11), "bankName": "Bank A", "accountNumber": "00005678", "accountName": "Company A"},
		map[string]any{"id": float64(10), "bankName": "Bank B", "accountNumber": "00001234", "accountName": "Company B"},
	}
	for _, token := range []string{owner, partner} {
		response = apiCall(t, app, "GET", "/api/v1/topups", "", token, "", 200)["data"].(map[string]any)
		if !reflect.DeepEqual(response["bankAccounts"], want) || response["bankInfo"] != nil {
			t.Fatal(response)
		}
	}
	response = apiCall(t, app, "POST", "/api/v1/topups", fmt.Sprintf(`{"partnerId":%.0f,"amount":10000,"proofUrl":"data:image/png;base64,AAA="}`, p["id"]), owner, "", 200)["data"].(map[string]any)
	if !reflect.DeepEqual(response["bankAccounts"], want) || response["bankInfo"] != nil {
		t.Fatal(response)
	}
	shipmentSQL(t, db, `UPDATE "BankAccount" SET "isActive"=false WHERE id=11`)
	response = apiCall(t, app, "GET", "/api/v1/topups", "", partner, "", 200)["data"].(map[string]any)
	if !reflect.DeepEqual(response["bankAccounts"], want[1:]) {
		t.Fatal(response)
	}
	shipmentSQL(t, db, `UPDATE "BankAccount" SET "isActive"=false`)
	response = apiCall(t, app, "GET", "/api/v1/topups", "", partner, "", 200)["data"].(map[string]any)
	if len(response["bankAccounts"].([]any)) != 0 {
		t.Fatal(response)
	}
	// A failed account lookup must not leave a newly created top-up behind.
	shipmentSQL(t, db, `ALTER TABLE "BankAccount" RENAME TO "UnavailableBankAccount"`)
	apiCall(t, app, "POST", "/api/v1/topups", fmt.Sprintf(`{"partnerId":%.0f,"amount":10000,"proofUrl":"data:image/png;base64,AAA="}`, p["id"]), owner, "", 500)
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "TopUpRequest"`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
