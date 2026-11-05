// Package capacity computes informational load badges; it never blocks operations.
package capacity

import (
	"math"

	"novalogistik/backend/internal/pricing"
)

type Load struct {
	Weight float64 `json:"totalActualWeightKg"`
	Volume float64 `json:"totalVolumeM3"`
	Koli   float64 `json:"totalKoli"`
}

type Limits struct{ Weight, Volume, Koli float64 }

type Dimension struct {
	Current     float64  `json:"current"`
	Maximum     *float64 `json:"maximum"`
	Utilization *float64 `json:"utilizationPercent"`
	Remaining   *float64 `json:"remaining"`
	OverBy      *float64 `json:"overBy"`
	Status      string   `json:"status"`
}

type Status struct {
	Overall string    `json:"overallStatus"`
	Weight  Dimension `json:"weight"`
	Volume  Dimension `json:"volume"`
	Koli    Dimension `json:"koli"`
}

func round(value float64, dp int) float64 {
	epsilon := math.Nextafter(1, 2) - 1
	if value < 0 {
		epsilon = -epsilon
	}
	factor := math.Pow10(dp)
	return pricing.Round((value+epsilon)*factor) / factor
}

func finite(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

// DimensionStatus matches legacy rounding, including a positive maximum rounded to zero.
func DimensionStatus(current, maximum float64, dp int, threshold float64) Dimension {
	d := Dimension{Current: round(current, dp), Status: "UNCONFIGURED"}
	if maximum <= 0 {
		return d
	}
	maxValue := round(maximum, dp)
	d.Maximum = finite(maxValue)
	d.Remaining = finite(round(maxValue-d.Current, dp))
	d.OverBy = finite(math.Max(0, round(d.Current-maxValue, dp)))
	utilization := round(d.Current/maxValue*100, 2)
	d.Utilization = finite(utilization)
	switch {
	case d.Current > maxValue:
		d.Status = "OVERLIMIT"
	case utilization >= threshold:
		d.Status = "WARNING"
	default:
		d.Status = "OK"
	}
	return d
}

func Compute(limits Limits, load Load, threshold float64) Status {
	s := Status{Weight: DimensionStatus(load.Weight, limits.Weight, 2, threshold), Volume: DimensionStatus(load.Volume, limits.Volume, 6, threshold), Koli: DimensionStatus(load.Koli, limits.Koli, 0, threshold)}
	counts := map[string]int{}
	for _, d := range []Dimension{s.Weight, s.Volume, s.Koli} {
		counts[d.Status]++
	}
	switch {
	case counts["OVERLIMIT"] > 0:
		s.Overall = "OVERLIMIT"
	case counts["WARNING"] > 0:
		s.Overall = "WARNING"
	case counts["UNCONFIGURED"] == 3:
		s.Overall = "UNCONFIGURED"
	case counts["UNCONFIGURED"] > 0:
		s.Overall = "PARTIALLY_CONFIGURED"
	default:
		s.Overall = "OK"
	}
	return s
}
