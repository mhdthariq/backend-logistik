package httpapi_test

import (
	"fmt"
	"strings"
	"testing"
)

const dashboardWindow = "?from=2026-01-01&to=2026-01-02"

func TestPhase8PermissionsAndEmptyPostgres(t *testing.T) {
	_, app, owner, _ := shipmentApp(t)
	worker := loginAs(t, app, "worker", "")
	for _, path := range []string{"/api/v1/dashboard", "/api/v1/dashboard/driver", "/api/v1/dashboard/kurir", "/api/v1/audit-logs"} {
		t.Run(path, func(t *testing.T) {
			apiCall(t, app, "GET", path, "", "", "", 401)
			apiCall(t, app, "GET", path+dashboardWindow, "", owner, "", 200)
			if path != "/api/v1/dashboard" {
				apiCall(t, app, "GET", path, "", worker, "", 403)
			}
			if path != "/api/v1/audit-logs" {
				apiCall(t, app, "GET", path+"?from=invalid", "", owner, "", 422)
			}
		})
	}
	d := apiCall(t, app, "GET", "/api/v1/dashboard"+dashboardWindow, "", worker, "", 200)["data"].(map[string]any)
	if d["counts"].(map[string]any)["shipments"] != nil || len(d["approvals"].(map[string]any)["pendingTopups"].([]any)) != 0 {
		t.Fatal(d)
	}
	d = apiCall(t, app, "GET", "/api/v1/dashboard/driver", "", owner, "", 200)["data"].(map[string]any)
	if d["current"] != nil || d["note"] == nil || d["period"].(map[string]any)["days"] != nil {
		t.Fatal(d)
	}
	d = apiCall(t, app, "GET", "/api/v1/dashboard/kurir"+"?from=2026-01-02&to=2026-01-01", "", owner, "", 200)["data"].(map[string]any)
	if d["period"].(map[string]any)["days"] != float64(2) || d["note"] == nil {
		t.Fatal(d)
	}
	for _, query := range []string{"?limit=no", "?offset=1.5", "?entityId=wrong", "?actorId=2147483648"} {
		apiCall(t, app, "GET", "/api/v1/audit-logs"+query, "", owner, "", 422)
	}
}

func TestPhase8OperationalDashboardsPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	grantFinance(t, db, 2, "shipment.view", "delivery.view", "audit_log.view")
	worker := loginAs(t, app, "worker", "")
	shipmentSQL(t, db, `INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","destinationWarehouseId","createdAt","updatedAt") VALUES (100,'VISIBLE',$1,'Jakarta','Bandung','CREATED',10,11,'2026-01-01','2026-01-01'),(101,'HIDDEN',$1,'Bandung','Other','CREATED',11,NULL,'2026-01-01','2026-01-01');
 INSERT INTO "DetailShipment" (id,"masterId","detailCode",description,"actualWeightKg","lengthCm","widthCm","heightCm","updatedAt") VALUES (100,100,'BOX','Box',2.345,100,100,100,NOW());
 INSERT INTO "Pickup" (id,"pickupCode","masterId","kurirId",status,"createdAt","completedAt","updatedAt") VALUES (100,'ACTIVE',100,10,'ASSIGNED','2025-01-01',NULL,NOW()),(101,'OTHER',100,11,'ASSIGNED','2026-01-01',NULL,NOW()),(102,'DONE',100,10,'COMPLETED','2025-01-01','2026-01-02 23:59:59.999',NOW()),(103,'OLD',100,10,'COMPLETED','2025-01-01','2025-01-02',NOW());
 INSERT INTO "Delivery" (id,"deliveryCode","masterId","kurirId",status,"createdAt","completedAt","updatedAt") VALUES (100,'DELIVERY',100,10,'COMPLETED','2025-01-01','2026-01-02',NOW()),(101,'OTHER',100,11,'ASSIGNED','2026-01-01',NULL,NOW());
 INSERT INTO "HandoverScan" ("pickupId","detailId",payload,result,"scanLevel",context) VALUES (100,100,'BOX','ok','detail','pickup'),(100,100,'BOX','duplicate','detail','pickup'),(100,NULL,'OTHER','unexpected','detail','pickup');
 INSERT INTO "Transport" (id,"transportCode","routeId","vehicleId","driverId","kenekId",status,"trackingMode","plannedDepartureAt","arrivedAt","createdAt","updatedAt") VALUES
 (100,'CURRENT',10,10,10,NULL,'DEPARTED','CHECKPOINT',NULL,NULL,'2026-01-05',NOW()),
 (101,'NEXT',10,11,11,10,'PLANNED','CHECKPOINT','2026-01-06',NULL,'2026-01-01',NOW()),
 (102,'FUTURE',NULL,11,10,NULL,'PLANNED','ACTIVITY',NULL,NULL,'2026-01-02',NOW()),
 (103,'COMPLETE',10,11,10,NULL,'COMPLETED','CHECKPOINT',NULL,'2026-01-02','2026-01-03',NOW()),
 (104,'FOREIGN',11,11,11,NULL,'DEPARTED','CHECKPOINT',NULL,NULL,'2026-01-06',NOW());
 INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (100,100);
 INSERT INTO "CheckpointRecord" ("transportId","checkpointId","recordedById","recordedAt",latitude,longitude,"withinRadius") VALUES (100,10,1,'2026-01-01',0,0,true);
 INSERT INTO "Payment" ("masterId",method,amount,status,"verifiedAt") VALUES (100,'CASH',25,'VERIFIED','2026-01-01'),(101,'CASH',75,'VERIFIED','2026-01-01');`, cid)

	courier := apiCall(t, app, "GET", "/api/v1/dashboard/kurir"+dashboardWindow, "", worker, "", 200)["data"].(map[string]any)
	counts := courier["counts"].(map[string]any)
	if counts["pickupsAssigned"] != float64(1) || counts["pickupsCompleted"] != float64(1) || counts["deliveriesDelivered"] != float64(1) {
		t.Fatal(courier)
	}
	pickups := courier["pickups"].([]any)
	if len(pickups) != 2 || pickups[0].(map[string]any)["pickupCode"] != "ACTIVE" || pickups[0].(map[string]any)["scannedCount"] != float64(1) {
		t.Fatal(pickups)
	}

	driver := apiCall(t, app, "GET", "/api/v1/dashboard/driver"+dashboardWindow, "", worker, "", 200)["data"].(map[string]any)
	current := driver["current"].(map[string]any)
	if current["transportCode"] != "CURRENT" || current["nextCheckpoint"].(map[string]any)["id"] != float64(11) || current["lastCheckinBy"] != "Owner" || current["totalWeightKg"] != 2.35 || current["totalVolumeM3"] != float64(1) || current["totalPrice"] != nil {
		t.Fatal(current)
	}
	if driver["completedCount"] != float64(1) || driver["totalTransportsAllTime"] != float64(4) || driver["upcoming"].([]any)[0].(map[string]any)["transportCode"] != "NEXT" || driver["future"].([]any)[0].(map[string]any)["nextCheckpoint"] != nil {
		t.Fatal(driver)
	}

	d := apiCall(t, app, "GET", "/api/v1/dashboard"+dashboardWindow, "", worker, "", 200)["data"].(map[string]any)
	if d["counts"].(map[string]any)["shipments"] != float64(1) || d["revenueVerified"] != float64(25) || d["capacitySummary"].(map[string]any)["overCapacityTransports"] != float64(1) {
		t.Fatal(d)
	}
	ownerDash := apiCall(t, app, "GET", "/api/v1/dashboard"+dashboardWindow, "", owner, "", 200)["data"].(map[string]any)
	if ownerDash["revenueVerified"] != float64(100) || len(ownerDash["gudang"].(map[string]any)["walkIns"].([]any)) != 2 {
		t.Fatal(ownerDash)
	}
}

func TestPhase8AuditScopePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedTransport(t, db)
	grantFinance(t, db, 2, "audit_log.view")
	worker := loginAs(t, app, "worker", "")
	shipmentSQL(t, db, `INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","destinationWarehouseId","updatedAt") VALUES (100,'OURS',$1,'Jakarta','Bandung','CREATED',10,11,NOW()),(101,'THEIRS',$1,'Bandung','Other','CREATED',11,NULL,NOW());
 INSERT INTO "Pickup" (id,"pickupCode","masterId","updatedAt") VALUES (100,'PICKUP',100,NOW());
 INSERT INTO "Delivery" (id,"deliveryCode","masterId","updatedAt") VALUES (100,'DELIVERY',101,NOW());
 INSERT INTO "DetailShipment" (id,"masterId","detailCode",description,"updatedAt") VALUES (100,100,'DETAIL','Box',NOW());
 INSERT INTO "Payment" (id,"masterId",method,amount) VALUES (100,101,'CASH',1);
 INSERT INTO "Transport" (id,"transportCode","routeId","vehicleId","updatedAt") VALUES (100,'TRANSPORT',10,10,NOW());
 DELETE FROM "AuditLog";
 INSERT INTO "AuditLog" (action,"entityType","entityId","entityLabel","actorId","beforeData","afterData","createdAt") VALUES
 ('updated','shipment',100,'Visible',1,'broken','{"safe":true}','2026-01-02'),
 ('updated','shipment',101,'Hidden',1,NULL,NULL,'2026-01-01'),
 ('updated','shipment',999,'Deleted',NULL,NULL,NULL,'2026-01-01'),
 ('updated','pickup',100,'Pickup',1,NULL,NULL,'2026-01-01'),
 ('updated','delivery',100,'Delivery',1,NULL,NULL,'2026-01-01'),
 ('updated','shipment_detail',100,'Detail',1,NULL,NULL,'2026-01-01'),
 ('updated','payment',100,'Payment',1,NULL,NULL,'2026-01-01'),
 ('updated','transport',100,'Transport',1,NULL,NULL,'2026-01-01'),
 ('updated','shipment',NULL,'Unknown',1,NULL,NULL,'2026-01-01'),
 ('login','auth',NULL,'System',NULL,NULL,NULL,'2026-01-01');`, cid)
	result := apiCall(t, app, "GET", "/api/v1/audit-logs?limit=500", "", worker, "", 200)
	rows := result["data"].([]any)
	if len(rows) != 5 || result["meta"].(map[string]any)["total"] != float64(5) {
		t.Fatal(result)
	}
	first := rows[0].(map[string]any)
	if first["beforeData"] != nil || first["afterData"].(map[string]any)["safe"] != true || first["actorName"] != "Owner" || first["actorUsername"] != "owner" || first["createdAt"] != "2026-01-02T00:00:00.000Z" {
		t.Fatal(first)
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		for _, key := range []string{"actorId", "master", "transport", "passwordHash"} {
			if _, ok := row[key]; ok {
				t.Fatal("private field", row)
			}
		}
	}
	result = apiCall(t, app, "GET", "/api/v1/audit-logs?limit=500&offset=1", "", worker, "", 200)
	if len(result["data"].([]any)) != 4 {
		t.Fatal(result)
	}
	result = apiCall(t, app, "GET", "/api/v1/audit-logs?search=oWnEr&entityType=shipment&entityId=100&actorId=1&action=updated", "", owner, "", 200)
	if len(result["data"].([]any)) != 1 {
		t.Fatal(result)
	}
	result = apiCall(t, app, "GET", "/api/v1/audit-logs?limit=1&offset=1", "", owner, "", 200)
	if result["meta"].(map[string]any)["total"] != float64(10) || len(result["data"].([]any)) != 1 {
		t.Fatal(result)
	}
	// Missing warehouse denies operational entries while retaining company-wide events.
	shipmentSQL(t, db, `UPDATE "Employee" SET "warehouseId"=NULL WHERE id=10`)
	result = apiCall(t, app, "GET", "/api/v1/audit-logs", "", worker, "", 200)
	if len(result["data"].([]any)) != 1 || result["data"].([]any)[0].(map[string]any)["actorName"] != "System" {
		t.Fatal(result)
	}
}

func TestPhase8MarketingPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	p, token := ownFinancePartner(t, db, app, owner, "dash-marketing", "MARKETING")
	grantFinance(t, db, p["userId"], "shipment.view", "customer.view")
	shipmentSQL(t, db, `UPDATE "Customer" SET "marketingPartnerId"=$1,"createdAt"='2026-01-01' WHERE id=$2`, p["id"], cid)
	newShipment(t, app, owner, cid, fmt.Sprintf(`,"createdByPartnerId":%.0f`, p["id"]))
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdAt"='2026-01-01',"createdByPartnerId"=$1`, p["id"])
	newShipment(t, app, owner, cid, "")
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET "createdAt"='2026-01-01'`)
	d := apiCall(t, app, "GET", "/api/v1/dashboard"+dashboardWindow, "", token, "", 200)["data"].(map[string]any)
	if d["role"] != "marketing" || d["counts"].(map[string]any)["shipments"] != float64(1) || d["counts"].(map[string]any)["customers"] != float64(1) || d["marketing"] == nil || d["gudang"] != nil {
		t.Fatal(d)
	}
	for _, raw := range d["approvals"].(map[string]any) {
		if len(raw.([]any)) != 0 {
			t.Fatal(d)
		}
	}
	if strings.Contains(fmt.Sprint(d["marketing"]), "passwordHash") {
		t.Fatal(d)
	}
}

func TestPhase8WarehouseWorkspacePostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedPickupStaff(t, db)
	shipmentSQL(t, db, `INSERT INTO "Role" (id,slug,name) VALUES (10,'staff-gudang','Warehouse staff'); INSERT INTO "UserRole" ("userId","roleId") VALUES (2,10);
 INSERT INTO "MasterShipment" (id,"masterCode","customerId",origin,destination,status,"originWarehouseId","destinationWarehouseId","arrivedWarehouseId","priceAmount","finalPriceAmount","updatedAt") VALUES
 (100,'HELD',$1,'Jakarta','Bandung','RECEIVED_AT_GUDANG',10,11,10,100,80,NOW()),(101,'OTHER',$1,'Jakarta','Bandung','AT_DEST_GUDANG',10,11,11,100,100,NOW());
 INSERT INTO "DetailShipment" ("masterId","detailCode",description,"actualWeightKg","updatedAt") VALUES (100,'BOX','Box',2.5,NOW());
 INSERT INTO "Payment" ("masterId",method,amount,status) VALUES (100,'CASH',30,'VERIFIED');`, cid)
	worker := loginAs(t, app, "worker", "")
	d := apiCall(t, app, "GET", "/api/v1/dashboard"+dashboardWindow, "", worker, "", 200)["data"].(map[string]any)
	w := d["gudang"].(map[string]any)
	held := w["heldSummary"].([]any)
	if d["role"] != "staff-gudang" || len(held) != 1 || w["scope"].(map[string]any)["warehouseId"] != float64(10) {
		t.Fatal(d)
	}
	h := held[0].(map[string]any)
	if h["heldShipments"] != float64(1) || h["heldPackages"] != float64(1) || h["heldWeightKg"] != 2.5 || h["unpaidCount"] != float64(1) {
		t.Fatal(h)
	}
	d = apiCall(t, app, "GET", "/api/v1/dashboard"+dashboardWindow, "", owner, "", 200)["data"].(map[string]any)
	if len(d["gudang"].(map[string]any)["heldSummary"].([]any)) != 2 {
		t.Fatal(d)
	}
}
