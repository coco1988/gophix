package takeout

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, json string) *Sidecar {
	t.Helper()
	s, err := Parse([]byte(json))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

const fullFixture = `{
  "title": "IMG_20201206_142433.jpg",
  "description": "Urlaub am See",
  "photoTakenTime": {"timestamp": "1607262273", "formatted": "06.12.2020, 13:24:33 UTC"},
  "creationTime": {"timestamp": "1607952273", "formatted": "14.12.2020, 13:24:33 UTC"},
  "geoData": {"latitude": 41.900947, "longitude": 12.464825, "altitude": 75.273},
  "geoDataExif": {"latitude": -33.856782, "longitude": 151.215281, "altitude": 0.0},
  "imageViews": "11",
  "url": "https://photos.google.com/photo/XYZ",
  "googlePhotosOrigin": {"mobileUpload": {}}
}`

func TestParse_Full(t *testing.T) {
	s := mustParse(t, fullFixture)
	if s.Title != "IMG_20201206_142433.jpg" {
		t.Errorf("title")
	}
	if s.Description != "Urlaub am See" {
		t.Errorf("description unicode")
	}
	if s.PhotoTaken == nil || *s.PhotoTaken != 1607262273 {
		t.Errorf("photoTakenTime")
	}
	if s.Geo == nil || s.Geo.Lat != 41.900947 {
		t.Errorf("geoData")
	}
	if got := s.PresentUntransferred(); len(got) == 0 || !strings.Contains(strings.Join(got, ","), "url") {
		t.Errorf("PresentUntransferred = %v", got)
	}
}

func TestModel_TimePrecedence(t *testing.T) {
	s := mustParse(t, fullFixture)
	if got := s.TakenUnix(); got == nil || *got != 1607262273 {
		t.Fatalf("photoTakenTime must win: %v", got)
	}

	// creationTime fallback when photoTakenTime absent.
	s2 := mustParse(t, `{"creationTime":{"timestamp":"1607952273"}}`)
	if got := s2.TakenUnix(); got == nil || *got != 1607952273 {
		t.Fatalf("creationTime fallback: %v", got)
	}

	// Neither present.
	s3 := mustParse(t, `{"title":"x"}`)
	if s3.TakenUnix() != nil {
		t.Fatal("expected nil taken time")
	}
}

func TestModel_GeoPrecedence(t *testing.T) {
	s := mustParse(t, fullFixture)
	g := s.EffectiveGeo()
	if g == nil || g.Lat != 41.900947 {
		t.Fatalf("geoData must win over geoDataExif: %+v", g)
	}

	// geoData redacted placeholder -> geoDataExif fallback.
	s2 := mustParse(t, `{"geoData":{"latitude":0.0,"longitude":0.0,"altitude":0.0},"geoDataExif":{"latitude":-33.86,"longitude":151.21,"altitude":0.0}}`)
	g2 := s2.EffectiveGeo()
	if g2 == nil || g2.Lat != -33.86 {
		t.Fatalf("placeholder must fall through to geoDataExif: %+v", g2)
	}

	// Both placeholders -> nil.
	s3 := mustParse(t, `{"geoData":{"latitude":0.0,"longitude":0.0},"geoDataExif":{"latitude":0.0,"longitude":0.0}}`)
	if s3.EffectiveGeo() != nil {
		t.Fatal("(0,0) placeholders must yield nil GPS")
	}

	// Out of range rejected.
	s4 := mustParse(t, `{"geoData":{"latitude":91.0,"longitude":12.0}}`)
	if s4.EffectiveGeo() != nil {
		t.Fatal("out-of-range latitude must be rejected")
	}
}

func TestModel_AltitudePlaceholder(t *testing.T) {
	s := mustParse(t, `{"geoData":{"latitude":10.0,"longitude":20.0,"altitude":0.0}}`)
	g := s.EffectiveGeo()
	if g.HasAltitude() {
		t.Fatal("altitude exactly 0.0 is treated as placeholder")
	}
	s2 := mustParse(t, `{"geoData":{"latitude":10.0,"longitude":20.0,"altitude":-12.5}}`)
	if !s2.EffectiveGeo().HasAltitude() {
		t.Fatal("negative altitude is valid (below sea level)")
	}
}

func TestModel_InvalidTimestampsAreErrors(t *testing.T) {
	if _, err := Parse([]byte(`{"photoTakenTime":{"timestamp":"notanumber"}}`)); err == nil {
		t.Fatal("unparsable timestamp must error")
	}
	if _, err := Parse([]byte(`{invalid json`)); err == nil {
		t.Fatal("malformed json must error")
	}
}

func TestModel_EmptyDescriptionDetection(t *testing.T) {
	s := mustParse(t, `{"description":""}`)
	if s.Description != "" {
		t.Fatal("empty description")
	}
}

func TestParse_TimestampRoundTrip(t *testing.T) {
	s := mustParse(t, fullFixture)
	tv := time.Unix(*s.PhotoTaken, 0).UTC()
	if tv.Year() != 2020 || tv.Month() != time.December || tv.Day() != 6 {
		t.Fatalf("unexpected instant: %v", tv)
	}
}
