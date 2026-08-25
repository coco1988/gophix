package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ts2019March = "1552000000" // 2019-03-07T23:06:40Z -> 2019-03-08T00:06+01 Berlin

// The organize fast path (no embedded-metadata read when JSON wins under the
// active policy) must produce exactly the same year placement as the slow
// path that reads embedded metadata.
func TestOrganize_FastPathMatchesSlowPath(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "IMG_20150601_120000.jpg")) // filename claims 2015
	sidecar(t, dir, "IMG_20150601_120000.jpg", ts2019March, "", 48.13, 11.58, "")

	cases := []struct{ name, policyFlag string }{
		{"preserve-existing", ""},
		{"prefer-json", "--time-policy=prefer-json"}, // fast path
		{"force-json-time", "--force-json-time"},     // fast path
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out")
			args := []string{"organize-by-year", "--timezone", "Europe/Berlin"}
			if tc.policyFlag != "" {
				args = append(args, tc.policyFlag)
			}
			args = append(args, dir, dst)
			code, out := run(t, "", args...)
			requireOK(t, out, code)

			got := filepath.Join(dst, "2019", "IMG_20150601_120000.jpg")
			if _, err := os.Stat(got); err != nil {
				t.Fatalf("expected %s (JSON time beats filename digits): %v\noutput:\n%s", got, err, out)
			}
		})
	}
}

// json-only without a JSON timestamp stays genuinely unknown (never falls
// back to mtime or filename), and Unknown/ requires the opt-in flag.
func TestOrganize_JSONOnly_NoTimestampStaysUnknown(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	name := "IMG_20150601_120000.jpg"
	writeJPEG(t, filepath.Join(dir, name))
	writeFile(t, filepath.Join(dir, name+".supplemental-metadata.json"),
		`{"title":"x","description":"no timestamps here"}`)

	dst := filepath.Join(t.TempDir(), "out")
	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin",
		"--time-policy", "json-only", dir, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "Unknown")); !os.IsNotExist(err) {
		t.Fatalf("Unknown/ created without --include-unknown-date\noutput:\n%s", out)
	}

	dst2 := filepath.Join(t.TempDir(), "out2")
	code, out = run(t, "", "organize-by-year", "--timezone", "Europe/Berlin",
		"--time-policy", "json-only", "--include-unknown-date", dir, dst2)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst2, "Unknown", name)); err != nil {
		t.Fatalf("expected Unknown/%s with --include-unknown-date: %v\noutput:\n%s", name, err, out)
	}
}

// fix keeps working end-to-end when metadata was pre-read in bulk and passed
// through to Apply (the cached-Info path), including full idempotency.
func TestFix_CachedInfoPathIdempotent(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "IMG_20200303_101010.jpg"))
	sidecar(t, dir, "IMG_20200303_101010.jpg", tsBerlinNoonWinter, "", 48.13, 11.58, "Cache-Pfad für Test")

	code, out := run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)
	if !strings.Contains(out, "updated directly") {
		t.Fatalf("first run should update directly\noutput:\n%s", out)
	}

	code, out = run(t, "", "fix", "--timezone", "Europe/Berlin", "--verbose", dir)
	requireOK(t, out, code)
	if !strings.Contains(out, "already correct:") || strings.Contains(out, "XMP sidecars written:      1") {
		t.Fatalf("second run must report already correct via cached Info\noutput:\n%s", out)
	}
	if strings.Count(out, "already correct") < 2 { // verbose line + summary counter
		t.Fatalf("expected already-correct on second run\noutput:\n%s", out)
	}
}
