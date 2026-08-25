package meta

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TimePolicy selects how embedded and JSON capture times interact.
type TimePolicy int

const (
	PolicyPreserveExisting TimePolicy = iota
	PolicyPreferJSON
	PolicyJSONOnly
)

// ParseTimePolicy converts a CLI string into a TimePolicy.
func ParseTimePolicy(s string) (TimePolicy, error) {
	switch s {
	case "preserve-existing":
		return PolicyPreserveExisting, nil
	case "prefer-json":
		return PolicyPreferJSON, nil
	case "json-only":
		return PolicyJSONOnly, nil
	default:
		return 0, fmt.Errorf("invalid --time-policy %q (want preserve-existing, prefer-json or json-only)", s)
	}
}

const (
	exifLayout = "2006:01:02 15:04:05"
	xmpLayout  = "2006-01-02T15:04:05"
	fileLayout = "2006:01:02 15:04:05-07:00"

	minSaneYear       = 1900
	maxSaneYear       = 2100
	maxReconcileDelta = 26 * time.Hour // spans all real-world UTC offsets
)

// Source labels required by the task specification.
const (
	SrcJsonPhotoTaken = "json-photoTakenTime"
	SrcJsonCreation   = "json-creationTime"
	SrcEmbDO          = "embedded-DateTimeOriginal"
	SrcEmbCreateDate  = "embedded-CreateDate"
	SrcEmbVideoDate   = "embedded-video-date"
	SrcFileNameDT     = "filename-date-time"
	SrcFileNameDO     = "filename-date-only"
	SrcFSMtime        = "filesystem-modification-time"
)

// ClockConfig bundles CLI time-related configuration.
type ClockConfig struct {
	Policy                TimePolicy
	ForceJSON             bool
	Timezone              string
	NoFilenameFallback    bool
	AssumeNoonForDateOnly bool
}

// Clock carries the timezone/time-policy configuration.
type Clock struct {
	Policy       TimePolicy
	ForceJSON    bool
	Zone         *time.Location // never nil; UTC when unresolved
	ZoneExplicit bool           // true when the user passed --timezone
	ZoneName     string

	UseFilename           bool // filename parsing enabled (default true)
	AssumeNoonForDateOnly bool
}

var fixedOffsetRe = regexp.MustCompile(`^([+-])(\d{2})(?::?(\d{2}))?$`)

// NewClock validates the configuration (including the --timezone spec, an
// IANA name or ±HH:MM offset) and builds a Clock.
func NewClock(cfg ClockConfig) (*Clock, error) {
	c := &Clock{
		Policy:                cfg.Policy,
		ForceJSON:             cfg.ForceJSON,
		Zone:                  time.UTC,
		UseFilename:           !cfg.NoFilenameFallback,
		AssumeNoonForDateOnly: cfg.AssumeNoonForDateOnly,
	}
	tzSpec := cfg.Timezone
	if tzSpec == "" {
		return c, nil
	}
	if strings.Contains(tzSpec, "/") {
		loc, err := time.LoadLocation(tzSpec)
		if err != nil {
			return nil, fmt.Errorf("invalid --timezone %q: %w", tzSpec, err)
		}
		c.Zone, c.ZoneExplicit, c.ZoneName = loc, true, tzSpec
		return c, nil
	}
	m := fixedOffsetRe.FindStringSubmatch(tzSpec)
	if m == nil {
		return nil, fmt.Errorf("invalid --timezone %q (want an IANA zone like Europe/Berlin or a fixed offset like +01:00)", tzSpec)
	}
	sign := 1
	if m[1] == "-" {
		sign = -1
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	secs := sign * (h*3600 + mi*60)
	if h > 23 || mi > 59 {
		return nil, fmt.Errorf("invalid --timezone offset %q", tzSpec)
	}
	name := fmt.Sprintf("UTC%s%02d:%02d", m[1], h, mi)
	c.Zone, c.ZoneExplicit, c.ZoneName = time.FixedZone(name, secs), true, name
	return c, nil
}

// Embedded holds existing embedded capture-time candidates read from a file.
type Embedded struct {
	PhotoDO      string // EXIF/XMP DateTimeOriginal raw value ("" if absent)
	PhotoDOOff   string // EXIF OffsetTimeOriginal ("+01:00", "" if absent)
	PhotoCD      string // EXIF CreateDate
	PhotoMD      string // EXIF ModifyDate
	XMPDO        string
	XMPCD        string
	VideoCreated []string // MediaCreateDate, TrackCreateDate, CreateDate raw values present
}

// Resolved is the effective capture date after applying the fallback chain.
type Resolved struct {
	Instant     time.Time // absolute instant (UTC); meaningful when HasAbsolute
	Local       time.Time // wall clock in the resolved zone
	Offset      *string   // "+01:00" style, nil when unknown
	Source      string    // one of the Src* constants
	Pattern     string    // matched filename pattern, when source is filename-*
	DateOnly    bool      // filename provided only a date (no time)
	HasAbsolute bool      // true when Instant is a trustworthy absolute instant
	Warnings    []string
}

// ResolveTaken applies the full selection chain:
//
//	preserve-existing: embedded-with-offset > reconciled embedded > JSON > filename > mtime
//	prefer-json / --force-json-time: JSON > embedded > filename > mtime
//	json-only: JSON > mtime
//
// label must be SrcJsonPhotoTaken or SrcJsonCreation depending on which JSON
// field provided the timestamp. fname may be nil.
func (c *Clock) ResolveTaken(taken *int64, label string, emb Embedded, fname *FileNameDate, mtime time.Time) *Resolved {
	var inst *time.Time
	if taken != nil {
		t := time.Unix(*taken, 0).UTC()
		inst = &t
	}

	useJSONFirst := c.Policy == PolicyPreferJSON || c.Policy == PolicyJSONOnly || (c.ForceJSON && inst != nil)

	if useJSONFirst {
		if inst != nil {
			return c.resolveJSON(inst, label)
		}
		if c.Policy == PolicyJSONOnly {
			return c.resolveFS(mtime)
		}
		if r, ok := c.resolveEmbedded(emb, inst); ok {
			return r
		}
		if r := c.resolveFilename(fname); r != nil {
			return r
		}
		return c.resolveFS(mtime)
	}

	if r, ok := c.resolveEmbedded(emb, inst); ok {
		return r
	}
	if inst != nil {
		return c.resolveJSON(inst, label)
	}
	if r := c.resolveFilename(fname); r != nil {
		return r
	}
	return c.resolveFS(mtime)
}

func (c *Clock) resolveJSON(instant *time.Time, label string) *Resolved {
	r := &Resolved{
		Instant:     instant.UTC(),
		Local:       instant.In(c.Zone),
		Source:      label,
		HasAbsolute: true,
	}
	if !c.ZoneExplicit {
		r.Warnings = append(r.Warnings,
			"no timezone could be determined (no embedded offset, no --timezone); writing the UTC clock time instead of local time - pass --timezone (e.g. Europe/Berlin) to fix this")
		return r
	}
	off := FormatOffset(r.Local)
	r.Offset = &off
	return r
}

// resolveFilename applies the filename fallback. A filename provides local
// wall-clock digits only; an offset is attached solely from --timezone.
func (c *Clock) resolveFilename(f *FileNameDate) *Resolved {
	if f == nil || !c.UseFilename {
		return nil
	}

	if f.DateOnly && !c.AssumeNoonForDateOnly {
		// Year/date selection only; never invent a full capture time.
		local := time.Date(f.Wall.Year(), f.Wall.Month(), f.Wall.Day(), 0, 0, 0, 0, c.Zone)
		return &Resolved{
			Local:    local,
			Source:   SrcFileNameDO,
			Pattern:  f.Pattern,
			DateOnly: true,
			Warnings: []string{fmt.Sprintf(
				"filename date %s matched date-only pattern %s; used for year organization only (no time invented)",
				f.Wall.Format("2006-01-02"), f.Pattern)},
		}
	}

	wall := f.Wall
	src := SrcFileNameDT
	if f.DateOnly { // --assume-noon-for-date-only promotes to a full time
		wall = time.Date(f.Wall.Year(), f.Wall.Month(), f.Wall.Day(), 12, 0, 0, 0, time.UTC)
		src = SrcFileNameDO
	}

	r := &Resolved{Source: src, Pattern: f.Pattern}
	y, mo, d := wall.Date()
	h, mi, s := wall.Clock()
	if c.ZoneExplicit {
		r.Local = time.Date(y, mo, d, h, mi, s, 0, c.Zone)
		r.Instant = r.Local.UTC()
		off := FormatOffset(r.Local)
		r.Offset = &off
		r.HasAbsolute = true
	} else {
		// Keep the clock digits without claiming any offset.
		r.Local = time.Date(y, mo, d, h, mi, s, 0, time.UTC)
		r.Instant = r.Local
		r.Warnings = append(r.Warnings,
			fmt.Sprintf("filename time %q has no timezone information and no --timezone was given; writing the clock digits without an offset claim", wall.Format(stampLayout)))
	}
	return r
}

// resolveEmbedded walks the embedded candidates. When jsonInstant is given,
// naive (offset-less) candidates must reconcile with it to be trusted.
func (c *Clock) resolveEmbedded(emb Embedded, jsonInstant *time.Time) (*Resolved, bool) {
	// Photos: DateTimeOriginal with its offset is authoritative.
	type cand struct {
		raw, off, label string
		isVideo         bool
	}
	cands := []cand{}
	add := func(raw, off, label string, isVideo bool) {
		if raw != "" {
			cands = append(cands, cand{raw, off, label, isVideo})
		}
	}
	add(emb.PhotoDO, emb.PhotoDOOff, SrcEmbDO, false)
	add(emb.XMPDO, "", SrcEmbDO, false)
	add(emb.PhotoCD, "", SrcEmbCreateDate, false)
	add(emb.XMPCD, "", SrcEmbCreateDate, false)
	add(emb.PhotoMD, "", SrcEmbCreateDate, false)
	for _, v := range emb.VideoCreated {
		if v != "" {
			add(v, "", SrcEmbVideoDate, true)
			break
		}
	}

	for _, cn := range cands {
		wall, err := time.Parse(exifLayout, cn.raw)
		if err != nil {
			continue
		}
		if wall.Year() < minSaneYear || wall.Year() > maxSaneYear {
			continue
		}

		// With a valid offset: preserve local wall clock + offset as-is.
		if cn.off != "" {
			if off, err := parseOffset(cn.off); err == nil {
				local := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, off)
				return &Resolved{
					Instant:     local.UTC(),
					Local:       local,
					Offset:      strPtr(FormatOffset(local)),
					Source:      cn.label,
					HasAbsolute: true,
				}, true
			}
		}

		// Without offset: reconcile against JSON when available.
		asUTC := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
		if jsonInstant != nil {
			delta := asUTC.Sub(jsonInstant.UTC())
			if delta < 0 {
				delta = -delta
			}
			if delta > maxReconcileDelta {
				continue // corrupt/irrelevant embedded time
			}
		}

		r := &Resolved{Source: cn.label, Instant: asUTC, Local: asUTC, HasAbsolute: true}
		if cn.isVideo {
			// QuickTime atom dates are UTC by container specification; their
			// clock digits must never be reinterpreted through a timezone.
			r.Local = asUTC.In(c.Zone)
			return r, true
		}
		if c.ZoneExplicit {
			r.Local = time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, c.Zone)
			r.Instant = r.Local.UTC()
			off := FormatOffset(r.Local)
			r.Offset = &off
			r.Warnings = append(r.Warnings,
				fmt.Sprintf("existing capture time %q has no offset; resolved with --timezone %s", cn.raw, c.ZoneName))
		} else {
			// Do not invent an offset. Keep the clock digits, interpret as UTC.
			r.Local = asUTC
			r.Instant = asUTC
			r.Warnings = append(r.Warnings,
				fmt.Sprintf("existing capture time %q has no offset and no --timezone was given; keeping the clock digits interpreted as UTC", cn.raw))
		}
		return r, true
	}
	return nil, false
}

func (c *Clock) resolveFS(mtime time.Time) *Resolved {
	r := &Resolved{
		Instant:     mtime.Truncate(time.Second).UTC(),
		Source:      SrcFSMtime,
		HasAbsolute: true,
	}
	r.Local = r.Instant.In(c.Zone)
	if !c.ZoneExplicit {
		r.Warnings = append(r.Warnings,
			"capture date fell back to the filesystem modification time; pass --timezone if the year looks wrong")
	} else {
		off := FormatOffset(r.Local)
		r.Offset = &off
	}
	return r
}

// parseOffset parses "+01:00"/"-0530"/"+01" into a fixed location.
func parseOffset(s string) (*time.Location, error) {
	m := fixedOffsetRe.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("bad offset %q", s)
	}
	sign := 1
	if m[1] == "-" {
		sign = -1
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	return time.FixedZone("", sign*(h*3600+mi*60)), nil
}

func strPtr(s string) *string { return &s }

// --- formatting helpers -----------------------------------------------------

// Exif renders t as an EXIF-style local datetime string.
func Exif(t time.Time) string { return t.Format(exifLayout) }

// XMP renders t as an XMP ISO datetime, appending the zone offset when known.
func XMP(t time.Time, offset *string) string {
	s := t.Format(xmpLayout)
	if offset == nil {
		return s
	}
	return s + *offset
}

// FileTS renders t for ExifTool FileModifyDate/FileCreateDate arguments.
func FileTS(t time.Time) string { return t.Format(fileLayout) }

// GPSDate renders the UTC date part for GPSDateStamp.
func GPSDate(instant time.Time) string { return instant.UTC().Format("2006:01:02") }

// GPSTime renders the UTC time part for GPSTimeStamp.
func GPSTime(instant time.Time) string { return instant.UTC().Format("15:04:05") }

// FormatOffset renders the zone offset of t as "+01:00".
func FormatOffset(t time.Time) string { return t.Format("-07:00") }

// ParseEmbeddedRaw parses a raw EXIF-style datetime string with sanity checks.
func ParseEmbeddedRaw(raw string) (time.Time, bool) {
	t, err := time.Parse(exifLayout, raw)
	if err != nil || t.Year() < minSaneYear || t.Year() > maxSaneYear {
		return time.Time{}, false
	}
	return t, true
}
