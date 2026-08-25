package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tsRome2022 = "1660486200" // 2022-08-14T14:10:00Z

func v2writeJPEG(t *testing.T, path string) {
	mustWriteFile(t, path, writeJPEGBytes(t, 100, 100, 30))
}

func v2sidecar(t *testing.T, dir, forName, taken string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, forName+".supplemental-metadata.json"),
		`{"title":"t","photoTakenTime":{"timestamp":"`+taken+`"},"creationTime":{"timestamp":"`+taken+`"}}`)
}

func readInfoStr(t *testing.T, p string) map[string]string {
	return map[string]string(readInfo(t, p))
}

// Embedded capture date wins: after a first fix established 2001 from JSON,
// a later sidecar claiming 2022 (and filename digits saying 1999) must not
// change anything about the dates.
func TestV2_EmbeddedWins(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "IMG_19990101_000000.jpg") // filename claims 1999 - must lose
	v2writeJPEG(t, p)

	writeFile(t, p+".supplemental-metadata.json",
		`{"title":"t","photoTakenTime":{"timestamp":"993513600"}}`) // 2001-06-25T12:00Z
	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	i := readInfoStr(t, p)
	if i["ExifIFD:DateTimeOriginal"] != "2001:06:26 02:00:00" {
		t.Fatalf("phase1 setup failed: %+v\noutput:\n%s", i, out)
	}

	writeFile(t, p+".supplemental-metadata.json",
		`{"title":"t","photoTakenTime":{"timestamp":"`+tsRome2022+`"}}`)
	code, out = run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)

	i = readInfoStr(t, p)
	if i["ExifIFD:DateTimeOriginal"] != "2001:06:26 02:00:00" {
		t.Fatalf("embedded date must win over JSON+filename: got %q\noutput:\n%s", i["ExifIFD:DateTimeOriginal"], out)
	}
	if i["ExifIFD:OffsetTimeOriginal"] != "+02:00" {
		t.Fatalf("existing offsets must be preserved untouched: %q", i["ExifIFD:OffsetTimeOriginal"])
	}
}

// JSON fills the gap when the photo has no usable embedded date.
func TestV2_JSONFillsGap(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "holiday.jpg")
	v2writeJPEG(t, p)
	v2sidecar(t, dir, "holiday.jpg", tsRome2022)

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)

	i := readInfoStr(t, p)
	if i["ExifIFD:DateTimeOriginal"] != "2022:08:14 16:10:00" || i["ExifIFD:OffsetTimeOriginal"] != "+02:00" {
		t.Fatalf("JSON date + timezone expected: %+v", i)
	}
}

// GPS: filled from JSON when missing; NEVER overwritten when present.
func TestV2_GPSFillIfMissing(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()

	// case 1: missing -> filled
	p1 := filepath.Join(dir, "a.jpg")
	v2writeJPEG(t, p1)
	writeFile(t, filepath.Join(dir, "a.jpg.supplemental-metadata.json"),
		`{"photoTakenTime":{"timestamp":"`+tsRome2022+`"},"geoData":{"latitude":41.8902,"longitude":12.4922,"altitude":26}}`)
	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	i1 := readInfoStr(t, p1)
	if i1["GPS:GPSLatitudeRef"] != "N" {
		t.Fatalf("GPS should be filled: %+v", i1)
	}

	// case 2: present -> preserved even though sidecar differs
	p2 := filepath.Join(dir, "b.jpg")
	v2writeJPEG(t, p2)
	writeFile(t, filepath.Join(dir, "b.jpg.supplemental-metadata.json"),
		`{"photoTakenTime":{"timestamp":"`+tsRome2022+`"},"geoData":{"latitude":-33.8568,"longitude":151.2153,"altitude":0}}`)
	code, out = run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)

	// embed GPS into b.jpg manually via exiftool through fix trick:
	// simplest reliable route: another sidecar-free fix cannot add GPS.
	// Instead assert that after this run b has NO gps (json must NOT be applied? No -
	// b had none before, so it MUST have been filled). For preserve-check we need
	// pre-existing GPS: reuse case-1 result by pointing its sidecar elsewhere.
	_ = p2
	i2 := readInfoStr(t, p2)
	if i2["GPS:GPSLatitudeRef"] == "" {
		t.Fatalf("GPS should be filled when missing: %+v", i2)
	}
}

// Embedded GPS beats a differing JSON location (the core new rule).
func TestV2_GPSPreservedWhenPresent(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "c.jpg")
	v2writeJPEG(t, p)

	// first: establish embedded GPS via sidecar (Sydney)
	writeFile(t, p+".supplemental-metadata.json",
		`{"photoTakenTime":{"timestamp":"`+tsRome2022+`"},"geoData":{"latitude":-33.8568,"longitude":151.2153,"altitude":5}}`)
	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)

	// second: sidecar now claims Rome - embedded Sydney must survive
	writeFile(t, p+".supplemental-metadata.json",
		`{"photoTakenTime":{"timestamp":"`+tsRome2022+`"},"geoData":{"latitude":41.8902,"longitude":12.4922,"altitude":26}}`)
	code, out = run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)

	i := readInfoStr(t, p)
	lat := i["GPS:GPSLatitude"]
	if lat != "33.8568" || i["GPS:GPSLatitudeRef"] != "S" {
		t.Fatalf("embedded Sydney GPS must win; got lat %q ref %q", lat, i["GPS:GPSLatitudeRef"])
	}
	if lon := i["GPS:GPSLongitude"]; lon != "151.2153" {
		t.Fatalf("embedded longitude must win; got %q", lon)
	}
}

// Filename is the last resort only.
func TestV2_FilenameLastResort(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "IMG_19990102_030405.jpg")
	v2writeJPEG(t, p)

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)
	if got := readInfoStr(t, p)["ExifIFD:DateTimeOriginal"]; got != "1999:01:02 03:04:05" {
		t.Fatalf("filename fallback expected, got %q\noutput:\n%s", got, out)
	}

	// Once written, filename-derived dates are embedded: the rule makes them
	// stick (a later sidecar cannot override existing metadata).
	v2sidecar(t, dir, "IMG_19990102_030405.jpg", tsRome2022)
	code, out = run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	if got := readInfoStr(t, p)["ExifIFD:DateTimeOriginal"]; got != "1999:01:02 03:04:05" {
		t.Fatalf("promoted filename date must persist: got %q", got)
	}
	if !strings.Contains(out, "already correct:") {
		t.Fatalf("second run must be a no-op\noutput:\n%s", out)
	}
}

// Undated media are left untouched and counted.
func TestV2_UndatedLeftUntouched(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "random_photo.xyz.jpg")
	v2writeJPEG(t, p)

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	if got := readInfoStr(t, p)["ExifIFD:DateTimeOriginal"]; got != "" {
		t.Fatalf("undated file must stay untouched, got %q", got)
	}
	if !strings.Contains(out, "undated") {
		t.Fatalf("undated counter expected in summary\noutput:\n%s", out)
	}
}

// Idempotency incl. the GPS-preserve path.
func TestV2_Idempotent(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "d.jpg")
	v2writeJPEG(t, p)
	v2sidecar(t, dir, "d.jpg", tsRome2022)

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	code, out = run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	if !strings.Contains(out, "already correct:") {
		t.Fatalf("second run must be a no-op\noutput:\n%s", out)
	}
}

// organize-by-year default layout + Unknown opt-in.
func TestV2_OrganizeBasics(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	dated := filepath.Join(dir, "e.jpg")
	v2writeJPEG(t, dated)
	v2sidecar(t, dir, "e.jpg", tsRome2022)
	undated := filepath.Join(dir, "f.jpg")
	v2writeJPEG(t, undated)

	dst := filepath.Join(t.TempDir(), "out")
	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin", dir, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "2022", "e.jpg")); err != nil {
		t.Fatalf("expected 2022/e.jpg: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dst, "Unknown")); !os.IsNotExist(err) {
		t.Fatalf("undated skipped by default - Unknown/ must not exist\noutput:\n%s", out)
	}

	dst2 := filepath.Join(t.TempDir(), "out2")
	code, out = run(t, "", "organize-by-year", "--include-unknown-date", dir, dst2)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst2, "Unknown", "f.jpg")); err != nil {
		t.Fatalf("Unknown opt-in failed: %v\n%s", err, out)
	}
}

// clean-json deletes only verified sidecars; generic ones survive.
func TestV2_CleanJsonProtections(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "g.jpg")
	v2writeJPEG(t, p)
	v2sidecar(t, dir, "g.jpg", tsRome2022)
	writeFile(t, filepath.Join(dir, "Metadaten.json"), `{"albums":[]}`)

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", dir)
	requireOK(t, out, code)
	code, out = run(t, "", "clean-json", "--yes", dir)
	requireOK(t, out, code)

	if _, err := os.Stat(p + ".supplemental-metadata.json"); !os.IsNotExist(err) {
		t.Fatal("verified sidecar should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "Metadaten.json")); err != nil {
		t.Fatal("generic album metadata must survive")
	}
}

// The `run` pipeline executes all three steps in order.
func TestV2_RunPipeline(t *testing.T) {
	hasExiftool(t)
	root := t.TempDir()
	lib := filepath.Join(root, "Google Fotos")
	album := filepath.Join(root, "Album X")

	same := writeJPEGBytes(t, 7, 77, 177)
	mustWriteFile(t, filepath.Join(lib, "h.jpg"), same)
	v2sidecar(t, lib, "h.jpg", tsRome2022)
	mustWriteFile(t, filepath.Join(album, "h_album.jpg"), same) // duplicate bytes
	undated := filepath.Join(lib, "i.jpg")
	v2writeJPEG(t, undated)

	dst := filepath.Join(t.TempDir(), "organized")
	code, out := run(t, "", "run", "--yes", root, dst)
	requireOK(t, out, code)

	for _, marker := range []string{"step 1/3", "step 2/3", "step 3/3"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("pipeline step missing: %s\noutput:\n%s", marker, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "2022", "h.jpg")); err != nil {
		t.Fatalf("dated file not organized: %v\noutput:\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "Album X", "h_album.jpg")); !os.IsNotExist(err) {
		t.Fatalf("duplicate album copy should be deleted by step 1\noutput:\n%s", out)
	}
}
