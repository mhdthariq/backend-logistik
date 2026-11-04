package httpapi

import (
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/gofiber/fiber/v3"
	"novalogistik/backend/internal/identity"
	"novalogistik/backend/internal/persistence"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }

func checkUsername(r *request, id any, username string) error {
	if !truthy(r.actor["isOwner"]) {
		return problem(403, "Hanya owner yang boleh mengubah username.")
	}
	if !usernamePattern.MatchString(username) {
		return problem(422, "Username hanya boleh berisi huruf, angka, titik, garis bawah, atau tanda hubung.")
	}
	duplicate, err := r.one(`SELECT to_jsonb(u) FROM "User" u WHERE username=$1 AND id<>$2`, username, id)
	if err != nil {
		return err
	}
	if duplicate != nil {
		return fieldProblem("Username sudah dipakai.", "username", "Username sudah dipakai.")
	}
	return nil
}

func (b *business) updateProfile(r *request) (any, error) {
	data, notes := map[string]any{}, map[string]any{}
	if v, exists := r.body["username"]; exists {
		username := strings.ToLower(textValue(v))
		if err := checkUsername(r, r.actor["id"], username); err != nil {
			return nil, err
		}
		data["username"], notes["username"] = username, username
	}
	if name := textValue(r.body["name"]); name != "" {
		if jsLength(name) < 2 {
			return nil, fieldProblem("Nama minimal 2 karakter.", "name", "Nama minimal 2 karakter.")
		}
		data["name"], notes["name"] = name, name
	}
	if password := textValue(r.body["newPassword"]); password != "" {
		if jsLength(password) < 8 {
			return nil, fieldProblem("Password baru minimal 8 karakter.", "newPassword", "Password baru minimal 8 karakter.")
		}
		current := textValue(r.body["currentPassword"])
		if current == "" {
			return nil, fieldProblem("Password saat ini wajib diisi untuk mengganti password.", "currentPassword", "Password saat ini wajib diisi.")
		}
		fresh, err := r.one(`SELECT to_jsonb(u) FROM "User" u WHERE id=$1`, r.actor["id"])
		if err != nil {
			return nil, err
		}
		if fresh == nil || !identity.VerifyPassword(current, textValue(fresh["passwordHash"])) {
			return nil, fieldProblem("Password saat ini salah.", "currentPassword", "Password saat ini salah.")
		}
		if current == password {
			return nil, fieldProblem("Password baru tidak boleh sama dengan password saat ini.", "newPassword", "Password baru tidak boleh sama dengan password lama.")
		}
		hash, err := identity.HashPassword(password)
		if err != nil {
			return nil, err
		}
		data["passwordHash"] = hash
		notes["passwordChanged"] = true
	}
	bank := map[string]any{}
	bankRequested := false
	for _, key := range []string{"bankName", "bankAccountName", "bankAccountNumber"} {
		if v, exists := r.body[key]; exists {
			bankRequested = true
			if s := nullable(v); s != nil {
				bank[key] = s
			}
		}
	}
	var partner any
	if len(data) == 0 && (r.actor["partnerId"] == nil || !bankRequested) {
		return fiber.Map{"updated": false, "message": "Tidak ada perubahan yang dikirim."}, nil
	}
	err := r.store.Transaction(r.ctx, func(tx persistence.Queryer) error {
		r.db = tx
		if len(data) > 0 {
			if _, err := r.updateRow("User", idValue(r.actor["id"]), data); err != nil {
				return err
			}
		}
		if r.actor["partnerId"] != nil && bankRequested {
			p, err := r.updateRow("Partner", idValue(r.actor["partnerId"]), bank)
			if err != nil {
				return err
			}
			partner = fiber.Map{"id": p["id"], "type": p["type"], "bank": fiber.Map{"bankName": p["bankName"], "bankAccountName": p["bankAccountName"], "bankAccountNumber": p["bankAccountNumber"]}}
			notes["bankAccountUpdated"] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.db = r.store.Pool
	b.audit(r, "updated_self", "user", r.actor["id"], textValue(r.actor["username"]), nil, notes)
	if notes["passwordChanged"] == true {
		b.audit(r, "change_password", "user", r.actor["id"], textValue(r.actor["name"])+" change password (self)", nil, fiber.Map{"changedBy": r.actor["name"], "changedByUsername": r.actor["username"], "targetUser": r.actor["username"], "selfService": true})
	}
	user, err := r.one(`SELECT jsonb_build_object('id',id,'username',username,'name',name,'isOwner',"isOwner") FROM "User" WHERE id=$1`, r.actor["id"])
	if err != nil {
		return nil, err
	}
	return fiber.Map{"updated": true, "user": user, "partner": partner}, nil
}
