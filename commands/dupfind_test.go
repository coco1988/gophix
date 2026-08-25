package commands

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeJPEGBytes returns deterministic JPEG bytes varying with the color.
func writeJPEGBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img.Set(3, 3, color.RGBA{R: r, G: g, B: b, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two byte-identical copies in different directories form one family; a
// distinct image is never flagged. Exit code stays 0 - finding is no error.
func TestDupFind_FamiliesAcrossDirs(t *testing.T) {
	root := t.TempDir()
	same := writeJPEGBytes(t, 200, 10, 10)
	mustWriteFile(t, filepath.Join(root, "Google Fotos", "IMG_1.jpg"), same)
	mustWriteFile(t, filepath.Join(root, "Album Urlaub", "IMG_1.jpg"), same)
	mustWriteFile(t, filepath.Join(root, "Google Fotos", "IMG_2.jpg"), writeJPEGBytes(t, 10, 200, 10))

	code, out := run(t, "", "find-duplicates", root)
	requireOK(t, out, code)

	if !strings.Contains(out, "1 duplicate families") {
		t.Fatalf("expected exactly one family\noutput:\n%s", out)
	}
	// The unique image is not listed as a copy; it only appears in the footer.
	if !strings.Contains(out, "3 files in 3 directories") || !strings.Contains(out, "1 skipped by unique size") {
		t.Fatalf("footer should account for all scanned files\noutput:\n%s", out)
	}
	if !strings.Contains(out, "KEEP") || !strings.Contains(out, "nothing was deleted or modified") {
		t.Fatalf("expected keep marker and safety note\noutput:\n%s", out)
	}
}

// Equal size but different content must never be reported as duplicates
// (guards the size-prefilter shortcut against merging by size alone).
func TestDupFind_SizePrefilterSoundness(t *testing.T) {
	root := t.TempDir()
	a := writeJPEGBytes(t, 5, 5, 5)
	b := writeJPEGBytes(t, 250, 250, 250)
	if len(a) != len(b) {
		// pad the shorter one to identical length; JPEG decoders ignore trailing bytes
		if len(a) < len(b) {
			a = append(a, make([]byte, len(b)-len(a))...)
		} else {
			b = append(b, make([]byte, len(a)-len(b))...)
		}
	}
	if len(a) != len(b) {
		t.Fatal("test setup failed to equalize sizes")
	}
	mustWriteFile(t, filepath.Join(root, "a.jpg"), a)
	mustWriteFile(t, filepath.Join(root, "b.jpg"), b)

	code, out := run(t, "", "find-duplicates", root)
	requireOK(t, out, code)
	if strings.Contains(out, "duplicate families found:") {
		t.Fatalf("equal-size different-content files must not be flagged\noutput:\n%s", out)
	}
	if !strings.Contains(out, "no duplicates found") {
		t.Fatalf("expected explicit no-duplicates footer\noutput:\n%s", out)
	}
}

// Keep suggestion prefers the copy WITH sidecar even when its path is longer.
func TestDupFind_RankingSidecarFirst(t *testing.T) {
	root := t.TempDir()
	same := writeJPEGBytes(t, 90, 90, 90)
	longDir := filepath.Join(root, "library-with-a-very-long-name")
	mustWriteFile(t, filepath.Join(longDir, "x.jpg"), same)
	writeFile(t, filepath.Join(longDir, "x.jpg.supplemental-metadata.json"),
		`{"title":"t","photoTakenTime":{"timestamp":"1552000000"}}`)
	mustWriteFile(t, filepath.Join(root, "album", "x.jpg"), same)

	code, out := run(t, "", "find-duplicates", root)
	requireOK(t, out, code)

	keepIdx := strings.Index(out, "★ KEEP "+filepath.Join(longDir, "x.jpg"))
	if keepIdx < 0 {
		t.Fatalf("sidecar copy should be suggested keeper\noutput:\n%s", out)
	}
	if !strings.Contains(out, "2019-03-07") { // capture date from JSON timestamp (UTC)
		t.Fatalf("capture date missing\noutput:\n%s", out)
	}
}

func TestDupFind_CSVAndJSONOutput(t *testing.T) {
	root := t.TempDir()
	same := writeJPEGBytes(t, 40, 40, 40)
	p1 := filepath.Join(root, "d1", "f.jpg")
	p2 := filepath.Join(root, "d2", "f.jpg")
	mustWriteFile(t, p1, same)
	mustWriteFile(t, p2, same)

	dir := t.TempDir()
	csvPath := filepath.Join(dir, "report.csv") // format inferred from extension
	code, out := run(t, "", "find-duplicates", "--output", csvPath, root)
	requireOK(t, out, code)
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("csv report not written: %v\noutput:\n%s", err, out)
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil || len(rows) != 3 { // header + 2 copies
		t.Fatalf("csv rows = %d err=%v", len(rows), err)
	}
	if rows[0][0] != "hash" || rows[0][1] != "path" {
		t.Fatalf("unexpected csv header: %v", rows[0])
	}
	keeps := 0
	for _, r := range rows[1:] {
		if r[3] == "true" {
			keeps++
		}
	}
	if keeps != 1 {
		t.Fatalf("exactly one copy must be marked is_keep, got %d: %v", keeps, rows)
	}

	jsonPath := filepath.Join(dir, "r.json")
	code, out = run(t, "", "find-duplicates", "--output", jsonPath, root)
	requireOK(t, out, code)
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("json report not written: %v", err)
	}
	var rep struct {
		Summary  map[string]any `json:"summary"`
		Families []struct {
			Hash   string `json:"hash"`
			Keep   string `json:"keep"`
			Copies []struct {
				Path   string `json:"path"`
				IsKeep bool   `json:"is_keep"`
			} `json:"copies"`
		} `json:"families"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(rep.Families) != 1 || rep.Families[0].Keep == "" || len(rep.Families[0].Copies) != 2 {
		t.Fatalf("unexpected json structure: %+v", rep.Families)
	}
}

// An unreadable media file degrades to a warning and non-zero exit without
// blocking the rest of the scan. Skipped when running as root (no EACCES).
func TestDupFind_UnreadableFileWarnsAndFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod-based unreadability ineffective")
	}
	root := t.TempDir()
	same := writeJPEGBytes(t, 70, 20, 20)
	p1 := filepath.Join(root, "ok1.jpg")
	p2 := filepath.Join(root, "ok2.jpg")
	mustWriteFile(t, p1, same)
	mustWriteFile(t, p2, same)
	locked := filepath.Join(root, "locked.jpg")
	mustWriteFile(t, locked, same)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o644)

	code, out := run(t, "", "find-duplicates", root)
	if code != ExitErrors {
		t.Fatalf("unreadable file must yield exit %d, got %d\noutput:\n%s", ExitErrors, code, out)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "locked.jpg") {
		t.Fatalf("expected warning naming the unreadable file\noutput:\n%s", out)
	}
}

func TestDupFind_EmptyTree(t *testing.T) {
	root := t.TempDir()
	code, out := run(t, "", "find-duplicates", root)
	requireOK(t, out, code)
	if !strings.Contains(out, "no media files were found") {
		t.Fatalf("expected explicit empty-tree note\noutput:\n%s", out)
	}
}

func TestDupFind_InvalidFormatRejected(t *testing.T) {
	code, _ := run(t, "", "find-duplicates", "--format", "xml", "/tmp")
	if code != ExitUsage {
		t.Fatalf("invalid --format must exit %d, got %d", ExitUsage, code)
	}
}
