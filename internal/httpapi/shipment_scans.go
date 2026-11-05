package httpapi

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf16"
)

func arrivalContext(s map[string]any) string {
	switch s["status"] {
	case "PICKED_UP":
		return "gudang_arrival"
	case "AT_DEST_GUDANG":
		return "transport_arrival"
	case "ARRIVED_AT_GUDANG":
		if s["destReceivedAt"] == nil {
			return "transport_arrival"
		}
	}
	return ""
}

func jsStringLength(s string) int { return len(utf16.Encode([]rune(s))) }

func normalizeScan(payload, corporate string) (string, error) {
	payload = strings.TrimSpace(payload)
	u, err := url.Parse(payload)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.EscapedPath() != "/tracking-paket" {
		return payload, nil
	}
	corp, resi := strings.TrimSpace(u.Query().Get("corporateId")), strings.TrimSpace(u.Query().Get("resi"))
	if corp == "" || resi == "" || jsStringLength(corp) > 128 || jsStringLength(resi) > 128 {
		return payload, nil
	}
	corporate = strings.TrimSpace(corporate)
	if corporate == "" {
		corporate = "LOCAL"
	}
	if corp != corporate {
		return "", problem(422, "QR resi berasal dari corporate yang berbeda.")
	}
	return resi, nil
}

type scanProgress struct {
	Total         int              `json:"total"`
	Scanned       int              `json:"scanned"`
	AllScanned    bool             `json:"allScanned"`
	Details       []map[string]any `json:"details"`
	IsB2B         bool             `json:"isB2B"`
	MasterScanned bool             `json:"masterScanned"`
}

// arrivalProgress selects the earliest successful scan for each package and
// master. A B2B master scan supplies receipt metadata for every detail.
func (r *request) arrivalProgress(s map[string]any, context string) (scanProgress, error) {
	return r.handoverProgress(s, context, nil)
}

func (r *request) handoverProgress(s map[string]any, context string, pickupID any) (scanProgress, error) {
	p := scanProgress{Details: []map[string]any{}}
	customer, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, s["customerId"])
	if err != nil {
		return p, err
	}
	p.IsB2B = customer["type"] == "b2b"
	predicate, args := `h."masterId"=$1 AND h.context=$2`, []any{s["id"], context}
	if pickupID != nil {
		predicate, args = `h."pickupId"=$1`, []any{pickupID}
	}
	scans, err := r.many(`SELECT to_jsonb(h)||jsonb_build_object('by',u.name) FROM "HandoverScan" h LEFT JOIN "User" u ON u.id=h."scannedById" WHERE `+predicate+` AND h.result IN ('ok','duplicate') ORDER BY h."scannedAt",h.id`, args...)
	if err != nil {
		return p, err
	}
	byDetail := map[int]map[string]any{}
	var master map[string]any
	for _, scan := range scans {
		if scan["scanLevel"] == "master" && master == nil {
			master = scan
		}
		if id := idValue(scan["detailId"]); id > 0 && byDetail[id] == nil {
			byDetail[id] = scan
		}
	}
	p.MasterScanned = master != nil
	details, err := r.many(`SELECT to_jsonb(d) FROM "DetailShipment" d WHERE "masterId"=$1 ORDER BY id`, s["id"])
	if err != nil {
		return p, err
	}
	for _, d := range details {
		hit := byDetail[idValue(d["id"])]
		if p.IsB2B && master != nil {
			hit = master
		}
		state := map[string]any{"id": d["id"], "detailCode": d["detailCode"], "description": d["description"], "scanned": hit != nil, "scannedAt": nil, "scannedByName": nil, "scanMethod": nil}
		if hit != nil {
			state["scannedAt"], state["scannedByName"], state["scanMethod"] = hit["scannedAt"], hit["by"], hit["method"]
			p.Scanned++
		}
		p.Details = append(p.Details, state)
	}
	p.Total = len(p.Details)
	p.AllScanned = p.Total > 0 && p.Scanned == p.Total || p.IsB2B && p.MasterScanned
	return p, nil
}

func (b *business) arrivalScans(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	context := arrivalContext(s)
	if context == "" {
		context = "gudang_arrival"
	}
	p, err := r.arrivalProgress(s, context)
	return map[string]any{"progress": p, "context": context}, err
}

func (r *request) insertArrivalScan(s map[string]any, context, level, payload, result, method string, detail any) (map[string]any, error) {
	return r.one(`INSERT INTO "HandoverScan" AS h ("masterId",context,"scanLevel","detailId",payload,result,method,"scannedById") VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING to_jsonb(h)`, s["id"], context, level, detail, payload, result, method, r.actor["id"])
}

func arrivalDone(context string) string {
	if context == "transport_arrival" {
		return "konfirmasi penerimaan dari transport."
	}
	return "konfirmasi tiba di gudang."
}

func (b *business) recordArrivalScan(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	context := arrivalContext(s)
	if context == "" {
		return nil, problem(422, fmt.Sprintf("Konfirmasi tiba di gudang hanya untuk shipment PICKED_UP (kurir) atau AT_DEST_GUDANG (transport) - saat ini: %s.", s["status"]))
	}
	payload, err := required(r.body, "payload")
	if err != nil {
		return nil, err
	}
	payload, err = normalizeScan(payload, r.c.Get("x-corp-id"))
	if err != nil {
		return nil, err
	}
	method := "TYPED"
	if r.body["method"] == "SCANNED" {
		method = "SCANNED"
	}
	before, err := r.arrivalProgress(s, context)
	if err != nil {
		return nil, err
	}
	level, result := "detail", "unexpected"
	var detail any
	message := "Kode tidak dikenali - tidak cocok dengan paket manapun pada shipment ini."
	if payload == s["masterCode"] || payload == s["resi"] {
		level, result = "master", "ok"
		message = "QR master terbaca - lanjut scan semua paket (detail barang)."
		if before.IsB2B {
			message = "Master Resi B2B terbaca - semua paket ter-scan. Siap " + arrivalDone(context)
		}
	} else {
		for _, d := range before.Details {
			if payload != d["detailCode"] {
				continue
			}
			detail, result = d["id"], "ok"
			if d["scanned"] == true {
				result = "duplicate"
			}
			break
		}
	}
	scan, err := r.insertArrivalScan(s, context, level, payload, result, method, detail)
	if err != nil {
		return nil, err
	}
	p, err := r.arrivalProgress(s, context)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"scan": scan, "message": message, "progress": p}
	if result == "unexpected" {
		return out, nil
	}
	out["context"] = context
	action := "scanned"
	label := fmt.Sprintf("%s · %s · %s", s["masterCode"], context, method)
	if level == "master" {
		label = fmt.Sprintf("%s · master · %s · %s", s["masterCode"], context, method)
	} else {
		input := "diketik"
		if method == "SCANNED" {
			input = "scan"
		}
		out["message"] = fmt.Sprintf("Paket OK (%d/%d) · %s.", p.Scanned, p.Total, input)
		if p.AllScanned {
			out["message"] = "Semua paket sudah discan - siap " + arrivalDone(context)
		}
		if result == "duplicate" {
			action, out["message"] = "duplicate_scan", "Paket ini sudah pernah discan."
		}
	}
	b.audit(r, action, "shipment", s["id"], label, nil, nil)
	return out, nil
}

func (b *business) scanAllArrival(r *request) (any, error) {
	s, err := r.shipment(true)
	if err != nil {
		return nil, err
	}
	context := arrivalContext(s)
	if context == "" {
		return nil, problem(422, fmt.Sprintf("Scan semua hanya untuk shipment PICKED_UP / AT_DEST_GUDANG (saat ini: %s).", s["status"]))
	}
	p, err := r.arrivalProgress(s, context)
	if err != nil {
		return nil, err
	}
	if p.Total == 0 {
		return nil, problem(422, "Shipment belum punya detail barang.")
	}
	if p.AllScanned {
		return map[string]any{"created": 0, "progress": p, "message": "Semua paket sudah discan."}, nil
	}
	created := 0
	label := textValue(s["masterCode"])
	message := ""
	if p.IsB2B {
		if _, err = r.insertArrivalScan(s, context, "master", textValue(s["masterCode"]), "ok", "SCANNED", nil); err != nil {
			return nil, err
		}
		created = 1
		label += " · master-scan-all (B2B)"
		message = "Master Resi B2B ter-scan (mode reader) - semua paket lengkap. Siap " + arrivalDone(context)
	} else {
		for _, d := range p.Details {
			if d["scanned"] == true {
				continue
			}
			if _, err = r.insertArrivalScan(s, context, "detail", textValue(d["detailCode"]), "ok", "SCANNED", d["id"]); err != nil {
				return nil, err
			}
			created++
		}
		label += fmt.Sprintf(" · scan-all (%d paket)", created)
		message = fmt.Sprintf("%d paket ditandai ter-scan (mode reader). Semua paket lengkap - siap %s", created, arrivalDone(context))
	}
	b.audit(r, "scanned", "shipment", s["id"], label, nil, nil)
	p, err = r.arrivalProgress(s, context)
	return map[string]any{"created": created, "progress": p, "message": message}, err
}
