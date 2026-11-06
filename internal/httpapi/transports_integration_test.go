package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func seedTransport(t *testing.T, db testDatabase) {
	t.Helper()
	seedPickupStaff(t, db)
	shipmentSQL(t, db, `INSERT INTO "Vehicle" (id,"vehicleNumber",status,"maxWeightKg","maxVolumeM3","maxKoli","updatedAt") VALUES (10,'TRUCK-10','ACTIVE',1,1,1,NOW()),(11,'TRUCK-11','ACTIVE',5000,50,100,NOW());
 INSERT INTO "Route" (id,name,origin,destination,"updatedAt") VALUES (10,'Outbound','Jakarta','Bandung',NOW()),(11,'Other','Other','Elsewhere',NOW());
 INSERT INTO "Checkpoint" (id,"routeId",name,sequence,latitude,longitude,"radiusMeters","updatedAt") VALUES (10,10,'Start',1,0,0,100,NOW()),(11,10,'Middle',2,0,1,100,NOW()),(12,10,'Finish',3,0,2,100,NOW());
 INSERT INTO "Permission" (id,slug,module,description) VALUES (200,'transport.view','Transport','view'),(201,'transport.checkin','Transport','checkin'),(202,'transport.create','Transport','create'),(203,'transport.depart','Transport','depart'),(204,'transport.arrive','Transport','arrive'),(205,'transport.delivery.approve','Transport','approve'),(206,'transport.expense.view','Expense','view');
 INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,200),(2,201),(2,206)`)
}
func transportBody(s map[string]any, extra string) string {
	return fmt.Sprintf(`{"routeId":10,"vehicleId":10,"driverId":10,"plannedDepartureAt":"2026-11-05T01:00:00Z","plannedArrivalAt":"2026-11-05T07:00:00Z","shipmentIds":[%.0f]%s}`, s["id"], extra)
}
func transportPath(t map[string]any) string { return fmt.Sprintf("/api/v1/transports/%.0f", t["id"]) }
func evidenceBody(t *testing.T, cp int, lon float64) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"checkpointId": cp, "latitude": 0, "longitude": lon, "locationAccuracy": 5, "locationLabel": "Test street", "timeZone": "Asia/Jakarta", "capturedAt": time.Now().UTC().Format(time.RFC3339Nano), "photo": handoverPhoto(t)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTransportListRelationsAndScopePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box","actualWeightKg":2,"lengthCm":100,"widthCm":100,"heightCm":100}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='IN_TRANSPORT',"chargeableWeightKg"=9,"priceAmount"=100 WHERE id=$1`, s["id"])
	shipmentSQL(t, db, `INSERT INTO "Transport" (id,"transportCode","routeId","vehicleId","driverId",status,"createdAt","updatedAt")
 SELECT g,'BULK-'||g,10,10,11,'ARRIVED','2026-11-01'::timestamp+g*interval '1 second',NOW() FROM generate_series(100,131) g;
 UPDATE "Transport" SET "driverId"=10,"kenekId"=11,status='COMPLETED' WHERE id=100;
 UPDATE "Transport" SET "routeId"=11,status='CANCELLED' WHERE id=130;
 UPDATE "Transport" SET "trackingMode"='ACTIVITY' WHERE id=129;
 UPDATE "Transport" SET "routeId"=NULL,"vehicleId"=11,"driverId"=NULL,"trackingMode"='ACTIVITY',status='PLANNED' WHERE id=131;
 INSERT INTO "Partner" (id,"userId",type,"updatedAt") VALUES (10,3,'VEHICLE_OWNER',NOW());
 UPDATE "Vehicle" SET "ownerId"=10 WHERE id=10;
 INSERT INTO "TransportSettlement" ("settlementCode","transportId","vehicleId","ownerId","transportValue","companyPercent","ownerPercent","companyAmount","ownerAmount","finalizedAt","updatedAt") VALUES ('SET-LIST',100,10,10,100,30,70,30,70,'2026-11-01',NOW());
 INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius","photoUrl") VALUES (100,10,0,0,true,'private-photo');
 UPDATE "Employee" SET "isActive"=false WHERE id=11`)
	shipmentSQL(t, db, `INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (100,$1)`, s["id"])
	rows := apiCall(t, app, "GET", "/api/v1/transports", "", owner, "", 200)["data"].([]any)
	if len(rows) != 32 || rows[0].(map[string]any)["id"] != float64(131) {
		t.Fatal("transport list lost rows or ordering", rows)
	}
	row := rows[len(rows)-1].(map[string]any)
	for key, want := range map[string]any{"id": float64(100), "driverName": "Courier", "kenekName": nil, "vehicleOwnerName": "Inherited Owner", "checkpointRecordsCount": float64(1), "shipmentCount": float64(1), "totalKoli": float64(1), "totalWeightKg": float64(9), "totalActualWeightKg": float64(2), "totalVolumeM3": float64(1), "totalPrice": float64(100)} {
		if row[key] != want {
			t.Errorf("%s = %v, want %v", key, row[key], want)
		}
	}
	if row["settlement"].(map[string]any)["ownerAmount"] != float64(70) || len(row["routeCheckpoints"].([]any)) != 3 || len(row["gudangIds"].([]any)) != 2 || row["shipments"].([]any)[0].(map[string]any)["id"] != s["id"] || row["capacity"].(map[string]any)["overallStatus"] != "OVERLIMIT" {
		t.Fatal("transport relations or aggregates changed", row)
	}
	empty := rows[0].(map[string]any)
	if empty["routeName"] != nil || empty["settlement"] != nil || empty["totalPrice"] != nil || empty["vehicleOwnerName"] != nil || len(empty["shipments"].([]any)) != 0 || len(empty["routeCheckpoints"].([]any)) != 0 {
		t.Fatal("optional list relations changed", empty)
	}
	activity := rows[2].(map[string]any)
	if activity["routeName"] != "Outbound" || len(activity["routeCheckpoints"].([]any)) != 0 {
		t.Fatal("activity transport exposed route checkpoints", activity)
	}
	for _, tc := range []struct {
		name, query, token string
		count              int
	}{
		{"search code", "?search=bulk-100", owner, 1},
		{"search vehicle", "?search=truck-11", owner, 1},
		{"search route", "?search=outbound", owner, 30},
		{"status", "?status=ARRIVED", owner, 29},
		{"history overrides status", "?history=true&status=PLANNED", owner, 31},
		{"executor assignments", "", worker, 1},
		{"explicit mine", "?mine=true", worker, 1},
		{"unbound owner mine", "?mine=true", owner, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := apiCall(t, app, "GET", "/api/v1/transports"+tc.query, "", tc.token, "", 200)["data"].([]any)
			if len(got) != tc.count {
				t.Fatalf("got %d rows, want %d", len(got), tc.count)
			}
		})
	}
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=11 WHERE id=2`)
	if got := apiCall(t, app, "GET", "/api/v1/transports?mine=true", "", worker, "", 200)["data"].([]any); len(got) != 30 || got[len(got)-1].(map[string]any)["id"] != float64(100) {
		t.Fatalf("driver/kenek assignments or warehouse scope changed: got %d rows, want 30 including transport 100", len(got))
	}
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=NULL WHERE id=2`)
	if got := apiCall(t, app, "GET", "/api/v1/transports", "", worker, "", 200)["data"].([]any); len(got) != 0 {
		t.Fatal("unbound executor saw transports", got)
	}
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=10 WHERE id=2; INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,202)`)
	if got := apiCall(t, app, "GET", "/api/v1/transports", "", worker, "", 200)["data"].([]any); len(got) != 30 {
		t.Fatal("warehouse scope changed", got)
	}
}

func TestTransportCheckpointLifecyclePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	worker := loginAs(t, app, "worker", "")
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/transports"}, {"POST", "/api/v1/transports"}, {"GET", "/api/v1/transports/999"}, {"PUT", "/api/v1/transports/999"}, {"DELETE", "/api/v1/transports/999"}, {"POST", "/api/v1/transports/999/checkins"}, {"POST", "/api/v1/transports/999/depart"}, {"POST", "/api/v1/transports/999/arrive"}, {"POST", "/api/v1/transports/999/complete"}, {"POST", "/api/v1/transports/999/print-log"}} {
		apiCall(t, app, route.method, route.path, `{}`, "", "", 401)
	}
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box","actualWeightKg":2,"lengthCm":100,"widthCm":100,"heightCm":100}]`)
	body := transportBody(s, ``)
	apiCall(t, app, "POST", "/api/v1/transports", body, worker, "", 403)
	apiCall(t, app, "POST", "/api/v1/transports", body, owner, "", 422)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"chargeableWeightKg"=9,"priceAmount"=100 WHERE id=$1`, s["id"])
	for _, bad := range []string{`null`, `{`, `{}`, strings.Replace(body, `"routeId":10`, `"routeId":11`, 1), strings.Replace(body, `"vehicleId":10`, `"vehicleId":999`, 1), strings.Replace(body, `"driverId":10`, `"driverId":10,"kenekId":10`, 1), strings.Replace(body, `"plannedArrivalAt":"2026-11-05T07:00:00Z"`, `"plannedArrivalAt":"2026-11-04T07:00:00Z"`, 1), strings.Replace(body, `"driverId":10`, `"driverId":10,"warehouseStaffId":"10"`, 1)} {
		apiCall(t, app, "POST", "/api/v1/transports", bad, owner, "", 422)
	}
	tr := apiCall(t, app, "POST", "/api/v1/transports", body, owner, "", 200)["data"].(map[string]any)
	path := transportPath(tr)
	if tr["transportCode"] != "TRP-2026-000001" || tr["status"] != "PLANNED" {
		t.Fatal(tr)
	}
	apiCall(t, app, "POST", "/api/v1/transports", body, owner, "", 422)
	detail := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if detail["totalWeightKg"] != float64(9) || detail["totalActualWeightKg"] != float64(2) || detail["totalKoli"] != float64(1) || detail["capacity"].(map[string]any)["overallStatus"] != "OVERLIMIT" || len(detail["checkpoints"].([]any)) != 3 {
		t.Fatal(detail)
	}
	rows := apiCall(t, app, "GET", "/api/v1/transports?search=truck-10&mine=true", "", worker, "", 200)["data"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	apiCall(t, app, "PUT", path, `{"origin":"Custom","warehouseStaffId":10}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/depart", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/checkins", `{"checkpointId":10,"latitude":0,"longitude":0}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 10, 0.1), worker, "", 422)
	checked := apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 10, 0), worker, "", 200)["data"].(map[string]any)
	if checked["autoArrived"] != false || checked["distanceKm"] != float64(0) {
		t.Fatal(checked)
	}
	apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 10, 0), worker, "", 422)
	apiCall(t, app, "PUT", path, `{"driverId":11}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/depart", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/complete", `{}`, owner, "", 409)
	checked = apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 12, 2), worker, "", 200)["data"].(map[string]any)
	if checked["autoArrived"] != true {
		t.Fatal(checked)
	}
	var status, drop string
	var received *time.Time
	if err := db.pool.QueryRow(t.Context(), `SELECT s.status,ts."dropStatus",s."destReceivedAt" FROM "MasterShipment" s JOIN "TransportShipment" ts ON ts."shipmentId"=s.id WHERE s.id=$1`, s["id"]).Scan(&status, &drop, &received); err != nil || status != "AT_DEST_GUDANG" || drop != "DROPPED" || received != nil {
		t.Fatal(status, drop, received, err)
	}
	apiCall(t, app, "POST", path+"/print-log", `{}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/undo-arrival", `{}`, worker, "", 403)
	undone := apiCall(t, app, "POST", path+"/undo-arrival", `{}`, owner, "", 200)["data"].(map[string]any)
	if undone["status"] != "DEPARTED" || undone["arrivedAt"] != nil {
		t.Fatal(undone)
	}
	// Transport undo intentionally preserves already-received shipment/drop history.
	apiCall(t, app, "POST", path+"/arrive", `{}`, owner, "", 200)
	completed := apiCall(t, app, "POST", path+"/complete", `{}`, owner, "", 200)["data"].(map[string]any)
	if completed["completedByUsername"] != "owner" || completed["completedByRole"] != "Owner" || completed["status"] != "COMPLETED" {
		t.Fatal(completed)
	}
	apiCall(t, app, "POST", path+"/complete", `{}`, owner, "", 409)
	apiCall(t, app, "POST", path+"/undo-arrival", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/arrive", `{}`, owner, "", 422)
	apiCall(t, app, "DELETE", path, `{}`, owner, "", 422)
	if got := apiCall(t, app, "GET", "/api/v1/transports?history=true", "", worker, "", 200)["data"].([]any); len(got) != 1 {
		t.Fatal(got)
	}
}
func TestTransportDropsAndReturnsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Box","actualWeightKg":2}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, `,"transportMode":"MULTI_DROP"`), owner, "", 200)["data"].(map[string]any)
	path := transportPath(tr)
	dropPath := fmt.Sprintf("%s/drops/%.0f", path, s["id"])
	apiCall(t, app, "POST", dropPath, `{"action":"ASSIGN","dropCheckpointId":10}`, owner, "", 422)
	apiCall(t, app, "POST", dropPath, `{"action":"ASSIGN","dropCheckpointId":11}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/return-task", `{}`, owner, "", 422)
	apiCall(t, app, "POST", dropPath+"/approve", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/delivery-approval", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 10, 0), owner, "", 200)
	apiCall(t, app, "POST", dropPath, `{"action":"DROP"}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 11, 1), owner, "", 200)
	board := apiCall(t, app, "GET", path+"/drops", "", owner, "", 200)["data"].(map[string]any)
	if board["shipments"].([]any)[0].(map[string]any)["dropStatus"] != "AT_DROP_POINT" {
		t.Fatal(board)
	}
	apiCall(t, app, "POST", dropPath, `{"action":"DROP"}`, owner, "", 200)
	apiCall(t, app, "POST", dropPath, `{"action":"DROP"}`, owner, "", 422)
	board = apiCall(t, app, "GET", path+"/drops", "", owner, "", 200)["data"].(map[string]any)
	if board["loadSummary"].(map[string]any)["remainingLoad"].(map[string]any)["totalKoli"] != float64(0) || board["vehicleEmpty"] != false {
		t.Fatal(board)
	}
	apiCall(t, app, "POST", dropPath+"/approve", `{}`, owner, "", 200)
	apiCall(t, app, "POST", dropPath+"/approve", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/delivery-approval", `{}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 12, 2), owner, "", 200)
	rt := apiCall(t, app, "POST", path+"/return-task", `{}`, owner, "", 200)["data"].(map[string]any)
	rp := fmt.Sprintf("/api/v1/return-tasks/%.0f", rt["id"])
	if rt["origin"] != "Bandung" || rt["destination"] != "Jakarta" {
		t.Fatal(rt)
	}
	apiCall(t, app, "POST", path+"/return-task", `{}`, owner, "", 422)
	detail := apiCall(t, app, "GET", rp, "", owner, "", 200)["data"].(map[string]any)
	if detail["returnCheckpoints"].([]any)[0].(map[string]any)["name"] != "Finish" {
		t.Fatal(detail)
	}
	if rows := apiCall(t, app, "GET", "/api/v1/return-tasks?search=bandung&status=CREATED", "", owner, "", 200)["data"].([]any); len(rows) != 1 {
		t.Fatal(rows)
	}
	apiCall(t, app, "POST", rp+"/create-transport", `{}`, owner, "", 422)
	apiCall(t, app, "POST", rp+"/approve", `{}`, owner, "", 200)
	apiCall(t, app, "POST", rp+"/approve", `{}`, owner, "", 422)
	ret := apiCall(t, app, "POST", rp+"/create-transport", `{}`, owner, "", 200)["data"].(map[string]any)
	back := ret["transport"].(map[string]any)
	backPath := transportPath(back)
	apiCall(t, app, "POST", rp+"/create-transport", `{}`, owner, "", 422)
	// Deleting and replanning a return leg must retain approval and reuse its route.
	backDetail := apiCall(t, app, "GET", backPath, "", owner, "", 200)["data"].(map[string]any)
	routeID := backDetail["routeId"]
	apiCall(t, app, "DELETE", backPath, `{}`, owner, "", 200)
	ret = apiCall(t, app, "POST", rp+"/create-transport", `{}`, owner, "", 200)["data"].(map[string]any)
	back = ret["transport"].(map[string]any)
	backPath = transportPath(back)
	backDetail = apiCall(t, app, "GET", backPath, "", owner, "", 200)["data"].(map[string]any)
	if backDetail["routeId"] != routeID {
		t.Fatal("return route not reused", backDetail)
	}
	cps := backDetail["checkpoints"].([]any)
	raw := evidenceBody(t, int(cps[0].(map[string]any)["id"].(float64)), 2)
	apiCall(t, app, "POST", backPath+"/checkins", raw, owner, "", 200)
	task := apiCall(t, app, "GET", rp, "", owner, "", 200)["data"].(map[string]any)
	if task["status"] != "DEPARTED" {
		t.Fatal(task)
	}
	apiCall(t, app, "POST", backPath+"/arrive", `{}`, owner, "", 200)
	task = apiCall(t, app, "GET", rp, "", owner, "", 200)["data"].(map[string]any)
	if task["status"] != "ARRIVED" {
		t.Fatal(task)
	}
	apiCall(t, app, "POST", backPath+"/undo-arrival", `{}`, owner, "", 200)
	task = apiCall(t, app, "GET", rp, "", owner, "", 200)["data"].(map[string]any)
	if task["status"] != "DEPARTED" {
		t.Fatal(task)
	}
}
func TestTransportActivityDirectPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	worker := loginAs(t, app, "worker", "")
	s := newShipment(t, app, owner, cid, `,"fulfillmentMode":"DIRECT","originWarehouseId":10,"destinationWarehouseId":11,"details":[{"description":"Direct box"}]`)
	tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, `,"trackingMode":"ACTIVITY","transportMode":"MULTI_DROP"`), owner, "", 200)["data"].(map[string]any)
	path := transportPath(tr)
	var pickups int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Pickup" WHERE "masterId"=$1 AND "kurirId"=10`, s["id"]).Scan(&pickups); err != nil || pickups != 1 {
		t.Fatal(pickups, err)
	}
	apiCall(t, app, "POST", path+"/activities", evidenceBody(t, 0, 0), owner, "", 403)
	for _, bad := range []string{`{}`, `{"latitude":0,"longitude":0}`, `{"capturedAt":"2020-01-01T00:00:00Z","latitude":0,"longitude":0}`, `{"latitude":"0","longitude":0}`} {
		apiCall(t, app, "POST", path+"/activities", bad, worker, "", 422)
	}
	activity := apiCall(t, app, "POST", path+"/activities", evidenceBody(t, 0, 0), worker, "", 200)["data"].(map[string]any)
	if !strings.HasPrefix(activity["photoUrl"].(string), "data:image/jpeg;base64,") || activity["recordedByUsername"] != "worker" {
		t.Fatal(activity)
	}
	older := evidenceBody(t, 0, 1)
	var evidence map[string]any
	if err := json.Unmarshal([]byte(older), &evidence); err != nil {
		t.Fatal(err)
	}
	evidence["capturedAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(evidence)
	apiCall(t, app, "POST", path+"/activities", string(raw), worker, "", 200)
	detail := apiCall(t, app, "GET", path, "", worker, "", 200)["data"].(map[string]any)
	if detail["currentLongitude"] != float64(0) {
		t.Fatal("older activity rewound position", detail)
	}
	if rows := apiCall(t, app, "GET", path+"/activities", "", worker, "", 200)["data"].([]any); len(rows) != 2 {
		t.Fatal(rows)
	}
	apiCall(t, app, "PUT", path, `{"trackingMode":"CHECKPOINT"}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 10, 0), worker, "", 422)
	apiCall(t, app, "POST", path+"/arrive", `{}`, owner, "", 422)
	apiCall(t, app, "POST", path+"/depart", `{}`, owner, "", 200)
	dropPath := fmt.Sprintf("%s/drops/%.0f", path, s["id"])
	apiCall(t, app, "POST", dropPath, `{"action":"ASSIGN","dropCheckpointId":11}`, owner, "", 422)
	apiCall(t, app, "POST", dropPath, `{"action":"DROP"}`, worker, "", 200)
	var status string
	var received *time.Time
	var deliveries int
	if err := db.pool.QueryRow(t.Context(), `SELECT status,"destReceivedAt",(SELECT count(*) FROM "Delivery" WHERE "masterId"=s.id AND "kurirId"=10) FROM "MasterShipment" s WHERE id=$1`, s["id"]).Scan(&status, &received, &deliveries); err != nil || status != "ARRIVED_AT_GUDANG" || received == nil || deliveries != 1 {
		t.Fatal(status, received, deliveries, err)
	}
	apiCall(t, app, "POST", path+"/arrive", `{}`, owner, "", 200)
	apiCall(t, app, "POST", path+"/activities", evidenceBody(t, 0, 0), worker, "", 422)
}

func TestTransportDirectArrivalAndEvidencePostgres(t *testing.T) {
	for _, mode := range []string{"ACTIVITY", "CHECKPOINT"} {
		t.Run(mode, func(t *testing.T) {
			db, app, owner, cid := shipmentApp(t)
			seedTransport(t, db)
			worker := loginAs(t, app, "worker", "")
			shipmentSQL(t, db, `INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,204)`)
			s := newShipment(t, app, owner, cid, `,"fulfillmentMode":"DIRECT","destinationWarehouseId":11,"details":[{"description":"Direct box"}]`)
			tr := apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, fmt.Sprintf(`,"trackingMode":%q`, mode)), owner, "", 200)["data"].(map[string]any)
			path := transportPath(tr)
			if mode == "ACTIVITY" {
				apiCall(t, app, "POST", path+"/depart", `{}`, owner, "", 200)
				apiCall(t, app, "POST", path+"/arrive", `{}`, worker, "", 422)
				bad := strings.Replace(evidenceBody(t, 0, 2), "data:image/png;base64,", "data:image/jpeg;base64,", 1)
				apiCall(t, app, "POST", path+"/arrive", bad, worker, "", 422)
				apiCall(t, app, "POST", path+"/arrive", evidenceBody(t, 0, 2), worker, "", 200)
				activities := apiCall(t, app, "GET", path+"/activities", "", worker, "", 200)["data"].([]any)
				if len(activities) != 1 || activities[0].(map[string]any)["locationLabel"] != "Test street" {
					t.Fatal(activities)
				}
			} else {
				apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 10, 0), worker, "", 200)
				apiCall(t, app, "POST", path+"/checkins", evidenceBody(t, 12, 2), worker, "", 200)
			}
			var status string
			var received *time.Time
			var deliveries int
			if err := db.pool.QueryRow(t.Context(), `SELECT status,"destReceivedAt",(SELECT count(*) FROM "Delivery" WHERE "masterId"=s.id AND "kurirId"=10) FROM "MasterShipment" s WHERE id=$1`, s["id"]).Scan(&status, &received, &deliveries); err != nil || status != "ARRIVED_AT_GUDANG" || received == nil || deliveries != 1 {
				t.Fatal(status, received, deliveries, err)
			}
		})
	}
}

func TestTransportLinkedWritesRollbackPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1`, s["id"])
	shipmentSQL(t, db, `CREATE FUNCTION reject_transport_tracking() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event='LOADED_TO_TRANSPORT' THEN RAISE EXCEPTION 'tracking unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_transport_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION reject_transport_tracking()`)
	apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, `,"trackingMode":"ACTIVITY"`), owner, "", 500)
	var status string
	var transports, links int
	if err := db.pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM "Transport"),(SELECT count(*) FROM "TransportShipment") FROM "MasterShipment" WHERE id=$1`, s["id"]).Scan(&status, &transports, &links); err != nil || status != "RECEIVED_AT_GUDANG" || transports != 0 || links != 0 {
		t.Fatal(status, transports, links, err)
	}
	shipmentSQL(t, db, `DROP TRIGGER reject_transport_tracking ON "TrackingEvent"`)
	apiCall(t, app, "POST", "/api/v1/transports", transportBody(s, `,"trackingMode":"ACTIVITY"`), owner, "", 200)
}
func concurrentTransportCalls(t *testing.T, app *fiber.App, token string, paths []string, body string) []int {
	t.Helper()
	statuses := make([]int, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				statuses[i] = -1
				return
			}
			statuses[i] = resp.StatusCode
			_ = resp.Body.Close()
		}(i, path)
	}
	wg.Wait()
	return statuses
}
