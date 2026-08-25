package meta

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sanity bounds shared by filename and metadata date validation.
const (
	minSaneYear = 1900
	maxSaneYear = 2100
)

// FileNameDate is a validated capture date parsed from a media filename.
type FileNameDate struct {
	Wall     time.Time // wall-clock digits as written (UTC-located until resolved)
	DateOnly bool      // true when only a date (no time) was found
	Pattern  string    // exact pattern label for reporting
}

// Filename patterns, in evaluation order. Full timestamps always win over
// date-only values. All patterns validate through time.Parse so impossible
// calendar values (month 13, day 32, hour 25, non-leap Feb 29) are rejected.
var (
	fnIMG   = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])(?:IMG|IMAGE)[-_](\d{8})_(\d{6})(?:\(\d+\)|[-_]\d+)*$`)
	fnVID   = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])VID[_-](\d{8})_(\d{6})(?:\(\d+\)|[-_]\d+)*$`)
	fnPXL   = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])PXL[_-](\d{8})_(\d{6})\d*(?:\(\d+\)|[-_]\d+)*$`)
	fnBare  = regexp.MustCompile(`(\d{8})_(\d{6})(?:\(\d+\)|[-_]\d+)?`)
	fnWA    = regexp.MustCompile(`(?i)(\d{4})-(\d{2})-(\d{2})_at_(\d{2})\.(\d{2})\.(\d{2})(?:\(\d+\))?`)
	fnDash  = regexp.MustCompile(`(?:^|[^0-9])(\d{4})-(\d{2})-(\d{2})(?:[^0-9]|$)`)
	fnEight = regexp.MustCompile(`(?:^|[^0-9])(\d{8})(?:[^0-9]|$)`)
)

const (
	patIMG       = "IMG_YYYYMMDD_HHMMSS"
	patVID       = "VID_YYYYMMDD_HHMMSS"
	patPXL       = "PXL_YYYYMMDD_HHMMSS"
	patBare      = "YYYYMMDD_HHMMSS"
	patWhatsApp  = "YYYY-MM-DD_at_HH.MM.SS"
	patDateDash  = "YYYY-MM-DD"
	patDateEight = "YYYYMMDD"
)

type fullMatch struct {
	wall    time.Time
	pattern string
}

type dateOnlyMatch struct {
	t       time.Time
	pattern string
}

// ParseFileName extracts the effective capture date from a media filename.
// It returns nil when nothing valid is found. When two *different* valid
// timestamps occur in one filename it returns an ambiguity error instead of
// guessing. The file extension is never parsed.
func ParseFileName(name string) (*FileNameDate, error) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))

	var fulls []fullMatch
	var dates []dateOnlyMatch
	structurallyInvalid := false

	// noteInvalid marks names whose digits LOOK like a timestamp but fail
	// validation; such filenames must be rejected outright instead of
	// scavenging a partial date from them.
	noteInvalid := func() { structurallyInvalid = true }

	for _, m := range fnIMG.FindAllStringSubmatch(base, -1) {
		if t, ok := parseFull(m[1], m[2]); ok {
			fulls = append(fulls, fullMatch{t, patIMG})
		} else {
			noteInvalid()
		}
	}
	for _, m := range fnVID.FindAllStringSubmatch(base, -1) {
		if t, ok := parseFull(m[1], m[2]); ok {
			fulls = append(fulls, fullMatch{t, patVID})
		} else {
			noteInvalid()
		}
	}
	for _, m := range fnPXL.FindAllStringSubmatch(base, -1) {
		if t, ok := parseFull(m[1], m[2]); ok { // extra digits after HHMMSS ignored
			fulls = append(fulls, fullMatch{t, patPXL})
		} else {
			noteInvalid()
		}
	}
	for _, m := range fnBare.FindAllStringSubmatch(base, -1) {
		if t, ok := parseFull(m[1], m[2]); ok {
			fulls = append(fulls, fullMatch{t, patBare})
		} else {
			noteInvalid()
		}
	}
	for _, m := range fnWA.FindAllStringSubmatch(base, -1) {
		raw := m[1] + "-" + m[2] + "-" + m[3] + "_" + m[4] + "." + m[5] + "." + m[6]
		t, err := time.Parse("2006-01-02_15.04.05", raw)
		if err == nil && saneYear(t.Year()) {
			fulls = append(fulls, fullMatch{t, patWhatsApp})
		}
	}

	bannedDatePrefixes := map[string]bool{}
	for _, f := range fulls {
		bannedDatePrefixes[f.wall.Format("20060102")] = true
	}
	for _, m := range fnDash.FindAllStringSubmatch(base, -1) {
		addDateOnly(&dates, m[1]+m[2]+m[3], patDateDash)
	}
	for _, m := range fnEight.FindAllStringSubmatch(base, -1) {
		s := m[1]
		// Skip 8-digit groups that are the first half of a full timestamp.
		if bannedDatePrefixes[s] && fullTailRe(s).MatchString(base) {
			continue
		}
		addDateOnly(&dates, s, patDateEight)
	}

	distinctFulls := distinctWalls(len(fulls), func(i int) time.Time { return fulls[i].wall })
	if len(fulls) > 0 {
		if distinctFulls > 1 {
			return nil, fmt.Errorf("multiple different timestamps in filename (%s and %s); not guessing",
				fulls[0].wall.Format(stampLayout), fulls[len(fulls)-1].wall.Format(stampLayout))
		}
		return &FileNameDate{Wall: fulls[0].wall, Pattern: pickPattern(fulls)}, nil
	}

	distinctDates := distinctWalls(len(dates), func(i int) time.Time { return dates[i].t })
	if len(dates) > 0 {
		if distinctDates > 1 {
			return nil, fmt.Errorf("multiple different dates in filename; not guessing")
		}
		if structurallyInvalid {
			// The date came from a timestamp-shaped name that failed time
			// validation; do not return a partial result.
			return nil, fmt.Errorf("filename contains an invalid timestamp")
		}
		return &FileNameDate{Wall: dates[0].t, DateOnly: true, Pattern: dates[0].pattern}, nil
	}
	if structurallyInvalid {
		return nil, fmt.Errorf("filename contains an invalid timestamp")
	}
	return nil, nil
}

const stampLayout = "2006-01-02 15:04:05"

func pickPattern(fulls []fullMatch) string {
	for _, p := range []string{patIMG, patVID, patPXL, patWhatsApp} {
		for _, f := range fulls {
			if f.pattern == p {
				return p
			}
		}
	}
	return patBare
}

func parseFull(d8, h6 string) (time.Time, bool) {
	t, err := time.Parse("20060102_150405", d8+"_"+h6)
	if err != nil || !saneYear(t.Year()) {
		return time.Time{}, false
	}
	return t, true
}

func addDateOnly(dates *[]dateOnlyMatch, d8, pattern string) {
	t, err := time.Parse("20060102", d8)
	if err != nil || !saneYear(t.Year()) {
		return
	}
	*dates = append(*dates, dateOnlyMatch{t, pattern})
}

// fullTailRe builds a matcher for "<date>_<6 digits>" occurrences.
// Worker goroutines call ParseFileName concurrently - the cache needs the
// mutex (unsynchronized writes here crashed with a runtime fatal).
var (
	fullTailMu    sync.Mutex
	fullTailCache = map[string]*regexp.Regexp{}
)

func fullTailRe(date string) *regexp.Regexp {
	fullTailMu.Lock()
	defer fullTailMu.Unlock()
	if re, ok := fullTailCache[date]; ok {
		return re
	}
	re := regexp.MustCompile(regexp.QuoteMeta(date) + `_\d{6}`)
	fullTailCache[date] = re
	return re
}

func saneYear(y int) bool { return y >= minSaneYear && y <= maxSaneYear }

func distinctWalls(n int, at func(int) time.Time) int {
	seen := map[int64]struct{}{}
	for i := 0; i < n; i++ {
		seen[at(i).Unix()] = struct{}{}
	}
	return len(seen)
}

// FilenameResult carries what the resolver needs from a filename probe.
type FilenameResult = FileNameDate
