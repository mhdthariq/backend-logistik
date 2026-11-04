package pricing_test

import (
	"math"
	"testing"

	"novalogistik/backend/internal/pricing"
)

func TestLegacyPricing(t *testing.T) {
	base := pricing.Tariff{Method: "PER_KG", RatePerKg: 4500, MinKg: 1, Multiplier: 250, RoundingMode: "UP", RoundingUnit: 0.5, MinKoli: 1}
	box := pricing.Detail{ActualWeightKg: 2, LengthCm: 100, WidthCm: 100, HeightCm: 100}
	for _, tt := range []struct {
		name      string
		details   []pricing.Detail
		change    func(*pricing.Tariff)
		kg, price float64
		method    string
		simple    bool
	}{
		{"actual heavier", []pricing.Detail{{ActualWeightKg: 10, LengthCm: 10, WidthCm: 10, HeightCm: 10}}, nil, 10, 45000, "PER_KG", false},
		{"volumetric heavier", []pricing.Detail{box}, nil, 250, 1125000, "PER_KG", false},
		{"direct volume wins", []pricing.Detail{{ActualWeightKg: 1, LengthCm: 999, WidthCm: 999, HeightCm: 999, VolumeM3: 1}}, nil, 250, 1125000, "PER_KG", false},
		{"minimum", []pricing.Detail{{ActualWeightKg: 0.1}}, func(t *pricing.Tariff) { t.MinKg = 3 }, 3, 13500, "PER_KG", false},
		{"round up", []pricing.Detail{{ActualWeightKg: 3.1}}, nil, 3.5, 15750, "PER_KG", false},
		{"nearest", []pricing.Detail{{ActualWeightKg: 3.2}}, func(t *pricing.Tariff) { t.RoundingMode = "NEAREST" }, 3, 13500, "PER_KG", false},
		{"nearest half", []pricing.Detail{{ActualWeightKg: 3.25}}, func(t *pricing.Tariff) { t.RoundingMode = "NEAREST" }, 3.5, 15750, "PER_KG", false},
		{"sum before rounding", []pricing.Detail{{ActualWeightKg: 1}, {ActualWeightKg: 1.2}, {ActualWeightKg: 0.3}}, nil, 2.5, 11250, "PER_KG", false},
		{"b2b no rounding", []pricing.Detail{{ActualWeightKg: 3.3}}, func(t *pricing.Tariff) { t.CustomerType = "b2b"; t.RatePerKg = 8000; t.RoundingUnit = 2 }, 3.3, 26400, "PER_KG", true},
		{"b2b minimum", []pricing.Detail{{ActualWeightKg: 0.3}}, func(t *pricing.Tariff) { t.CustomerType = "b2b"; t.MinKg = 5; t.RatePerKg = 8000 }, 5, 40000, "PER_KG", true},
		{"b2b ignores volume", []pricing.Detail{box}, func(t *pricing.Tariff) { t.CustomerType = "b2b"; t.RatePerKg = 8000 }, 2, 16000, "PER_KG", true},
		{"simple b2c", []pricing.Detail{box}, func(t *pricing.Tariff) { t.CustomerType = "b2c"; t.Method = "PER_KG_SIMPLE"; t.MinKg = 3 }, 3, 13500, "PER_KG_SIMPLE", true},
		{"koli", []pricing.Detail{{ActualWeightKg: 999}, {ActualWeightKg: 0.001}}, func(t *pricing.Tariff) { t.Method = "PER_KOLI"; t.RatePerKoli = 25000 }, 999.5, 50000, "PER_KOLI", false},
		{"koli minimum", []pricing.Detail{{ActualWeightKg: 1}}, func(t *pricing.Tariff) { t.Method = "PER_KOLI"; t.RatePerKoli = 25000; t.MinKoli = 5 }, 1, 125000, "PER_KOLI", false},
		{"koli missing rate", []pricing.Detail{{ActualWeightKg: 1}}, func(t *pricing.Tariff) { t.Method = "PER_KOLI" }, 1, 0, "PER_KOLI", false},
		{"cubic", []pricing.Detail{box, box}, func(t *pricing.Tariff) { t.Method = "PER_CUBIC"; t.RatePerCubic = 850000 }, 500, 1700000, "PER_CUBIC", false},
		{"cubic direct", []pricing.Detail{{ActualWeightKg: 1, VolumeM3: 1.5}}, func(t *pricing.Tariff) { t.Method = "PER_CUBIC"; t.RatePerCubic = 800000 }, 375, 1200000, "PER_CUBIC", false},
		{"cubic minimum", []pricing.Detail{{ActualWeightKg: 1, VolumeM3: 0.05}}, func(t *pricing.Tariff) { t.Method = "PER_CUBIC"; t.RatePerCubic = 800000; t.MinM3 = 0.2 }, 12.5, 160000, "PER_CUBIC", false},
		{"legacy method", []pricing.Detail{{ActualWeightKg: 5}}, func(t *pricing.Tariff) { t.Method = "legacy" }, 5, 22500, "PER_KG", false},
		{"default multiplier", []pricing.Detail{box}, func(t *pricing.Tariff) { t.Multiplier = 0 }, 250, 1125000, "PER_KG", false},
		{"default unit", []pricing.Detail{{ActualWeightKg: 3.1}}, func(t *pricing.Tariff) { t.RoundingUnit = 0 }, 3.5, 15750, "PER_KG", false},
		{"empty", nil, nil, 1, 4500, "PER_KG", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tariff := base
			if tt.change != nil {
				tt.change(&tariff)
			}
			r := pricing.Compute(tt.details, tariff)
			if math.Abs(r.ChargeableKg-tt.kg) > 1e-9 || r.Price != tt.price || r.Method != tt.method || r.SimpleKg != tt.simple {
				t.Fatalf("result=%+v, want kg=%v price=%v method=%s simple=%v", r, tt.kg, tt.price, tt.method, tt.simple)
			}
			if r.KoliCount != len(tt.details) {
				t.Fatal("package count differs")
			}
		})
	}
}

func TestPreviewDomainContract(t *testing.T) {
	r := pricing.Compute([]pricing.Detail{{ActualWeightKg: 2.1, VolumeM3: 0.01}}, pricing.Tariff{Method: "PER_KG", RatePerKg: 1000, MinKg: 1, Multiplier: 250, RoundingUnit: 0.5})
	want := pricing.Result{ActualKg: 2.1, VolumetricKg: 2.5, ChargeableKg: 2.5, Price: 2500, Multiplier: 250, Method: "PER_KG", TotalVolumeM3: 0.01, KoliCount: 1, RateApplied: 1000}
	if r != want {
		t.Fatalf("got %+v; want %+v", r, want)
	}
}

func TestJavaScriptRounding(t *testing.T) {
	for _, tt := range []struct{ input, want float64 }{{0.5, 1}, {0.49999999999999994, 0}, {-0.5, 0}, {-1.5, -1}, {1.49, 1}, {1.5, 2}} {
		if got := pricing.Round(tt.input); got != tt.want {
			t.Errorf("round(%v)=%v want %v", tt.input, got, tt.want)
		}
	}
}
