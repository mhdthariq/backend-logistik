package httpapi

import (
	"math"
	"testing"
)

func TestIDsDoNotTruncateOrOverflow(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		want  int
	}{{"integer", float64(4), 4}, {"numeric string", "4", 4}, {"fraction", 1.9, 0}, {"negative", -1, 0}, {"overflow", "2147483648", 0}, {"NaN", math.NaN(), 0}, {"object", map[string]any{"id": 1}, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := idValue(tt.value); got != tt.want {
				t.Fatalf("id=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestVehiclePhotoValidation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		valid bool
	}{{"cleared", nil, true}, {"empty", "", true}, {"png", "data:image/png;base64,aGVsbG8=", true}, {"remote URL", "https://example.com/photo.png", false}, {"svg", "data:image/svg+xml;base64,aGVsbG8=", false}, {"invalid encoding", "data:image/png;base64,a==", false}, {"object", map[string]any{}, false}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := vehiclePhoto(tt.value)
			if (err == nil) != tt.valid {
				t.Fatalf("unexpected photo validation: %v", err)
			}
		})
	}
}

func TestCheckpointInputBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name  string
		body  map[string]any
		valid bool
	}{
		{"valid", map[string]any{"name": "Origin", "latitude": -90, "longitude": 180, "radiusKm": 0.01}, true},
		{"missing coordinates", map[string]any{"name": "Origin"}, false},
		{"invalid latitude", map[string]any{"name": "Origin", "latitude": 90.1, "longitude": 0}, false},
		{"invalid radius", map[string]any{"name": "Origin", "latitude": 0, "longitude": 0, "radiusMeters": 9}, false},
		{"invalid name", map[string]any{"name": " ", "latitude": 0, "longitude": 0}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCheckpoint(tt.body, 0)
			if (err == nil) != tt.valid {
				t.Fatalf("unexpected checkpoint validation: %v", err)
			}
		})
	}
}
