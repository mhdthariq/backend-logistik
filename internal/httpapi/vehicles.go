package httpapi

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"

	"github.com/gofiber/fiber/v3"
)

func vehiclePhoto(raw any) (any, error) {
	if raw == nil || raw == "" {
		return nil, nil
	}
	s, ok := raw.(string)
	const max = 4 * 1024 * 1024
	if ok && len(s) <= ((max+2)/3)*4+32 {
		match := regexp.MustCompile(`^data:image/(jpeg|png|webp);base64,([A-Za-z0-9+/]+={0,2})$`).FindStringSubmatch(s)
		if len(match) == 3 && len(match[2])%4 == 0 {
			if decoded, err := base64.StdEncoding.DecodeString(match[2]); err == nil && len(decoded) <= max {
				return s, nil
			}
		}
	}
	return nil, problem(422, "Foto kendaraan harus JPG, PNG, atau WEBP (maksimal 4 MB).")
}
func requiredNumber(body map[string]any, key string, min float64) (any, error) {
	v := numeric(body[key])
	if v == nil || v.(float64) < min {
		return nil, fieldProblem(fmt.Sprintf("The %s field must be a valid number ≥ %g.", key, min), key, fmt.Sprintf("The %s field must be a valid number >= %g.", key, min))
	}
	return v, nil
}
func vehicleStatus(v any) bool { return v == "ACTIVE" || v == "MAINTENANCE" || v == "INACTIVE" }

func (b *business) vehicles(r *request) (any, error) {
	return r.many(`SELECT to_jsonb(v)||jsonb_build_object(
 'owner',(SELECT to_jsonb(p)||jsonb_build_object('user',jsonb_build_object('name',u.name)) FROM "Partner" p JOIN "User" u ON u.id=p."userId" WHERE p.id=v."ownerId"),
 'assignments',COALESCE((SELECT jsonb_agg(to_jsonb(a)||jsonb_build_object('driver',(SELECT to_jsonb(e) FROM "Employee" e WHERE e.id=a."driverId"),'kenek',(SELECT to_jsonb(e) FROM "Employee" e WHERE e.id=a."kenekId"))) FROM "VehicleAssignment" a WHERE a."vehicleId"=v.id AND a."validTo" IS NULL),'[]'::jsonb),
 '_count',jsonb_build_object('transports',(SELECT count(*) FROM "Transport" t WHERE t."vehicleId"=v.id)))
 FROM "Vehicle" v WHERE ($1='' OR status=$1) AND ($2='' OR "vehicleNumber" ILIKE $3 ESCAPE '\' OR name ILIKE $3 ESCAPE '\') ORDER BY v.id DESC`, textValue(r.c.Query("status")), textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
}

func vehicleOwner(r *request, raw any, creating bool) (any, error) {
	id := numeric(raw)
	if id == nil || (creating && id.(float64) == 0) {
		return nil, nil
	}
	p, err := r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, problem(404, "Vehicle Owner tidak ditemukan.")
	}
	if p["type"] != "VEHICLE_OWNER" {
		if creating {
			return nil, fieldProblem("Partner terpilih bukan Vehicle Owner.", "ownerId", "Harus partner bertipe Vehicle Owner.")
		}
		return nil, problem(422, "Partner terpilih bukan Vehicle Owner.")
	}
	return p["id"], nil
}

func vehicleDimensions(body map[string]any) (any, any, any, any) {
	l, w, h := numeric(body["lengthM"]), numeric(body["widthM"]), numeric(body["heightM"])
	var volume any
	if l != nil && w != nil && h != nil && l.(float64) > 0 && w.(float64) > 0 && h.(float64) > 0 {
		volume = math.Floor(l.(float64)*w.(float64)*h.(float64)*1000+0.5) / 1000
	}
	return l, w, h, volume
}
func vehicleKoli(raw any) (any, error) {
	v := numeric(raw)
	if v == nil {
		return nil, nil
	}
	if v.(float64) < 0 {
		return nil, fieldProblem("Max Koli tidak boleh negatif.", "maxKoli", "Max Koli tidak boleh negatif.")
	}
	return int(math.Floor(v.(float64) + 0.5)), nil
}

func (b *business) createVehicle(r *request) (any, error) {
	number, err := required(r.body, "vehicleNumber")
	if err != nil {
		return nil, err
	}
	existing, err := r.one(`SELECT to_jsonb(v) FROM "Vehicle" v WHERE "vehicleNumber"=$1`, number)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fieldProblem(fmt.Sprintf("Nomor polisi %s sudah terdaftar.", number), "vehicleNumber", "Nomor polisi sudah terdaftar.")
	}
	status := r.body["status"]
	if !vehicleStatus(status) {
		status = "ACTIVE"
	}
	owner, err := vehicleOwner(r, r.body["ownerId"], true)
	if err != nil {
		return nil, err
	}
	l, w, h, volume := vehicleDimensions(r.body)
	koli, err := vehicleKoli(r.body["maxKoli"])
	if err != nil {
		return nil, err
	}
	photo, err := vehiclePhoto(r.body["photoDataUrl"])
	if err != nil {
		return nil, err
	}
	weight, err := requiredNumber(r.body, "maxWeightKg", 1)
	if err != nil {
		return nil, err
	}
	if volume == nil {
		volume, err = requiredNumber(r.body, "maxVolumeM3", 0.1)
		if err != nil {
			return nil, err
		}
	}
	v, err := r.one(`INSERT INTO "Vehicle" AS v ("vehicleNumber","photoDataUrl",name,status,"maxWeightKg","maxVolumeM3","lengthM","widthM","heightM","maxKoli",notes,"ownerId","updatedAt") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW()) RETURNING to_jsonb(v)`, number, photo, nullable(r.body["name"]), status, weight, volume, l, w, h, koli, nullable(r.body["notes"]), owner)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "vehicle", v["id"], number, nil, v)
	return v, nil
}

func (b *business) updateVehicle(r *request) (any, error) {
	v, err := r.find("Vehicle", "Kendaraan tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	data := patchFields(r.body, v, []string{"name", "notes"}, nil, false)
	if raw, ok := r.body["vehicleNumber"]; ok {
		data["vehicleNumber"] = nullable(raw)
		if data["vehicleNumber"] == nil {
			data["vehicleNumber"] = v["vehicleNumber"]
		}
	}
	if raw, ok := r.body["photoDataUrl"]; ok {
		photo, err := vehiclePhoto(raw)
		if err != nil {
			return nil, err
		}
		data["photoDataUrl"] = photo
	}
	if raw := r.body["status"]; vehicleStatus(raw) {
		data["status"] = raw
	}
	if _, ok := r.body["maxWeightKg"]; ok {
		weight, err := requiredNumber(r.body, "maxWeightKg", 1)
		if err != nil {
			return nil, err
		}
		data["maxWeightKg"] = weight
	}
	l, w, h, volume := vehicleDimensions(r.body)
	for key, value := range map[string]any{"lengthM": l, "widthM": w, "heightM": h} {
		if _, ok := r.body[key]; ok {
			data[key] = value
		}
	}
	if volume != nil {
		data["maxVolumeM3"] = volume
	} else if _, ok := r.body["maxVolumeM3"]; ok {
		volume, err := requiredNumber(r.body, "maxVolumeM3", 0.1)
		if err != nil {
			return nil, err
		}
		data["maxVolumeM3"] = volume
	}
	if raw, ok := r.body["maxKoli"]; ok {
		koli, err := vehicleKoli(raw)
		if err != nil {
			return nil, err
		}
		data["maxKoli"] = koli
	}
	if raw, ok := r.body["ownerId"]; ok {
		owner, err := vehicleOwner(r, raw, false)
		if err != nil {
			return nil, err
		}
		data["ownerId"] = owner
	}
	updated, err := r.updateRow("Vehicle", idValue(v["id"]), data)
	if err != nil {
		return nil, err
	}
	changes := diff(v, updated)
	capacity := []any{}
	for _, key := range []string{"maxWeightKg", "maxVolumeM3", "maxKoli"} {
		if change, ok := changes[key].(fiber.Map); ok {
			capacity = append(capacity, fiber.Map{"field": key, "before": change["before"], "after": change["after"]})
		}
	}
	after := fiber.Map{}
	for k, value := range updated {
		after[k] = value
	}
	if len(capacity) > 0 {
		after["capacityConfigChanged"], after["capacityChanges"] = true, capacity
	}
	b.audit(r, "updated", "vehicle", v["id"], textValue(updated["vehicleNumber"]), changes, after)
	return updated, nil
}

func (b *business) deleteVehicle(r *request) (any, error) {
	v, err := r.find("Vehicle", "Kendaraan tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	var count int
	if err := r.db.QueryRow(r.ctx, `SELECT count(*) FROM "Transport" WHERE "vehicleId"=$1`, v["id"]).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		updated, err := r.updateRow("Vehicle", idValue(v["id"]), map[string]any{"status": "INACTIVE"})
		if err != nil {
			return nil, err
		}
		b.audit(r, "deactivated", "vehicle", v["id"], textValue(v["vehicleNumber"]), nil, nil)
		return fiber.Map{"deactivated": true, "vehicle": updated}, nil
	}
	if err := r.exec(`DELETE FROM "Vehicle" WHERE id=$1`, v["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "vehicle", v["id"], textValue(v["vehicleNumber"]), v, nil)
	return fiber.Map{"deleted": true}, nil
}
