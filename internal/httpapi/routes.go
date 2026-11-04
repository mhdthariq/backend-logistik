package httpapi

import (
	"fmt"
	"math"

	"github.com/gofiber/fiber/v3"
)

const routeSelect = `SELECT to_jsonb(r)||jsonb_build_object('checkpoints',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY sequence) FROM "Checkpoint" c WHERE c."routeId"=r.id),'[]'::jsonb))`

func (b *business) routes(r *request) (any, error) {
	return r.many(routeSelect+`||jsonb_build_object('_count',jsonb_build_object('transports',(SELECT count(*) FROM "Transport" t WHERE t."routeId"=r.id))) FROM "Route" r WHERE ($1 OR "isActive") AND ($2='' OR name ILIKE $3 ESCAPE '\' OR origin ILIKE $3 ESCAPE '\' OR destination ILIKE $3 ESCAPE '\') ORDER BY r.id DESC`, boolValue(r.c.Query("include_inactive"), true), textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
}

func (b *business) createRoute(r *request) (any, error) {
	name, err := required(r.body, "name")
	if err != nil {
		return nil, err
	}
	route, err := r.one(`INSERT INTO "Route" AS r (name,origin,destination,"isActive","updatedAt") VALUES ($1,$2,$3,true,NOW()) RETURNING to_jsonb(r)`, name, nullable(r.body["origin"]), nullable(r.body["destination"]))
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "route", route["id"], name, nil, route)
	return route, nil
}

func (b *business) route(r *request) (any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	route, err := r.one(routeSelect+`||jsonb_build_object('transports',COALESCE((SELECT jsonb_agg(to_jsonb(recent)) FROM (SELECT * FROM "Transport" t WHERE t."routeId"=r.id ORDER BY "createdAt" DESC LIMIT 10) recent),'[]'::jsonb)) FROM "Route" r WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if route == nil {
		return nil, problem(404, "Rute tidak ditemukan.")
	}
	return route, nil
}

func (b *business) updateRoute(r *request) (any, error) {
	existing, err := r.find("Route", "Rute tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	data := patchFields(r.body, existing, []string{"name", "origin", "destination"}, nil, true)
	route, err := r.updateRow("Route", idValue(existing["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "route", route["id"], textValue(route["name"]), diff(existing, route), nil)
	return route, nil
}

func (b *business) deleteRoute(r *request) (any, error) {
	route, err := r.find("Route", "Rute tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	var count int
	if err := r.db.QueryRow(r.ctx, `SELECT count(*) FROM "Transport" WHERE "routeId"=$1`, route["id"]).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		updated, err := r.updateRow("Route", idValue(route["id"]), map[string]any{"isActive": false})
		if err != nil {
			return nil, err
		}
		b.audit(r, "deactivated", "route", route["id"], textValue(route["name"]), nil, nil)
		return fiber.Map{"deactivated": true, "route": updated}, nil
	}
	if err := r.exec(`DELETE FROM "Route" WHERE id=$1`, route["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "route", route["id"], textValue(route["name"]), route, nil)
	return fiber.Map{"deleted": true}, nil
}

type checkpointInput struct {
	id                  int
	name                string
	latitude, longitude float64
	radius              int
}

func parseCheckpoint(body map[string]any, index int) (checkpointInput, error) {
	n := index + 1
	cp := checkpointInput{id: idValue(body["id"]), radius: 250}
	cp.name, _ = body["name"].(string)
	cp.name = textValue(cp.name)
	if cp.name == "" {
		return cp, fieldProblem(fmt.Sprintf("Nama checkpoint #%d wajib diisi.", n), "name", fmt.Sprintf("Checkpoint #%d: nama wajib diisi.", n))
	}
	for _, item := range []struct {
		key      string
		min, max float64
		target   *float64
	}{{"latitude", -90, 90, &cp.latitude}, {"longitude", -180, 180, &cp.longitude}} {
		v, exists := body[item.key]
		var value any
		if exists && (v == nil || v == "") {
			value = float64(0)
		} else {
			value = numeric(v)
		}
		if value == nil || value.(float64) < item.min || value.(float64) > item.max {
			return cp, fieldProblem(fmt.Sprintf("Checkpoint #%d: %s harus angka %.0f..%.0f.", n, item.key, item.min, item.max), item.key, fmt.Sprintf("Checkpoint #%d: %s tidak valid.", n, item.key))
		}
		*item.target = math.Floor(value.(float64)*1e7+0.5) / 1e7
	}
	radius := float64(250)
	if v := numeric(body["radiusMeters"]); v != nil {
		radius = v.(float64)
	} else if raw, exists := body["radiusMeters"]; exists && raw != nil && raw != "" {
		radius = math.NaN()
	} else if v := numeric(body["radiusKm"]); v != nil {
		radius = v.(float64) * 1000
	} else if raw, exists := body["radiusKm"]; exists && raw != nil && raw != "" {
		radius = math.NaN()
	}
	if math.IsNaN(radius) || radius < 10 || radius > 10000 {
		return cp, fieldProblem(fmt.Sprintf("Checkpoint #%d: radius harus antara 10 m (0.01 KM) dan 10000 m (10 KM).", n), "radiusMeters", fmt.Sprintf("Checkpoint #%d: radius 10..10000 meter.", n))
	}
	cp.radius = int(math.Floor(radius + 0.5))
	return cp, nil
}

func insertCheckpoint(r *request, routeID any, cp checkpointInput, sequence int) (map[string]any, error) {
	return r.one(`INSERT INTO "Checkpoint" AS c ("routeId",name,sequence,latitude,longitude,"radiusMeters","updatedAt") VALUES ($1,$2,$3,$4,$5,$6,NOW()) RETURNING to_jsonb(c)`, routeID, cp.name, sequence, cp.latitude, cp.longitude, cp.radius)
}

func (b *business) appendCheckpoint(r *request) (any, error) {
	route, err := r.find("Route", "Rute tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	var count, maxSequence int
	if err := r.db.QueryRow(r.ctx, `SELECT count(*),COALESCE(max(sequence),0) FROM "Checkpoint" WHERE "routeId"=$1`, route["id"]).Scan(&count, &maxSequence); err != nil {
		return nil, err
	}
	cp, err := parseCheckpoint(r.body, count)
	if err != nil {
		return nil, err
	}
	checkpoint, err := insertCheckpoint(r, route["id"], cp, maxSequence+1)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "checkpoint", checkpoint["id"], fmt.Sprintf("%s · #%d %s", route["name"], maxSequence+1, cp.name), nil, checkpoint)
	return checkpoint, nil
}

func (b *business) saveCheckpoints(r *request) (any, error) {
	route, err := r.find("Route", "Rute tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	raw, ok := r.body["checkpoints"].([]any)
	if !ok {
		return nil, fieldProblem("Body wajib berisi array `checkpoints`.", "checkpoints", "Array checkpoints wajib ada.")
	}
	if len(raw) < 3 {
		return nil, problem(422, fmt.Sprintf("Rute wajib memiliki minimal 3 checkpoint (dikirim: %d).", len(raw)))
	}
	checkpoints, err := r.many(`SELECT to_jsonb(c) FROM "Checkpoint" c WHERE "routeId"=$1 ORDER BY sequence`, route["id"])
	if err != nil {
		return nil, err
	}
	existingIDs := map[int]bool{}
	maxSequence := 0
	for _, cp := range checkpoints {
		existingIDs[idValue(cp["id"])] = true
		if n := idValue(cp["sequence"]); n > maxSequence {
			maxSequence = n
		}
	}
	parsed := make([]checkpointInput, 0, len(raw))
	sent := map[int]bool{}
	created := 0
	for i, v := range raw {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, problem(422, fmt.Sprintf("Checkpoint #%d tidak valid.", i+1))
		}
		cp, err := parseCheckpoint(obj, i)
		if err != nil {
			return nil, err
		}
		if cp.id > 0 {
			if !existingIDs[cp.id] {
				return nil, problem(422, fmt.Sprintf("Checkpoint id %d tidak milik rute %s.", cp.id, route["name"]))
			}
			if sent[cp.id] {
				return nil, problem(422, "Checkpoint id tidak boleh duplikat.")
			}
			sent[cp.id] = true
		} else {
			created++
		}
		parsed = append(parsed, cp)
	}
	deleted := 0
	for id := range existingIDs {
		if !sent[id] {
			if err := r.exec(`DELETE FROM "CheckpointRecord" WHERE "checkpointId"=$1`, id); err != nil {
				return nil, err
			}
			if err := r.exec(`DELETE FROM "Checkpoint" WHERE id=$1`, id); err != nil {
				return nil, err
			}
			deleted++
		}
	}
	// Move all existing positions above the old and new ranges before reordering.
	// This avoids transient violations of the (routeId, sequence) unique key.
	if err := r.exec(`UPDATE "Checkpoint" SET sequence=sequence+$2 WHERE "routeId"=$1`, route["id"], maxSequence+len(parsed)+1); err != nil {
		return nil, err
	}
	for i, cp := range parsed {
		if cp.id <= 0 {
			if _, err := insertCheckpoint(r, route["id"], cp, i+1); err != nil {
				return nil, err
			}
		} else {
			if _, err := r.updateRow("Checkpoint", cp.id, map[string]any{"name": cp.name, "sequence": i + 1, "latitude": cp.latitude, "longitude": cp.longitude, "radiusMeters": cp.radius}); err != nil {
				return nil, err
			}
		}
	}
	updated := len(parsed) - created
	b.audit(r, "updated", "route", route["id"], fmt.Sprintf("%s · bulk save %d checkpoint", route["name"], len(parsed)), nil, fiber.Map{"total": len(parsed), "created": created, "updated": updated, "deleted": deleted})
	rows, err := r.many(`SELECT to_jsonb(c) FROM "Checkpoint" c WHERE "routeId"=$1 ORDER BY sequence`, route["id"])
	if err != nil {
		return nil, err
	}
	return fiber.Map{"routeId": route["id"], "total": len(rows), "created": created, "updated": updated, "deleted": deleted, "checkpoints": rows}, nil
}

func (b *business) updateCheckpoint(r *request) (any, error) {
	cp, err := r.find("Checkpoint", "Checkpoint tidak ditemukan.", true)
	if err != nil {
		return nil, err
	}
	data := patchFields(r.body, cp, []string{"name"}, nil, true)
	for _, item := range []struct {
		key string
		min float64
	}{{"latitude", -90}, {"longitude", -180}, {"radiusMeters", 10}} {
		if raw, exists := r.body[item.key]; exists {
			value := numeric(raw)
			if value == nil || value.(float64) < item.min {
				return nil, fieldProblem(fmt.Sprintf("The %s field must be a valid number ≥ %g.", item.key, item.min), item.key, fmt.Sprintf("The %s field must be a valid number >= %g.", item.key, item.min))
			}
			data[item.key] = value
			if item.key == "radiusMeters" {
				data[item.key] = int(math.Floor(value.(float64) + 0.5))
			}
		}
	}
	updated, err := r.updateRow("Checkpoint", idValue(cp["id"]), data)
	if err != nil {
		return nil, err
	}
	route, err := r.one(`SELECT to_jsonb(r) FROM "Route" r WHERE id=$1`, cp["routeId"])
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "checkpoint", cp["id"], fmt.Sprintf("%s · #%v %s", route["name"], cp["sequence"], updated["name"]), nil, updated)
	return updated, nil
}

func (b *business) deleteCheckpoint(r *request) (any, error) {
	cp, err := r.find("Checkpoint", "Checkpoint tidak ditemukan.", false)
	if err != nil {
		return nil, err
	}
	route, err := r.one(`SELECT to_jsonb(r) FROM "Route" r WHERE id=$1 FOR UPDATE`, cp["routeId"])
	if err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT to_jsonb(c) FROM "Checkpoint" c WHERE "routeId"=$1 ORDER BY sequence`, cp["routeId"])
	if err != nil {
		return nil, err
	}
	if len(rows) <= 3 {
		return nil, problem(422, "Rute wajib memiliki minimal 3 checkpoint. Tambahkan checkpoint lain sebelum menghapus yang ini.")
	}
	if err := r.exec(`DELETE FROM "Checkpoint" WHERE id=$1`, cp["id"]); err != nil {
		return nil, err
	}
	maxSequence := 0
	for _, row := range rows {
		if n := idValue(row["sequence"]); n > maxSequence {
			maxSequence = n
		}
	}
	if err := r.exec(`UPDATE "Checkpoint" SET sequence=sequence+$2 WHERE "routeId"=$1`, cp["routeId"], maxSequence+len(rows)+1); err != nil {
		return nil, err
	}
	seq := 0
	for _, row := range rows {
		if row["id"] != cp["id"] {
			seq++
			if _, err := r.updateRow("Checkpoint", idValue(row["id"]), map[string]any{"sequence": seq}); err != nil {
				return nil, err
			}
		}
	}
	b.audit(r, "deleted", "checkpoint", cp["id"], fmt.Sprintf("%s · #%v %s", route["name"], cp["sequence"], cp["name"]), cp, nil)
	return fiber.Map{"deleted": true}, nil
}
