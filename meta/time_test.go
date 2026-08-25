package meta

import (
	"testing"
	"time"
)

func berlin(t *testing.T) *Clock {
	t.Helper()
	c, err := NewClock(ClockConfig{Policy: PolicyPreserveExisting, Timezone: "Europe/Berlin"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ts(y int, mo time.Month, d, h, mi, s int) *int64 {
	return int64Ptr(time.Date(y, mo, d, h, mi, s, 0, time.UTC).Unix())
}

func int64Ptr(v int64) *int64 { return &v }

func utc(y int, mo time.Month, d, h, mi, s int) *time.Time {
	t := time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	return &t
}

// T1/T2: historical DST correctness for Europe/Berlin.
func TestClock_BerlinWinterSummer(t *testing.T) {
	c := berlin(t)

	r := c.resolveJSON(utc(2020, 1, 15, 12, 0, 0), SrcJsonPhotoTaken)
	if got := Exif(r.Local); got != "2020:01:15 13:00:00" {
		t.Errorf("CET winter: %s", got)
	}
	if r.Offset == nil || *r.Offset != "+01:00" {
		t.Errorf("CET offset: %v", r.Offset)
	}

	r = c.resolveJSON(utc(2020, 7, 15, 12, 0, 0), SrcJsonPhotoTaken)
	if got := Exif(r.Local); got != "2020:07:15 14:00:00" {
		t.Errorf("CEST summer: %s", got)
	}
	if r.Offset == nil || *r.Offset != "+02:00" {
		t.Errorf("CEST offset: %v", r.Offset)
	}
}

// T6: unresolved timezone warns instead of faking local time.
func TestClock_UnresolvedTimezoneWarning(t *testing.T) {
	c, _ := NewClock(ClockConfig{Policy: PolicyPreferJSON})
	r := c.resolveJSON(utc(2020, 1, 15, 12, 0, 0), SrcJsonCreation)
	if r.Source != SrcJsonCreation {
		t.Fatalf("%+v", r)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected prominent warning when timezone unresolved")
	}
	if r.Offset != nil {
		t.Fatalf("must not invent an offset: %+v", r.Offset)
	}
}

// T3: existing embedded time with offset preserved by default (preserve-existing).
func TestClock_PreserveExistingWithOffset(t *testing.T) {
	c := berlin(t)
	emb := Embedded{
		PhotoDO:    "2019:05:05 10:00:00",
		PhotoDOOff: "+02:00",
	}
	json := ts(2020, 12, 6, 13, 24, 33)
	r := c.ResolveTaken(json, SrcJsonPhotoTaken, emb, nil, time.Time{})
	if r.Source != SrcEmbDO {
		t.Fatalf("embedded+offset must be preserved: %+v", r)
	}
	if got := Exif(r.Local); got != "2019:05:05 10:00:00" {
		t.Fatalf("wall clock changed: %s", got)
	}
}

// T4/--force-json-time and prefer-json overwrite existing times.
func TestClock_ForceAndPreferJSON(t *testing.T) {
	emb := Embedded{PhotoDO: "2019:05:05 10:00:00", PhotoDOOff: "+02:00"}
	json := ts(2020, 12, 6, 13, 24, 33)

	for _, cfg := range []ClockConfig{
		{Policy: PolicyPreserveExisting, ForceJSON: true, Timezone: "Europe/Berlin"},
		{Policy: PolicyPreferJSON, Timezone: "Europe/Berlin"},
	} {
		c, _ := NewClock(cfg)
		r := c.ResolveTaken(json, SrcJsonPhotoTaken, emb, nil, time.Time{})
		if r.Source != SrcJsonPhotoTaken {
			t.Fatalf("config %+v: json must win, got %s", cfg, r.Source)
		}
		if got := Exif(r.Local); got != "2020:12:06 14:24:33" {
			t.Fatalf("config %+v: local time %s", cfg, got)
		}
	}
}

// json-only ignores embedded entirely; falls back to mtime only when JSON absent.
func TestClock_JSONOnly(t *testing.T) {
	c, _ := NewClock(ClockConfig{Policy: PolicyJSONOnly})
	emb := Embedded{PhotoDO: "2019:05:05 10:00:00", PhotoDOOff: "+02:00"}

	r := c.ResolveTaken(ts(2020, 12, 6, 13, 24, 33), SrcJsonPhotoTaken, emb, nil, time.Time{})
	if r.Source != SrcJsonPhotoTaken {
		t.Fatalf("json-only must use json: %s", r.Source)
	}

	mtime := time.Date(2001, 1, 1, 8, 30, 0, 0, time.UTC)
	r = c.ResolveTaken(nil, "", emb, nil, mtime)
	if r.Source != SrcFSMtime {
		t.Fatalf("mtime fallback expected: %s", r.Source)
	}
}

// Naive embedded times reconcile against the JSON instant within ±26h.
func TestClock_ReconcileWindow(t *testing.T) {
	c := berlin(t)
	emb := Embedded{PhotoCD: "2020:12:06 18:24:33"} // naive, ~5h from JSON instant
	r := c.ResolveTaken(ts(2020, 12, 6, 13, 24, 33), SrcJsonPhotoTaken, emb, nil, time.Time{})
	if r.Source != SrcEmbCreateDate {
		t.Fatalf("reconcilable naive embedded time should win under preserve-existing: %s", r.Source)
	}

	// Far-off embedded time is treated as corrupt; JSON takes over.
	embFar := Embedded{PhotoCD: "1999:01:01 00:00:00"}
	r = c.ResolveTaken(ts(2020, 12, 6, 13, 24, 33), SrcJsonPhotoTaken, embFar, nil, time.Time{})
	if r.Source != SrcJsonPhotoTaken {
		t.Fatalf("corrupt embedded must fall through to json: %s", r.Source)
	}
}

// GPS formatting is always derived from the absolute UTC instant.
func TestFormatting_GPSUTC(t *testing.T) {
	inst := time.Date(2020, 12, 6, 13, 24, 33, 0, time.UTC)
	if GPSDate(inst) != "2020:12:06" || GPSTime(inst) != "13:24:33" {
		t.Fatalf("GPS stamps: %s %s", GPSDate(inst), GPSTime(inst))
	}
	local := inst.In(time.FixedZone("+02", 2*3600))
	if GPSDate(local) != "2020:12:06" || GPSTime(local) != "13:24:33" {
		t.Fatal("GPS stamps must not shift with display zone")
	}
}

// Video embedded dates are never reinterpreted through the user's zone.
func TestClock_VideoDatesStayUTC(t *testing.T) {
	c := berlin(t)
	emb := Embedded{VideoCreated: []string{"2020:12:06 13:24:33"}} // atom digits = UTC
	r := c.ResolveTaken(nil, "", emb, nil, time.Time{})
	if r.Source != SrcEmbVideoDate {
		t.Fatalf("%+v", r)
	}
	if !r.Instant.Equal(time.Date(2020, 12, 6, 13, 24, 33, 0, time.UTC)) {
		t.Fatalf("video atom digits reinterpreted: %v", r.Instant)
	}
}

func TestParseTimePolicy(t *testing.T) {
	for _, s := range []string{"preserve-existing", "prefer-json", "json-only"} {
		if _, err := ParseTimePolicy(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	if _, err := ParseTimePolicy("bogus"); err == nil {
		t.Error("bogus policy accepted")
	}
}

func TestNewClock_TimezoneSpecs(t *testing.T) {
	for _, spec := range []string{"Europe/Berlin", "+01:00", "-0530", "+01"} {
		if _, err := NewClock(ClockConfig{Timezone: spec}); err != nil {
			t.Errorf("%s: %v", spec, err)
		}
	}
	for _, spec := range []string{"No/Such/Zone", "+99:00", "12:34"} {
		if _, err := NewClock(ClockConfig{Timezone: spec}); err == nil {
			t.Errorf("%s accepted", spec)
		}
	}
}
