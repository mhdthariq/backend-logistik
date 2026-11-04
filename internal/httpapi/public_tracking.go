package httpapi

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

type trackingKey struct{ company, code string }
type trackingAttempt struct {
	count   int
	expires time.Time
}
type trackingLimiter struct {
	mu       sync.Mutex
	attempts map[trackingKey]trackingAttempt
}

func (l *trackingLimiter) allow(key trackingKey, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[trackingKey]trackingAttempt)
	}
	for k, e := range l.attempts {
		if !now.Before(e.expires) {
			delete(l.attempts, k)
		}
	}
	e, exists := l.attempts[key]
	if exists && e.count >= 10 || !exists && len(l.attempts) >= 10000 {
		return false
	}
	if !exists {
		e.expires = now.Add(15 * time.Minute)
	}
	e.count++
	l.attempts[key] = e
	return true
}

func normalizeResiPhone(value any) string {
	s, ok := value.(string)
	if !ok || jsLength(s) > 40 {
		return ""
	}
	var digits strings.Builder
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits.WriteRune(c)
		case c == '+' || c == '(' || c == ')' || c == '.' || c == '-' || strings.TrimSpace(string(c)) == "":
		default:
			return ""
		}
	}
	result := digits.String()
	if strings.HasPrefix(result, "62") {
		result = "0" + result[2:]
	}
	if len(result) < 8 || len(result) > 15 {
		return ""
	}
	return result
}

func maskResiValue(value any) any {
	if value == nil {
		return nil
	}
	chars := []rune(textValue(value))
	if len(chars) == 0 {
		return nil
	}
	half := len(chars) / 2
	return string(chars[:half]) + strings.Repeat("*", len(chars)-half)
}

func (b *business) registerPublicTracking(app *fiber.App) {
	app.Get("/api/v1/public/track/:resi", func(c fiber.Ctx) error {
		return problem(405, "Scan QR pada resi dan verifikasi nomor telepon untuk melacak paket.")
	})
	lookup := b.handle("", false, false, b.publicTracking)
	app.Post("/api/v1/public/track/:resi", func(c fiber.Ctx) error {
		c.Set("Cache-Control", "no-store, private")
		corp := c.Get("x-corp-id")
		if strings.TrimSpace(corp) == "" || jsLength(corp) > 128 {
			return problem(422, "Scan QR pada resi untuk membuka pelacakan.")
		}
		var body map[string]any
		if err := json.Unmarshal(c.Body(), &body); err != nil {
			body = nil
		}
		code := strings.TrimSpace(c.Params("resi"))
		if code == "" || jsLength(code) > 128 || normalizeResiPhone(body["phone"]) == "" {
			return problem(422, "Masukkan nomor telepon lengkap pengirim atau penerima.")
		}
		if !b.trackingAttempts.allow(trackingKey{strings.Clone(strings.TrimSpace(corp)), strings.Clone(code)}, time.Now()) {
			return problem(429, "Terlalu banyak percobaan. Coba lagi dalam 15 menit.")
		}
		return lookup(c)
	})
}

var shipmentStatusLabels = map[string]string{
	"CREATED": "Created", "READY_FOR_PICKUP": "Ready for Pickup", "PICKED_UP": "Picked Up",
	"RECEIVED_AT_GUDANG": "Arrived at Origin Gudang", "IN_TRANSPORT": "In Transport",
	"AT_DEST_GUDANG": "Tiba di Gudang Tujuan", "ARRIVED_AT_GUDANG": "Arrived at Gudang Tujuan",
	"DELIVERED": "Delivered", "CANCELLED": "Cancelled",
}

func (b *business) publicTracking(r *request) (any, error) {
	s, err := r.one(`SELECT to_jsonb(s)||jsonb_build_object('customer',jsonb_build_object('name',c.name,'companyName',c."companyName",'type',c.type,'phone',c.phone)) FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId" WHERE s.status<>'CANCELLED' AND (s."masterCode"=$1 OR s.resi=$1 OR EXISTS(SELECT 1 FROM "DetailShipment" d WHERE d."masterId"=s.id AND d."detailCode"=$1)) LIMIT 1`, strings.TrimSpace(r.c.Params("resi")))
	if err != nil {
		return nil, err
	}
	denied := problem(403, "Resi atau nomor telepon tidak sesuai. Gunakan nomor lengkap pengirim atau penerima.")
	if s == nil {
		return nil, denied
	}
	customer := s["customer"].(map[string]any)
	senderPhone := s["pengirimPhone"]
	if senderPhone == nil {
		senderPhone = customer["phone"]
	}
	phone := normalizeResiPhone(r.body["phone"])
	if phone == "" || phone != normalizeResiPhone(senderPhone) && phone != normalizeResiPhone(s["penerimaContact"]) {
		return nil, denied
	}
	// Fetch only the public fields. Child scan codes, finances and credentials
	// cannot enter the response through a nested relation.
	packages, err := r.many(`SELECT jsonb_build_object('id',d.id,'description',d.description,'lengthCm',d."lengthCm",'widthCm',d."widthCm",'heightCm',d."heightCm",'actualWeightKg',d."actualWeightKg") FROM "DetailShipment" d WHERE "masterId"=$1 ORDER BY id`, s["id"])
	if err != nil {
		return nil, err
	}
	events, err := r.many(`SELECT jsonb_build_object('id',e.id,'event',e.event,'description',e.description,'occurredAt',e."occurredAt",'actorName',u.name) FROM "TrackingEvent" e LEFT JOIN "User" u ON u.id=e."actorId" WHERE e."masterId"=$1 ORDER BY e."occurredAt" DESC`, s["id"])
	if err != nil {
		return nil, err
	}
	warehouse := func(id any, support bool) (map[string]any, error) {
		if support {
			return r.one(`SELECT jsonb_build_object('name',name,'city',city,'customerSupportContact',"customerSupportContact") FROM "Warehouse" WHERE id=$1`, id)
		}
		return r.one(`SELECT jsonb_build_object('name',name,'city',city) FROM "Warehouse" WHERE id=$1`, id)
	}
	origin, err := warehouse(s["originWarehouseId"], false)
	if err != nil {
		return nil, err
	}
	destination, err := warehouse(s["destinationWarehouseId"], true)
	if err != nil {
		return nil, err
	}
	arrived, err := warehouse(s["arrivedWarehouseId"], false)
	if err != nil {
		return nil, err
	}
	senderName := s["pengirimName"]
	if senderName == nil {
		senderName = customer["companyName"]
	}
	if senderName == nil {
		senderName = customer["name"]
	}
	label := shipmentStatusLabels[textValue(s["status"])]
	if label == "" {
		label = textValue(s["status"])
	}
	return map[string]any{
		"masterCode": s["masterCode"], "resi": s["resi"], "status": s["status"], "statusLabel": label,
		"origin": s["origin"], "destination": s["destination"], "originWarehouse": origin, "destinationWarehouse": destination, "arrivedWarehouse": arrived,
		"sender":       map[string]any{"name": senderName, "phone": maskResiValue(senderPhone)},
		"recipient":    map[string]any{"name": s["penerimaName"], "address": maskResiValue(s["penerimaAddress"]), "phone": maskResiValue(s["penerimaContact"])},
		"customerType": customer["type"], "createdAt": s["createdAt"], "updatedAt": s["updatedAt"],
		"packages": packages, "totals": shipmentTotals(packages, false), "trackingEvents": events,
	}, nil
}
