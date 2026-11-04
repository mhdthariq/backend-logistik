package httpapi

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

func (b *business) employees(r *request) (any, error) {
	return r.many(`SELECT to_jsonb(e)||jsonb_build_object(
 'warehouse',(SELECT jsonb_build_object('id',w.id,'name',w.name,'city',w.city) FROM "Warehouse" w WHERE w.id=e."warehouseId"),
 'user',(SELECT (to_jsonb(u)-'passwordHash')||jsonb_build_object('roles',COALESCE((SELECT jsonb_agg(to_jsonb(ur)||jsonb_build_object('role',to_jsonb(role)) ORDER BY role.id) FROM "UserRole" ur JOIN "Role" role ON role.id=ur."roleId" WHERE ur."userId"=u.id),'[]'::jsonb)) FROM "User" u WHERE u."employeeId"=e.id))
 FROM "Employee" e WHERE ($1 OR e."isActive") AND ($2='' OR e.name ILIKE $3 ESCAPE '\' OR e."employeeNumber" ILIKE $3 ESCAPE '\' OR e.position ILIKE $3 ESCAPE '\') ORDER BY e.id`, boolValue(r.c.Query("include_inactive"), true), textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
}

func searchPattern(s string) string {
	return "%" + regexp.MustCompile(`([%_\\])`).ReplaceAllString(s, `\$1`) + "%"
}

func (b *business) createEmployee(r *request) (any, error) {
	name, err := required(r.body, "name")
	if err != nil {
		return nil, err
	}
	// Transaction-scoped advisory lock preserves the first-unused legacy code rule
	// while serializing concurrent Go writers. Legacy writers still need cutover.
	if err := r.exec(`SELECT pg_advisory_xact_lock(731021)`); err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT jsonb_build_object('code',"employeeNumber") FROM "Employee"`)
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	pattern := regexp.MustCompile(`(\d+)\s*$`)
	for _, row := range rows {
		code := textValue(row["code"])
		if len(code) >= 4 && code[:4] == "EMP-" {
			match := pattern.FindStringSubmatch(code)
			if len(match) > 1 {
				n, _ := strconv.Atoi(match[1])
				used[n] = true
			}
		}
	}
	n := 1
	for used[n] {
		n++
	}
	var warehouse any
	if id := idValue(r.body["warehouseId"]); id != 0 {
		warehouse = id
	}
	e, err := r.one(`INSERT INTO "Employee" AS e ("employeeNumber",name,phone,position,"warehouseId","isActive","updatedAt") VALUES ($1,$2,$3,$4,$5,true,CURRENT_TIMESTAMP) RETURNING to_jsonb(e)`, fmt.Sprintf("EMP-%06d", n), name, nullable(r.body["phone"]), nullable(r.body["position"]), warehouse)
	if err != nil {
		return nil, err
	}
	b.audit(r, "created", "employee", e["id"], textValue(e["name"]), nil, e)
	return e, nil
}

func employeeByID(r *request) (map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	e, err := r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, problem(404, "Employee tidak ditemukan.")
	}
	return e, nil
}

func (b *business) updateEmployee(r *request) (any, error) {
	existing, err := employeeByID(r)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	if v, ok := r.body["name"]; ok {
		name := nullable(v)
		if name == nil {
			name = existing["name"]
		}
		data["name"] = name
	}
	for _, key := range []string{"phone", "position"} {
		if v, ok := r.body[key]; ok {
			data[key] = nullable(v)
		}
	}
	if v, ok := r.body["warehouseId"]; ok {
		var id any
		if truthy(v) {
			id = idValue(v)
		}
		data["warehouseId"] = id
	}
	if v, ok := r.body["isActive"]; ok {
		data["isActive"] = truthy(v)
	}
	e, err := r.updateRow("Employee", idValue(existing["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "employee", e["id"], textValue(e["name"]), diff(existing, e), nil)
	return e, nil
}

func (b *business) disableEmployee(r *request) (any, error) {
	existing, err := employeeByID(r)
	if err != nil {
		return nil, err
	}
	e, err := r.updateRow("Employee", idValue(existing["id"]), map[string]any{"isActive": false})
	if err != nil {
		return nil, err
	}
	if err := r.exec(`UPDATE "User" SET "isActive"=false,"updatedAt"=CURRENT_TIMESTAMP WHERE "employeeId"=$1`, existing["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "disabled", "employee", e["id"], textValue(e["name"]), nil, nil)
	return fiber.Map{"disabled": true, "employee": e}, nil
}

func diff(before, after map[string]any) map[string]any {
	changes := map[string]any{}
	for k, v := range after {
		if fmt.Sprint(v) != fmt.Sprint(before[k]) {
			changes[k] = fiber.Map{"before": before[k], "after": v}
		}
	}
	return changes
}
