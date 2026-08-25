package takeout

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// numberSuffixRe matches duplicate-download suffixes like "(1)".
var numberSuffixRe = regexp.MustCompile(`\(\d+\)`)

// genericSidecarNames are album-level or export-level JSON files that must
// never be matched to media, modified, or deleted.
var genericSidecarNames = map[string]struct{}{
	"metadata.json":              {},
	"metadaten.json":             {},
	"album.json":                 {},
	"shared_album_comments.json": {},
}

// junkNames are operating-system artifacts ignored during classification.
var junkNames = map[string]struct{}{
	"thumbs.db":   {},
	"desktop.ini": {},
	".ds_store":   {},
}

// supplementalSuffixes are the known truncations of ".supplemental-metadata",
// longest first, per the task specification priority order.
var supplementalSuffixes = []string{
	"-metadata",
	"-metadat",
	"-metada",
	"-metad",
	"-meta",
	"-met",
	"",
}

// plausibleMediaExts are extensions accepted by the reverse-renamed rule.
var plausibleMediaExts = map[string]struct{}{
	"jpg": {}, "jpeg": {}, "png": {}, "gif": {}, "webp": {}, "heic": {}, "heif": {},
	"avif": {}, "bmp": {}, "tif": {}, "tiff": {}, "dng": {}, "cr2": {}, "nef": {},
	"arw": {}, "raf": {}, "orf": {}, "rw2": {},
	"mp4": {}, "mov": {}, "m4v": {}, "3gp": {}, "3g2": {}, "avi": {}, "mkv": {},
	"webm": {}, "mpg": {}, "mpeg": {}, "ts": {}, "mts": {}, "m2ts": {}, "wmv": {}, "flv": {},
}

// Match is the result of a sidecar lookup for one media file.
type Match struct {
	FileName string // sidecar filename, empty when nothing matched
	Exact    bool   // true for exact patterns (priorities 1-9)
	Rule     string // human-readable rule that produced the match
	Cands    []string
}

// Found reports whether a sidecar was matched.
func (m Match) Found() bool { return m.FileName != "" }

// Matcher associates media files with their JSON sidecars inside one directory.
// It tracks claims so two media files never silently share a sidecar.
type Matcher struct {
	files   []string          // all entries in the directory (filenames only)
	byLower map[string]string // lower(name) -> actual name
	claimed map[string]string // lower(sidecar) -> media that claimed it
}

// NewMatcher indexes a directory listing for sidecar lookups.
func NewMatcher(entries []os.DirEntry) *Matcher {
	m := &Matcher{
		byLower: make(map[string]string, len(entries)),
		claimed: make(map[string]string),
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		m.files = append(m.files, name)
		lower := strings.ToLower(name)
		if _, exists := m.byLower[lower]; !exists {
			m.byLower[lower] = name
		}
	}
	sort.Strings(m.files)
	return m
}

// Classify splits a directory listing into media candidates and JSON files.
// XMP sidecars, junk files, dotfiles and directories are excluded; media
// detection is otherwise left to ExifTool at processing time (no allowlist).
type Classified struct {
	Media []string
	Jsons []string
}

func (m *Matcher) Classify() Classified {
	var c Classified
	for _, name := range m.files {
		lower := strings.ToLower(name)
		ext := strings.TrimPrefix(filepath.Ext(lower), ".")
		if strings.HasPrefix(lower, ".") {
			continue
		}
		if _, isJunk := junkNames[lower]; isJunk {
			continue
		}
		switch ext {
		case "json":
			c.Jsons = append(c.Jsons, name)
		case "xmp":
			continue
		default:
			c.Media = append(c.Media, name)
		}
	}
	return c
}

// Match finds the best sidecar for mediaName following the documented
// priority order. Non-exact selections are reported via Exact=false.
func (m *Matcher) Match(mediaName string) Match {
	var found []string

	pick := func(candidate string, exact bool, rule string) (Match, bool) {
		lower := strings.ToLower(candidate)
		actual, ok := m.byLower[lower]
		if !ok {
			return Match{}, false
		}
		if isGenericSidecar(actual) {
			return Match{}, false
		}
		if owner, claimed := m.claimed[lower]; claimed && owner != mediaName {
			return Match{}, false
		}
		found = append(found, actual)
		res := Match{FileName: actual, Exact: exact, Rule: rule, Cands: found}
		m.claimed[lower] = mediaName
		return res, true
	}

	// Priorities 1-7: exact known supplemental suffixes.
	for _, suf := range supplementalSuffixes {
		label := "supplemental-metadata"
		if suf != "-metadata" && suf != "" {
			label = "supplemental-metadata (exact truncated suffix)"
		} else if suf == "" {
			label = "supplemental"
		}
		if res, ok := pick(mediaName+".supplemental"+suf+".json", true, label); ok {
			return res
		}
	}

	mediaExt := filepath.Ext(mediaName)
	baseName := strings.TrimSuffix(mediaName, mediaExt)

	// Priority 8: legacy full-name .json.
	if res, ok := pick(mediaName+".json", true, "legacy <media>.json"); ok {
		return res
	}
	// Priority 9: legacy basename .json.
	if baseName != "" {
		if res, ok := pick(baseName+".json", true, "legacy <basename>.json"); ok {
			return res
		}
	}

	// Priority 10: generic truncated supplemental rule.
	for _, f := range m.files {
		if isTruncatedSupplemental(mediaName, f) {
			if res, ok := pick(f, false, "generic truncated supplemental"); ok {
				return res
			}
		}
	}

	// Priority 11: legacy heuristics.
	// 11a: -edited stripped names.
	if idx := strings.Index(strings.ToLower(mediaName), "-edited"); idx > 0 {
		stripped := mediaName[:idx] + mediaName[idx+len("-edited"):]
		if res, ok := m.matchLegacy(mediaName, stripped); ok {
			return res
		}
	}
	// 11b: mp4 files with photo-style sidecar names.
	if strings.EqualFold(mediaExt, ".mp4") {
		for _, ext := range []string{".jpg", ".jpeg", ".heic"} {
			for _, variant := range []string{ext, strings.ToUpper(ext)} {
				if res, ok := pick(baseName+variant+".json", false, "legacy mp4 photo-sidecar name"); ok {
					return res
				}
			}
		}
	}
	// 11c: duplicate-numbering moves like IMG(1).jpg -> IMG.jpg(1).json.
	if match := numberSuffixRe.FindString(mediaName); match != "" {
		moved := strings.Replace(mediaName, match, "", 1) + match + ".json"
		if res, ok := pick(moved, false, "legacy duplicate-numbering move"); ok {
			return res
		}
	}
	// 11d: long-name 46-char truncation.
	if len(mediaName) > 46 {
		if res, ok := pick(mediaName[:46]+".json", false, "legacy 46-char truncation"); ok {
			return res
		}
		if match := numberSuffixRe.FindString(mediaName); match != "" {
			trunc := mediaName[:46-len(match)] + match + ".json"
			if res, ok := pick(trunc, false, "legacy 46-char truncation with numbering"); ok {
				return res
			}
		}
	}

	// Priority 12: reverse-renamed rule (sidecar of pre-extension-fix media).
	if res, ok := m.matchReverseRenamed(mediaName, &found); ok {
		return res
	}

	return Match{Cands: found}
}

// matchLegacy applies the plain legacy patterns to an alternative media name.
func (m *Matcher) matchLegacy(mediaName, altName string) (Match, bool) {
	if res, ok := m.tryPick(mediaName, altName+".json", false, "legacy -edited name"); ok {
		return res, true
	}
	base := strings.TrimSuffix(altName, filepath.Ext(altName))
	if base != "" {
		if res, ok := m.tryPick(mediaName, base+".json", false, "legacy -edited name"); ok {
			return res, true
		}
	}
	return Match{}, false
}

func (m *Matcher) tryPick(mediaName, candidate string, exact bool, rule string) (Match, bool) {
	lower := strings.ToLower(candidate)
	actual, ok := m.byLower[lower]
	if !ok || isGenericSidecar(actual) {
		return Match{}, false
	}
	if owner, claimed := m.claimed[lower]; claimed && owner != mediaName {
		return Match{}, false
	}
	m.claimed[lower] = mediaName
	return Match{FileName: actual, Exact: exact, Rule: rule}, true
}

func (m *Matcher) matchReverseRenamed(mediaName string, found *[]string) (Match, bool) {
	const marker = ".supplemental-metadata.json"
	mediaExt := filepath.Ext(mediaName)
	mediaBase := strings.TrimSuffix(mediaName, mediaExt)
	if mediaBase == "" {
		return Match{}, false
	}
	var best []string
	for _, f := range m.files {
		lower := strings.ToLower(f)
		if !strings.HasSuffix(lower, marker) {
			continue
		}
		stem := f[:len(f)-len(marker)]
		dot := strings.LastIndex(stem, ".")
		if dot <= 0 {
			continue
		}
		stemBase, stemExt := stem[:dot], stem[dot+1:]
		if !strings.EqualFold(stemBase, mediaBase) {
			continue
		}
		if strings.EqualFold(stemExt, strings.TrimPrefix(mediaExt, ".")) {
			continue
		}
		if _, plausible := plausibleMediaExts[strings.ToLower(stemExt)]; !plausible {
			continue
		}
		best = append(best, f)
	}
	if len(best) == 0 {
		return Match{}, false
	}
	sidecar := best[0]
	lower := strings.ToLower(sidecar)
	if owner, claimed := m.claimed[lower]; claimed {
		if owner != mediaName {
			return Match{}, false
		}
	}
	*found = append(*found, sidecar)
	m.claimed[lower] = mediaName
	return Match{FileName: sidecar, Exact: false, Rule: "reverse-renamed (extension was fixed)", Cands: *found}, true
}

// Unclaimed returns JSON filenames that no media file claimed, excluding
// generic album-level documents.
func (m *Matcher) Unclaimed() []string {
	var out []string
	for _, name := range m.files {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		if ext != "json" || isGenericSidecar(name) {
			continue
		}
		if _, ok := m.claimed[strings.ToLower(name)]; !ok {
			out = append(out, name)
		}
	}
	return out
}

// IsGenericSidecar reports whether name is an album/export-level document.
func IsGenericSidecar(name string) bool { return isGenericSidecar(name) }

func isGenericSidecar(name string) bool {
	_, ok := genericSidecarNames[strings.ToLower(name)]
	return ok
}

// isTruncatedSupplemental implements the generic truncated-sidecar rule:
// starts with "<media>.supplemental", ends with ".json", and the middle part
// is a prefix-truncation of "-metadata". Google truncates only lowercase
// suffixes, so the middle must be lowercase ASCII in the ORIGINAL name;
// prefix and suffix match case-insensitively like everything else.
func isTruncatedSupplemental(mediaName, candidate string) bool {
	lower := strings.ToLower(candidate)
	pre := strings.ToLower(mediaName) + ".supplemental"
	if !strings.HasPrefix(lower, pre) || !strings.HasSuffix(lower, ".json") {
		return false
	}
	if len(candidate) == len(pre)+len(".json") {
		return true // exactly "<media>.supplemental.json"
	}
	mid := candidate[len(pre) : len(candidate)-len(".json")]
	if len(mid) == 0 || mid[0] != '-' {
		return false
	}
	for i := 1; i < len(mid); i++ {
		if mid[i] < 'a' || mid[i] > 'z' {
			return false
		}
	}
	return len(mid)-1 <= len("-metadata")
}

// Describe renders a short description of a match for logging.
func (m Match) Describe() string {
	if !m.Found() {
		return "no sidecar"
	}
	tag := "exact"
	if !m.Exact {
		tag = "non-exact"
	}
	return fmt.Sprintf("%s (%s, %s)", m.FileName, m.Rule, tag)
}
