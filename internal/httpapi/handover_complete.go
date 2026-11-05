package httpapi

import (
	"fmt"
	"strings"
	"time"
)

func (b *business) confirmPickup(r *request) (any, error)    { return b.completeHandover(r, false) }
func (b *business) completeDelivery(r *request) (any, error) { return b.completeHandover(r, true) }

func (b *business) completeHandover(r *request, delivery bool) (any, error) {
	task, s, err := r.pickupOrDelivery(delivery)
	if err != nil {
		return nil, err
	}
	kind, table, code, status, event := "pickup", "Pickup", "pickupCode", "PICKED_UP", "PICKED_UP"
	if delivery {
		kind, table, code, status, event = "delivery", "Delivery", "deliveryCode", "COMPLETED", "DELIVERED"
	}
	if delivery {
		if task["status"] == "COMPLETED" {
			return nil, problem(422, "Delivery sudah selesai.")
		}
		if task["status"] == "FAILED" {
			return nil, problem(422, "Delivery ditandai gagal - buat task baru bila perlu.")
		}
	} else {
		switch task["status"] {
		case "COMPLETED":
			return nil, problem(422, "Pickup sudah selesai.")
		case "CANCELLED":
			return nil, problem(422, "Pickup sudah dibatalkan.")
		case "PICKED_UP":
			return nil, problem(422, "Paket sudah diambil kurir (Picked Up) - pickup akan selesai otomatis saat paket tiba di gudang.")
		}
	}
	progress, err := r.handoverProgress(s, kind, task["id"])
	if err != nil {
		return nil, err
	}
	if progress.Total == 0 {
		if delivery {
			return nil, problem(422, "Shipment belum punya detail barang.")
		}
		return nil, problem(422, "Shipment belum punya detail barang - tambahkan detail sebelum pickup.")
	}
	if !progress.AllScanned {
		if delivery {
			remaining := []string{}
			for _, d := range progress.Details {
				if d["scanned"] != true {
					remaining = append(remaining, textValue(d["detailCode"]))
				}
			}
			return nil, problem(422, fmt.Sprintf("Belum semua paket discan (%d/%d). Sisa: %s", progress.Scanned, progress.Total, strings.Join(remaining, ", ")))
		}
		return nil, problem(422, fmt.Sprintf("Belum semua paket discan (%d/%d, sisa %d paket).", progress.Scanned, progress.Total, progress.Total-progress.Scanned))
	}
	c, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return nil, err
	}
	if !delivery && c["type"] == "b2b" {
		var invoiced bool
		if err = r.db.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM "InvoiceLine" WHERE "shipmentId"=$1)`, s["id"]).Scan(&invoiced); err != nil {
			return nil, err
		}
		if !invoiced {
			return nil, problem(422, "Shipment B2B belum ditagirkan ke invoice manapun - tambahkan shipment ini ke invoice perusahaan customer sebelum pickup.")
		}
	}
	data, hasLocation, err := parseHandoverEvidence(r.body, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	proof := ""
	if delivery {
		proof, err = required(r.body, "proofOfDelivery")
		if err != nil {
			return nil, err
		}
	}
	photo, _ := r.body["photoUrl"].(string)
	if photo == "" {
		if delivery {
			return nil, problem(422, "Foto bukti serah terima wajib disertakan sebelum konfirmasi.")
		}
		return nil, problem(422, "Foto bukti pickup wajib disertakan sebelum konfirmasi.")
	}
	if _, err = optionalProof(photo); err != nil {
		return nil, err
	}
	kurir := textValue(r.actor["name"])
	if task["kurirId"] != nil {
		e, err := r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1`, task["kurirId"])
		if err != nil {
			return nil, err
		}
		if e != nil {
			kurir = textValue(e["name"])
		}
	}
	label := "Pickup " + textValue(task[code])
	tracking := "Picked-up by " + kurir
	if delivery {
		label = "Delivery " + textValue(task[code])
		tracking = fmt.Sprintf("Delivered to %s by %s - received by: %s", c["name"], kurir, proof)
	}
	if s["fulfillmentMode"] == "DIRECT" {
		cp, err := r.one(`SELECT jsonb_build_object('name',cp.name) FROM (SELECT t.id FROM "Transport" t JOIN "TransportShipment" ts ON ts."transportId"=t.id WHERE ts."shipmentId"=$1 ORDER BY t.id LIMIT 1) t LEFT JOIN LATERAL (SELECT cr."checkpointId" FROM "CheckpointRecord" cr WHERE cr."transportId"=t.id ORDER BY cr."recordedAt" DESC,cr.id DESC LIMIT 1) cr ON true LEFT JOIN "Checkpoint" cp ON cp.id=cr."checkpointId"`, s["id"])
		if err != nil {
			return nil, err
		}
		if cp != nil && truthy(cp["name"]) {
			tracking = fmt.Sprintf("Picked up from '%s'", cp["name"])
			if delivery {
				tracking = fmt.Sprintf("Delivery Success on '%s'", cp["name"])
			}
		}
	}
	photo, err = processEvidencePhoto(r.ctx, photo, hasLocation, data, evidenceCaption{Company: r.company, Label: label, Origin: textValue(s["origin"]), Destination: textValue(s["destination"]), Driver: kurir, RecordedBy: textValue(r.actor["name"])})
	if err != nil {
		return nil, problem(422, "Foto bukti tidak dapat diproses. Ambil ulang foto.")
	}
	data["photoUrl"], data["status"], data["notes"] = photo, status, task["notes"]
	if notes := nullable(r.body["notes"]); notes != nil {
		data["notes"] = notes
	}
	if delivery {
		data["completedAt"], data["proofOfDelivery"] = time.Now().UTC(), proof
	}
	updated, err := r.updateRow(table, idValue(task["id"]), data)
	if err != nil {
		return nil, err
	}
	if delivery && s["status"] != "DELIVERED" || !delivery && s["status"] == "READY_FOR_PICKUP" {
		if _, err = r.updateRow("MasterShipment", idValue(s["id"]), map[string]any{"status": event}); err != nil {
			return nil, err
		}
	}
	if err = r.tracking(s, event, tracking); err != nil {
		return nil, err
	}
	after := map[string]any{"packagesScanned": fmt.Sprintf("%d/%d", progress.Scanned, progress.Total), "kurir": kurir, "hasPhoto": true}
	if delivery {
		after["proof"] = proof
	}
	b.audit(r, "status_change", kind, task["id"], textValue(task[code])+" → "+status, nil, after)
	updated["tracking"] = tracking
	return updated, nil
}

func (r *request) pickupOrDelivery(delivery bool) (map[string]any, map[string]any, error) {
	if delivery {
		return r.deliveryTask(true, true)
	}
	return r.pickupTask(true, true)
}
