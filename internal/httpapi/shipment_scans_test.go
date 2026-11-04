package httpapi

import (
	"strings"
	"testing"
)

func TestScanNormalization(t *testing.T) {
	for _, tc := range []struct {
		input, corp, want string
		denied            bool
	}{
		{" CHILD-1 ", "A", "CHILD-1", false},
		{"https://example.test/tracking-paket?corporateId=CORP+A&resi=CHILD-1", " CORP A ", "CHILD-1", false},
		{"http://example.test/tracking-paket?corporateId=LOCAL&resi=M-1", "", "M-1", false},
		{"https://example.test/tracking-paket?corporateId=B&resi=M-1", "A", "", true},
		{"https://example.test/other?corporateId=B&resi=M-1", "A", "https://example.test/other?corporateId=B&resi=M-1", false},
		{"https://example.test/tracking-paket?resi=M-1", "A", "https://example.test/tracking-paket?resi=M-1", false},
		{"https://example.test/tracking-paket?corporateId=A&resi=" + strings.Repeat("x", 129), "A", "https://example.test/tracking-paket?corporateId=A&resi=" + strings.Repeat("x", 129), false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeScan(tc.input, tc.corp)
			if got != tc.want || (err != nil) != tc.denied {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}

func TestScanArrivalContext(t *testing.T) {
	for _, tc := range []struct {
		status   string
		received any
		want     string
	}{
		{"PICKED_UP", nil, "gudang_arrival"},
		{"AT_DEST_GUDANG", nil, "transport_arrival"},
		{"ARRIVED_AT_GUDANG", nil, "transport_arrival"},
		{"ARRIVED_AT_GUDANG", "2026-11-04T00:00:00Z", ""},
		{"CREATED", nil, ""}, {"DELIVERED", nil, ""}, {"CANCELLED", nil, ""},
	} {
		t.Run(tc.status+textValue(tc.received), func(t *testing.T) {
			if got := arrivalContext(map[string]any{"status": tc.status, "destReceivedAt": tc.received}); got != tc.want {
				t.Fatal(got)
			}
		})
	}
	if jsStringLength("📦") != 2 {
		t.Fatal("reason/QR limits must count UTF-16 code units")
	}
}
