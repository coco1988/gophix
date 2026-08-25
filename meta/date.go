package meta

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The v2 date rule, in priority order:
//
//	1. embedded DateTimeOriginal/CreateDate (valid)  -> taken as-is
//	2. JSON photoTakenTime -> creationTime           -> written with optional --timezone
//	3. filename pattern (IMG_/VID_/PXL_/WhatsApp/…)   -> last resort
//
// GPS coordinates are only filled when the photo has none. No policies, no
// offset reconciliation: what the file already says wins.

const (
	exifLayout = "2006:01:02 15:04:05"
	fileLayout = "2006:01:02 15:04:05-07:00"
)

// Date source labels for reporting.
const (
	SrcEmbedded = "embedded"
	SrcJSON     = "json"
	SrcFilename = "filename"
)

// DateResult is the resolved capture date of one file.
type DateResult struct {
	Source   string
	Wall     time.Time // wall clock to write into EXIF date fields
	Offset   *string   // "+01:00" when known (--timezone applied to a JSON/filename date)
	Instant  time.Time // absolute instant for filesystem/GPS purposes
	DateOnly bool      // filename matched a date only: year organization, no time invented
}

// ResolveDate applies the v2 rule and reports which source won.
// taken is the effective JSON timestamp (photoTakenTime or creationTime);
// fname the filename probe; zone an explicitly configured timezone or nil.
func ResolveDate(info Info, taken *int64, fname *FileNameDate, zone *time.Location) (*DateResult, bool) {
	// 1. Embedded capture date wins untouched.
	for _, key := range []string{"ExifIFD:DateTimeOriginal", "ExifIFD:CreateDate", "XMP-exif:DateTimeOriginal"} {
		if raw, ok := info.Str(key); ok {
			if t, err := time.Parse(exifLayout, raw); err == nil && saneYear(t.Year()) {
				return &DateResult{
					Source:  SrcEmbedded,
					Wall:    t,
					Instant: time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC),
				}, true
			}
		}
	}

	// 2. JSON timestamp.
	if taken != nil {
		instant := time.Unix(*taken, 0).UTC()
		r := &DateResult{Source: SrcJSON}
		if zone != nil {
			r.Wall = instant.In(zone)
			r.Offset = strPtr(FormatOffset(r.Wall))
		} else {
			r.Wall = instant
		}
		r.Instant = instant
		return r, true
	}

	// 3. Filename, last resort.
	if fname != nil {
		if fname.DateOnly {
			return &DateResult{
				Source:   SrcFilename,
				Wall:     fname.Wall,
				Instant:  fname.Wall,
				DateOnly: true,
			}, true
		}
		r := &DateResult{Source: SrcFilename}
		if zone != nil {
			y, mo, d := fname.Wall.Date()
			h, mi, s := fname.Wall.Clock()
			r.Wall = time.Date(y, mo, d, h, mi, s, 0, zone)
			r.Instant = r.Wall.UTC()
			off := FormatOffset(r.Wall)
			r.Offset = &off
		} else {
			r.Wall = fname.Wall
			r.Instant = fname.Wall
		}
		return r, true
	}

	return nil, false
}

// LoadZone parses a --timezone specification: IANA name ("Europe/Berlin"),
// fixed offset ("+01:00", "+0530", "-07"), or empty for nil (UTC digits).
func LoadZone(spec string) (*time.Location, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	if strings.Contains(spec, "/") {
		return time.LoadLocation(spec)
	}
	m := fixedOffsetRe.FindStringSubmatch(spec)
	if m == nil {
		return nil, fmt.Errorf("invalid --timezone %q (want an IANA zone like Europe/Berlin or a fixed offset like +01:00)", spec)
	}
	sign := 1
	if m[1] == "-" {
		sign = -1
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	if h > 23 || mi > 59 {
		return nil, fmt.Errorf("invalid --timezone offset %q", spec)
	}
	name := fmt.Sprintf("UTC%s%02d:%02d", m[1], h, mi)
	return time.FixedZone(name, sign*(h*3600+mi*60)), nil
}

var fixedOffsetRe = regexp.MustCompile(`^([+-])(\d{2})(?::?(\d{2}))?$`)

func strPtr(s string) *string { return &s }

// FormatOffset renders the offset of t as "+01:00".
func FormatOffset(t time.Time) string { return t.Format("-07:00") }

// FileTS renders t for ExifTool FileModifyDate/FileCreateDate arguments.
func FileTS(t time.Time) string { return t.Format(fileLayout) }

// Exif renders t as an EXIF-style datetime string.
func Exif(t time.Time) string { return t.Format(exifLayout) }

// ParseEmbeddedRaw validates an embedded EXIF-style datetime.
func ParseEmbeddedRaw(raw string) (time.Time, bool) {
	t, err := time.Parse(exifLayout, raw)
	if err != nil || !saneYear(t.Year()) {
		return time.Time{}, false
	}
	return t, true
}

// UndatableError is returned when no date source could produce a result.
type UndatableError struct{ Name string }

func (e *UndatableError) Error() string {
	return fmt.Sprintf("no usable capture date found for %s", e.Name)
}
