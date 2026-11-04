package httpapi_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/identity"
	"novalogistik/backend/internal/tenant"
)

func TestAccessValidationPostgres(t *testing.T) {
	db := disposableDB(t)
	seedIdentity(t, db)
	registry := tenant.New(tenant.Config{DatabaseURL: db.dsn, Mode: "development"})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	owner, worker := loginAs(t, app, "owner", ""), loginAs(t, app, "worker", "")
	for _, tt := range []struct {
		name, method, path, body, token string
		status                          int
	}{
		{"empty role slug", "POST", "/api/v1/roles", `{"name":"中文"}`, owner, 422},
		{"duplicate role", "POST", "/api/v1/roles", `{"name":"Worker"}`, owner, 422},
		{"used role deletion", "DELETE", "/api/v1/roles/2", "", owner, 422},
		{"permission list required", "PUT", "/api/v1/roles/2/permissions", `{}`, owner, 422},
		{"unknown role", "PUT", "/api/v1/roles/999", `{}`, owner, 404},
		{"short password", "POST", "/api/v1/users", `{"username":"short","name":"Name","password":"short"}`, owner, 422},
		{"duplicate username", "POST", "/api/v1/users", `{"username":"worker","name":"Name","password":"long-password"}`, owner, 422},
		{"unknown employee", "POST", "/api/v1/users", `{"username":"unknown","name":"Name","password":"long-password","employeeId":999}`, owner, 422},
		{"invalid direct permission", "POST", "/api/v1/users", `{"username":"badpermission","name":"Name","password":"long-password","permissionIds":[999]}`, owner, 422},
		{"owner admin edit prohibited", "PUT", "/api/v1/users/1", `{"name":"Other"}`, owner, 422},
		{"invalid username syntax", "PUT", "/api/v1/users/2", `{"username":"not allowed"}`, owner, 422},
		{"duplicate username edit", "PUT", "/api/v1/users/2", `{"username":"owner"}`, owner, 422},
		{"profile username owner only", "PUT", "/api/v1/profile", `{"username":"other"}`, worker, 403},
		{"profile short name", "PUT", "/api/v1/profile", `{"name":"A"}`, worker, 422},
		{"profile short password", "PUT", "/api/v1/profile", `{"newPassword":"short"}`, worker, 422},
		{"profile current password required", "PUT", "/api/v1/profile", `{"newPassword":"new-password"}`, worker, 422},
		{"profile same password", "PUT", "/api/v1/profile", `{"newPassword":"pässword-123","currentPassword":"pässword-123"}`, worker, 422},
		{"missing user", "DELETE", "/api/v1/users/999", "", owner, 404},
		{"invalid id", "DELETE", "/api/v1/warehouses/not-an-id", "", owner, 422},
		{"unknown employee", "PUT", "/api/v1/employees/999", `{}`, owner, 404},
	} {
		t.Run(tt.name, func(t *testing.T) { apiCall(t, app, tt.method, tt.path, tt.body, tt.token, "", tt.status) })
	}
	warehouse := apiCall(t, app, "POST", "/api/v1/warehouses", `{"name":"Partner Warehouse"}`, owner, "", 200)["data"].(map[string]any)
	partnerUser := apiCall(t, app, "POST", "/api/v1/users", `{"username":"partner","name":"Partner","password":"long-password","roleIds":[3]}`, owner, "", 200)["data"].(map[string]any)
	userPath := fmt.Sprintf("/api/v1/users/%d", int(partnerUser["id"].(float64)))
	apiCall(t, app, "PUT", userPath, fmt.Sprintf(`{"warehouseId":%d}`, int(warehouse["id"].(float64))), owner, "", 200)
	apiCall(t, app, "PUT", userPath, `{"warehouseId":"general","roleIds":[3],"password":"changed-password"}`, owner, "", 200)
	partnerToken := "partner-profile-token"
	if _, err := db.pool.Exec(context.Background(), `INSERT INTO "SessionToken" (token,"userId","expiresAt") VALUES ($1,$2,$3)`, identity.TokenHash(partnerToken), partnerUser["id"], time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	profile := apiCall(t, app, "PUT", "/api/v1/profile", `{"bankName":"Bank","bankAccountName":"Partner","bankAccountNumber":"123456"}`, partnerToken, "", 200)["data"].(map[string]any)
	if profile["partner"].(map[string]any)["bank"].(map[string]any)["bankAccountNumber"] != "123456" {
		t.Fatal("partner bank update lost")
	}
	apiCall(t, app, "GET", "/api/v1/profile", "", partnerToken, "", 200)
	apiCall(t, app, "POST", "/api/v1/users", `{"username":"invalidshare","name":"Partner","password":"long-password","roleIds":[3],"companyPercent":0,"partnerPercent":100}`, owner, "", 422)
	apiCall(t, app, "POST", "/api/v1/users", `{"username":"invalidsum","name":"Partner","password":"long-password","roleIds":[3],"companyPercent":50,"partnerPercent":20}`, owner, "", 422)
	// Editing a system role as owner may change its description/permissions, but
	// never its immutable slug/name.
	role := apiCall(t, app, "PUT", "/api/v1/roles/3", `{"name":"Renamed","description":"Updated","permissionIds":[1]}`, owner, "", 200)["data"].(map[string]any)
	if role["name"] != "Marketing" || role["description"] != "Updated" {
		t.Fatal("system role field locking changed")
	}
	apiCall(t, app, "PUT", "/api/v1/profile", `{"username":"owner.updated","name":"Updated Owner"}`, owner, "", 200)
	apiCall(t, app, "PUT", userPath, `{"isActive":false}`, owner, "", 200)
	apiCall(t, app, "GET", "/api/v1/auth/me", "", partnerToken, "", 401)
}
