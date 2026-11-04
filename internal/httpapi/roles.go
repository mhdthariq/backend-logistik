package httpapi

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/text/unicode/norm"
)

func slugify(s string) string {
	s = norm.NFKD.String(s)
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
	return strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-"), "-")
}

const roleSelect = `SELECT to_jsonb(role)||jsonb_build_object('permissions',COALESCE((SELECT jsonb_agg(to_jsonb(rp)||jsonb_build_object('permission',to_jsonb(p)) ORDER BY p.id) FROM "RolePermission" rp JOIN "Permission" p ON p.id=rp."permissionId" WHERE rp."roleId"=role.id),'[]'::jsonb))`
const roleCount = `||jsonb_build_object('_count',jsonb_build_object('users',(SELECT count(*) FROM "UserRole" ur WHERE ur."roleId"=role.id)))`

func (b *business) roles(r *request) (any, error) {
	return r.many(roleSelect + roleCount + ` FROM "Role" role ORDER BY role.id`)
}
func roleByID(r *request) (map[string]any, error) {
	id, err := r.id()
	if err != nil {
		return nil, err
	}
	role, err := r.one(`SELECT to_jsonb(role) FROM "Role" role WHERE id=$1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, problem(404, "Role tidak ditemukan.")
	}
	return role, nil
}

func replacePermissions(r *request, roleID any, permissionIDs []int) error {
	for _, id := range permissionIDs {
		p, err := r.one(`SELECT to_jsonb(p) FROM "Permission" p WHERE id=$1`, id)
		if err != nil {
			return err
		}
		if p == nil {
			return problem(422, fmt.Sprintf("Permission id %d tidak ditemukan.", id))
		}
	}
	if err := r.exec(`DELETE FROM "RolePermission" WHERE "roleId"=$1`, roleID); err != nil {
		return err
	}
	for _, id := range permissionIDs {
		if err := r.exec(`INSERT INTO "RolePermission" ("roleId","permissionId") VALUES ($1,$2) ON CONFLICT DO NOTHING`, roleID, id); err != nil {
			return err
		}
	}
	return nil
}

func (b *business) createRole(r *request) (any, error) {
	name, err := required(r.body, "name")
	if err != nil {
		return nil, err
	}
	slug := slugify(name)
	if slug == "" {
		return nil, fieldProblem("Nama role harus menghasilkan slug yang valid.", "name", "Nama role harus menghasilkan slug yang valid.")
	}
	existing, err := r.one(`SELECT to_jsonb(role) FROM "Role" role WHERE slug=$1`, slug)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fieldProblem(fmt.Sprintf("Role slug %q sudah dipakai.", slug), "slug", "Slug sudah dipakai.")
	}
	role, err := r.one(`INSERT INTO "Role" AS role (slug,name,description,"isSystem") VALUES ($1,$2,$3,false) RETURNING to_jsonb(role)`, slug, name, nullable(r.body["description"]))
	if err != nil {
		return nil, err
	}
	permissionIDs, _ := ids(r.body["permissionIds"])
	if err := replacePermissions(r, role["id"], permissionIDs); err != nil {
		return nil, err
	}
	b.audit(r, "created", "role", role["id"], name, nil, fiber.Map{"slug": slug, "permissions": len(permissionIDs)})
	return r.one(roleSelect+` FROM "Role" role WHERE role.id=$1`, role["id"])
}

func (b *business) updateRole(r *request) (any, error) {
	existing, err := roleByID(r)
	if err != nil {
		return nil, err
	}
	system := truthy(existing["isSystem"])
	if system && !truthy(r.actor["isOwner"]) {
		return nil, problem(422, "Role sistem hanya bisa diubah oleh owner, dan hanya permission-nya.")
	}
	data := map[string]any{}
	if !system {
		if v, ok := r.body["name"]; ok {
			name := textValue(v)
			if name == "" {
				name = textValue(existing["name"])
			}
			slug := slugify(name)
			if slug == "" {
				return nil, fieldProblem("Nama role harus menghasilkan slug yang valid.", "name", "Nama role harus menghasilkan slug yang valid.")
			}
			duplicate, err := r.one(`SELECT to_jsonb(role) FROM "Role" role WHERE slug=$1 AND id<>$2`, slug, existing["id"])
			if err != nil {
				return nil, err
			}
			if duplicate != nil {
				return nil, fieldProblem(fmt.Sprintf("Role slug %q sudah dipakai.", slug), "name", "Nama role menghasilkan slug yang sudah dipakai.")
			}
			data["name"], data["slug"] = name, slug
		}
	}
	if v, ok := r.body["description"]; ok {
		data["description"] = nullable(v)
	}
	role, err := r.updateRow("Role", idValue(existing["id"]), data)
	if err != nil {
		return nil, err
	}
	permissionIDs, ok := ids(r.body["permissionIds"])
	if ok {
		if err := replacePermissions(r, role["id"], permissionIDs); err != nil {
			return nil, err
		}
	}
	b.audit(r, "updated", "role", role["id"], textValue(role["name"]), diff(existing, role), fiber.Map{"permissionsUpdated": ok})
	return r.one(roleSelect+roleCount+` FROM "Role" role WHERE role.id=$1`, role["id"])
}

func (b *business) rolePermissions(r *request) (any, error) {
	role, err := roleByID(r)
	if err != nil {
		return nil, err
	}
	if truthy(role["isSystem"]) && !truthy(r.actor["isOwner"]) {
		return nil, problem(422, "Permission role sistem hanya bisa diubah oleh owner.")
	}
	permissionIDs, ok := ids(r.body["permissionIds"])
	if !ok {
		return nil, problem(422, "permissionIds wajib berupa array.")
	}
	if err := replacePermissions(r, role["id"], permissionIDs); err != nil {
		return nil, err
	}
	b.audit(r, "updated", "role", role["id"], textValue(role["name"])+" · permissions", nil, fiber.Map{"permissionIds": permissionIDs})
	return fiber.Map{"updated": true, "permissionCount": len(permissionIDs)}, nil
}

func (b *business) deleteRole(r *request) (any, error) {
	role, err := roleByID(r)
	if err != nil {
		return nil, err
	}
	if truthy(role["isSystem"]) {
		return nil, problem(422, "Role sistem tidak bisa dihapus.")
	}
	var count int
	if err := r.db.QueryRow(r.ctx, `SELECT count(*) FROM "UserRole" WHERE "roleId"=$1`, role["id"]).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, problem(422, fmt.Sprintf("Role masih dipakai oleh %d user.", count))
	}
	if err := r.exec(`DELETE FROM "Role" WHERE id=$1`, role["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "role", role["id"], textValue(role["name"]), role, nil)
	return fiber.Map{"deleted": true}, nil
}
