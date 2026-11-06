package httpapi_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func gudangWorkspace(t *testing.T, app *fiber.App, token, corp string) map[string]any {
	t.Helper()
	return apiCall(t, app, "GET", "/api/v1/gudang", "", token, corp, 200)["data"].(map[string]any)
}

func gudangRow(t *testing.T, rows any, id float64) map[string]any {
	t.Helper()
	for _, raw := range rows.([]any) {
		row := raw.(map[string]any)
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("missing row %.0f in %v", id, rows)
	return nil
}

func TestGudangWorkspacePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	worker := loginAs(t, app, "worker", "")
	apiCall(t, app, "GET", "/api/v1/gudang", "", "", "", 401)
	apiCall(t, app, "GET", "/api/v1/gudang", "", worker, "", 403)
	empty := gudangWorkspace(t, app, owner, "")
	for _, key := range []string{"arrivals", "transportArrivals", "walkIns"} {
		if rows, ok := empty[key].([]any); !ok || len(rows) != 0 {
			t.Fatal(empty)
		}
	}
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,'shipment.view','Shipment','view'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120);
	 UPDATE "Customer" SET "companyName"='Sender Company' WHERE id=$1;
	 INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","destinationWarehouseId","arrivedWarehouseId","priceAmount","discountAmount","finalPriceAmount","destReceivedAt","createdAt","updatedAt") VALUES
	 (100,'G-PICKED',$1,'Jakarta','Bandung','PICKED_UP',10,11,NULL,100,20,NULL,NULL,'2026-01-01','2026-01-03'),
	 (101,'G-CREATED',$1,'Jakarta','Bandung','CREATED',10,11,NULL,NULL,0,NULL,NULL,'2026-01-01','2026-01-01'),
	 (102,'G-READY',$1,'Jakarta','Bandung','READY_FOR_PICKUP',10,11,NULL,100,20,75,NULL,'2026-01-02','2026-01-02'),
	 (103,'G-ORIGIN',$1,'Jakarta','Bandung','RECEIVED_AT_GUDANG',10,11,10,100,10,70,NULL,'2026-01-01','2026-01-04'),
	 (104,'G-DEST',$1,'Jakarta','Bandung','AT_DEST_GUDANG',10,11,11,NULL,0,NULL,NULL,'2026-01-01','2026-01-05'),
	 (105,'G-LEGACY',$1,'Jakarta','Bandung','ARRIVED_AT_GUDANG',10,11,NULL,NULL,0,NULL,NULL,'2026-01-01','2026-01-06'),
	 (106,'G-RECEIVED',$1,'Jakarta','Bandung','ARRIVED_AT_GUDANG',10,11,11,10,0,10,'2026-01-07','2026-01-01','2026-01-07'),
	 (107,'G-TRANSIT',$1,'Jakarta','Bandung','IN_TRANSPORT',10,11,NULL,NULL,0,NULL,NULL,'2026-01-01','2026-01-08'),
	 (108,'G-CANCELLED',$1,'Jakarta','Bandung','CANCELLED',10,11,NULL,NULL,0,NULL,NULL,'2026-01-01','2026-01-08');
	 INSERT INTO "DetailShipment" (id,"masterId","detailCode",description,"actualWeightKg","lengthCm","widthCm","heightCm","volumeM3","updatedAt") VALUES
	 (100,100,'G-A','Box A',1.234,100,100,100,99,NOW()),(101,100,'G-B','Box B',2.345,50,50,50,NULL,NOW()),
	 (102,103,'G-C','Held',1.235,100,100,100,NULL,NOW()),(103,104,'G-D','Destination',3,100,100,100,NULL,NOW());
	 INSERT INTO "Payment" ("masterId",method,amount,status) VALUES (100,'CASH',30,'VERIFIED'),(100,'CASH',500,'PENDING'),(103,'CASH',20,'VERIFIED'),(106,'CASH',20,'VERIFIED');
	 INSERT INTO "Pickup" ("pickupCode","masterId","kurirId","completedAt","updatedAt") VALUES ('G-P1',100,11,'2026-01-01',NOW()),('G-P2',100,10,'2026-01-02',NOW());
	 INSERT INTO "HandoverScan" ("masterId",context,"scanLevel","detailId",payload,result,method) VALUES
	 (100,'gudang_arrival','detail',100,'G-A','ok','SCANNED'),(100,'gudang_arrival','detail',100,'G-A','duplicate','SCANNED'),(100,'gudang_arrival','detail',101,'G-B','ok','UNKNOWN'),
	 (100,'gudang_arrival','master',NULL,'G-PICKED','ok','SCANNED'),(100,'gudang_arrival','detail',101,'G-B','unexpected','SCANNED'),(100,'pickup','detail',101,'G-B','ok','SCANNED'),
	 (104,'transport_arrival','detail',103,'G-D','ok','TYPED'),(104,'transport_arrival','detail',103,'G-D','duplicate','SCANNED'),(104,'gudang_arrival','detail',103,'G-D','ok','SCANNED');
	 INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'G-VEHICLE',1000,10,NOW());
	 INSERT INTO "Transport" (id,"transportCode","vehicleId","driverId","kenekId","createdAt","updatedAt") VALUES (10,'G-OLD',10,11,NULL,'2026-01-01',NOW()),(11,'G-LATEST',10,10,11,'2026-01-02',NOW());
	 INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,104),(11,104)`, cid)
	workspace := gudangWorkspace(t, app, owner, "")
	if len(workspace) != 5 || workspace["scope"].(map[string]any)["scoped"] != false {
		t.Fatal(workspace)
	}
	arrivals := workspace["arrivals"].([]any)
	if len(arrivals) != 1 {
		t.Fatal(arrivals)
	}
	a := arrivals[0].(map[string]any)
	if a["customerName"] != "Sender Company" || a["customerPhone"] != "123" || a["detailsCount"] != float64(2) || math.Abs(a["totalWeightKg"].(float64)-3.579) > 1e-9 || a["totalVolumeM3"] != 1.125 || a["scannedCount"] != float64(3) || a["paidAmount"] != float64(30) || a["remainingAmount"] != float64(50) || a["dpOk"] != true || a["pickupCode"] != "G-P2" || a["kurirName"] != "Courier" {
		t.Fatal(a)
	}
	if m := a["scannedByMethod"].(map[string]any); m["SCANNED"] != float64(2) || m["TYPED"] != float64(1) {
		t.Fatal(m)
	}
	walkIns := workspace["walkIns"].([]any)
	if len(walkIns) != 2 || walkIns[0].(map[string]any)["id"] != float64(102) || gudangRow(t, walkIns, 101)["finalPriceAmount"] != nil || gudangRow(t, walkIns, 102)["finalPriceAmount"] != float64(75) {
		t.Fatal(walkIns)
	}
	transport := workspace["transportArrivals"].([]any)
	if len(transport) != 2 || transport[0].(map[string]any)["id"] != float64(104) {
		t.Fatal(transport)
	}
	dest := gudangRow(t, transport, 104)
	if dest["transportCode"] != "G-LATEST" || dest["driverName"] != "Courier" || dest["kenekName"] != "Other courier" || dest["originWarehouseName"] != "Origin" || dest["scannedCount"] != float64(2) {
		t.Fatal(dest)
	}
	legacy := gudangRow(t, transport, 105)
	for _, key := range []string{"transportCode", "driverName", "kenekName", "arrivedWarehouseId"} {
		if value, exists := legacy[key]; !exists || value != nil {
			t.Fatal("missing explicit null", key, legacy)
		}
	}
	wh := workspace["warehouses"].([]any)
	if len(wh) != 2 || wh[0].(map[string]any)["id"] != float64(11) {
		t.Fatal(wh)
	}
	origin := gudangRow(t, wh, 10)
	if origin["heldShipments"] != float64(1) || origin["heldPackages"] != float64(1) || origin["heldWeightKg"] != 1.24 || origin["unpaidCount"] != float64(0) {
		t.Fatal(origin)
	}
	h := origin["shipments"].([]any)[0].(map[string]any)
	if h["remainingAmount"] != float64(50) || h["stage"] != "origin" || h["originWarehouseName"] != nil {
		t.Fatal(h)
	}
	destination := gudangRow(t, wh, 11)
	if destination["heldShipments"] != float64(3) || destination["shipments"].([]any)[0].(map[string]any)["id"] != float64(106) || gudangRow(t, destination["shipments"], 106)["remainingAmount"] != float64(0) || gudangRow(t, destination["shipments"], 105)["remainingAmount"] != nil {
		t.Fatal(destination)
	}
	// A warehouse worker sees origin work only; receipt moves work to destination staff.
	scoped := gudangWorkspace(t, app, worker, "")
	if scoped["scope"].(map[string]any)["warehouseId"] != float64(10) || len(scoped["warehouses"].([]any)) != 1 || len(scoped["transportArrivals"].([]any)) != 0 || len(scoped["arrivals"].([]any)) != 1 {
		t.Fatal(scoped)
	}
	shipmentSQL(t, db, `UPDATE "Employee" SET "warehouseId"=11 WHERE id=10`)
	scoped = gudangWorkspace(t, app, worker, "")
	if len(scoped["arrivals"].([]any)) != 0 || len(scoped["walkIns"].([]any)) != 0 || len(scoped["transportArrivals"].([]any)) != 2 {
		t.Fatal(scoped)
	}
	shipmentSQL(t, db, `UPDATE "User" SET "employeeId"=NULL WHERE id=2`)
	scoped = gudangWorkspace(t, app, worker, "")
	for _, key := range []string{"arrivals", "transportArrivals", "walkIns", "warehouses"} {
		if len(scoped[key].([]any)) != 0 {
			t.Fatal("unbound account leaked warehouse data", scoped)
		}
	}
}

func TestGudangWarehouseFallbacksPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,'shipment.view','Shipment','view'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120);
	 INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (12,'WH-SAME','Same City','Jakarta',NOW());
	 INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","destinationWarehouseId","arrivedWarehouseId","updatedAt") VALUES
	 (100,'G-FALLBACK',$1,'jakarta','BANDUNG','AT_DEST_GUDANG',NULL,NULL,NULL,NOW()),
	 (101,'G-EXPLICIT',$1,'Jakarta','Jakarta','AT_DEST_GUDANG',10,11,NULL,NOW()),
	 (102,'G-OVERRIDE',$1,'Jakarta','Bandung','AT_DEST_GUDANG',10,11,12,NOW()),
	 (103,'G-ORIGIN',$1,'JAKARTA','Bandung','RECEIVED_AT_GUDANG',NULL,NULL,NULL,NOW()),
	 (104,'G-WALKIN',$1,'Jakarta','Bandung','CREATED',11,NULL,NULL,NOW())`, cid)
	worker := loginAs(t, app, "worker", "")
	w := gudangWorkspace(t, app, worker, "")
	if len(w["transportArrivals"].([]any)) != 0 || len(w["walkIns"].([]any)) != 1 || gudangRow(t, w["warehouses"], 10)["heldShipments"] != float64(1) {
		t.Fatal(w)
	}
	// Queue/held membership prioritizes IDs; walk-in scope also includes matching cities.
	shipmentSQL(t, db, `UPDATE "Employee" SET "warehouseId"=11 WHERE id=10`)
	w = gudangWorkspace(t, app, worker, "")
	if len(w["transportArrivals"].([]any)) != 2 || gudangRow(t, w["warehouses"], 11)["heldShipments"] != float64(2) {
		t.Fatal(w)
	}
	shipmentSQL(t, db, `UPDATE "Warehouse" SET "isActive"=false WHERE id=11`)
	w = gudangWorkspace(t, app, worker, "")
	if len(w["warehouses"].([]any)) != 0 || len(w["transportArrivals"].([]any)) != 1 || w["scope"].(map[string]any)["scoped"] != false {
		t.Fatal(w)
	}
	// Inactive origin names stay null, as the legacy workspace uses active warehouses.
	shipmentSQL(t, db, `UPDATE "Warehouse" SET "isActive"=false WHERE id=10`)
	w = gudangWorkspace(t, app, owner, "")
	if gudangRow(t, w["transportArrivals"], 101)["originWarehouseName"] != nil {
		t.Fatal(w)
	}
}

func TestGudangTenantIsolationPostgres(t *testing.T) {
	a, other := disposableDB(t), disposableDB(t)
	for i, db := range []testDatabase{a, other} {
		seedIdentity(t, db)
		shipmentSQL(t, db, `INSERT INTO "Warehouse" (id,code,name,city,"updatedAt") VALUES (10,'WH','Warehouse','Jakarta',NOW()); INSERT INTO "Customer" (id,code,name,"updatedAt") VALUES (10,'C',$1,NOW()); INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","updatedAt") VALUES (10,'SHARED',10,'Jakarta','Bandung','PICKED_UP',10,NOW())`, fmt.Sprintf("Tenant %d", i))
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
	tokenA, tokenB := loginAs(t, app, "owner", "A"), loginAs(t, app, "owner", "B")
	apiCall(t, app, "GET", "/api/v1/gudang", "", tokenA, "B", 401)
	for _, tc := range []struct{ token, corp, name string }{{tokenA, "A", "Tenant 0"}, {tokenB, "B", "Tenant 1"}} {
		w := gudangWorkspace(t, app, tc.token, tc.corp)
		if row := gudangRow(t, w["arrivals"], 10); row["customerName"] != tc.name || row["detailsCount"] != float64(0) || row["pickupCode"] != nil || row["kurirName"] != nil {
			t.Fatal(row)
		}
		dashboard := apiCall(t, app, "GET", "/api/v1/dashboard?from=2000-01-01&to=2099-01-01", "", tc.token, tc.corp, 200)["data"].(map[string]any)
		if row := dashboard["recentShipments"].([]any)[0].(map[string]any); row["customerName"] != tc.name {
			t.Fatal(row)
		}
		audits := apiCall(t, app, "GET", "/api/v1/audit-logs?entityType=auth", "", tc.token, tc.corp, 200)["data"].([]any)
		if len(audits) != 1 {
			t.Fatal(audits)
		}
	}
	apiCall(t, app, "GET", "/api/v1/dashboard", "", tokenA, "B", 401)
	apiCall(t, app, "GET", "/api/v1/audit-logs", "", tokenA, "B", 401)
}
