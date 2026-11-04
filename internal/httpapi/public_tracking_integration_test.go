package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func TestPublicTrackingPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	s := newShipment(t, app, owner, cid, `,"pengirimPhone":"081234567890","penerimaContact":"089876543210","penerimaAddress":"Jalan Mawar 123","details":[{"description":"Box","actualWeightKg":3.3,"lengthCm":100,"widthCm":100,"heightCm":100}]`)
	path := "/api/v1/public/track/" + s["masterCode"].(string)
	apiCall(t, app, "GET", path, "", "", "", 405)
	apiCall(t, app, "POST", path, `{"phone":"081234567890"}`, "", "", 422)
	for _, body := range []string{`null`, `{`, `[]`, `{}`, `{"phone":81234567890}`, `{"phone":"7890"}`, `{"phone":"0812****7890"}`} {
		apiCall(t, app, "POST", path, body, "", "A", 422)
	}
	for _, phone := range []string{"081234567890", "+6281234567890", "089876543210"} {
		payload := apiCall(t, app, "POST", path, fmt.Sprintf(`{"phone":%q}`, phone), "", "A", 200)["data"].(map[string]any)
		if payload["sender"].(map[string]any)["phone"] != "081234******" || payload["recipient"].(map[string]any)["address"] != "Jalan M********" {
			t.Fatal(payload)
		}
		for _, key := range []string{"customer", "priceAmount", "finalPriceAmount", "invoiceLines", "payments", "details", "createdByPartnerId", "pengirimPhone"} {
			if _, ok := payload[key]; ok {
				t.Errorf("private field %s exposed", key)
			}
		}
		packages := payload["packages"].([]any)
		if len(packages) != 1 || packages[0].(map[string]any)["detailCode"] != nil {
			t.Fatal(packages)
		}
		if payload["totals"].(map[string]any)["totalVolumeM3"] != float64(1) {
			t.Fatal(payload["totals"])
		}
		for _, raw := range payload["trackingEvents"].([]any) {
			event := raw.(map[string]any)
			if event["actorName"] != "Owner" || event["actor"] != nil {
				t.Fatal(event)
			}
		}
	}
	// Shipment snapshot takes precedence over the current customer's phone.
	wrong := apiCall(t, app, "POST", path, `{"phone":"080000000000"}`, "", "A", 403)
	missing := apiCall(t, app, "POST", "/api/v1/public/track/UNKNOWN", `{"phone":"081234567890"}`, "", "A", 403)
	if wrong["message"] != missing["message"] || wrong["data"] != nil {
		t.Fatal("tracking lookup leaks existence")
	}
	apiCall(t, app, "POST", path, `{"phone":"12345678"}`, "", "A", 403)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "pengirimPhone"=NULL,resi='ALTERNATE-RESI' WHERE id=$1; UPDATE "Customer" SET phone='081111111111' WHERE id=$2`, s["id"], cid)
	apiCall(t, app, "POST", "/api/v1/public/track/ALTERNATE-RESI", `{"phone":"081111111111"}`, "", "A", 200)
	child := s["details"].([]any)[0].(map[string]any)["detailCode"].(string)
	apiCall(t, app, "POST", "/api/v1/public/track/"+child, `{"phone":"081111111111"}`, "", "A", 200)
	req := httptest.NewRequest("POST", path, strings.NewReader(`{"phone":"081111111111"}`))
	req.Header.Set("x-corp-id", "A")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Cache-Control") != "no-store, private" {
		t.Fatal("cacheable tracking response")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='CANCELLED' WHERE id=$1`, s["id"])
	apiCall(t, app, "POST", "/api/v1/public/track/"+child, `{"phone":"081111111111"}`, "", "A", 403)
	for range 10 {
		apiCall(t, app, "POST", "/api/v1/public/track/RATE-LIMIT", `{"phone":"081234567890"}`, "", "A", 403)
	}
	// Changing phone or forwarded IP cannot reset the shipment-level limit.
	apiCall(t, app, "POST", "/api/v1/public/track/RATE-LIMIT", `{"phone":"080000000000"}`, "", "A", 429)
	apiCall(t, app, "POST", "/api/v1/public/track/RATE-LIMIT", `{"phone":"080000000000"}`, "", "B", 403)
}

func TestPublicTrackingTenantIsolation(t *testing.T) {
	a, other := disposableDB(t), disposableDB(t)
	for i, db := range []testDatabase{a, other} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Customer" (id,code,name,phone,"updatedAt") VALUES (10,'C-TRACK','Sender',$1,NOW()); INSERT INTO "MasterShipment" ("masterCode","customerId",origin,destination,"updatedAt") VALUES ('SHARED-CODE',10,'A','B',NOW())`, fmt.Sprintf("08123456789%d", i))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		db := a
		if r.URL.Query().Get("KdCustomer") == "B" {
			db = other
		}
		u, err := url.Parse(db.dsn)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"Status": "Ok", "Data": map[string]any{"KdCustomer": r.URL.Query().Get("KdCustomer"), "NamaCustomer": db.name, "Server": u.Host, "Db": db.name, "Username": u.User.Username()}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	registry := tenant.New(tenant.Config{Mode: "production", LicenseURL: server.URL, LicenseKey: "test", LicenseToken: "test", LicenseApplication: "test", MaxPools: 2})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	path := "/api/v1/public/track/SHARED-CODE"
	apiCall(t, app, "POST", path, `{"phone":"081234567890"}`, "", "A", 200)
	apiCall(t, app, "POST", path, `{"phone":"081234567890"}`, "", "B", 403)
	apiCall(t, app, "POST", path, `{"phone":"081234567891"}`, "", "B", 200)
	token := loginAs(t, app, "owner", "A")
	apiCall(t, app, "POST", "/api/v1/shipments/1/cancel", `{}`, token, "B", 401)
	apiCall(t, app, "POST", "/api/v1/shipments/1/cancel", `{}`, token, "A", 200)
	var status string
	if err := other.pool.QueryRow(context.Background(), `SELECT status FROM "MasterShipment" WHERE "masterCode"='SHARED-CODE'`).Scan(&status); err != nil || status != "CREATED" {
		t.Fatalf("cross-tenant mutation: %s %v", status, err)
	}
}
