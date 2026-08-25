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

func TestResolveDate_FilenameWithAndWithoutZone(t *testing.T) {
	fname := &FileNameDate{Wall: time.Date(2020, 12, 6, 14, 24, 33, 0, time.UTC)}

	zone, _ := LoadZone("Europe/Berlin")
	r, ok := ResolveDate(Info{}, nil, fname, zone)
	if !ok || r.Source != SrcFilename || r.DateOnly {
		t.Fatalf("%+v ok=%v", r, ok)
	}
	if r.Offset == nil || *r.Offset != "+01:00" {
		t.Fatalf("zone must attach: %+v", r)
	}
	if got := Exif(r.Wall); got != "2020:12:06 14:24:33" {
		t.Fatalf("clock digits changed: %s", got)
	}

	r3, ok := ResolveDate(Info{}, nil, fname, nil)
	if !ok || r3.Offset != nil || r3.DateOnly {
		t.Fatalf("naive filename date keeps digits without offset claim: %+v", r3)
	}
}
