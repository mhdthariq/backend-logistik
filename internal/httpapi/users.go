package httpapi

import (
	"fmt"
	"math"
	"strings"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/identity"
)

const userSelect = `SELECT (to_jsonb(u)-'passwordHash')||jsonb_build_object(
 'employee',(SELECT to_jsonb(e) FROM "Employee" e WHERE e.id=u."employeeId"),
 'roles',COALESCE((SELECT jsonb_agg(to_jsonb(ur)||jsonb_build_object('role',to_jsonb(role)) ORDER BY role.id) FROM "UserRole" ur JOIN "Role" role ON role.id=ur."roleId" WHERE ur."userId"=u.id),'[]'::jsonb),
 'partner',(SELECT to_jsonb(p)||jsonb_build_object('warehouse',(SELECT jsonb_build_object('id',w.id,'name',w.name) FROM "Warehouse" w WHERE w.id=p."warehouseId")) FROM "Partner" p WHERE p."userId"=u.id))`
const userPermissions = `||jsonb_build_object('permissions',COALESCE((SELECT jsonb_agg(to_jsonb(up)||jsonb_build_object('permission',to_jsonb(p)) ORDER BY p.id) FROM "UserPermission" up JOIN "Permission" p ON p.id=up."permissionId" WHERE up."userId"=u.id),'[]'::jsonb))`

func decorateUser(u map[string]any, permissions, inheritOwner bool) map[string]any {
	u["partnerId"], u["partnerType"], u["partnerWarehouseId"], u["partnerWarehouseName"] = nil, nil, nil, nil
	if p, ok := u["partner"].(map[string]any); ok {
		u["partnerId"], u["partnerType"], u["partnerWarehouseId"] = p["id"], p["type"], p["warehouseId"]
		if w, ok := p["warehouse"].(map[string]any); ok {
			u["partnerWarehouseName"] = w["name"]
		}
	}
	if inheritOwner {
		for _, v := range u["roles"].([]any) {
			if v.(map[string]any)["role"].(map[string]any)["slug"] == "owner" {
				u["isOwner"] = true
			}
		}
	}
	if permissions {
		permissionIDs, direct := []any{}, []any{}
		for _, v := range u["permissions"].([]any) {
			p := v.(map[string]any)
			permissionIDs = append(permissionIDs, p["permissionId"])
			direct = append(direct, p["permission"])
		}
		u["permissionIds"], u["directPermissions"] = permissionIDs, direct
	}
	return u
}

func (b *business) users(r *request) (any, error) {
	users, err := r.many(userSelect+userPermissions+` FROM "User" u WHERE ($1 OR u."isActive") AND ($2='' OR u.name ILIKE $3 ESCAPE '\' OR u.username ILIKE $3 ESCAPE '\') ORDER BY u.id`, boolValue(r.c.Query("include_inactive"), true), textValue(r.c.Query("search")), searchPattern(r.c.Query("search")))
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		decorateUser(u, true, true)
	}
	return users, nil
}

func userByID(r *request) (map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	u, err := r.one(`SELECT to_jsonb(u) FROM "User" u WHERE id=$1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, problem(404, "User tidak ditemukan.")
	}
	return u, nil
}

func validateUserPermissions(r *request, permissionIDs []int) error {
	for _, id := range permissionIDs {
		p, err := r.one(`SELECT to_jsonb(p) FROM "Permission" p WHERE id=$1`, id)
		if err != nil {
			return err
		}
		if p == nil {
			return fieldProblem("Ada permission yang tidak ditemukan.", "permissionIds", "Permission tidak valid.")
		}
	}
	return nil
}

func selectedRoles(r *request, roleIDs []int, employeeID any, field string) ([]string, error) {
	slugs := []string{}
	for _, id := range roleIDs {
		role, err := r.one(`SELECT to_jsonb(role) FROM "Role" role WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		if role == nil {
			continue
		}
		slug := textValue(role["slug"])
		if employeeID != nil && (slug == "marketing" || slug == "vehicle-owner") {
			return nil, fieldProblem("Partner Marketing / Vehicle Owner tidak boleh terhubung ke employee.", field, "Partner tidak boleh terhubung ke employee.")
		}
		slugs = append(slugs, slug)
	}
	return slugs, nil
}

func assignUserRoles(r *request, userID any, roleIDs []int) error {
	if err := r.exec(`DELETE FROM "UserRole" WHERE "userId"=$1`, userID); err != nil {
		return err
	}
	for _, id := range roleIDs {
		if err := r.exec(`INSERT INTO "UserRole" ("userId","roleId") SELECT $1,id FROM "Role" WHERE id=$2 ON CONFLICT DO NOTHING`, userID, id); err != nil {
			return err
		}
	}
	return nil
}
func assignUserPermissions(r *request, userID any, permissionIDs []int) error {
	if err := validateUserPermissions(r, permissionIDs); err != nil {
		return err
	}
	if err := r.exec(`DELETE FROM "UserPermission" WHERE "userId"=$1`, userID); err != nil {
		return err
	}
	for _, id := range permissionIDs {
		if err := r.exec(`INSERT INTO "UserPermission" ("userId","permissionId") VALUES ($1,$2) ON CONFLICT DO NOTHING`, userID, id); err != nil {
			return err
		}
	}
	return nil
}

func (b *business) createUser(r *request) (any, error) {
	username, err := required(r.body, "username")
	if err != nil {
		return nil, err
	}
	username = strings.ToLower(username)
	name, err := required(r.body, "name")
	if err != nil {
		return nil, err
	}
	password, err := required(r.body, "password")
	if err != nil {
		return nil, err
	}
	if jsLength(password) < 8 {
		return nil, fieldProblem("Password minimal 8 karakter.", "password", "Password minimal 8 karakter.")
	}
	existing, err := r.one(`SELECT to_jsonb(u) FROM "User" u WHERE username=$1`, username)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fieldProblem(fmt.Sprintf("Username %s sudah dipakai.", username), "username", "Username sudah dipakai.")
	}
	var employeeID any
	if id := idValue(r.body["employeeId"]); id != 0 {
		e, err := r.one(`SELECT to_jsonb(e) FROM "Employee" e WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		if e == nil {
			return nil, fieldProblem("Employee tidak ditemukan.", "employeeId", "Employee tidak ditemukan.")
		}
		linked, err := r.one(`SELECT to_jsonb(u) FROM "User" u WHERE "employeeId"=$1`, id)
		if err != nil {
			return nil, err
		}
		if linked != nil {
			return nil, fieldProblem("Employee ini sudah memiliki akun user.", "employeeId", "Employee sudah memiliki akun.")
		}
		employeeID = id
	}
	roleIDs, _ := ids(r.body["roleIds"])
	permissionIDs, _ := ids(r.body["permissionIds"])
	if err := validateUserPermissions(r, permissionIDs); err != nil {
		return nil, err
	}
	slugs, err := selectedRoles(r, roleIDs, employeeID, "employeeId")
	if err != nil {
		return nil, err
	}
	hash, err := identity.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u, err := r.one(`INSERT INTO "User" AS u (username,name,"passwordHash","employeeId","isActive","updatedAt") VALUES ($1,$2,$3,$4,true,CURRENT_TIMESTAMP) RETURNING to_jsonb(u)-'passwordHash'`, username, name, hash, employeeID)
	if err != nil {
		return nil, err
	}
	if err := assignUserRoles(r, u["id"], roleIDs); err != nil {
		return nil, err
	}
	if err := assignUserPermissions(r, u["id"], permissionIDs); err != nil {
		return nil, err
	}
	if err := provisionPartner(r, u["id"], slugs, true); err != nil {
		return nil, err
	}
	b.audit(r, "created", "user", u["id"], username, nil, fiber.Map{"username": username, "name": name})
	p, err := r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE "userId"=$1`, u["id"])
	if err != nil {
		return nil, err
	}
	if p != nil {
		b.audit(r, "created", "partner", p["id"], username, nil, fiber.Map{"name": name, "type": p["type"], "username": username})
	}
	full, err := r.one(userSelect+` FROM "User" u WHERE u.id=$1`, u["id"])
	if err != nil {
		return nil, err
	}
	return decorateUser(full, false, false), nil
}

func provisionPartner(r *request, userID any, slugs []string, creating bool) error {
	typeName := ""
	for _, slug := range slugs {
		if slug == "vehicle-owner" {
			typeName = "VEHICLE_OWNER"
		}
	}
	for _, slug := range slugs {
		if slug == "marketing" {
			typeName = "MARKETING"
		}
	}
	if typeName == "" {
		return nil
	}
	var warehouse any
	raw, explicit := r.body["warehouseId"]
	if explicit && raw != nil && raw != "" && raw != "general" && raw != "none" {
		id := idValue(raw)
		if id <= 0 {
			return fieldProblem("Gudang tidak valid.", "warehouseId", "Gudang tidak valid.")
		}
		warehouse = id
	}
	if typeName == "VEHICLE_OWNER" {
		warehouse = nil
	}
	if warehouse != nil {
		w, err := r.one(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE id=$1 AND "isActive"`, warehouse)
		if err != nil {
			return err
		}
		if w == nil {
			return fieldProblem("Gudang tidak ditemukan / tidak aktif.", "warehouseId", "Gudang tidak ditemukan / tidak aktif.")
		}
	}
	existing, err := r.one(`SELECT to_jsonb(p) FROM "Partner" p WHERE "userId"=$1`, userID)
	if err != nil {
		return err
	}
	if existing != nil {
		data := map[string]any{}
		if existing["type"] != typeName {
			data["type"] = typeName
		}
		if typeName == "MARKETING" && explicit {
			data["warehouseId"] = warehouse
		}
		_, err := r.updateRow("Partner", idValue(existing["id"]), data)
		return err
	}
	company, partner := 80.0, 20.0
	if creating {
		for key, target := range map[string]*float64{"companyPercent": &company, "partnerPercent": &partner} {
			if v := r.body[key]; v != nil && textValue(v) != "" {
				if n, ok := v.(float64); ok {
					*target = n
				} else {
					var n float64
					if _, err := fmt.Sscan(textValue(v), &n); err == nil {
						*target = n
					}
				}
			}
		}
	}
	if company <= 0 || partner <= 0 {
		return problem(422, "Persentase company dan partner harus lebih besar dari 0.")
	}
	if math.IsNaN(company) || math.IsInf(company, 0) || math.IsNaN(partner) || math.IsInf(partner, 0) || math.Abs(company+partner-100) > 0.001 {
		return problem(422, "Persentase company + partner harus tepat 100%.")
	}
	p, err := r.one(`INSERT INTO "Partner" AS p ("userId",type,"companyPercent","partnerPercent","warehouseId","updatedAt") VALUES ($1,$2,$3,$4,$5,CURRENT_TIMESTAMP) RETURNING to_jsonb(p)`, userID, typeName, company, partner, warehouse)
	if err != nil {
		return err
	}
	return r.exec(`INSERT INTO "Wallet" ("partnerId","updatedAt") VALUES ($1,CURRENT_TIMESTAMP) ON CONFLICT ("partnerId") DO NOTHING`, p["id"])
}

func (b *business) updateUser(r *request) (any, error) {
	existing, err := userByID(r)
	if err != nil {
		return nil, err
	}
	if truthy(existing["isOwner"]) {
		return nil, problem(422, "Akun Owner tidak bisa diubah dari UI.")
	}
	data := map[string]any{}
	if v, ok := r.body["username"]; ok {
		username := strings.ToLower(textValue(v))
		if err := checkUsername(r, existing["id"], username); err != nil {
			return nil, err
		}
		data["username"] = username
	}
	if v, ok := r.body["name"]; ok {
		name := nullable(v)
		if name == nil {
			name = existing["name"]
		}
		data["name"] = name
	}
	passwordChanged := false
	if password := textValue(r.body["password"]); password != "" {
		// Admin route historically checks a trimmed value but hashes the original.
		password = fmt.Sprint(r.body["password"])
		if jsLength(password) < 8 {
			return nil, fieldProblem("Password minimal 8 karakter.", "password", "Password minimal 8 karakter.")
		}
		hash, err := identity.HashPassword(password)
		if err != nil {
			return nil, err
		}
		data["passwordHash"] = hash
		passwordChanged = true
	}
	if v, ok := r.body["isActive"]; ok {
		data["isActive"] = truthy(v)
	}
	if roleIDs, ok := ids(r.body["roleIds"]); ok {
		slugs, err := selectedRoles(r, roleIDs, existing["employeeId"], "roleIds")
		if err != nil {
			return nil, err
		}
		if err := assignUserRoles(r, existing["id"], roleIDs); err != nil {
			return nil, err
		}
		if err := provisionPartner(r, existing["id"], slugs, false); err != nil {
			return nil, err
		}
	} else if _, ok := r.body["warehouseId"]; ok {
		roles, err := r.many(`SELECT jsonb_build_object('slug',role.slug) FROM "UserRole" ur JOIN "Role" role ON role.id=ur."roleId" WHERE ur."userId"=$1`, existing["id"])
		if err != nil {
			return nil, err
		}
		slugs := []string{}
		for _, role := range roles {
			slugs = append(slugs, textValue(role["slug"]))
		}
		if err := provisionPartner(r, existing["id"], slugs, false); err != nil {
			return nil, err
		}
	}
	if permissionIDs, ok := ids(r.body["permissionIds"]); ok {
		if err := assignUserPermissions(r, existing["id"], permissionIDs); err != nil {
			return nil, err
		}
	}
	u, err := r.updateRow("User", idValue(existing["id"]), data)
	if err != nil {
		return nil, err
	}
	b.audit(r, "updated", "user", u["id"], textValue(u["username"]), nil, fiber.Map{"name": u["name"], "roles": r.body["roleIds"], "warehouseId": r.body["warehouseId"]})
	if passwordChanged {
		b.audit(r, "change_password", "user", u["id"], textValue(r.actor["name"])+" change password "+textValue(u["username"]), nil, fiber.Map{"changedBy": r.actor["name"], "changedByUsername": r.actor["username"], "targetUser": u["username"]})
	}
	full, err := r.one(userSelect+userPermissions+` FROM "User" u WHERE u.id=$1`, u["id"])
	if err != nil {
		return nil, err
	}
	return decorateUser(full, true, false), nil
}

func (b *business) disableUser(r *request) (any, error) {
	existing, err := userByID(r)
	if err != nil {
		return nil, err
	}
	if truthy(existing["isOwner"]) {
		return nil, problem(422, "Akun Owner tidak bisa dinonaktifkan.")
	}
	u, err := r.updateRow("User", idValue(existing["id"]), map[string]any{"isActive": false})
	if err != nil {
		return nil, err
	}
	delete(u, "passwordHash")
	if err := r.exec(`DELETE FROM "SessionToken" WHERE "userId"=$1`, existing["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "disabled", "user", u["id"], textValue(u["username"]), nil, nil)
	return fiber.Map{"disabled": true, "user": u}, nil
}
