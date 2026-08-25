package commands

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexdachin/gophix/meta"
)

// --- helpers ---------------------------------------------------------------

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(2, 2, color.RGBA{R: 200, A: 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tsBerlinNoonWinter = "1607256000" // 2020-12-06T12:00:00Z -> 13:00+01 Berlin

func sidecar(t *testing.T, dir, forName string, taken, created string, lat, lon float64, desc string) {
	t.Helper()
	var geo, geoExif string
	if lat != 0 || lon != 0 {
		geo = `"geoData": {"latitude": LAT, "longitude": LON, "altitude": 75.5},`
		geoExif = `"geoDataExif": {"latitude": LAT, "longitude": LON, "altitude": 75.5},`
		geo = strings.Replace(geo, "LAT", fstr(lat), 1)
		geo = strings.Replace(geo, "LON", fstr(lon), 1)
		geoExif = strings.Replace(geoExif, "LAT", fstr(lat), 1)
		geoExif = strings.Replace(geoExif, "LON", fstr(lon), 1)
	}
	json := `{
	  "title": "` + forName + `",
	  "description": "` + desc + `",` + "\n" + geo + "\n" + geoExif + `
	  "photoTakenTime": {"timestamp": "` + taken + `"},
	  "creationTime": {"timestamp": "` + created + `"}
	}`
	writeFile(t, filepath.Join(dir, forName+".supplemental-metadata.json"), json)
}

func fstr(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	return s
}

func run(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	// Commands print human-readable progress via fmt to process stdout;
	// capture it wholesale for assertions.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()

	var in bytes.Buffer
	in.WriteString(stdin)
	code := Run(args, w, w, &in)

	w.Close()
	out := <-done
	os.Stdout = old
	return code, out
}

func requireOK(t *testing.T, out string, code int) {
	t.Helper()
	if code != ExitOK {
		t.Fatalf("exit=%d output:\n%s", code, out)
	}
}

func readInfo(t *testing.T, path string) meta.Info {
	t.Helper()
	info, err := meta.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func hasExiftool(t *testing.T) {
	t.Helper()
	if err := meta.Available(); err != nil {
		t.Skip("exiftool not available")
	}
}

var _ = exec.Command

// --- integration tests ------------------------------------------------------

// I15/T5/F6: full image scenario - dates (local+tz), GPS incl UTC stamps,
// description; JSON beats any filename digits.
func I15_ImageFix(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "IMG_19990101_000000.jpg")) // filename suggests 1999
	sidecar(t, dir, "IMG_19990101_000000.jpg", tsBerlinNoonWinter, "", 41.900947, -12.464825, "Urlaub am See ✓")

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)

	p := filepath.Join(dir, "IMG_19990101_000000.jpg")
	info := readInfo(t, p)

	if got, _ := info.Str("ExifIFD:DateTimeOriginal"); got != "2020:12:06 13:00:00" {
		t.Errorf("DateTimeOriginal = %q", got)
	}
	if got, _ := info.Str("ExifIFD:OffsetTimeOriginal"); got != "+01:00" {
		t.Errorf("OffsetTimeOriginal = %q", got)
	}
	lat, _ := info.Float("GPS:GPSLatitude")
	if lat != 41.900947 {
		t.Errorf("GPSLatitude = %v", lat)
	}
	ref, _ := info.Str("GPS:GPSLongitudeRef")
	if ref != "W" { // negative longitude must yield West
		t.Errorf("GPSLongitudeRef = %q", ref)
	}
	if got, _ := info.Str("GPS:GPSDateStamp"); got != "2020:12:06" {
		t.Errorf("GPSDateStamp = %q (must be UTC date)", got)
	}
	if got, _ := info.Str("GPS:GPSTimeStamp"); got != "12:00:00" {
		t.Errorf("GPSTimeStamp = %q (must be UTC)", got)
	}
	if got, _ := info.Str("IFD0:ImageDescription"); got != "Urlaub am See ✓" {
		t.Errorf("description unicode broken: %q", got)
	}
}

// I18/F11: second run performs no writes.
func I18_IdempotentSecondRun(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "a.jpg"))
	sidecar(t, dir, "a.jpg", tsBerlinNoonWinter, "", 0, 0, "")

	for i := 0; i < 2; i++ {
		code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
		requireOK(t, out, code)
	}
	_, out := run(t, "", "fix", "--verbose", "--timezone", "Europe/Berlin", dir)
	if !strings.Contains(out, "already correct") {
		t.Fatalf("second run must report already correct:\n%s", out)
	}
	mt := statModTime(t, filepath.Join(dir, "a.jpg"))
	time.Sleep(1100 * time.Millisecond)
	_, out2 := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out2, ExitOK)
	if mt != statModTime(t, filepath.Join(dir, "a.jpg")) {
		t.Fatal("idempotent run touched the file")
	}
}

func statModTime(t *testing.T, p string) time.Time {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

// snapshot equality helper (maps are not comparable directly)
func sameSnapshot(a, b map[string]struct {
	size int64
	mt   time.Time
}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va.size != vb.size || !va.mt.Equal(vb.mt) {
			return false
		}
	}
	return true
}

// I19: dry-run changes nothing.
func I19_DryRunNoChange(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "a.jpg"))
	sidecar(t, dir, "a.jpg", tsBerlinNoonWinter, "", 10, 20, "desc")

	before := snapshot(t, dir)
	code, out := run(t, "", "fix", "--dry-run", "--verbose", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	if !strings.Contains(out, "[dry-run]") && !strings.Contains(out, "planned") {
		t.Fatalf("dry-run output should indicate planning:\n%s", out)
	}
	if after := snapshot(t, dir); !sameSnapshot(before, after) {
		t.Fatal("dry-run modified files")
	}
}

func snapshot(t *testing.T, dir string) map[string]struct {
	size int64
	mt   time.Time
} {
	t.Helper()
	out := map[string]struct {
		size int64
		mt   time.Time
	}{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		out[p] = struct {
			size int64
			mt   time.Time
		}{fi.Size(), fi.ModTime()}
		return nil
	})
	return out
}

// I8: invalid sidecar JSON -> error + non-zero exit.
func I8_InvalidJSONError(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "a.jpg"))
	writeFile(t, filepath.Join(dir, "a.jpg.supplemental-metadata.json"), "{not json")

	code, out := run(t, "", "fix", dir)
	if code != ExitErrors {
		t.Fatalf("exit=%d want %d\n%s", code, ExitErrors, out)
	}
	if !strings.Contains(out, "invalid sidecar") {
		t.Fatalf("missing diagnostic:\n%s", out)
	}
}

// I13: GPS placeholders are never written.
func I13_NoGPSNoPlaceholder(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "a.jpg"))
	sidecar(t, dir, "a.jpg", tsBerlinNoonWinter, "", 0, 0, "")
	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)

	info := readInfo(t, filepath.Join(dir, "a.jpg"))
	if _, ok := info.Str("GPS:GPSLatitude"); ok {
		t.Fatal("placeholder (0,0) must not be written as GPS coordinates")
	}
}

// I14 covered by I15 (unicode). XMP fallback:
func I17_XMPFallback(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mystery.bin"), "not a real media file")
	sidecar(t, dir, "mystery.bin", tsBerlinNoonWinter, "", 0, 0, "sidecar text")

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)
	// The extension fixer may rename unrecognized files (e.g. .bin -> .txt);
	// the XMP sidecar then belongs to the renamed media.
	xmps, _ := filepath.Glob(filepath.Join(dir, "mystery.*.xmp"))
	if len(xmps) == 0 {
		t.Fatalf("expected XMP sidecar fallback:\n%s", out)
	}
	info := readInfo(t, xmps[0])
	if got, _ := info.Str("XMP-dc:Description"); got != "sidecar text" {
		t.Fatalf("xmp description = %q", got)
	}
}

// I20/I21: clean-json protections.
func I20_CleanJsonProtections(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()

	// verified pair
	writeJPEG(t, filepath.Join(dir, "ok.jpg"))
	sidecar(t, dir, "ok.jpg", tsBerlinNoonWinter, "", 0, 0, "")
	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)

	// generic album json - never deletable
	writeFile(t, filepath.Join(dir, "Metadaten.json"), `{"title":"album"}`)
	// unmatched sidecar
	writeFile(t, filepath.Join(dir, "orphan.json"), `{}`)
	// invalid sidecar matched to media
	writeJPEG(t, filepath.Join(dir, "bad.jpg"))
	writeFile(t, filepath.Join(dir, "bad.jpg.supplemental-metadata.json"), "{oops")
	// unsynchronized media (never fixed)
	writeJPEG(t, filepath.Join(dir, "unfixed.jpg"))
	sidecar(t, dir, "unfixed.jpg", tsBerlinNoonWinter, "", 0, 0, "")

	// non-interactive without --yes must refuse
	code, out = run(t, "", "clean-json", dir)
	if code == ExitOK && strings.Contains(out, "deleted") {
		t.Fatal("must not delete without confirmation")
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.jpg.supplemental-metadata.json")); err == nil {
		// refused deletion is fine; continue with --yes below
		_ = err
	}

	code, out = run(t, "", "clean-json", "--yes", "--verbose", dir)
	requireOK(t, out, code)

	if _, err := os.Stat(filepath.Join(dir, "ok.jpg.supplemental-metadata.json")); !os.IsNotExist(err) {
		t.Error("verified sidecar should have been deleted")
	}
	for _, keep := range []string{"Metadaten.json", "orphan.json", "bad.jpg.supplemental-metadata.json", "unfixed.jpg.supplemental-metadata.json"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s must be kept", keep)
		}
	}
	if !strings.Contains(out, "json kept (generic):       1") {
		t.Logf("output:\n%s", out)
	}
}

func I21_CleanJsonDryRun(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "ok.jpg"))
	sidecar(t, dir, "ok.jpg", tsBerlinNoonWinter, "", 0, 0, "")
	run(t, "", "fix", "--timezone", "Europe/Berlin", dir)

	before := snapshot(t, dir)
	code, out := run(t, "", "clean-json", "--dry-run", dir)
	requireOK(t, out, code)
	if !strings.Contains(out, "would delete") {
		t.Fatalf("dry-run must announce:\n%s", out)
	}
	if after := snapshot(t, dir); len(before) != len(after) {
		t.Fatal("dry-run deleted files")
	}
}

// I22-I28: organize-by-year.
func orgSetup(t *testing.T) (src, dst string) {
	hasExiftool(t)
	src = t.TempDir()
	dst = t.TempDir() + "/out"
	writeJPEG(t, filepath.Join(src, "IMG_20201206_142433.jpg"))
	sidecar(t, src, "IMG_20201206_142433.jpg", tsBerlinNoonWinter, "", 0, 0, "")
	return src, dst
}

func I22_OrganizeYearFolder(t *testing.T) {
	src, dst := orgSetup(t)
	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin", src, dst)
	requireOK(t, out, code)
	target := filepath.Join(dst, "2020", "IMG_20201206_142433.jpg")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("year folder missing:\n%s", out)
	}
}

// T8: year from resolved LOCAL time, not the UTC clock.
func T8_OrganizeLocalYearBoundary(t *testing.T) {
	hasExiftool(t)
	src, dst := orgSetup(t)
	// An instant that is 2020-12-31T23:30Z == 2021-01-01T00:30+01:00.
	berlin := time.FixedZone("+01", 3600)
	epoch := time.Date(2021, 1, 1, 0, 30, 0, 0, berlin).Unix()
	writeJPEG(t, filepath.Join(src, "edge.jpg"))
	writeFile(t, filepath.Join(src, "edge.jpg.supplemental-metadata.json"),
		`{"photoTakenTime":{"timestamp":"`+strconv.FormatInt(epoch, 10)+`"}}`)
	os.Remove(filepath.Join(src, "IMG_20201206_142433.jpg"))
	os.Remove(filepath.Join(src, "IMG_20201206_142433.jpg.supplemental-metadata.json"))

	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin", "--verbose", src, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "2021", "edge.jpg")); err != nil {
		t.Fatalf("local-time year boundary failed:\n%s", out)
	}
}

// F6: JSON beats filename during fix.
func F6_JSONBeatsFilename(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "IMG_19990101_000000.jpg"))
	sidecar(t, dir, "IMG_19990101_000000.jpg", tsBerlinNoonWinter, "", 0, 0, "")
	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	got, _ := readInfo(t, filepath.Join(dir, "IMG_19990101_000000.jpg")).Str("ExifIFD:DateTimeOriginal")
	if got != "2020:12:06 13:00:00" {
		t.Fatalf("json time must win over filename: %q", got)
	}
}

// F7: existing valid DateTimeOriginal wins under default policy.
func T3_F7_PreserveExisting(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	jpg := filepath.Join(dir, "p.jpg")
	writeJPEG(t, jpg)
	// Pre-write an embedded capture time WITH offset.
	cmd := exec.Command("exiftool", "-m", "-overwrite_original",
		"-DateTimeOriginal=2019:05:05 10:00:00",
		"-CreateDate=2019:05:05 10:00:00",
		"-OffsetTimeOriginal=+02:00", jpg)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup exiftool: %v %s", err, b)
	}
	sidecar(t, dir, "p.jpg", tsBerlinNoonWinter, "", 0, 0, "")

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	got, _ := readInfo(t, jpg).Str("ExifIFD:DateTimeOriginal")
	if got != "2019:05:05 10:00:00" {
		t.Fatalf("preserve-existing failed: %q", got)
	}

	// T4: --force-json-time explicitly overwrites it.
	code, out = run(t, "", "fix", "--force-json-time", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	got, _ = readInfo(t, jpg).Str("ExifIFD:DateTimeOriginal")
	if got != "2020:12:06 13:00:00" {
		t.Fatalf("force-json-time failed: %q", got)
	}
}

// T6: unresolved timezone warns instead of mislabeling UTC as local.
func T6_UnresolvedTimezoneWarning(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "n.jpg"))
	sidecar(t, dir, "n.jpg", tsBerlinNoonWinter, "", 0, 0, "")
	code, out := run(t, "", "fix", "--verbose", dir) // no --timezone
	requireOK(t, out, code)
	if !strings.Contains(out, "UTC") {
		t.Fatalf("expected explicit UTC warning:\n%s", out)
	}
	got, _ := readInfo(t, filepath.Join(dir, "n.jpg")).Str("ExifIFD:DateTimeOriginal")
	if got != "2020:12:06 12:00:00" {
		t.Fatalf("expected plain UTC clock digits, got %q", got)
	}
}

// F8/F9/F10: filename fallback behaviors.
func F8_FilenameOverridesFileModifyDate(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	jpg := filepath.Join(dir, "IMG_20150827_101112.jpg")
	writeJPEG(t, jpg)
	old := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	os.Chtimes(jpg, old, old)

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	info := readInfo(t, jpg)
	got, _ := info.Str("ExifIFD:DateTimeOriginal")
	if got != "2015:08:27 10:11:12" {
		t.Fatalf("filename time not applied: %q", got)
	}
	fm, _ := info.Str("System:FileModifyDate")
	if !strings.HasPrefix(fm, "2015:08:27 10:11:12") {
		t.Fatalf("FileModifyDate not synced from filename: %q", fm)
	}
}

func F9_DateOnlyOrganizesWithoutInventingTime(t *testing.T) {
	hasExiftool(t)
	src := t.TempDir()
	dst := t.TempDir() + "/out"
	writeFile(t, filepath.Join(src, "2019-03-01.txt"), "media-ish") // date-only name
	// give it a sidecar-less scan; use a jpeg so processing proceeds
	writeJPEG(t, filepath.Join(src, "holiday-2019-03-01.jpg"))

	code, out := run(t, "", "organize-by-year", src, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "2019", "holiday-2019-03-01.jpg")); err != nil {
		t.Fatalf("date-only year organization failed:\n%s", out)
	}
}

func F10_NoFilenameFallbackFlag(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "IMG_20150827_101112.jpg"))
	code, out := run(t, "", "fix", "--no-filename-fallback", "--verbose", dir)
	requireOK(t, out, code)
	if !strings.Contains(out, "filesystem-modification-time") {
		t.Fatalf("expected mtime fallback when filename parsing disabled:\n%s", out)
	}
}

// I25/I26/I28: safety of organization.
func I25_I28_OrganizeSafety(t *testing.T) {
	hasExiftool(t)
	src := t.TempDir()
	dst := t.TempDir() + "/out"

	// Two different images sharing one basename across folders -> identical
	// destination target names within the same year.
	dirA := filepath.Join(src, "r1")
	dirB := filepath.Join(src, "r2")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeJPEG(t, filepath.Join(dirA, "same.jpg"))
	sidecar(t, dirA, "same.jpg", tsBerlinNoonWinter, "", 0, 0, "")

	big := image.NewRGBA(image.Rect(0, 0, 12, 12))
	big.Set(5, 5, color.RGBA{B: 200, A: 255})
	f2, err := os.Create(filepath.Join(dirB, "same.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f2, big, nil); err != nil {
		t.Fatal(err)
	}
	f2.Close()
	writeFile(t, filepath.Join(dirB, "same.jpg.supplemental-metadata.json"),
		`{"photoTakenTime":{"timestamp":"`+tsBerlinNoonWinter+`"}}`)

	before := snapshot(t, src)

	// I26 default mode copies and leaves sources untouched.
	code, out := run(t, "", "organize-by-year", "--keep-json", "--verbose", src, dst)
	requireOK(t, out, code)
	if after := snapshot(t, src); !sameSnapshot(before, after) {
		t.Fatal("source changed during copy-mode organization")
	}
	if strings.Contains(out, "collision") == false && !strings.Contains(out, "-20") {
		t.Fatalf("expected a collision-resolved name:\n%s", out)
	}

	yearDir := filepath.Join(dst, "2020")
	entries, _ := os.ReadDir(yearDir)
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	// Exactly one keeps the original name; the other is suffixed.
	if !names["same.jpg"] {
		t.Fatalf("original name lost: %v", names)
	}
	suffixed := 0
	for n := range names {
		if n == "same.jpg" || strings.HasSuffix(n, ".json") {
			continue
		}
		suffixed++
		// I28: a sidecar must follow the renamed media base, keeping its
		// original suffix style ("X.jpg.supplemental-metadata.json" or the
		// legacy "X.json").
		stem := strings.TrimSuffix(n, ".jpg")
		if !names[n+".supplemental-metadata.json"] && !names[stem+".json"] {
			t.Errorf("sidecar did not follow renamed media %s: %v", n, names)
		}
		// Content of both variants must be intact and distinct.
		b1, _ := os.ReadFile(filepath.Join(yearDir, "same.jpg"))
		b2, _ := os.ReadFile(filepath.Join(yearDir, n))
		if bytes.Equal(b1, b2) {
			t.Error("collision resolution overwrote content")
		}
	}
	if suffixed != 1 {
		t.Fatalf("expected exactly one suffixed copy, got %d: %v", suffixed, names)
	}

	// Re-run: everything already present, nothing duplicated further.
	code, out = run(t, "", "organize-by-year", "--keep-json", src, dst)
	requireOK(t, out, code)
	if !strings.Contains(out, "already present") || strings.Contains(out, "collisions resolved:       0") == false {
		if !strings.Contains(out, "already present") {
			t.Fatalf("re-run should report skipped identical targets:\n%s", out)
		}
	}
}

// I27: --move removes sources only after verified copies.
func I27_MoveOptIn(t *testing.T) {
	hasExiftool(t)
	src, dst := orgSetup(t)

	// without --move nothing is removed
	run(t, "", "organize-by-year", src, dst)
	if _, err := os.Stat(filepath.Join(src, "IMG_20201206_142433.jpg")); err != nil {
		t.Fatal("copy-mode removed the source")
	}

	code, out := run(t, "", "organize-by-year", "--move", src, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(src, "IMG_20201206_142433.jpg")); !os.IsNotExist(err) {
		t.Fatalf("--move left the source behind:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dst, "2020", "IMG_20201206_142433.jpg")); err != nil {
		t.Fatalf("--move lost the destination copy:\n%s", out)
	}
}

// I24: Unknown requires --include-unknown-date.
func I24_UnknownDateOptIn(t *testing.T) {
	hasExiftool(t)
	src := t.TempDir()
	dst := t.TempDir() + "/out"
	writeJPEG(t, filepath.Join(src, "undated.jpg"))

	code, out := run(t, "", "organize-by-year", "--time-policy", "json-only", src, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "Unknown")); err == nil {
		t.Fatal("Unknown/ requires --include-unknown-date")
	}
	if !strings.Contains(out, "no usable capture date") {
		t.Fatalf("undated file should be reported:\n%s", out)
	}

	code, out = run(t, "", "organize-by-year", "--time-policy", "json-only", "--include-unknown-date", src, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "Unknown", "undated.jpg")); err != nil {
		t.Fatalf("opt-in Unknown placement failed:\n%s", out)
	}
}

// I29: missing exiftool -> actionable error, distinct exit code.
func I29_MissingExiftool(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-dir")
	code, out := run(t, "", "fix", t.TempDir())
	if code != ExitNoExiftool {
		t.Fatalf("exit=%d want %d\n%s", code, ExitNoExiftool, out)
	}
	if !strings.Contains(out, "exiftool not found") {
		t.Fatalf("message not actionable:\n%s", out)
	}
}

// I30: empty tree prints a useful note, no false success claim.
func I30_EmptyTree(t *testing.T) {
	hasExiftool(t)
	code, out := run(t, "", "fix", t.TempDir())
	requireOK(t, out, code)
	if !strings.Contains(out, "nothing to do") {
		t.Fatalf("empty tree must be visible:\n%s", out)
	}
}

// Windows shell quoting: 'C:\dir\' arrives as `C:\dir"` - must be repaired.
func WinPathManglingSanitized(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "a.jpg"))
	sidecar(t, dir, "a.jpg", tsBerlinNoonWinter, "", 0, 0, "")

	mangled := dir + `\` + `"` // simulates PowerShell's "C:\...\dir\" mangling
	code, out := run(t, "", "fix", "--dry-run", mangled)
	requireOK(t, out, code)

	quoted := `"` + dir + `"`
	code, out = run(t, "", "fix", "--dry-run", quoted)
	requireOK(t, out, code)
	if strings.Contains(out, "cannot access") {
		t.Fatalf("quoted path not handled:\n%s", out)
	}
}

// T7/I16: video container behavior (needs a real MP4 fixture).
func I16_VideoFix(t *testing.T) {
	hasExiftool(t)
	fixtures := os.Getenv("GOPHIX_FIXTURE_DIR")
	if fixtures == "" {
		t.Skip("GOPHIX_FIXTURE_DIR not set; provide a directory containing a small .mp4")
	}
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	var mp4 string
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
			mp4 = filepath.Join(fixtures, e.Name())
			break
		}
	}
	if mp4 == "" {
		t.Skip("no .mp4 in GOPHIX_FIXTURE_DIR")
	}

	dir := t.TempDir()
	data, err := os.ReadFile(mp4)
	if err != nil {
		t.Fatal(err)
	}
	name := "VID_20200101_120000.mp4"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar(t, dir, name, tsBerlinNoonWinter, "", 0, 0, "")

	code, out := run(t, "", "fix", "--force-json-time", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)
	info := readInfo(t, filepath.Join(dir, name))
	qtCreate, _ := info.Str("QuickTime:CreateDate")
	if qtCreate != "2020:12:06 13:00:00" { // stored as UTC per container spec
		t.Fatalf("QuickTime CreateDate = %q, want UTC instant 13:00:00", qtCreate)
	}
	mc, _ := info.Str("Track1:MediaCreateDate")
	if mc != "2020:12:06 13:00:00" {
		t.Fatalf("Track1 MediaCreateDate = %q", mc)
	}
}

func TestIntegration(t *testing.T) {
	t.Run("I15_ImageFix", I15_ImageFix)
	t.Run("I17_XMPFallback", I17_XMPFallback)
	t.Run("I18_IdempotentSecondRun", I18_IdempotentSecondRun)
	t.Run("I19_DryRunNoChange", I19_DryRunNoChange)
	t.Run("I8_InvalidJSONError", I8_InvalidJSONError)
	t.Run("I13_NoGPSNoPlaceholder", I13_NoGPSNoPlaceholder)
	t.Run("I20_CleanJsonProtections", I20_CleanJsonProtections)
	t.Run("I21_CleanJsonDryRun", I21_CleanJsonDryRun)
	t.Run("I22_OrganizeYearFolder", I22_OrganizeYearFolder)
	t.Run("T8_OrganizeLocalYearBoundary", T8_OrganizeLocalYearBoundary)
	t.Run("I24_UnknownDateOptIn", I24_UnknownDateOptIn)
	t.Run("I25_I28_OrganizeSafety", I25_I28_OrganizeSafety)
	t.Run("I27_MoveOptIn", I27_MoveOptIn)
	t.Run("I29_MissingExiftool", I29_MissingExiftool)
	t.Run("I30_EmptyTree", I30_EmptyTree)
	t.Run("WinPathManglingSanitized", WinPathManglingSanitized)
	t.Run("F6_JSONBeatsFilename", F6_JSONBeatsFilename)
	t.Run("T3_F7_PreserveExisting", T3_F7_PreserveExisting)
	t.Run("T6_UnresolvedTimezoneWarning", T6_UnresolvedTimezoneWarning)
	t.Run("F8_FilenameOverridesFileModifyDate", F8_FilenameOverridesFileModifyDate)
	t.Run("F9_DateOnlyOrganizes", F9_DateOnlyOrganizesWithoutInventingTime)
	t.Run("F10_NoFilenameFallbackFlag", F10_NoFilenameFallbackFlag)
	t.Run("I16_VideoFix", I16_VideoFix)
}
