package httpapi

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/identity"
	"novalogistik/backend/internal/persistence"
)

const authUserSQL = `SELECT jsonb_build_object(
 'id',u.id,'username',u.username,'name',u.name,'isOwner',u."isOwner",'isActive',u."isActive",
 'employeeId',u."employeeId",'warehouseId',e."warehouseId",'warehouseName',w.name,
 'partnerId',p.id,'partnerType',p.type,
 'roles',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',role.id,'slug',role.slug,'name',role.name) ORDER BY ur."roleId") FROM "UserRole" ur JOIN "Role" role ON role.id=ur."roleId" WHERE ur."userId"=u.id),'[]'::jsonb),
 'permissions',COALESCE((SELECT jsonb_agg(DISTINCT perm.slug) FROM "UserRole" ur JOIN "RolePermission" rp ON rp."roleId"=ur."roleId" JOIN "Permission" perm ON perm.id=rp."permissionId" WHERE ur."userId"=u.id),'[]'::jsonb),
 'directPermissions',COALESCE((SELECT jsonb_agg(perm.slug) FROM "UserPermission" up JOIN "Permission" perm ON perm.id=up."permissionId" WHERE up."userId"=u.id),'[]'::jsonb)
) FROM "User" u LEFT JOIN "Employee" e ON e.id=u."employeeId" LEFT JOIN "Warehouse" w ON w.id=e."warehouseId" LEFT JOIN "Partner" p ON p."userId"=u.id WHERE u.id=$1`

func loadAuthUser(ctx context.Context, db persistence.Queryer, id any, login bool) (map[string]any, error) {
	u, err := persistence.Object(ctx, db, authUserSQL, id)
	if err != nil || u == nil {
		return u, err
	}
	permissions := u["permissions"].([]any)
	owner := truthy(u["isOwner"])
	if !login {
		for _, v := range u["roles"].([]any) {
			if v.(map[string]any)["slug"] == "owner" {
				owner = true
			}
		}
		seen := map[any]bool{}
		for _, v := range permissions {
			seen[v] = true
		}
		for _, v := range u["directPermissions"].([]any) {
			if !seen[v] {
				permissions = append(permissions, v)
				seen[v] = true
			}
		}
	}
	if owner {
		permissions = []any{"*"}
	}
	u["isOwner"], u["permissions"] = owner, permissions
	delete(u, "directPermissions")
	return u, nil
}

func authUser(ctx context.Context, db persistence.Queryer, token string, login bool) (map[string]any, error) {
	if token == "" {
		return nil, nil
	}
	session, err := persistence.Object(ctx, db, `SELECT jsonb_build_object('userId',"userId") FROM "SessionToken" WHERE token=$1 AND "expiresAt">=CURRENT_TIMESTAMP`, identity.TokenHash(token))
	if err != nil || session == nil {
		return nil, err
	}
	u, err := loadAuthUser(ctx, db, session["userId"], login)
	if err != nil || u == nil {
		return u, err
	}
	if !truthy(u["isActive"]) {
		return nil, nil
	}
	return u, nil
}

func (b *business) registerIdentity(app *fiber.App) {
	app.Get("/api/v1/mobile/config", b.handle("", true, false, mobileConfig))
	app.Post("/api/v1/auth/login", b.handle("", false, false, b.login))
	app.Post("/api/v1/auth/logout", b.handle("", false, false, b.logout))
	app.Get("/api/v1/auth/me", b.handle("", true, false, func(r *request) (any, error) { r.actor["company"] = fiber.Map{"name": r.company}; return r.actor, nil }))
	app.Get("/api/v1/profile", b.handle("", true, false, b.profile))
	app.Put("/api/v1/profile", b.handle("", true, false, b.updateProfile))
	app.Get("/api/v1/employees", b.handle("employee.view", true, false, b.employees))
	app.Post("/api/v1/employees", b.handle("employee.create", true, true, b.createEmployee))
	app.Put("/api/v1/employees/:id", b.handle("employee.update", true, true, b.updateEmployee))
	app.Delete("/api/v1/employees/:id", b.handle("employee.disable", true, true, b.disableEmployee))
	app.Get("/api/v1/roles", b.handle("role.view", true, false, b.roles))
	app.Post("/api/v1/roles", b.handle("role.create", true, true, b.createRole))
	app.Put("/api/v1/roles/:id", b.handle("role.update", true, true, b.updateRole))
	app.Delete("/api/v1/roles/:id", b.handle("role.delete", true, true, b.deleteRole))
	app.Put("/api/v1/roles/:id/permissions", b.handle("role.update", true, true, b.rolePermissions))
	app.Get("/api/v1/users", b.handle("user.view", true, false, b.users))
	app.Post("/api/v1/users", b.handle("user.create", true, true, b.createUser))
	app.Put("/api/v1/users/:id", b.handle("user.update", true, true, b.updateUser))
	app.Delete("/api/v1/users/:id", b.handle("user.disable", true, true, b.disableUser))
}

func (b *business) login(r *request) (any, error) {
	username, err := required(r.body, "username")
	if err != nil {
		return nil, err
	}
	password, err := required(r.body, "password")
	if err != nil {
		return nil, err
	}
	u, err := r.one(`SELECT to_jsonb(u) FROM "User" u WHERE username=$1`, strings.ToLower(username))
	if err != nil {
		return nil, err
	}
	if u == nil || !truthy(u["isActive"]) || !identity.VerifyPassword(password, textValue(u["passwordHash"])) {
		return nil, problem(401, "Username atau password salah.")
	}
	sessionUser, err := loadAuthUser(r.ctx, r.db, u["id"], true)
	if err != nil {
		return nil, err
	}
	token, hash, err := identity.NewToken()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(12 * time.Hour)
	if err := r.exec(`INSERT INTO "SessionToken" (token,"userId","expiresAt") VALUES ($1,$2,$3)`, hash, u["id"], expires); err != nil {
		return nil, err
	}
	r.actor = sessionUser
	b.audit(r, "login", "auth", nil, textValue(u["username"]), nil, nil)
	return fiber.Map{"token": token, "expiresAt": expires.Format("2006-01-02T15:04:05.000Z"), "user": sessionUser, "company": fiber.Map{"name": r.company}}, nil
}

func (b *business) logout(r *request) (any, error) {
	token := bearer(r.c)
	if token != "" {
		u, err := authUser(r.ctx, r.db, token, false)
		if err != nil {
			return nil, err
		}
		if err := r.exec(`DELETE FROM "SessionToken" WHERE token=$1`, identity.TokenHash(token)); err != nil {
			return nil, err
		}
		if u != nil {
			r.actor = u
			b.audit(r, "logout", "auth", nil, textValue(u["username"]), nil, nil)
		}
	}
	return fiber.Map{"success": true}, nil
}

func (b *business) profile(r *request) (any, error) {
	return r.one(`SELECT jsonb_build_object('id',u.id,'username',u.username,'name',u.name,'isOwner',u."isOwner",
 'roles',COALESCE((SELECT jsonb_agg(jsonb_build_object('name',role.name,'slug',role.slug) ORDER BY role.id) FROM "UserRole" ur JOIN "Role" role ON role.id=ur."roleId" WHERE ur."userId"=u.id),'[]'::jsonb),
 'warehouse',CASE WHEN w.id IS NULL THEN NULL ELSE jsonb_build_object('name',w.name,'city',w.city) END,
 'partner',CASE WHEN p.id IS NULL THEN NULL ELSE jsonb_build_object('id',p.id,'type',p.type,'profitShare',jsonb_build_object('company',p."companyPercent",'partner',p."partnerPercent"),'bank',jsonb_build_object('bankName',p."bankName",'bankAccountName',p."bankAccountName",'bankAccountNumber',p."bankAccountNumber")) END)
 FROM "User" u LEFT JOIN "Employee" e ON e.id=u."employeeId" LEFT JOIN "Warehouse" w ON w.id=e."warehouseId" LEFT JOIN "Partner" p ON p."userId"=u.id WHERE u.id=$1`, r.actor["id"])
}
