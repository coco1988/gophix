package meta

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTinyJPEG(t *testing.T, path string) {
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

// ReadMany must return one Info per readable file and isolate unreadable
// ones as individual errors instead of failing the whole batch.
func TestReadMany_SplitsResultsAndErrors(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip("exiftool not available")
	}
	t.Cleanup(CloseAll) // shut down pooled stay_open exiftool so go test's pipes close
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jpg")
	b := filepath.Join(dir, "b.jpg")
	missing := filepath.Join(dir, "missing.jpg")
	writeTinyJPEG(t, a)
	writeTinyJPEG(t, b)

	infos, errs := ReadMany([]string{a, b, missing})

	if len(errs) != 1 {
		t.Fatalf("want exactly 1 error, got %v", errs)
	}
	me, ok := errs[missing]
	if !ok {
		t.Fatalf("error not reported for missing file: %v", errs)
	}
	if !strings.Contains(me.Error(), "missing.jpg") && !strings.Contains(me.Error(), "no metadata") {
		t.Errorf("unexpected error text: %v", me)
	}
	if len(infos) != 2 {
		t.Fatalf("want 2 infos, got %d (%v)", len(infos), infos)
	}
	for _, p := range []string{a, b} {
		info, ok := infos[p]
		if !ok {
			t.Fatalf("no info for %s", p)
		}
		if info.FileType() != "JPEG" {
			t.Errorf("%s FileType = %q, want JPEG", p, info.FileType())
		}
	}
}

// ExifTool echoes SourceFile in its own path form; on Windows that means
// forward slashes and possibly a different drive-letter case. The bulk-read
// must still map results back to the caller's exact path strings.
func TestMatchSourceFile_TolerantForms(t *testing.T) {
	win := `C:\Users\Mama\Takeout\IMG_1.jpg`
	wanted := map[string]string{
		normKey(win):                       win,
		normKey("/data/Takeout/IMG_2.jpg"): "/data/Takeout/IMG_2.jpg",
	}

	cases := []struct{ echo, want string }{
		{`C:\Users\Mama\Takeout\IMG_1.jpg`, win},               // exact as passed
		{`C:/Users/Mama/Takeout/IMG_1.jpg`, win},               // forward slashes
		{`c:/users/mama/takeout/img_1.jpg`, win},               // case differences
		{"/data/Takeout/IMG_2.jpg", "/data/Takeout/IMG_2.jpg"}, // unix exact
	}
	for _, tc := range cases {
		got, ok := matchSourceFile(tc.echo, wanted)
		if !ok || got != tc.want {
			t.Errorf("matchSourceFile(%q) = %q,%v; want %q", tc.echo, got, ok, tc.want)
		}
	}
	if _, ok := matchSourceFile(`D:\somewhere\else.jpg`, wanted); ok {
		t.Error("unrelated SourceFile must not match")
	}
}

// A single corrupt/unreadable media file inside one chunk must not poison
// the results of the other files in the same ExifTool call.
func TestReadMany_BadFileDoesNotPoisonChunk(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip("exiftool not available")
	}
	t.Cleanup(CloseAll)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.jpg")
	writeTinyJPEG(t, good)

	junk := filepath.Join(dir, "junk.jpg")
	if err := os.WriteFile(junk, []byte("this is not an image at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	infos, errs := ReadMany([]string{good, junk})

	if info, ok := infos[good]; !ok || info.FileType() != "JPEG" {
		t.Fatalf("good file lost when batch contained junk: infos=%v errs=%v", infos, errs)
	}
	if e, ok := errs[junk]; ok {
		t.Logf("junk correctly rejected: %v", e)
	}
}
