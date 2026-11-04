package httpapi

import (
	"fmt"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

func threshold(r *request) float64 {
	row, err := r.one(`SELECT jsonb_build_object('value',value) FROM "SystemSetting" WHERE key='capacity.warningThresholdPct'`)
	if err != nil || row == nil {
		return 90
	}
	n, err := strconv.ParseFloat(textValue(row["value"]), 64)
	if err != nil || n <= 0 || n >= 1000 {
		return 90
	}
	return n
}
func (b *business) settings(r *request) (any, error) {
	return fiber.Map{"capacity": fiber.Map{"warningThresholdPct": threshold(r)}}, nil
}
func (b *business) updateSettings(r *request) (any, error) {
	if !truthy(r.actor["isOwner"]) {
		return nil, problem(403, "Hanya Owner yang dapat mengubah pengaturan sistem.")
	}
	raw, ok := r.body["warningThresholdPct"]
	if !ok {
		return nil, fieldProblem("Tidak ada pengaturan yang diubah.", "body", "Kirim warningThresholdPct.")
	}
	v := numeric(raw)
	if v == nil || v.(float64) < 1 || v.(float64) > 99 {
		return nil, fieldProblem("Warning threshold harus antara 1-99%.", "warningThresholdPct", "Harus antara 1-99.")
	}
	before := threshold(r)
	if err := r.exec(`INSERT INTO "SystemSetting" (key,value,"updatedById","updatedAt") VALUES ('capacity.warningThresholdPct',$1,$2,NOW()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,"updatedById"=EXCLUDED."updatedById","updatedAt"=NOW()`, fmt.Sprint(v), r.actor["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "updated", "system_setting", nil, "Capacity Settings", fiber.Map{"warningThresholdPct": before}, fiber.Map{"warningThresholdPct": v})
	return b.settings(r)
}
