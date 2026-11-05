package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"slices"
	"strings"
	"testing"
	"time"
)

func handoverPhoto(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestHandoverCompletionPostgres(t *testing.T) {
	for _, delivery := range []bool{false, true} {
		t.Run(fmt.Sprint("delivery=", delivery), func(t *testing.T) {
			db, app, owner, cid := shipmentApp(t)
			seedDeliveryStaff(t, db)
			worker := loginAs(t, app, "worker", "")
			table, kind, permission, suffix, event, initial, terminal := "Pickup", "pickups", "pickup.confirm", "confirm", "PICKED_UP", "READY_FOR_PICKUP", "PICKED_UP"
			if delivery {
				table, kind, permission, suffix, event, initial, terminal = "Delivery", "deliveries", "delivery.confirm", "complete", "DELIVERED", "RECEIVED_AT_GUDANG", "COMPLETED"
			}
			shipmentSQL(t, db, `INSERT INTO "Permission" (id,slug,module,description) VALUES (120,$1,'Handover','confirm'); INSERT INTO "UserPermission" ("userId","permissionId") VALUES (2,120)`, permission)
			s := newShipment(t, app, owner, cid, `,"originWarehouseId":10,"details":[{"description":"Box A"},{"description":"Box B"}]`)
			shipmentSQL(t, db, `UPDATE "MasterShipment" SET status=$1,"priceAmount"=100 WHERE id=$2`, initial, s["id"])
			task := apiCall(t, app, "POST", "/api/v1/"+kind, fmt.Sprintf(`{"masterId":%.0f,"kurirId":10,"notes":"keep","latitude":-7,"longitude":110}`, s["id"]), owner, "", 200)["data"].(map[string]any)
			base := fmt.Sprintf("/api/v1/%s/%.0f", kind, task["id"])
			path := base + "/" + suffix
			photo := handoverPhoto(t)
			body := map[string]any{"photoUrl": photo, "proofOfDelivery": " Receiver ", "payment": map[string]any{"amount": 100, "method": "CASH"}}
			encode := func() string {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			apiCall(t, app, "POST", path, encode(), "", "", 401)
			apiCall(t, app, "POST", fmt.Sprintf("/api/v1/%s/99999/%s", kind, suffix), encode(), owner, "", 404)
			apiCall(t, app, "POST", path, encode(), worker, "", 422)
			details := apiCall(t, app, "GET", base, "", owner, "", 200)["data"].(map[string]any)["progress"].(map[string]any)["details"].([]any)
			for _, d := range details {
				apiCall(t, app, "POST", base+"/scans", fmt.Sprintf(`{"payload":%q}`, d.(map[string]any)["detailCode"]), worker, "", 200)
			}
			for _, bad := range []string{`null`, `{`, `{"photoUrl":"data:image/png;base64,AAAA","proofOfDelivery":"R"}`, `{"photoUrl":"https://example.com/a.png","proofOfDelivery":"R"}`} {
				apiCall(t, app, "POST", path, bad, worker, "", 422)
			}
			for _, bad := range []map[string]any{{"latitude": nil}, {"latitude": "1", "longitude": 1}, {"latitude": 91.0, "longitude": 1.0}, {"timeZone": "UTC"}, {"capturedAt": time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339)}, {"locationAccuracy": -1}, {"mockLocation": "false"}} {
				for k, v := range bad {
					body[k] = v
				}
				apiCall(t, app, "POST", path, encode(), worker, "", 422)
				for k := range bad {
					delete(body, k)
				}
			}
			shipmentSQL(t, db, `UPDATE "`+table+`" SET "kurirId"=11 WHERE id=$1`, task["id"])
			apiCall(t, app, "POST", path, encode(), worker, "", 403)
			shipmentSQL(t, db, `UPDATE "`+table+`" SET "kurirId"=10 WHERE id=$1; UPDATE "MasterShipment" SET "originWarehouseId"=11,origin='Bandung' WHERE id=$2`, task["id"], s["id"])
			apiCall(t, app, "POST", path, encode(), worker, "", 403)
			shipmentSQL(t, db, `UPDATE "MasterShipment" SET "originWarehouseId"=10,origin='Jakarta' WHERE id=$1`, s["id"])
			// Failure after the task write must roll back task, shipment, proof and audits.
			shipmentSQL(t, db, `CREATE FUNCTION fail_handover_tracking() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event IN ('PICKED_UP','DELIVERED') THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_handover_tracking BEFORE INSERT ON "TrackingEvent" FOR EACH ROW EXECUTE FUNCTION fail_handover_tracking()`)
			apiCall(t, app, "POST", path, encode(), worker, "", 500)
			var status string
			var stored *string
			if err := db.pool.QueryRow(t.Context(), `SELECT status,"photoUrl" FROM "`+table+`" WHERE id=$1`, task["id"]).Scan(&status, &stored); err != nil || status != "ASSIGNED" || stored != nil {
				t.Fatal(status, stored, err)
			}
			shipmentSQL(t, db, `DROP TRIGGER fail_handover_tracking ON "TrackingEvent"`)
			out := apiCall(t, app, "POST", path, encode(), worker, "", 200)["data"].(map[string]any)
			if out["status"] != terminal || out["photoUrl"] != photo || out["notes"] != "keep" || out["latitude"] != float64(-7) || out["captureLatitude"] != nil || out["timeZone"] != "Asia/Jakarta" {
				t.Fatal(out)
			}
			wantTracking := "Picked-up by Courier"
			if delivery {
				wantTracking = "Delivered to Sender by Courier - received by: Receiver"
			}
			if out["tracking"] != wantTracking {
				t.Fatal(out["tracking"])
			}
			if err := db.pool.QueryRow(t.Context(), `SELECT status FROM "MasterShipment" WHERE id=$1`, s["id"]).Scan(&status); err != nil || status != event {
				t.Fatal(status, err)
			}
			var payments int
			if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "Payment" WHERE "masterId"=$1`, s["id"]).Scan(&payments); err != nil || payments != 0 {
				t.Fatal("legacy payment payload was not ignored", payments, err)
			}
			apiCall(t, app, "POST", path, encode(), owner, "", 422)
			// GPS stamps a JPEG; planned task/map coordinates remain unchanged.
			shipmentSQL(t, db, `UPDATE "`+table+`" SET status='ASSIGNED' WHERE id=$1; UPDATE "MasterShipment" SET status=$2 WHERE id=$3`, task["id"], initial, s["id"])
			body["latitude"], body["longitude"], body["locationAccuracy"], body["locationLabel"], body["timeZone"], body["mockLocation"] = -6.2, 106.8, 5, "Depot", "Asia/Makassar", true
			out = apiCall(t, app, "POST", path, encode(), worker, "", 200)["data"].(map[string]any)
			if out["captureLatitude"] != -6.2 || out["latitude"] != float64(-7) || out["locationLabel"] != "Depot" || out["mockLocation"] != true || !strings.HasPrefix(out["photoUrl"].(string), "data:image/jpeg;base64,") || len(out["photoUrl"].(string)) > 2_500_000 {
				t.Fatal("capture metadata or stamping mismatch")
			}
			readStatus := 200
			if delivery {
				readStatus = 403
			}
			apiCall(t, app, "GET", base, "", worker, "", readStatus)
		})
	}
}

func TestPickupB2BInvoiceAndDirectTrackingPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedDeliveryStaff(t, db)
	shipmentSQL(t, db, `UPDATE "Customer" SET type='b2b' WHERE id=$1`, cid)
	s := newShipment(t, app, owner, cid, `,"fulfillmentMode":"DIRECT","details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='READY_FOR_PICKUP',"priceAmount"=100 WHERE id=$1`, s["id"])
	shipmentSQL(t, db, `INSERT INTO "Invoice" (id,"invoiceNumber","customerId","updatedAt") VALUES (10,'INV-COMPLETE',$1,NOW()); INSERT INTO "InvoiceLine" ("invoiceId","shipmentId",description,"unitPrice") VALUES (10,$2,'Box',100)`, cid, s["id"])
	p := apiCall(t, app, "POST", "/api/v1/pickups", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"]), owner, "", 200)["data"].(map[string]any)
	base := fmt.Sprintf("/api/v1/pickups/%.0f", p["id"])
	body := fmt.Sprintf(`{"photoUrl":%q}`, handoverPhoto(t))
	apiCall(t, app, "POST", base+"/scans", fmt.Sprintf(`{"payload":%q}`, s["masterCode"]), owner, "", 200)
	shipmentSQL(t, db, `DELETE FROM "InvoiceLine" WHERE "shipmentId"=$1`, s["id"])
	apiCall(t, app, "POST", base+"/confirm", body, owner, "", 422)
	shipmentSQL(t, db, `INSERT INTO "InvoiceLine" ("invoiceId","shipmentId",description,"unitPrice") VALUES (10,$1,'Box',100);
	 INSERT INTO "Route" (id,name,"updatedAt") VALUES (10,'Route',NOW()); INSERT INTO "Checkpoint" (id,"routeId",name,sequence,latitude,longitude,"updatedAt") VALUES (10,10,'Pickup depot',1,0,0,NOW()),(11,10,'Delivery depot',2,0,0,NOW());
	 INSERT INTO "Vehicle" (id,"vehicleNumber","maxWeightKg","maxVolumeM3","updatedAt") VALUES (10,'B-HANDOVER',1000,10,NOW());
	 INSERT INTO "Transport" (id,"transportCode","routeId","vehicleId","updatedAt") VALUES (10,'T-HANDOVER',10,10,NOW()); INSERT INTO "TransportShipment" ("transportId","shipmentId") VALUES (10,$1);
	 INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius") VALUES (10,10,0,0,true)`, s["id"])
	out := apiCall(t, app, "POST", base+"/confirm", body, owner, "", 200)["data"].(map[string]any)
	if out["tracking"] != "Picked up from 'Pickup depot'" {
		t.Fatal(out["tracking"])
	}
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG' WHERE id=$1; INSERT INTO "CheckpointRecord" ("transportId","checkpointId",latitude,longitude,"withinRadius") VALUES (10,11,0,0,true)`, s["id"])
	d := apiCall(t, app, "POST", "/api/v1/deliveries", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"]), owner, "", 200)["data"].(map[string]any)
	base = fmt.Sprintf("/api/v1/deliveries/%.0f", d["id"])
	apiCall(t, app, "POST", base+"/scans", fmt.Sprintf(`{"payload":%q}`, s["masterCode"]), owner, "", 200)
	out = apiCall(t, app, "POST", base+"/complete", fmt.Sprintf(`{"photoUrl":%q,"proofOfDelivery":"R"}`, handoverPhoto(t)), owner, "", 200)["data"].(map[string]any)
	if out["tracking"] != "Delivery Success on 'Delivery depot'" {
		t.Fatal(out["tracking"])
	}
}

func TestHandoverConcurrentCompletionPostgres(t *testing.T) {
	db, app, owner, cid := shipmentApp(t)
	seedDeliveryStaff(t, db)
	s := newShipment(t, app, owner, cid, `,"details":[{"description":"Box"}]`)
	shipmentSQL(t, db, `UPDATE "MasterShipment" SET status='RECEIVED_AT_GUDANG',"priceAmount"=100 WHERE id=$1`, s["id"])
	d := apiCall(t, app, "POST", "/api/v1/deliveries", fmt.Sprintf(`{"masterId":%.0f,"kurirId":10}`, s["id"]), owner, "", 200)["data"].(map[string]any)
	base := fmt.Sprintf("/api/v1/deliveries/%.0f", d["id"])
	progress := apiCall(t, app, "GET", base, "", owner, "", 200)["data"].(map[string]any)["progress"].(map[string]any)
	code := progress["details"].([]any)[0].(map[string]any)["detailCode"]
	apiCall(t, app, "POST", base+"/scans", fmt.Sprintf(`{"payload":%q}`, code), owner, "", 200)
	statuses := concurrentPosts(t, app, base+"/complete", fmt.Sprintf(`{"photoUrl":%q,"proofOfDelivery":"R"}`, handoverPhoto(t)), owner, 2)
	slices.Sort(statuses)
	if statuses[0] != 200 || statuses[1] != 422 {
		t.Fatal(statuses)
	}
	var events int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM "TrackingEvent" WHERE "masterId"=$1 AND event='DELIVERED'`, s["id"]).Scan(&events); err != nil || events != 1 {
		t.Fatal(events, err)
	}
}
