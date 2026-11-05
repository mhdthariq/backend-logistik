package httpapi

import (
	"testing"
	"time"
)

func TestCapacityTrendWindow(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw   string
		count int
		first string
	}{
		{"", 14, "2026-10-23"}, {"bad", 14, "2026-10-23"}, {"0", 14, "2026-10-23"},
		{"-10", 7, "2026-10-30"}, {"1", 7, "2026-10-30"}, {"7", 7, "2026-10-30"},
		{"100", 90, "2026-08-08"}, {"Infinity", 90, "2026-08-08"}, {"-Infinity", 7, "2026-10-30"},
		{"7.5", 8, "2026-10-30"}, {"0x10", 16, "2026-10-21"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			from, buckets := capacityTrendWindow(tc.raw, now)
			if len(buckets) != tc.count || from.Format("2006-01-02") != tc.first || buckets[0].Date != tc.first || buckets[0].Count != 0 {
				t.Fatal(from, buckets)
			}
		})
	}
	_, buckets := capacityTrendWindow("7", now.In(time.FixedZone("WIB", 7*3600)))
	if buckets[0].Date != "2026-10-29" || buckets[0].Label != "30 Okt" || buckets[6].Label != "05 Nov" {
		t.Fatal(buckets)
	}
	_, buckets = capacityTrendWindow("7", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if buckets[0].Date != "2026-02-23" || buckets[6].Date != "2026-03-01" {
		t.Fatal(buckets)
	}
}
