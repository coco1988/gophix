package meta

import (
	"testing"
	"time"
)

func mustFile(t *testing.T, name string) *FileNameDate {
	t.Helper()
	f, err := ParseFileName(name)
	if err != nil {
		t.Fatalf("ParseFileName(%q): %v", name, err)
	}
	if f == nil {
		t.Fatalf("ParseFileName(%q): no date found", name)
	}
	return f
}

func expectWall(t *testing.T, f *FileNameDate, y int, mo time.Month, d, h, mi, s int) {
	t.Helper()
	w := f.Wall
	if w.Year() != y || w.Month() != mo || w.Day() != d || w.Hour() != h || w.Minute() != mi || w.Second() != s {
		t.Fatalf("got %s, want %04d-%02d-%02d %02d:%02d:%02d", w, y, mo, d, h, mi, s)
	}
}

// F1
func TestFilename_IMGPattern(t *testing.T) {
	f := mustFile(t, "IMG_20201206_142433.jpg")
	expectWall(t, f, 2020, time.December, 6, 14, 24, 33)
	if f.DateOnly || f.Pattern != patIMG {
		t.Fatalf("pattern/dateOnly: %+v", f)
	}
}

// F2
func TestFilename_VIDPattern(t *testing.T) {
	f := mustFile(t, "VID_20201206_142433.mp4")
	expectWall(t, f, 2020, time.December, 6, 14, 24, 33)
	if f.DateOnly || f.Pattern != patVID {
		t.Fatalf("%+v", f)
	}
}

// F3: extra digits after the six time digits are ignored.
func TestFilename_PXLExtraDigits(t *testing.T) {
	f := mustFile(t, "PXL_20201206_142433123.jpg")
	expectWall(t, f, 2020, time.December, 6, 14, 24, 33)
	if f.Pattern != patPXL || f.DateOnly {
		t.Fatalf("%+v", f)
	}
}

// F4: impossible calendar values rejected.
func TestFilename_InvalidValuesRejected(t *testing.T) {
	for _, name := range []string{
		"IMG_20201332_142433.jpg", // month 13 / day 32
		"IMG_20200230_142433.jpg", // Feb 30
		"IMG_20210229_142433.jpg", // non-leap Feb 29
		"IMG_20201206_252433.jpg", // hour 25
		"IMG_12345678.jpg",        // ambiguous sequence number
	} {
		f, err := ParseFileName(name)
		if err == nil && f != nil {
			t.Errorf("%s: must be rejected, got %+v", name, f)
		}
	}
}

// Leap years are valid.
func TestFilename_LeapYear(t *testing.T) {
	f := mustFile(t, "IMG_20240229_101112.jpg")
	expectWall(t, f, 2024, time.February, 29, 10, 11, 12)
}

// F5: two different valid timestamps -> refuse to guess.
func TestFilename_AmbiguousSkipped(t *testing.T) {
	f, err := ParseFileName("20200101_101010_and_IMG_20201206_142433.jpg")
	if err == nil {
		t.Fatalf("expected ambiguity error, got %+v", f)
	}
	if f != nil {
		t.Fatalf("nil result required on ambiguity")
	}
}

func TestFilename_DateOnlyForms(t *testing.T) {
	f := mustFile(t, "2020-12-06.jpg")
	if !f.DateOnly || f.Pattern != patDateDash {
		t.Fatalf("%+v", f)
	}
	expectWall(t, f, 2020, time.December, 6, 0, 0, 0)

	f = mustFile(t, "scan_20201206_backup.png")
	if !f.DateOnly || f.Pattern != patDateEight {
		t.Fatalf("%+v", f)
	}

	// WhatsApp style full timestamp.
	f = mustFile(t, "WhatsApp_Image_2020-12-06_at_14.24.33.jpeg")
	expectWall(t, f, 2020, time.December, 6, 14, 24, 33)
	if f.DateOnly || f.Pattern != patWhatsApp {
		t.Fatalf("%+v", f)
	}
}

// Date-only never shadows a full timestamp in the same name.
func TestFilename_FullBeatsDateOnly(t *testing.T) {
	f := mustFile(t, "2020-01-01_trip_IMG_20201206_142433.jpg")
	if f.DateOnly {
		t.Fatal("full timestamp must win over date-only")
	}
	expectWall(t, f, 2020, time.December, 6, 14, 24, 33)
}

// Collision suffixes after the date/time portion are tolerated.
func TestFilename_CollisionSuffixes(t *testing.T) {
	for _, name := range []string{"IMG_20201206_142433(1).jpg", "IMG_20201206_142433_1.jpg", "IMG_20201206_142433-2.jpg"} {
		f := mustFile(t, name)
		expectWall(t, f, 2020, time.December, 6, 14, 24, 33)
	}
}

// Extension digits are never parsed.
func TestFilename_ExtensionIgnored(t *testing.T) {
	f, err := ParseFileName("holiday_20201206.mp4")
	if err != nil || f == nil {
		t.Fatalf("expected date-only from stem, err=%v f=%+v", err, f)
	}
	if !f.DateOnly {
		t.Fatalf("stem digits only: %+v", f)
	}
}

func TestClock_FilenameResolution(t *testing.T) {
	// T6/F-precedence: JSON overrides filename.
	taken := int64(1607262273) // 2020-12-06T13:24:33Z
	fname := &FileNameDate{Wall: time.Date(2019, 1, 1, 10, 0, 0, 0, time.UTC)}
	clock, _ := NewClock(ClockConfig{Policy: PolicyPreferJSON})
	r := clock.ResolveTaken(&taken, SrcJsonPhotoTaken, Embedded{}, fname, time.Time{})
	if r.Source != SrcJsonPhotoTaken {
		t.Fatalf("json must win: %+v", r)
	}

	// Default policy: valid embedded DateTimeOriginal with offset wins over filename.
	clock2, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting})
	emb := Embedded{PhotoDO: "2019:05:05 10:00:00", PhotoDOOff: "+02:00"}
	r2 := clock2.ResolveTaken(nil, "", emb, fname, time.Time{})
	if r2.Source != SrcEmbDO {
		t.Fatalf("embedded+offset must win: %+v", r2)
	}

	// Filename used when nothing else exists; with explicit zone it gains an offset.
	clock3, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting, Timezone: "Europe/Berlin"})
	r3 := clock3.ResolveTaken(nil, "", Embedded{}, fname, time.Unix(1000000, 0))
	if r3.Source != SrcFileNameDT {
		t.Fatalf("filename fallback expected: %+v", r3)
	}
	_ = clock

	// --no-filename-fallback skips the filename source entirely.
	clock4, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting, NoFilenameFallback: true, Timezone: "Europe/Berlin"})
	embNil := Embedded{}
	r5 := clock4.ResolveTaken(nil, "", embNil, fname, time.Unix(1600000000, 0))
	if r5.Source != SrcFSMtime {
		t.Fatalf("filename fallback must be disabled: %+v", r5)
	}
}

func TestClock_FileNameDateTimeWithZone(t *testing.T) {
	emb := Embedded{}
	fname := &FileNameDate{Wall: time.Date(2020, 12, 6, 14, 24, 33, 0, time.UTC)}

	// Without any other source and non-json-only policy the filename applies.
	clock2, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting, Timezone: "Europe/Berlin"})
	r := clock2.ResolveTaken(nil, "", emb, fname, time.Time{})
	if r.Source != SrcFileNameDT {
		t.Fatalf("%+v", r)
	}
	if !r.HasAbsolute || r.Offset == nil || *r.Offset != "+01:00" {
		t.Fatalf("zone must attach: %+v", r)
	}
	if got := Exif(r.Local); got != "2020:12:06 14:24:33" {
		t.Fatalf("clock digits changed: %s", got)
	}

	// Without zone: digits kept, no offset claim, warning present.
	clock3, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting})
	r3 := clock3.ResolveTaken(nil, "", emb, fname, time.Time{})
	if r3.Offset != nil || len(r3.Warnings) == 0 {
		t.Fatalf("naive filename time must warn: %+v", r3)
	}
}

func TestClock_DateOnlyOrganizeYearNoInventedTime(t *testing.T) {
	clock, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting})
	fname := &FileNameDate{Wall: time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC), DateOnly: true, Pattern: patDateDash}
	r := clock.ResolveTaken(nil, "", Embedded{}, fname, time.Time{})
	if !r.DateOnly || r.Source != SrcFileNameDO {
		t.Fatalf("%+v", r)
	}
	if r.Local.Year() != 2019 || r.Local.Month() != time.March {
		t.Fatalf("year resolution: %+v", r.Local)
	}
	if r.HasAbsolute {
		t.Fatal("date-only without assume-noon has no trustworthy absolute instant for GPS")
	}

	// assume-noon promotes it into a writable full time.
	clock2, _ := NewClock(ClockConfig{Policy: PolicyPreserveExisting, AssumeNoonForDateOnly: true, Timezone: "Europe/Berlin"})
	r2 := clock2.ResolveTaken(nil, "", Embedded{}, fname, time.Time{})
	if r2.DateOnly || r2.Offset == nil {
		t.Fatalf("assume-noon: %+v", r2)
	}
	if got := Exif(r2.Local); got != "2019:03:01 12:00:00" {
		t.Fatalf("noon: %s", got)
	}
}
