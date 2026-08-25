package meta

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alexdachin/gophix/takeout"
)

// Options controls dry-run/verbose behavior for metadata operations.
type Options struct {
	DryRun  bool
	Verbose bool
	Out     io.Writer // target for verbose diagnostics; defaults to os.Stdout
}

func (o Options) writer() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stdout
}

// GPS is the effective, validated location to write.
type GPS struct {
	Lat, Lon, Alt float64
	HasAlt        bool
}

// Plan describes everything needed to bring one media file to its desired
// metadata state.
type Plan struct {
	MediaPath string
	Sidecar   *takeout.Sidecar
	Clock     *Clock
	Resolved  *Resolved // nil when no capture date could be determined

	WriteDesc bool // description non-empty in JSON
	Desc      string
	Title     string // written only when non-empty (maps to XMP-dc:Title)
	GPSVal    *GPS   // nil when JSON has no usable location

	filenamePattern string

	isVideo     bool
	args        []string           // embedded-write exiftool arguments
	xmpArgs     []string           // XMP-sidecar write arguments (-o target)
	expect      map[string]string  // exact string expectations after write
	expectF     map[string]float64 // numeric expectations after write (epsilon)
	fsModWant   string             // FileModifyDate value we want on the media
	wantFS      bool               // true when Resolved != nil and full time applies
	fsModInMain bool               // FileModifyDate already part of the main write args
}

// FilenamePattern returns the matched filename pattern when the effective
// date came from the filename fallback.
func (p *Plan) FilenamePattern() string { return p.filenamePattern }

// ExpectDebug exposes expectations for debugging/tests.
func (p *Plan) ExpectDebug() map[string]string { return p.expect }

// ExpectFDebug exposes numeric expectations for debugging/tests.
func (p *Plan) ExpectFDebug() map[string]float64 { return p.expectF }

// ResolvedDebug exposes the resolved capture date for debugging/tests.
func (p *Plan) ResolvedDebug() *Resolved { return p.Resolved }

// Outcome classifies what Apply did with a file.
type Outcome int

const (
	OutcomeAlreadyCorrect Outcome = iota
	OutcomeUpdated
	OutcomeSidecar
	OutcomeDryRunPlanned
	OutcomeFailed
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAlreadyCorrect:
		return "already correct"
	case OutcomeUpdated:
		return "updated directly"
	case OutcomeSidecar:
		return "written to XMP sidecar"
	case OutcomeDryRunPlanned:
		return "planned (dry-run)"
	default:
		return "failed"
	}
}

const gpsEpsilon = 1e-6

// BuildPlan computes the desired metadata state of one file from its sidecar,
// current metadata and clock configuration.
func BuildPlan(mediaPath string, info Info, sc *takeout.Sidecar, clock *Clock, opts Options) (*Plan, error) {
	p := &Plan{
		MediaPath: mediaPath,
		Sidecar:   sc,
		Clock:     clock,
		isVideo:   info.IsVideo(),
		expect:    make(map[string]string),
		expectF:   make(map[string]float64),
	}

	if sc != nil {
		p.Desc = sc.Description
		p.WriteDesc = strings.TrimSpace(sc.Description) != ""
		p.Title = sc.Title
		if g := sc.EffectiveGeo(); g != nil {
			hasAlt := g.HasAltitude()
			p.GPSVal = &GPS{Lat: g.Lat, Lon: g.Lon, Alt: g.Alt, HasAlt: hasAlt}
		}
	}

	// Effective capture date via the full fallback chain.
	var taken *int64
	jsonLabel := ""
	if sc != nil {
		if sc.PhotoTaken != nil {
			taken, jsonLabel = sc.PhotoTaken, SrcJsonPhotoTaken
		} else if sc.Creation != nil {
			taken, jsonLabel = sc.Creation, SrcJsonCreation
		}
	}
	emb := EmbeddedFromInfo(info)
	stat, statErr := os.Stat(mediaPath)
	var mtime time.Time
	if statErr == nil {
		mtime = stat.ModTime()
	} else {
		mtime = time.Unix(0, 0)
	}

	var fname *FileNameDate
	if clock.UseFilename {
		f, ferr := ParseFileName(mediaPath)
		if ferr != nil {
			if opts.Verbose {
				fmt.Fprintf(opts.writer(), "   ⚠️  %s: %v\n", filepath.Base(mediaPath), ferr)
			}
		}
		fname = f
	}
	p.Resolved = clock.ResolveTaken(taken, jsonLabel, emb, fname, mtime)
	if p.Resolved == nil {
		return nil, fmt.Errorf("no usable capture date could be determined")
	}
	if fname != nil && p.Resolved.Source == SrcFileNameDT || fname != nil && p.Resolved.Source == SrcFileNameDO {
		p.filenamePattern = fname.Pattern
	}

	p.buildArgs()
	p.buildExpectations()
	return p, nil
}

// Warnings returns the timezone-related warnings of the resolved date.
func (p *Plan) Warnings() []string {
	if p.Resolved == nil {
		return nil
	}
	return p.Resolved.Warnings
}

// SourceLabel returns the verbose-mode label of the selected date source.
func (p *Plan) SourceLabel() string {
	if p.Resolved == nil {
		return ""
	}
	return p.Resolved.Source
}

func EmbeddedFromInfo(info Info) Embedded {
	e := Embedded{}
	e.PhotoDO, _ = info.Str("ExifIFD:DateTimeOriginal")
	e.PhotoDOOff, _ = info.Str("ExifIFD:OffsetTimeOriginal")
	e.XMPDO, _ = info.Str("XMP-exif:DateTimeOriginal")
	e.PhotoCD, _ = info.Str("ExifIFD:CreateDate")
	e.XMPCD, _ = info.Str("XMP-xmp:CreateDate")
	e.PhotoMD, _ = info.Str("IFD0:ModifyDate")
	for _, k := range []string{"QuickTime:CreateDate", "Track1:MediaCreateDate", "Track1:TrackCreateDate"} {
		if v, ok := info.Str(k); ok {
			e.VideoCreated = append(e.VideoCreated, v)
		}
	}
	return e
}

func (p *Plan) buildArgs() {
	r := p.Resolved
	a := []string{"-m", "-overwrite_original"}

	// Date-only filename results never populate full capture times; with
	// --assume-noon-for-date-only Resolved.DateOnly is already false.
	fullTime := r != nil && !r.DateOnly

	if fullTime && !p.isVideo {
		local := Exif(r.Local)
		a = append(a,
			"-DateTimeOriginal="+local,
			"-CreateDate="+local,
			"-ModifyDate="+local,
		)
		if r.Offset != nil {
			a = append(a,
				"-OffsetTimeOriginal="+*r.Offset,
				"-OffsetTimeDigitized="+*r.Offset,
				"-OffsetTime="+*r.Offset,
			)
		}
	} else if fullTime && p.isVideo {
		// QuickTime atom dates are written verbatim by ExifTool (no
		// timezone conversion); they are UTC per container specification.
		utc := Exif(r.Instant)
		a = append(a,
			"-QuickTime:CreateDate="+utc,
			"-QuickTime:ModifyDate="+utc,
		)
		for _, track := range []string{"Track1", "Track2"} {
			a = append(a,
				"-"+track+":MediaCreateDate="+utc,
				"-"+track+":MediaModifyDate="+utc,
				"-"+track+":TrackCreateDate="+utc,
				"-"+track+":TrackModifyDate="+utc,
			)
		}
	}

	if p.WriteDesc {
		a = append(a,
			"-EXIF:ImageDescription="+p.Desc,
			"-XMP-dc:Description="+p.Desc,
		)
		if !p.isVideo {
			a = append(a, "-IPTC:Caption-Abstract="+p.Desc)
		}
	}
	if p.Title != "" {
		a = append(a, "-XMP-dc:Title="+p.Title)
	}
	if p.GPSVal != nil {
		g := p.GPSVal
		latRef, lonRef := "N", "E"
		lat, lon := g.Lat, g.Lon
		if lat < 0 {
			latRef, lat = "S", -lat
		}
		if lon < 0 {
			lonRef, lon = "W", -lon
		}
		a = append(a,
			fmt.Sprintf("-GPSLatitude=%.6f", lat),
			"-GPSLatitudeRef="+latRef,
			fmt.Sprintf("-GPSLongitude=%.6f", lon),
			"-GPSLongitudeRef="+lonRef,
		)
		if g.HasAlt {
			alt, altRef := g.Alt, "0"
			if alt < 0 {
				alt, altRef = -alt, "1"
			}
			a = append(a, fmt.Sprintf("-GPSAltitude=%.3f", alt), "-GPSAltitudeRef="+altRef)
		}
		if fullTime && r.HasAbsolute {
			a = append(a,
				"-GPSDateStamp="+GPSDate(r.Instant),
				"-GPSTimeStamp="+GPSTime(r.Instant),
			)
		}
	}
	p.args = a

	// XMP sidecar variant.
	xa := []string{}
	if fullTime {
		xa = append(xa,
			"-XMP-exif:DateTimeOriginal="+XMP(r.Local, r.Offset),
			"-XMP-xmp:CreateDate="+XMP(r.Local, r.Offset),
		)
	}
	if p.WriteDesc {
		xa = append(xa, "-XMP-dc:Description="+p.Desc)
	}
	if p.Title != "" {
		xa = append(xa, "-XMP-dc:Title="+p.Title)
	}
	if p.GPSVal != nil {
		g := p.GPSVal
		xa = append(xa,
			fmt.Sprintf("-XMP-exif:GPSLatitude=%+.6f", g.Lat),
			fmt.Sprintf("-XMP-exif:GPSLongitude=%+.6f", g.Lon),
		)
		if g.HasAlt {
			xa = append(xa, fmt.Sprintf("-XMP-exif:GPSAltitude=%.3f", g.Alt))
		}
		if fullTime && r.HasAbsolute {
			xa = append(xa, "-XMP-exif:GPSDateTime="+XMPUTC(r.Instant))
		}
	}
	p.xmpArgs = xa

	if fullTime {
		p.fsModWant = FileTS(r.Local)
		p.wantFS = true
		// Synchronize FileModifyDate within the same ExifTool invocation -
		// one process less per updated file.
		p.args = append(p.args, "-FileModifyDate="+p.fsModWant)
		p.fsModInMain = true
	}
}

// XMPUTC renders an instant as XMP datetime with Z (GPS semantics).
func XMPUTC(instant time.Time) string { return instant.UTC().Format("2006-01-02T15:04:05") + "Z" }

// xmpReadExpect returns how exiftool displays an XMP datetime on read-back.
func xmpReadExpect(r *Resolved) string {
	s := Exif(r.Local)
	if r.Offset != nil {
		s += *r.Offset
	}
	return s
}

func (p *Plan) buildExpectations() {
	r := p.Resolved
	fullTime := r != nil && !r.DateOnly
	if fullTime && p.isVideo {
		utc := Exif(r.Instant)
		for _, k := range []string{
			"QuickTime:CreateDate", "QuickTime:ModifyDate",
			"Track1:MediaCreateDate", "Track1:MediaModifyDate",
			"Track1:TrackCreateDate", "Track1:TrackModifyDate",
		} {
			p.expect[k] = utc
		}
	} else if fullTime {
		local := Exif(r.Local)
		p.expect["ExifIFD:DateTimeOriginal"] = local
		p.expect["ExifIFD:CreateDate"] = local
		p.expect["IFD0:ModifyDate"] = local
		if r.Offset != nil {
			p.expect["ExifIFD:OffsetTimeOriginal"] = *r.Offset
		}
	}
	if p.WriteDesc {
		// Read-back group spelling: with -G1 ExifTool reports the tag under
		// IFD0 even though it is written via the -EXIF: alias.
		p.expect["IFD0:ImageDescription"] = p.Desc
		p.expect["XMP-dc:Description"] = p.Desc
		// IPTC Caption-Abstract verified leniently (only when present).
		p.expect["IPTC:Caption-Abstract(optional)"] = p.Desc
	}
	if p.Title != "" {
		p.expect["XMP-dc:Title"] = p.Title
	}
	if p.GPSVal != nil {
		g := p.GPSVal
		p.expectF["GPS:GPSLatitude"] = math.Abs(g.Lat)
		p.expectF["GPS:GPSLongitude"] = math.Abs(g.Lon)
		p.expect["GPS:GPSLatitudeRef"] = refN(g.Lat)
		p.expect["GPS:GPSLongitudeRef"] = refE(g.Lon)
		if g.HasAlt {
			p.expectF["GPS:GPSAltitude"] = math.Abs(g.Alt)
		}
		if fullTime && r.HasAbsolute {
			p.expect["GPS:GPSDateStamp"] = GPSDate(r.Instant)
			p.expect["GPS:GPSTimeStamp"] = GPSTime(r.Instant)
		}
	}
}

func refN(lat float64) string {
	if lat < 0 {
		return "S"
	}
	return "N"
}

func refE(lon float64) string {
	if lon < 0 {
		return "W"
	}
	return "E"
}

// MetaSatisfied reports whether the current metadata already equals the plan
// (filesystem timestamps excluded).
func (p *Plan) MetaSatisfied(cur Info) bool {
	return len(p.MetaMismatches(cur)) == 0
}

// MetaMismatches lists every expectation that does not match cur.
func (p *Plan) MetaMismatches(cur Info) []string {
	var bad []string
	for k, want := range p.expect {
		optional := strings.HasSuffix(k, "(optional)")
		key := strings.TrimSuffix(k, "(optional)")
		got, exists := cur[key]
		if optional && !exists {
			continue
		}
		if !exists || got != want {
			bad = append(bad, fmt.Sprintf("%s=%q want %q", key, got, want))
		}
	}
	for k, want := range p.expectF {
		got, ok := cur.Float(k)
		if !ok || math.Abs(got-want) > gpsEpsilon {
			bad = append(bad, fmt.Sprintf("%s=%v want %v", k, got, want))
		}
	}
	return bad
}

// FSSatisfied reports whether the filesystem modification time already equals
// the planned instant. FileAccessDate is never read or written.
func (p *Plan) FSSatisfied(cur Info) bool {
	if !p.wantFS {
		return true
	}
	got, ok := cur.Str("System:FileModifyDate")
	if !ok {
		return false
	}
	t, err := time.Parse(fileLayout, got)
	if err != nil {
		return false
	}
	return t.Equal(p.Resolved.Local.Truncate(time.Second)) ||
		t.UTC().Equal(p.Resolved.Instant)
}

// FSResult reports what the filesystem timestamp synchronization did.
type FSResult struct {
	ModSet      bool   // FileModifyDate written
	CreateState string // "set", "unsupported", "failed", "skipped"
}

// Apply brings the media file to the planned state and reports what the
// filesystem timestamp synchronization did. cur is the file's metadata as
// previously read by Read/ReadMany (e.g. during planning); it is re-read only
// when nil or after a successful write for verification.
func Apply(p *Plan, cur Info, opts Options) (Outcome, FSResult, error) {
	if cur == nil {
		var err error
		if cur, err = Read(p.MediaPath); err != nil {
			return OutcomeFailed, FSResult{CreateState: "skipped"}, err
		}
	}

	metaOK := p.MetaSatisfied(cur)
	fsOK := p.FSSatisfied(cur)
	if metaOK && fsOK {
		return OutcomeAlreadyCorrect, FSResult{CreateState: "skipped"}, nil
	}

	if opts.DryRun {
		return OutcomeDryRunPlanned, FSResult{CreateState: "skipped"}, nil
	}

	// mainWriteRan tracks whether the embedded-metadata write (which carries
	// -FileModifyDate when fullTime) actually executed and succeeded.
	mainWriteRan := false
	fail := func(format string, args ...any) (Outcome, FSResult, error) {
		return OutcomeFailed, p.applyFS(opts, false), fmt.Errorf(format, args...)
	}

	if !metaOK {
		out, werr := Exec(append(p.args, p.MediaPath))
		if werr != nil {
			if opts.Verbose {
				fmt.Fprintf(opts.writer(), "   exiftool output:\n%s\n", string(out))
			}
			// Fall back to an XMP sidecar based on the ExifTool outcome.
			ok, xerr := p.writeXMPSidecar(opts)
			fsRes := p.applyFS(opts, false)
			if !ok {
				if xerr != nil {
					return fail("%v; XMP fallback failed: %v", werr, xerr)
				}
				return fail("%v; XMP verification failed", werr)
			}
			return OutcomeSidecar, fsRes, nil
		}
		mainWriteRan = true

		after, rerr := Read(p.MediaPath)
		if rerr != nil {
			return OutcomeFailed, FSResult{CreateState: "skipped"}, rerr
		}
		if bad := p.MetaMismatches(after); len(bad) > 0 {
			if opts.Verbose {
				fmt.Fprintf(opts.writer(), "   embedded write verification mismatch: %s\n", strings.Join(bad, "; "))
			}
			ok, xerr := p.writeXMPSidecar(opts)
			fsRes := p.applyFS(opts, mainWriteRan)
			if !ok {
				if xerr != nil {
					return fail("verification failed after write and XMP fallback failed: %v", xerr)
				}
				return fail("metadata verification failed after write (mismatches: %s) and XMP fallback", strings.Join(bad, "; "))
			}
			return OutcomeSidecar, fsRes, nil
		}
	}

	fsRes := p.applyFS(opts, mainWriteRan)
	return OutcomeUpdated, fsRes, nil
}

// applyFS synchronizes filesystem timestamps from the resolved instant.
// FileModifyDate everywhere; FileCreateDate attempted where the OS supports
// it (reported unsupported otherwise, never an error). FileAccessDate is
// never touched.
func (p *Plan) applyFS(opts Options, mainWriteRan bool) FSResult {
	res := FSResult{CreateState: "skipped"}
	if !p.wantFS || opts.DryRun {
		return res
	}
	if mainWriteRan && p.fsModInMain {
		res.ModSet = true // covered by the main invocation
	} else if _, err := Exec([]string{"-m", "-overwrite_original",
		"-FileModifyDate=" + p.fsModWant, p.MediaPath}); err == nil {
		res.ModSet = true
	} else if opts.Verbose {
		fmt.Fprintf(opts.writer(), "   warning: could not set FileModifyDate for %s\n", p.MediaPath)
	}
	if runtime.GOOS == "linux" {
		// Ordinary Linux filesystems do not expose a writable birth time via
		// ExifTool - do not waste a process attempt on it.
		res.CreateState = "unsupported"
		return res
	}
	if _, err := Exec([]string{"-m", "-overwrite_original",
		"-FileCreateDate=" + p.fsModWant, p.MediaPath}); err != nil {
		res.CreateState = "unsupported"
	} else {
		res.CreateState = "set"
	}
	return res
}

// writeXMPSidecar creates or updates <media>.xmp and verifies it.
func (p *Plan) writeXMPSidecar(opts Options) (bool, error) {
	xmp := p.MediaPath + ".xmp"
	if _, err := os.Stat(xmp); os.IsNotExist(err) {
		args := append([]string{"-m", "-o", xmp}, p.xmpArgs...)
		if out, err := Exec(args); err != nil {
			return false, fmt.Errorf("cannot create XMP sidecar %s: %v\noutput: %s", xmp, err, string(out))
		}
	} else {
		args := append([]string{"-m", "-overwrite_original"}, p.xmpArgs...)
		args = append(args, xmp)
		if out, err := Exec(args); err != nil {
			return false, fmt.Errorf("cannot update XMP sidecar %s: %v\noutput: %s", xmp, err, string(out))
		}
	}

	cur, err := Read(xmp)
	if err != nil {
		return false, err
	}
	// Core requirements for a valid XMP fallback result. Note: exiftool
	// renders XMP datetime values in its EXIF-style display format on read,
	// so expectations use the colon form.
	checks := map[string]string{}
	if p.Resolved != nil && !p.Resolved.DateOnly {
		want := xmpReadExpect(p.Resolved)
		checks["XMP-exif:DateTimeOriginal"] = want
		checks["XMP-xmp:CreateDate"] = want
	}
	if p.WriteDesc {
		checks["XMP-dc:Description"] = p.Desc
	}
	if p.Title != "" {
		checks["XMP-dc:Title"] = p.Title
	}
	for k, want := range checks {
		if got, _ := cur.Str(k); got != want {
			return false, fmt.Errorf("XMP sidecar field %s = %q, want %q", k, got, want)
		}
	}
	return true, nil
}
