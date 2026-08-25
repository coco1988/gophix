// Package takeout models Google Photos Takeout sidecar JSON files and
// implements the sidecar matching rules for current and legacy exports.
package takeout

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Geo holds a geographic location from a Takeout sidecar.
type Geo struct {
	Lat float64
	Lon float64
	Alt float64
}

// ValidCoords reports whether the coordinates are usable. The redacted
// placeholder (0,0) and out-of-range values are rejected.
func (g Geo) ValidCoords() bool {
	if g.Lat == 0 && g.Lon == 0 {
		return false
	}
	return g.Lat >= -90 && g.Lat <= 90 && g.Lon >= -180 && g.Lon <= 180
}

// HasAltitude reports whether the altitude is distinguishable from the
// Takeout placeholder value of exactly 0.0.
func (g Geo) HasAltitude() bool {
	return g.Alt != 0
}

// Sidecar is the typed representation of a Google Photos Takeout metadata file.
type Sidecar struct {
	Title       string
	Description string
	PhotoTaken  *int64 // photoTakenTime.timestamp, unix seconds
	Creation    *int64 // creationTime.timestamp, unix seconds
	Geo         *Geo   // geoData
	GeoExif     *Geo   // geoDataExif

	Raw map[string]any // top-level raw keys, for known-field reporting only
}

type rawTime struct {
	Timestamp *string `json:"timestamp"`
}

type rawGeo struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Altitude  *float64 `json:"altitude"`
}

type rawSidecar struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	PhotoTakenTime *rawTime `json:"photoTakenTime"`
	CreationTime   *rawTime `json:"creationTime"`
	GeoData        *rawGeo  `json:"geoData"`
	GeoDataExif    *rawGeo  `json:"geoDataExif"`
}

// KnownNotTransferred lists well-known sidecar fields that are intentionally
// not mapped into media metadata. It is used for verbose reporting only.
var KnownNotTransferred = []string{
	"imageViews",
	"url",
	"googlePhotosOrigin",
	"appSource",
	"geoData.latitudeSpan",
	"geoData.longitudeSpan",
}

// ParseFile reads and parses a sidecar from disk.
func ParseFile(path string) (*Sidecar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes a Takeout sidecar JSON document. Malformed JSON or unparsable
// timestamps return an error; such sidecars must never be silently ignored.
func Parse(data []byte) (*Sidecar, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON document")
	}
	var raw rawSidecar
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("cannot decode takeout json: %w", err)
	}

	s := &Sidecar{
		Title:       raw.Title,
		Description: raw.Description,
	}
	_ = json.Unmarshal(data, &s.Raw) // best-effort raw key map

	var err error
	if s.PhotoTaken, err = parseUnix(raw.PhotoTakenTime, "photoTakenTime.timestamp"); err != nil {
		return nil, err
	}
	if s.Creation, err = parseUnix(raw.CreationTime, "creationTime.timestamp"); err != nil {
		return nil, err
	}
	s.Geo = convertGeo(raw.GeoData)
	s.GeoExif = convertGeo(raw.GeoDataExif)

	return s, nil
}

func parseUnix(rt *rawTime, field string) (*int64, error) {
	if rt == nil || rt.Timestamp == nil || *rt.Timestamp == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(*rt.Timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("field %s: unparsable unix timestamp %q", field, *rt.Timestamp)
	}
	return &v, nil
}

func convertGeo(rg *rawGeo) *Geo {
	if rg == nil || rg.Latitude == nil || rg.Longitude == nil {
		return nil
	}
	g := &Geo{Lat: *rg.Latitude, Lon: *rg.Longitude}
	if rg.Altitude != nil {
		g.Alt = *rg.Altitude
	}
	return g
}

// TakenUnix returns the effective capture instant per priority:
// photoTakenTime first, then creationTime, else nil.
func (s *Sidecar) TakenUnix() *int64 {
	if s.PhotoTaken != nil {
		return s.PhotoTaken
	}
	return s.Creation
}

// EffectiveGeo returns the usable location per priority: geoData first, then
// geoDataExif. Placeholder and out-of-range coordinates yield nil.
func (s *Sidecar) EffectiveGeo() *Geo {
	if s.Geo != nil && s.Geo.ValidCoords() {
		return s.Geo
	}
	if s.GeoExif != nil && s.GeoExif.ValidCoords() {
		return s.GeoExif
	}
	return nil
}

// PresentUntransferred returns the known fields present in this sidecar that
// are deliberately not written into media metadata.
func (s *Sidecar) PresentUntransferred() []string {
	var found []string
	if s.Raw == nil {
		return nil
	}
	for _, k := range KnownNotTransferred {
		top := k
		if i := strings.IndexByte(k, '.'); i >= 0 {
			top = k[:i]
		}
		if _, ok := s.Raw[top]; ok {
			found = append(found, k)
		}
	}
	return found
}
