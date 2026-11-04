package persistence

import "testing"

func TestNormalizeDates(t *testing.T) {
	row := map[string]any{"createdAt": "2026-01-02T03:04:05.123", "notes": "2026-01-02T03:04:05.123", "child": map[string]any{"expiresAt": "2026-01-02T10:04:05+07:00"}, "rows": []any{map[string]any{"updatedAt": "2026-01-02T03:04:05"}}, "effectiveTo": nil}
	normalizeDates(row)
	if row["createdAt"] != "2026-01-02T03:04:05.123Z" || row["notes"] != "2026-01-02T03:04:05.123" || row["effectiveTo"] != nil {
		t.Fatalf("unexpected dates: %v", row)
	}
	if row["child"].(map[string]any)["expiresAt"] != "2026-01-02T03:04:05.000Z" || row["rows"].([]any)[0].(map[string]any)["updatedAt"] != "2026-01-02T03:04:05.000Z" {
		t.Fatal("nested date normalization failed")
	}
}
