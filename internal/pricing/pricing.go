// Package pricing contains the framework-free legacy shipment pricing rules.
package pricing

import "math"

// Detail represents one physical package. Positive VolumeM3 overrides dimensions.
type Detail struct {
	LengthCm, WidthCm, HeightCm, ActualWeightKg, VolumeM3 float64
}

// Tariff contains the billing method, minimums and rounding settings.
type Tariff struct {
	Method, CustomerType, RoundingMode         string
	RatePerKg, MinKg, Multiplier, RoundingUnit float64
	RatePerKoli, RatePerCubic, MinKoli, MinM3  float64
}

// Result matches computePricing in the legacy domain module.
type Result struct {
	ActualKg           float64 `json:"actualKg"`
	VolumetricKg       float64 `json:"volumetricKg"`
	ChargeableKg       float64 `json:"chargeableKg"`
	Price              float64 `json:"price"`
	Multiplier         float64 `json:"multiplier"`
	Method             string  `json:"method"`
	TotalVolumeM3      float64 `json:"totalVolumeM3"`
	KoliCount          int     `json:"koliCount"`
	ChargeableKoli     float64 `json:"chargeableKoli"`
	ChargeableVolumeM3 float64 `json:"chargeableVolumeM3"`
	RateApplied        float64 `json:"rateApplied"`
	SimpleKg           bool    `json:"simpleKg"`
}

// Round matches JavaScript Math.round, including negative half ties.
func Round(value float64) float64 {
	floor := math.Floor(value)
	if value-floor >= 0.5 {
		return floor + 1
	}
	return floor
}

// Volume returns direct volume when positive and dimensional volume otherwise.
func Volume(d Detail) float64 {
	if d.VolumeM3 > 0 {
		return d.VolumeM3
	}
	return d.LengthCm * d.WidthCm * d.HeightCm / 1_000_000
}

// Compute aggregates packages before applying minimums and a single rounding step.
func Compute(details []Detail, t Tariff) Result {
	r := Result{Method: t.Method, Multiplier: t.Multiplier, KoliCount: len(details)}
	switch r.Method {
	case "PER_KG_SIMPLE", "PER_KOLI", "PER_CUBIC":
	default:
		r.Method = "PER_KG"
	}
	if r.Multiplier <= 0 {
		r.Multiplier = 250
	}
	for _, d := range details {
		r.ActualKg += d.ActualWeightKg
		r.TotalVolumeM3 += Volume(d)
		r.VolumetricKg += Volume(d) * r.Multiplier
	}
	r.SimpleKg = r.Method == "PER_KG_SIMPLE" || (r.Method == "PER_KG" && t.CustomerType == "b2b")
	r.ChargeableKg = math.Max(r.ActualKg, t.MinKg)
	if !r.SimpleKg {
		r.ChargeableKg = math.Max(math.Max(r.ActualKg, r.VolumetricKg), t.MinKg)
		unit := t.RoundingUnit
		if unit <= 0 {
			unit = 0.5
		}
		if t.RoundingMode == "NEAREST" {
			r.ChargeableKg = Round(r.ChargeableKg/unit) * unit
		} else {
			r.ChargeableKg = math.Ceil(r.ChargeableKg/unit) * unit
		}
	}
	switch r.Method {
	case "PER_KOLI":
		r.ChargeableKoli = math.Max(float64(len(details)), t.MinKoli)
		r.RateApplied = t.RatePerKoli
		r.Price = Round(r.ChargeableKoli * r.RateApplied)
	case "PER_CUBIC":
		r.ChargeableVolumeM3 = math.Max(r.TotalVolumeM3, t.MinM3)
		r.RateApplied = t.RatePerCubic
		r.Price = Round(r.ChargeableVolumeM3 * r.RateApplied)
	default:
		r.RateApplied = t.RatePerKg
		r.Price = Round(r.ChargeableKg * r.RateApplied)
	}
	return r
}
