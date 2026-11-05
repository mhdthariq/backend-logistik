package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type capacityTrendBucket struct {
	Date  string `json:"date"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

func capacityTrendWindow(raw string, now time.Time) (time.Time, []capacityTrendBucket) {
	raw = strings.TrimSpace(raw)
	days := number(raw)
	// Number() accepts infinities, which the legacy clamp maps to its endpoints.
	if n, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(n) && (!math.IsInf(n, 0) || raw == "Infinity" || raw == "+Infinity" || raw == "-Infinity") {
		days = n
	}
	if len(raw) > 2 && raw[0] == '0' && strings.ContainsAny(raw[1:2], "xXbBoO") {
		if n, err := strconv.ParseUint(raw, 0, 64); err == nil {
			days = float64(n)
		}
	}
	if days == 0 {
		days = 14
	}
	days = math.Min(90, math.Max(7, days))
	// JavaScript setDate truncates the resulting day, including fractional ranges.
	from := time.Date(now.Year(), now.Month(), int(float64(now.Day())-(days-1)), 0, 0, 0, 0, now.Location())
	months := [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	buckets := make([]capacityTrendBucket, 0, int(math.Ceil(days)))
	for i := 0; float64(i) < days; i++ {
		d := from.AddDate(0, 0, i)
		buckets = append(buckets, capacityTrendBucket{Date: d.UTC().Format("2006-01-02"), Label: fmt.Sprintf("%02d %s", d.Day(), months[d.Month()-1])})
	}
	return from, buckets
}

func (b *business) capacityTrend(r *request) (any, error) {
	from, buckets := capacityTrendWindow(r.c.Query("days"), time.Now())
	rows, err := r.db.Query(r.ctx, `SELECT "afterData","createdAt" FROM "AuditLog" WHERE "entityType"='vehicle' AND "createdAt">=$1 ORDER BY "createdAt"`, from.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indices := make(map[string]int, len(buckets))
	for i, bucket := range buckets {
		indices[bucket.Date] = i
	}
	total := 0
	for rows.Next() {
		var raw *string
		var created time.Time
		if err := rows.Scan(&raw, &created); err != nil {
			return nil, err
		}
		var after map[string]any
		if raw == nil || json.Unmarshal([]byte(*raw), &after) != nil || !truthy(after["capacityConfigChanged"]) {
			continue
		}
		if i, ok := indices[created.UTC().Format("2006-01-02")]; ok {
			buckets[i].Count++
			total++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"buckets": buckets, "total": total}, nil
}
