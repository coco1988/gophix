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

// Plan describes everything needed to bring one media file to its desired state.
type Plan struct {
	MediaPath string
	Date      *DateResult // nil: nothing to write (caller decides what that means)

	WriteDesc bool
	Desc      string
	Title     string
	GPSVal    *GPS

	isVideo bool

	args        []string           // embedded-write exiftool arguments
	xmpArgs     []string           // XMP-sidecar write arguments
	expect      map[string]string  // exact string expectations after write
	expectF     map[string]float64 // numeric expectations after write
	fsWant      string             // FileModifyDate value we want on the media
	wantFS      bool
	fsModInMain bool
}

// gpsEpsilon is the comparison tolerance for GPS coordinates.
const gpsEpsilon = 1e-6

// gpsInInfo reports whether the media already carries usable coordinates
// (both present, non-placeholder, in range). Existing GPS is never overwritten.
func gpsInInfo(i Info) bool {
	lat, ok1 := i.Float("GPS:GPSLatitude")
	lon, ok2 := i.Float("GPS:GPSLongitude")
	if !ok1 || !ok2 {
		return false
	}
	if lat == 0 && lon == 0 {
		return false // Takeout-style redaction placeholder
	}
	return math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

// BuildPlan computes the desired metadata state of one file under the v2 rules.
// When no date source works it returns an *UndatableError.
func BuildPlan(mediaPath string, info Info, sc *takeout.Sidecar, zone *time.Location) (*Plan, error) {
	p := &Plan{
		MediaPath: mediaPath,
		isVideo:   info.IsVideo(),
		expect:    map[string]string{},
		expectF:   map[string]float64{},
	}

	if sc != nil {
		p.Desc = sc.Description
		p.WriteDesc = strings.TrimSpace(sc.Description) != ""
		p.Title = sc.Title
		if !gpsInInfo(info) {
			if g := sc.EffectiveGeo(); g != nil {
				p.GPSVal = &GPS{Lat: g.Lat, Lon: g.Lon, Alt: g.Alt, HasAlt: g.HasAltitude()}
			}
		}
	}

	var taken *int64
	if sc != nil {
		taken = sc.TakenUnix()
	}
	var fname *FileNameDate
	if f, err := ParseFileName(mediaPath); err == nil {
		fname = f
	}

	date, ok := ResolveDate(info, taken, fname, zone)
	if !ok {
		base := filepath.Base(mediaPath)
		return nil, &UndatableError{Name: base}
	}
	p.Date = date
	p.buildArgs()
	p.buildExpectations()
	return p, nil
}

func (p *Plan) buildArgs() {
	r := p.Date
	// -q keeps camera-specific noise (e.g. "Duplicate MakerNoteUnknown tag")
	// off the terminal; genuine errors still surface and fail the run.
	a := []string{"-m", "-q", "-overwrite_original"}

	full := !r.DateOnly
	switch {
	case full && p.isVideo:
		// QuickTime atom dates are UTC per container spec.
		utc := Exif(r.Instant.UTC())
		a = append(a,
			"-QuickTime:CreateDate="+utc,
			"-QuickTime:ModifyDate="+utc)
		for _, track := range []string{"Track1", "Track2"} {
			a = append(a,
				"-"+track+":MediaCreateDate="+utc,
				"-"+track+":MediaModifyDate="+utc,
				"-"+track+":TrackCreateDate="+utc,
				"-"+track+":TrackModifyDate="+utc)
		}
	case full:
		local := Exif(r.Wall)
		a = append(a,
			"-DateTimeOriginal="+local,
			"-CreateDate="+local,
			"-ModifyDate="+local)
		if r.Offset != nil {
			a = append(a,
				"-OffsetTimeOriginal="+*r.Offset,
				"-OffsetTimeDigitized="+*r.Offset,
				"-OffsetTime="+*r.Offset)
		}
	}

	if p.WriteDesc {
		a = append(a,
			"-EXIF:ImageDescription="+p.Desc,
			"-XMP-dc:Description="+p.Desc)
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
			"-GPSLongitudeRef="+lonRef)
		if g.HasAlt {
			alt, altRef := g.Alt, "0"
			if alt < 0 {
				alt, altRef = -alt, "1"
			}
			a = append(a, fmt.Sprintf("-GPSAltitude=%.3f", alt), "-GPSAltitudeRef="+altRef)
		}
		if full {
			a = append(a,
				"-GPSDateStamp="+r.Instant.UTC().Format("2006:01:02"),
				"-GPSTimeStamp="+r.Instant.UTC().Format("15:04:05"))
		}
	}

	if full {
		// Filesystem dates ALWAYS follow the chosen date so Explorer/gallery
		// sorting is repaired. Naive wall clocks are interpreted in the
		// effective timezone (machine locale unless --timezone was given),
		// so Explorer shows exactly the camera's clock digits.
		p.fsWant = FileTS(r.FSTime)
		p.wantFS = true
		a = append(a, "-FileModifyDate="+p.fsWant)
		p.fsModInMain = true
	}
	p.args = a

	// XMP sidecar variant (used when embedded writing fails).
	xa := []string{}
	if full && !p.isVideo {
		xa = append(xa,
			"-XMP-exif:DateTimeOriginal="+xmpLocal(r),
			"-XMP-xmp:CreateDate="+xmpLocal(r))
	}
	if full && p.isVideo {
		xa = append(xa, "-XMP-xmp:CreateDate="+xmpUTC(r))
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
			fmt.Sprintf("-XMP-exif:GPSLongitude=%+.6f", g.Lon))
		if g.HasAlt {
			xa = append(xa, fmt.Sprintf("-XMP-exif:GPSAltitude=%.3f", g.Alt))
		}
		if full {
			xa = append(xa, "-XMP-exif:GPSDateTime="+r.Instant.UTC().Format("2006-01-02T15:04:05")+"Z")
		}
	}
	p.xmpArgs = xa
}

func xmpLocal(r *DateResult) string {
	s := r.Wall.Format("2006-01-02T15:04:05")
	if r.Offset != nil {
		s += *r.Offset
	}
	return s
}

func xmpUTC(r *DateResult) string {
	return r.Instant.UTC().Format("2006-01-02T15:04:05") + "Z"
}

func (p *Plan) buildExpectations() {
	r := p.Date
	full := !r.DateOnly
	if full && p.isVideo {
		utc := Exif(r.Instant.UTC())
		for _, k := range []string{
			"QuickTime:CreateDate", "QuickTime:ModifyDate",
			"Track1:MediaCreateDate", "Track1:MediaModifyDate",
			"Track1:TrackCreateDate", "Track1:TrackModifyDate",
		} {
			p.expect[k] = utc
		}
	} else if full {
		local := Exif(r.Wall)
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
		if full {
			p.expect["GPS:GPSDateStamp"] = r.Instant.UTC().Format("2006:01:02")
			p.expect["GPS:GPSTimeStamp"] = r.Instant.UTC().Format("15:04:05")
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

// MetaSatisfied reports whether current metadata already equals the plan.
func (p *Plan) MetaSatisfied(cur Info) bool { return len(p.MetaMismatches(cur)) == 0 }

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

// FSSatisfied reports whether the filesystem modification time already matches.
func (p *Plan) FSSatisfied(cur Info) bool {
	if !p.wantFS {
		return true
	}
	got, ok := cur.Str("System:FileModifyDate")
	if !ok {
		return false
	}
	t, err := time.Parse(fileLayout, got)
	return err == nil && t.Format(fileLayout) == p.fsWant
}

// FSResult reports what filesystem timestamp synchronization did.
type FSResult struct {
	ModSet      bool
	CreateState string // "set", "unsupported", "failed", "skipped"
}

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

// Apply brings the media file to the planned state. cur is the bulk-read
// metadata; it is re-read only when nil or after an actual write (verification).
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

// applyFS synchronizes filesystem timestamps from the resolved date.
func (p *Plan) applyFS(opts Options, mainWriteRan bool) FSResult {
	res := FSResult{CreateState: "skipped"}
	if !p.wantFS || opts.DryRun {
		return res
	}
	if mainWriteRan && p.fsModInMain {
		res.ModSet = true
	} else if _, err := Exec([]string{"-m", "-q", "-overwrite_original",
		"-FileModifyDate=" + p.fsWant, p.MediaPath}); err == nil {
		res.ModSet = true
	} else if opts.Verbose {
		fmt.Fprintf(opts.writer(), "   warning: could not set FileModifyDate for %s\n", p.MediaPath)
	}
	if runtime.GOOS == "linux" {
		res.CreateState = "unsupported"
		return res
	}
	if _, err := Exec([]string{"-m", "-q", "-overwrite_original",
		"-FileCreateDate=" + p.fsWant, p.MediaPath}); err != nil {
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
		args := append([]string{"-m", "-q", "-o", xmp}, p.xmpArgs...)
		if out, err := Exec(args); err != nil {
			return false, fmt.Errorf("cannot create XMP sidecar %s: %v\noutput: %s", xmp, err, string(out))
		}
	} else {
		args := append([]string{"-m", "-q", "-overwrite_original"}, p.xmpArgs...)
		args = append(args, xmp)
		if out, err := Exec(args); err != nil {
			return false, fmt.Errorf("cannot update XMP sidecar %s: %v\noutput: %s", xmp, err, string(out))
		}
	}

	cur, err := Read(xmp)
	if err != nil {
		return false, err
	}
	checks := map[string]string{}
	if p.Date != nil && !p.Date.DateOnly && !p.isVideo {
		// exiftool renders XMP datetimes in its EXIF-style display format.
		want := Exif(p.Date.Wall)
		if p.Date.Offset != nil {
			want += *p.Date.Offset
		}
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
