package capacity

import (
	"encoding/json"
	"testing"
)

func TestDimensionStatus(t *testing.T) {
	for _, tc := range []struct {
		current, maximum, threshold float64
		dp                          int
		status                      string
		utilization, remaining      float64
	}{
		{2800, 3500, 90, 2, "OK", 80, 700},
		{3200, 3500, 90, 2, "WARNING", 91.43, 300},
		{3500, 3500, 90, 2, "WARNING", 100, 0},
		{3800, 3500, 90, 2, "OVERLIMIT", 108.57, -300},
		{850, 1000, 80, 2, "WARNING", 85, 150},
		{799, 1000, 80, 2, "OK", 79.9, 201},
		{75, 60, 90, 0, "OVERLIMIT", 125, -15},
		{11.2, 12, 90, 6, "WARNING", 93.33, 0.8},
		{0, 3500, 90, 2, "OK", 0, 3500},
	} {
		d := DimensionStatus(tc.current, tc.maximum, tc.dp, tc.threshold)
		if d.Status != tc.status || d.Utilization == nil || *d.Utilization != tc.utilization || d.Remaining == nil || *d.Remaining != tc.remaining {
			t.Fatalf("%+v: %+v", tc, d)
		}
	}
	for _, maximum := range []float64{0, -5} {
		d := DimensionStatus(82, maximum, 2, 90)
		if d.Current != 82 || d.Status != "UNCONFIGURED" || d.Maximum != nil || d.Utilization != nil || d.Remaining != nil || d.OverBy != nil {
			t.Fatal(d)
		}
	}
	if d := DimensionStatus(1.005, 2, 2, 90); d.Current != 1.01 {
		t.Fatal(d)
	}
	for _, current := range []float64{0, 1} {
		d := DimensionStatus(current, 0.001, 2, 90)
		if d.Utilization != nil || d.Maximum == nil || *d.Maximum != 0 {
			t.Fatal(d)
		}
		if _, err := json.Marshal(d); err != nil {
			t.Fatal("non-finite utilization must serialize as null", err)
		}
	}
}

func TestOverallPrecedence(t *testing.T) {
	for _, tc := range []struct {
		limits Limits
		load   Load
		want   string
	}{
		{Limits{5000, 15, 100}, Load{3000, 10, 50}, "OK"},
		{Limits{3500, 15, 100}, Load{3800, 10, 50}, "OVERLIMIT"},
		{Limits{5000, 10, 100}, Load{3000, 12, 50}, "OVERLIMIT"},
		{Limits{5000, 15, 60}, Load{3000, 10, 75}, "OVERLIMIT"},
		{Limits{5000, 15, 0}, Load{3000, 10, 50}, "PARTIALLY_CONFIGURED"},
		{Limits{}, Load{4500, 12, 82}, "UNCONFIGURED"},
		{Limits{5000, 15, 0}, Load{4500, 10, 50}, "WARNING"},
	} {
		if got := Compute(tc.limits, tc.load, 90); got.Overall != tc.want {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}
