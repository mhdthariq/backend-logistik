package httpapi_test

import (
	"testing"
	"time"
)

func TestCapacityTrendPostgres(t *testing.T) {
	db, app, owner, _ := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	path := "/api/v1/capacity/trend"
	apiCall(t, app, "GET", path, "", "", "", 401)
	apiCall(t, app, "GET", path, "", worker, "", 403)
	empty := apiCall(t, app, "GET", path, "", owner, "", 200)["data"].(map[string]any)
	if len(empty["buckets"].([]any)) != 14 || empty["total"] != float64(0) {
		t.Fatal(empty)
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (100,'vehicle.view','Vehicle','view'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,100)`)
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day()-6, 0, 0, 0, 0, now.Location()).UTC()
	for _, raw := range []any{`{"capacityConfigChanged":true}`, `{"capacityConfigChanged":1}`, `{"capacityConfigChanged":"yes"}`, `{"capacityConfigChanged":[]}`, `{"capacityConfigChanged":{}}`, `{"capacityConfigChanged":false}`, `{"capacityConfigChanged":0}`, `{"capacityConfigChanged":""}`, `{"capacityConfigChanged":null}`, `{}`, `{bad`, `[]`, `null`, `true`, nil} {
		shipmentSQL(t, db, `INSERT INTO "AuditLog" (action,"entityType","afterData","createdAt") VALUES ('updated','vehicle',$1,$2)`, raw, from)
	}
	shipmentSQL(t, db, `INSERT INTO "AuditLog" (action,"entityType","afterData","createdAt") VALUES
	 ('updated','vehicle','{"capacityConfigChanged":true}',$1),
	 ('updated','shipment','{"capacityConfigChanged":true}',$2),
	 ('updated','vehicle','{"capacityConfigChanged":true}',$3)`, from.Add(-time.Millisecond), from, now.AddDate(0, 0, 3))
	result := apiCall(t, app, "GET", path+"?days=7", "", worker, "", 200)["data"].(map[string]any)
	if result["total"] != float64(5) || len(result["buckets"].([]any)) != 7 {
		t.Fatal(result)
	}
	first := result["buckets"].([]any)[0].(map[string]any)
	if first["count"] != float64(5) || first["date"] != from.UTC().Format("2006-01-02") || len(first) != 3 {
		t.Fatal(first)
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"?days=0", 14}, {"?days=oops", 14}, {"?days=1", 7}, {"?days=200", 90}, {"?days=7.5", 8}} {
		out := apiCall(t, app, "GET", path+tc.query, "", owner, "", 200)["data"].(map[string]any)
		if len(out["buckets"].([]any)) != tc.count {
			t.Fatal(tc.query, out)
		}
	}
	shipmentSQL(t, db, `ALTER TABLE "AuditLog" RENAME TO "HiddenAuditLog"`)
	apiCall(t, app, "GET", path, "", owner, "", 500)
}
