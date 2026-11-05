package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandoverEvidenceMetadata(t *testing.T) {
	now := time.Date(2026, 11, 3, 16, 30, 0, 0, time.UTC)
	data, gps, err := parseHandoverEvidence(map[string]any{}, now)
	if err != nil || gps || data["timeZone"] != "Asia/Jakarta" || data["locationLabel"] != nil || data["captureLatitude"] != nil || data["capturedAt"] != now {
		t.Fatal(data, gps, err)
	}
	data, gps, err = parseHandoverEvidence(map[string]any{"capturedAt": "2026-11-04T00:30:00+08:00", "timeZone": "Asia/Makassar", "locationLabel": "  Jalan\n Pelabuhan 日本 \u202e", "mockLocation": false, "latitude": float64(0), "longitude": float64(180)}, now)
	if err != nil || !gps || data["locationLabel"] != "Jalan Pelabuhan 日本" || data["capturedAt"] != now || data["captureLatitude"] != float64(0) {
		t.Fatal(data, gps, err)
	}
	for _, body := range []map[string]any{{"timeZone": "WITA"}, {"timeZone": nil}, {"locationLabel": strings.Repeat("x", 241)}, {"locationLabel": 23}, {"capturedAt": "2026-11-03T16:30:00"}, {"capturedAt": "2026-11-03T16:24:59Z"}, {"capturedAt": "2026-11-03T16:30:31Z"}, {"mockLocation": "false"}, {"locationAccuracy": -1}, {"capturedAt": nil}, {"latitude": "0", "longitude": float64(0)}, {"latitude": nil}, {"latitude": float64(0), "longitude": float64(181)}} {
		t.Run(string(mustEvidenceJSON(t, body)), func(t *testing.T) {
			if _, _, err := parseHandoverEvidence(body, now); err == nil {
				t.Fatal(body)
			}
		})
	}
	for _, stamp := range []string{"2026-11-03T16:25:00Z", "2026-11-03T16:30:30Z"} {
		if _, _, err := parseHandoverEvidence(map[string]any{"capturedAt": stamp}, now); err != nil {
			t.Fatal(stamp, err)
		}
	}
}

func mustEvidenceJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEvidencePhotoProcessing(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			src.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	photo := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	meta, _, err := parseHandoverEvidence(map[string]any{"locationLabel": "Jalan Pelabuhan, Medan", "latitude": -6.2, "longitude": 106.8, "locationAccuracy": float64(5)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	caption := evidenceCaption{Company: "NovaLogistik", Label: "Pickup PICK-2026-000001", Origin: "Jakarta", Destination: "Medan", Driver: "Assigned courier", RecordedBy: "Operator"}
	unchanged, err := processEvidencePhoto(t.Context(), photo, false, meta, caption)
	if err != nil || unchanged != photo {
		t.Fatal(err)
	}
	out, err := processEvidencePhoto(t.Context(), photo, true, meta, caption)
	if err != nil || !strings.HasPrefix(out, "data:image/jpeg;base64,") || len(out) > 2_500_000 {
		t.Fatal(len(out), err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	stamped, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if stamped.Bounds().Dx() != 800 || stamped.Bounds().Dy() != 496 || stamped.At(40, 30) == src.At(40, 30) {
		t.Fatal(stamped.Bounds())
	}
	caption.MaxPhotoBytes = 10 * 1024 * 1024 / 8
	limited, err := processEvidencePhoto(t.Context(), photo, true, meta, caption)
	if err != nil || len(limited) > caption.MaxPhotoBytes {
		t.Fatal(len(limited), err)
	}
	caption.MaxPhotoBytes = 1
	if _, err := processEvidencePhoto(t.Context(), photo, true, meta, caption); err == nil {
		t.Fatal("ignored photo storage limit")
	}
	for _, bad := range []string{"bad", "data:image/png;base64,AAAA", "data:image/png;base64,@@@@"} {
		if _, err := processEvidencePhoto(t.Context(), bad, false, meta, caption); err == nil {
			t.Fatal(bad)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = processEvidencePhoto(ctx, photo, true, meta, caption); err == nil {
		t.Fatal("ignored cancelled context")
	}
}

func TestEvidenceOrientation(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.Set(x, y, color.RGBA{uint8(x + y*3 + 1), 0, 0, 255})
		}
	}
	for orientation, want := range map[int][]uint8{1: {1, 2, 3, 4, 5, 6}, 2: {3, 2, 1, 6, 5, 4}, 3: {6, 5, 4, 3, 2, 1}, 4: {4, 5, 6, 1, 2, 3}, 5: {1, 4, 2, 5, 3, 6}, 6: {4, 1, 5, 2, 6, 3}, 7: {6, 3, 5, 2, 4, 1}, 8: {3, 6, 2, 5, 1, 4}} {
		out := orientEvidence(src, orientation)
		actual := []uint8{}
		for y := 0; y < out.Bounds().Dy(); y++ {
			for x := 0; x < out.Bounds().Dx(); x++ {
				r, _, _, _ := out.At(x, y).RGBA()
				actual = append(actual, uint8(r>>8))
			}
		}
		if !bytes.Equal(actual, want) {
			t.Fatal(orientation, actual, want)
		}
		tiff := make([]byte, 26)
		copy(tiff, "II")
		binary.LittleEndian.PutUint16(tiff[2:], 42)
		binary.LittleEndian.PutUint32(tiff[4:], 8)
		binary.LittleEndian.PutUint16(tiff[8:], 1)
		binary.LittleEndian.PutUint16(tiff[10:], 0x112)
		binary.LittleEndian.PutUint16(tiff[12:], 3)
		binary.LittleEndian.PutUint32(tiff[14:], 1)
		binary.LittleEndian.PutUint16(tiff[18:], uint16(orientation))
		segment := append([]byte("Exif\x00\x00"), tiff...)
		raw := []byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(segment) + 2)}
		raw = append(raw, segment...)
		if got := jpegEvidenceOrientation(raw); got != orientation {
			t.Fatal(got, orientation)
		}
		for i := 0; i < len(raw); i++ {
			jpegEvidenceOrientation(raw[:i])
		}
	}
}

func TestEvidenceAddressFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/reverse" || r.URL.Query().Get("lat") != "-6.2" || r.Header.Get("User-Agent") == "" {
			t.Error(r.URL, r.Header)
		}
		if _, err := w.Write([]byte(`{"display_name":"Jalan\n Pelabuhan 日本"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	t.Setenv("NOMINATIM_URL", server.URL)
	evidenceAddresses.Lock()
	evidenceAddresses.next = time.Time{}
	evidenceAddresses.Unlock()
	for range 2 {
		if got := reverseEvidenceAddress(t.Context(), -6.2, 106.8); got != "Jalan Pelabuhan 日本" {
			t.Fatal(got)
		}
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if got := reverseEvidenceAddress(t.Context(), -6.3, 106.8); got != "" || calls.Load() != 1 {
		t.Fatal(got, calls.Load())
	}
	t.Setenv("NOMINATIM_URL", "file:///etc/passwd")
	if got := reverseEvidenceAddress(t.Context(), -6.2, 106.8); got != "" {
		t.Fatal(got)
	}
}
