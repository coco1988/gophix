package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tsMarch2019 = "1552000000" // 2019-03-07T23:06:40Z -> local 2019-03-08 (Europe/Berlin)

// Each --layout value must produce exactly the documented folder structure.
func TestOrganize_Layouts(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	name := "IMG_20150601_120000.jpg" // filename claims 2015; JSON wins
	writeJPEG(t, filepath.Join(dir, name))
	sidecar(t, dir, name, tsMarch2019, "", 0, 0, "")

	cases := []struct {
		layout   string
		wantPath string // relative to destination root
	}{
		{"yyyy", "2019/" + name},
		{"yyyy/mm", "2019/03/" + name},
		{"yyyy-mm", "2019-03/" + name},
		{"flat", name},
	}
	for _, tc := range cases {
		t.Run(tc.layout, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out")
			code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin",
				"--layout", tc.layout, dir, dst)
			requireOK(t, out, code)

			if _, err := os.Stat(filepath.Join(dst, tc.wantPath)); err != nil {
				t.Fatalf("--layout %s: expected %s: %v\noutput:\n%s", tc.layout, tc.wantPath, err, out)
			}
		})
	}
}

// Omitting --layout keeps the historical yyyy behavior.
func TestOrganize_LayoutDefaultIsYear(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	name := "IMG_20150601_120000.jpg"
	writeJPEG(t, filepath.Join(dir, name))
	sidecar(t, dir, name, tsMarch2019, "", 0, 0, "")

	dst := filepath.Join(t.TempDir(), "out")
	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin", dir, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "2019", name)); err != nil {
		t.Fatalf("default layout must be yyyy/: %v\noutput:\n%s", err, out)
	}
}

// In flat mode every file lands directly in <dst>/; same-named files with
// different content must both survive via deterministic collision names.
func TestOrganize_FlatCollisionNeverOverwrites(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	name := "IMG_20150601_120000.jpg"

	writeJPEG(t, filepath.Join(dir, name))
	sidecar(t, dir, name, tsMarch2019, "", 0, 0, "")

	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, filepath.Join(sub, name))                  // identical stem in another album dir
	sidecar(t, sub, name, tsBerlinNoonWinter, "", 0, 0, "") // different capture time

	dst := filepath.Join(t.TempDir(), "out")
	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin",
		"--layout", "flat", "--verbose", dir, dst)
	requireOK(t, out, code)

	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	var jpgs []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jpg") {
			jpgs = append(jpgs, e.Name())
		}
	}
	if len(jpgs) != 2 {
		t.Fatalf("want both files preserved in flat layout, got %v\noutput:\n%s", jpgs, out)
	}
	oneIsOriginal := false
	for _, j := range jpgs {
		if j == name {
			oneIsOriginal = true
		}
	}
	if !oneIsOriginal {
		t.Fatalf("original name should be used for the first file: %v", jpgs)
	}
}

// Unknown-date placement is independent of the layout.
func TestOrganize_LayoutUnknownStillOptIn(t *testing.T) {
	hasExiftool(t)
	dir := t.TempDir()
	name := "random_name_xyz.jpg"
	writeJPEG(t, filepath.Join(dir, name))

	dst := filepath.Join(t.TempDir(), "out")
	code, out := run(t, "", "organize-by-year", "--timezone", "Europe/Berlin",
		"--layout", "flat", "--time-policy", "json-only", "--include-unknown-date", dir, dst)
	requireOK(t, out, code)
	if _, err := os.Stat(filepath.Join(dst, "Unknown", name)); err != nil {
		t.Fatalf("Unknown/ must exist regardless of layout: %v\noutput:\n%s", err, out)
	}
}

func TestOrganize_LayoutInvalidValueRejected(t *testing.T) {
	code, _ := run(t, "", "organize-by-year", "--layout", "yyyy/mm/dd", "/tmp", "/tmp/dest")
	if code != ExitUsage {
		t.Fatalf("invalid --layout must exit %d, got %d", ExitUsage, code)
	}
	code, _ = run(t, "", "organize-by-year", "--layout=YYYY/MM", "/tmp", "/tmp/dest")
	if code != ExitUsage {
		t.Fatalf("layouts are case-sensitive and must exit %d, got %d", ExitUsage, code)
	}
}
