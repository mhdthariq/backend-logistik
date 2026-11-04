package httpapi

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResiPrivacy(t *testing.T) {
	for _, tt := range []struct {
		input any
		want  string
	}{
		{"+62 812-3456-7890", "081234567890"}, {"081234567890", "081234567890"},
		{"7890", ""}, {"0812****7890", ""}, {"' OR 1=1--", ""}, {nil, ""}, {81234567890, ""},
		{"1234567890123456", ""},
	} {
		t.Run(fmt.Sprint(tt.input), func(t *testing.T) {
			if got := normalizeResiPhone(tt.input); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		input any
		want  any
	}{
		{"081234567890", "081234******"}, {"Jalan Mawar 123", "Jalan M********"}, {"A", "*"}, {" 😀界A ", "😀**"}, {" ", nil}, {nil, nil},
	} {
		t.Run(fmt.Sprint(tt.input), func(t *testing.T) {
			if got := maskResiValue(tt.input); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestTrackingLimiter(t *testing.T) {
	var l trackingLimiter
	now := time.Unix(100, 0)
	key := trackingKey{"A", "RESI"}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if l.allow(key, now) {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d concurrent attempts", allowed.Load())
	}
	if !l.allow(trackingKey{"B", "RESI"}, now) {
		t.Fatal("another tenant shares limit")
	}
	if !l.allow(key, now.Add(15*time.Minute)) {
		t.Fatal("window did not expire")
	}
	for i := range 9999 {
		if !l.allow(trackingKey{"A", fmt.Sprint(i)}, now.Add(15*time.Minute)) {
			t.Fatal("capacity reached early")
		}
	}
	if l.allow(trackingKey{"A", "overflow"}, now.Add(15*time.Minute)) {
		t.Fatal("unbounded limiter")
	}
	if !l.allow(trackingKey{"A", "overflow"}, now.Add(30*time.Minute)) {
		t.Fatal("expired entries retained")
	}
}
