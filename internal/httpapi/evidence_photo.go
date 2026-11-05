package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // Decode the PNG proof format accepted by the API.
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func cleanEvidenceText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func parseHandoverEvidence(body map[string]any, now time.Time) (map[string]any, bool, error) {
	location, ok := body["locationLabel"].(string)
	if body["locationLabel"] != nil && (!ok || len([]rune(location)) > 240) {
		return nil, false, problem(422, "Alamat lokasi harus teks maksimal 240 karakter.")
	}
	zone := "Asia/Jakarta"
	if v, exists := body["timeZone"]; exists {
		if v != "Asia/Jakarta" && v != "Asia/Makassar" && v != "Asia/Jayapura" {
			return nil, false, problem(422, "Zona waktu harus Asia/Jakarta, Asia/Makassar, atau Asia/Jayapura.")
		}
		zone = v.(string)
	}
	if v := body["mockLocation"]; v != nil {
		if _, ok := v.(bool); !ok {
			return nil, false, problem(422, "mockLocation harus boolean.")
		}
	}
	if v := body["locationAccuracy"]; v != nil {
		if n, ok := v.(float64); !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return nil, false, problem(422, "Akurasi lokasi harus angka non-negatif.")
		}
	}
	captured := now
	if v, exists := body["capturedAt"]; exists {
		s, ok := v.(string)
		hasZone := strings.HasSuffix(s, "Z") || len(s) >= 6 && (s[len(s)-6] == '+' || s[len(s)-6] == '-') && s[len(s)-3] == ':'
		if !ok || len(s) < 20 || s[10] != 'T' || !hasZone {
			return nil, false, problem(422, "Waktu pengambilan foto wajib berupa timestamp dengan zona waktu.")
		}
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || parsed.Before(now.Add(-5*time.Minute)) || parsed.After(now.Add(30*time.Second)) {
			return nil, false, problem(422, "Foto dan GPS harus baru diambil (maksimal 5 menit, toleransi waktu 30 detik).")
		}
		captured = parsed.UTC()
	}
	_, latPresent := body["latitude"]
	_, lonPresent := body["longitude"]
	hasLocation := latPresent || lonPresent
	var lat, lon any
	if hasLocation {
		a, okA := body["latitude"].(float64)
		b, okB := body["longitude"].(float64)
		if !okA || !okB || math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) || math.Abs(a) > 90 || math.Abs(b) > 180 {
			return nil, false, problem(422, "Koordinat GPS tidak valid.")
		}
		lat, lon = a, b
	}
	return map[string]any{"locationLabel": nullable(cleanEvidenceText(location)), "timeZone": zone, "capturedAt": captured, "mockLocation": body["mockLocation"], "captureLatitude": lat, "captureLongitude": lon, "captureAccuracy": body["locationAccuracy"]}, hasLocation, nil
}

type evidenceCaption struct {
	Company, Label, Origin, Destination, Driver, RecordedBy string
	VehiclePlate, VehicleDescription                        string
	ShipmentCount                                           int
	MaxPhotoBytes                                           int
}

func processEvidencePhoto(ctx context.Context, photo string, stamp bool, meta map[string]any, caption evidenceCaption) (string, error) {
	limit := caption.MaxPhotoBytes
	if limit <= 0 {
		limit = 2_500_000
	}
	_, payload, ok := strings.Cut(photo, ",")
	if !ok {
		return "", fmt.Errorf("missing photo payload")
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 20_000_000 || format != "jpeg" && format != "png" {
		return "", fmt.Errorf("unsupported photo dimensions or format")
	}
	if !strings.HasPrefix(photo, "data:image/"+format+";base64,") {
		return "", fmt.Errorf("photo data does not match declared format")
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	if !stamp {
		return photo, nil
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	source = orientEvidence(source, jpegEvidenceOrientation(raw))
	w, h := source.Bounds().Dx(), source.Bounds().Dy()
	scale := math.Min(1, 1600/float64(max(w, h)))
	w, h = max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	out := image.NewRGBA(image.Rect(0, 0, w, max(h, int(math.Round(float64(w)*0.62)))))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.RGBA{7, 61, 55, 255}), image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(out, image.Rect(0, 0, w, h), source, source.Bounds(), draw.Src, nil)
	if textValue(meta["locationLabel"]) == "" {
		metaCopy := make(map[string]any, len(meta))
		for k, v := range meta {
			metaCopy[k] = v
		}
		meta = metaCopy
		meta["locationLabel"] = reverseEvidenceAddress(ctx, number(meta["captureLatitude"]), number(meta["captureLongitude"]))
	}
	if err = drawEvidenceWatermark(out, meta, caption); err != nil {
		return "", err
	}
	for _, quality := range []int{88, 78, 68, 58} {
		var buf bytes.Buffer
		if err = jpeg.Encode(&buf, out, &jpeg.Options{Quality: quality}); err != nil {
			return "", err
		}
		result := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		if len(result) <= limit {
			return result, nil
		}
	}
	for _, factor := range []float64{0.8, 0.6, 0.5} {
		resized := image.NewRGBA(image.Rect(0, 0, max(1, int(math.Round(float64(w)*factor))), max(1, int(math.Round(float64(out.Bounds().Dy())*factor)))))
		xdraw.CatmullRom.Scale(resized, resized.Bounds(), out, out.Bounds(), draw.Src, nil)
		var buf bytes.Buffer
		if err = jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 78}); err != nil {
			return "", err
		}
		result := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		if len(result) <= limit {
			return result, nil
		}
	}
	return "", fmt.Errorf("stamped photo exceeds storage limit")
}

// Mobile JPEG orientation is in an EXIF TIFF header; validate offsets before reads.
func jpegEvidenceOrientation(raw []byte) int {
	if len(raw) < 4 || raw[0] != 0xff || raw[1] != 0xd8 {
		return 1
	}
	for pos := 2; pos+4 <= len(raw) && raw[pos] == 0xff; {
		marker := raw[pos+1]
		if marker == 0xda || marker == 0xd9 {
			break
		}
		size := int(binary.BigEndian.Uint16(raw[pos+2 : pos+4]))
		if size < 2 || size > len(raw)-pos-2 {
			break
		}
		segment := raw[pos+4 : pos+2+size]
		pos += 2 + size
		if marker != 0xe1 || len(segment) < 14 || string(segment[:6]) != "Exif\x00\x00" {
			continue
		}
		tiff := segment[6:]
		var order binary.ByteOrder = binary.LittleEndian
		if string(tiff[:2]) == "MM" {
			order = binary.BigEndian
		} else if string(tiff[:2]) != "II" {
			continue
		}
		if order.Uint16(tiff[2:4]) != 42 {
			continue
		}
		offset := int64(order.Uint32(tiff[4:8]))
		if offset < 8 || offset+2 > int64(len(tiff)) {
			continue
		}
		entries := int(order.Uint16(tiff[offset : offset+2]))
		for i := 0; i < entries; i++ {
			start := int(offset) + 2 + i*12
			if start+12 > len(tiff) {
				break
			}
			e := tiff[start : start+12]
			if order.Uint16(e[:2]) == 0x112 && order.Uint16(e[2:4]) == 3 && order.Uint32(e[4:8]) == 1 {
				if orientation := int(order.Uint16(e[8:10])); orientation >= 1 && orientation <= 8 {
					return orientation
				}
			}
		}
	}
	return 1
}

func orientEvidence(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	ow, oh := w, h
	if orientation >= 5 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			out.Set(x, y, src.At(src.Bounds().Min.X+sx, src.Bounds().Min.Y+sy))
		}
	}
	return out
}

func drawEvidenceWatermark(dst *image.RGBA, meta map[string]any, c evidenceCaption) error {
	regular, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return err
	}
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return err
	}
	scale := float64(dst.Bounds().Dx()) / 1000
	viewHeight := float64(dst.Bounds().Dy()) / scale
	cardY := viewHeight - 390
	green := color.RGBA{0, 87, 76, 235}
	rect := func(x, y, w, h float64, fill color.Color) {
		r := image.Rect(int(x*scale), int(y*scale), int((x+w)*scale), int((y+h)*scale))
		draw.Draw(dst, r, image.NewUniform(fill), image.Point{}, draw.Over)
	}
	text := func(x, y, size float64, value string, heavy bool, maxChars int, ink color.Color) {
		if err != nil {
			return
		}
		runes := []rune(cleanEvidenceText(value))
		if len(runes) > maxChars {
			runes = runes[:maxChars]
		}
		faceFont := regular
		if heavy {
			faceFont = bold
		}
		var face font.Face
		face, err = opentype.NewFace(faceFont, &opentype.FaceOptions{Size: math.Max(1, size*scale), DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return
		}
		d := font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face, Dot: fixed.P(int(x*scale), int(y*scale))}
		d.DrawString(string(runes))
		if closeErr := face.Close(); closeErr != nil {
			err = closeErr
		}
	}
	white := color.White
	if c.Company == "" {
		c.Company = os.Getenv("NEXT_PUBLIC_COMPANY_NAME")
		if c.Company == "" {
			c.Company = "NovaLogistik"
		}
	}
	rect(34, 32, 540, 72, green)
	text(56, 63, 25, c.Company, true, 35, white)
	text(56, 88, 18, c.Label, false, 50, white)
	accuracy := "Akurasi tidak tersedia"
	if meta["captureAccuracy"] != nil {
		accuracy = fmt.Sprintf("Akurasi GPS ±%.0f m", number(meta["captureAccuracy"]))
	}
	rect(718, 35, 248, 52, color.RGBA{24, 45, 49, 210})
	text(730, 68, 18, accuracy, false, 30, white)
	rect(34, cardY, 932, 334, green)
	for _, y := range []float64{94, 216} {
		rect(56, cardY+y, 888, 1, color.RGBA{255, 255, 255, 100})
	}
	for _, x := range []float64{390, 650} {
		rect(x, cardY+18, 2, 60, color.RGBA{196, 220, 53, 255})
	}
	rect(525, cardY+234, 2, 78, color.RGBA{196, 220, 53, 255})
	zone := textValue(meta["timeZone"])
	offset, zoneLabel := 7, "WIB"
	if zone == "Asia/Makassar" {
		offset, zoneLabel = 8, "WITA"
	}
	if zone == "Asia/Jayapura" {
		offset, zoneLabel = 9, "WIT"
	}
	captured := meta["capturedAt"].(time.Time).In(time.FixedZone(zone, offset*3600))
	weekdays := [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	months := [...]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
	text(58, cardY+32, 18, fmt.Sprintf("%s, %d %s %d", weekdays[captured.Weekday()], captured.Day(), months[captured.Month()-1], captured.Year()), false, 55, white)
	text(58, cardY+70, 34, captured.Format("15:04"), true, 5, white)
	text(164, cardY+70, 22, zoneLabel, false, 4, white)
	text(410, cardY+37, 23, c.Origin+" →", true, 20, white)
	text(410, cardY+68, 23, c.Destination, true, 18, white)
	text(670, cardY+37, 22, "Bukti operasional", true, 22, white)
	count := c.ShipmentCount
	if count == 0 && c.VehiclePlate == "" {
		count = 1
	}
	text(670, cardY+68, 20, fmt.Sprintf("(%d shipment)", count), false, 20, white)
	text(58, cardY+121, 18, "Alamat Lokasi", false, 20, white)
	address := textValue(meta["locationLabel"])
	if address == "" {
		address = "Alamat tidak tersedia · lihat koordinat GPS"
	}
	words := strings.Fields(address)
	lines := []string{""}
	for _, word := range words {
		i := len(lines) - 1
		if len([]rune(lines[i]+" "+word)) > 65 && lines[i] != "" {
			if len(lines) == 3 {
				break
			}
			lines = append(lines, word)
		} else {
			lines[i] = strings.TrimSpace(lines[i] + " " + word)
		}
	}
	for i, line := range lines {
		text(58, cardY+145+float64(i)*21, 20, line, true, 70, white)
	}
	text(58, cardY+207, 14, fmt.Sprintf("%.6f, %.6f", number(meta["captureLatitude"]), number(meta["captureLongitude"])), false, 45, white)
	text(58, cardY+242, 18, "Kendaraan", false, 20, white)
	rect(58, cardY+251, 300, 34, color.RGBA{255, 227, 45, 255})
	plate := c.VehiclePlate
	if plate == "" {
		plate = "Tidak tersedia"
	}
	text(68, cardY+277, 27, plate, true, 18, color.RGBA{6, 61, 54, 255})
	text(58, cardY+309, 15, c.VehicleDescription, false, 36, white)
	text(550, cardY+242, 18, "Supir", false, 20, white)
	text(550, cardY+278, 26, c.Driver, true, 25, white)
	text(550, cardY+309, 15, c.RecordedBy, false, 36, white)
	if truthy(meta["mockLocation"]) {
		text(220, cardY-14, 20, "PERINGATAN: lokasi simulasi dilaporkan", false, 60, color.RGBA{255, 227, 45, 255})
	}
	text(330, viewHeight-33, 13, "Alamat: © OpenStreetMap contributors", false, 50, white)
	text(420, viewHeight-10, 18, "By NovaLogistik", true, 25, white)
	return err
}

var evidenceAddresses = struct {
	sync.Mutex
	next  time.Time
	cache map[string]struct {
		address string
		expires time.Time
	}
}{cache: make(map[string]struct {
	address string
	expires time.Time
})}

func reverseEvidenceAddress(ctx context.Context, lat, lon float64) string {
	base := os.Getenv("NOMINATIM_URL")
	if base == "" {
		base = "https://nominatim.openstreetmap.org"
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/reverse")
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	key := fmt.Sprintf("%s:%.5f,%.5f", u.String(), lat, lon)
	evidenceAddresses.Lock()
	now := time.Now()
	if entry, ok := evidenceAddresses.cache[key]; ok && now.Before(entry.expires) {
		evidenceAddresses.Unlock()
		return entry.address
	}
	// shortcut: process-local fallback limiter/cache, share before adding replicas.
	if now.Before(evidenceAddresses.next) {
		evidenceAddresses.Unlock()
		return ""
	}
	evidenceAddresses.next = now.Add(1100 * time.Millisecond)
	evidenceAddresses.Unlock()
	u.RawQuery = url.Values{"format": {"jsonv2"}, "lat": {fmt.Sprint(lat)}, "lon": {fmt.Sprint(lon)}, "accept-language": {"id"}}.Encode()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "NovaLogistik/0.2 (operational evidence photos)")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return ""
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&body); err != nil {
		return ""
	}
	address := []rune(cleanEvidenceText(body.DisplayName))
	address = address[:min(240, len(address))]
	ttl := 24 * time.Hour
	if len(address) == 0 {
		ttl = time.Minute
	}
	evidenceAddresses.Lock()
	defer evidenceAddresses.Unlock()
	if len(evidenceAddresses.cache) >= 1000 {
		for k := range evidenceAddresses.cache {
			delete(evidenceAddresses.cache, k)
			break
		}
	}
	evidenceAddresses.cache[key] = struct {
		address string
		expires time.Time
	}{string(address), now.Add(ttl)}
	return string(address)
}
