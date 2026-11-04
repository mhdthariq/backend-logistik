package httpapi

import (
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
)

func mobileVersion(raw string, fallback int64) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n > 9007199254740991 {
		return fallback
	}
	return n
}
func mobileConfig(r *request) (any, error) {
	minimum := mobileVersion(os.Getenv("MOBILE_ANDROID_MIN_VERSION_CODE"), 1)
	latest := mobileVersion(os.Getenv("MOBILE_ANDROID_LATEST_VERSION_CODE"), minimum)
	if latest < minimum {
		latest = minimum
	}
	distribution := "private"
	if os.Getenv("MOBILE_DISTRIBUTION") == "public" {
		distribution = "public"
	}
	r.c.Set("Cache-Control", "private, no-store")
	return fiber.Map{
		"serverTime":   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"android":      fiber.Map{"minimumVersionCode": minimum, "latestVersionCode": latest, "distribution": distribution},
		"evidence":     fiber.Map{"timeZones": []string{"Asia/Jakarta", "Asia/Makassar", "Asia/Jayapura"}, "defaultTimeZone": "Asia/Jakarta", "maxLocationLabelLength": 240, "maxPhotoDataUrlLength": 2500000, "maxCaptureAgeSeconds": 300, "maxFutureSkewSeconds": 30, "serverWatermark": true, "locationLabelSource": "device"},
		"capabilities": fiber.Map{"offlineCapture": false, "idempotentMutations": false, "refreshSessions": false, "pushNotifications": false, "mockLocationMetadata": true},
	}, nil
}
