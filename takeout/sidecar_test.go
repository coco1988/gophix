package takeout

import (
	"os"
	"strings"
	"testing"
)

func mkMatcher(t *testing.T, names ...string) *Matcher {
	t.Helper()
	entries := make([]os.DirEntry, 0, len(names))
	for _, n := range names {
		entries = append(entries, fakeDirEntry{name: n, dir: strings.HasSuffix(n, "/")})
	}
	return NewMatcher(entries)
}

func matchName(t *testing.T, names []string, media string) Match {
	t.Helper()
	return mkMatcher(t, names...).Match(media)
}

func TestMatch_SupplementalMetadata(t *testing.T) {
	m := matchName(t, []string{"IMG.jpg", "IMG.jpg.supplemental-metadata.json"}, "IMG.jpg")
	if !m.Found() || m.FileName != "IMG.jpg.supplemental-metadata.json" || !m.Exact {
		t.Fatalf("got %+v", m)
	}
}

func TestMatch_TruncatedSuffixes(t *testing.T) {
	variants := []string{
		"IMG.jpg.supplemental-metadat.json",
		"IMG.jpg.supplemental-metada.json",
		"IMG.jpg.supplemental-metad.json",
		"IMG.jpg.supplemental-meta.json",
		"IMG.jpg.supplemental-met.json",
		"IMG.jpg.supplemental.json",
	}
	for _, v := range variants {
		m := matchName(t, []string{"IMG.jpg", v}, "IMG.jpg")
		if !m.Found() {
			t.Errorf("variant %s not matched", v)
			continue
		}
		if !m.Exact {
			t.Errorf("variant %s should be an exact-list priority match", v)
		}
	}
}

func TestMatch_GenericTruncated(t *testing.T) {
	// A truncation not present in the exact list still matches via the
	// generic rule, but is flagged non-exact.
	m := matchName(t, []string{"IMG.jpg", "IMG.jpg.supplemental-meta.jsonx"}, "IMG.jpg")
	if m.Found() {
		t.Fatalf("should not match .jsonx")
	}
	m2names := []string{"IMG.heic", "IMG.heic.supplemental.json"}
	_ = m2names
	// generic middle truncation like "-metadat" is in the exact list; craft one outside:
	names := []string{"PXL.png", "PXL.png.supplemental-met.json"}
	_ = names
	// true generic case: shorter prefix like "-met" exists in list; use "-me":
	m = matchName(t, []string{"A.mov", "A.mov.supplemental-me.json"}, "A.mov")
	if !m.Found() || m.Exact {
		t.Fatalf("generic truncated rule: got %+v, want found & non-exact", m)
	}
	// Unrelated suffixes must never match. Note: "-META" is a case variant
	// of the exact "-metadata" pattern and therefore legitimately matches
	// under the required case-insensitive matching.
	for _, bad := range []string{"A.mov.supplemental-metadata-old.json", "A.mov.supplemental9.json", "A.mov.supplemental-m3ta.json", "A.mov.supplemental-metadata.bak.json"} {
		m = matchName(t, []string{"A.mov", bad}, "A.mov")
		if m.Found() && m.FileName == bad {
			t.Fatalf("unrelated file %s must not be selected", bad)
		}
	}
	m = matchName(t, []string{"A.mov", "A.mov.supplemental-META.json"}, "A.mov")
	if !m.Found() || !m.Exact {
		t.Fatalf("case variant of exact suffix must match exactly: %+v", m)
	}
}

func TestMatch_LegacyFullNameAndBasename(t *testing.T) {
	m := matchName(t, []string{"IMG.jpg", "IMG.jpg.json"}, "IMG.jpg")
	if !m.Found() || m.FileName != "IMG.jpg.json" {
		t.Fatalf("legacy full-name: %+v", m)
	}
	m = matchName(t, []string{"IMG.jpg", "IMG.json"}, "IMG.jpg")
	if !m.Found() || m.FileName != "IMG.json" {
		t.Fatalf("legacy basename: %+v", m)
	}
}

func TestMatch_GenericExcluded(t *testing.T) {
	for _, generic := range []string{"metadata.json", "Metadaten.json", "album.json", "shared_album_comments.json"} {
		names := []string{"IMG.jpg", "IMG.jpg.supplemental-metadata.json"}
		if !strings.EqualFold(generic, "Metadaten.json") {
			names = append(names, generic)
		} else {
			names = append(names, generic)
		}
		m := matchName(t, names, "IMG.jpg")
		if m.Found() && m.FileName == generic {
			t.Fatalf("generic %s must never be selected", generic)
		}
		if m.Found() && m.FileName != "IMG.jpg.supplemental-metadata.json" {
			t.Fatalf("expected supplemental sidecar, got %s", m.FileName)
		}
	}
	// A lone generic file next to media without its own sidecar matches nothing.
	m := matchName(t, []string{"IMG.jpg", "Metadaten.json"}, "IMG.jpg")
	if m.Found() {
		t.Fatalf("Metadaten.json matched: %+v", m)
	}
}

func TestMatch_CaseInsensitive(t *testing.T) {
	m := matchName(t, []string{"img.JPG", "IMG.JPG.SUPPLEMENTAL-METADATA.JSON"}, "img.JPG")
	if !m.Found() || !strings.HasSuffix(m.FileName, ".JSON") {
		t.Fatalf("case-insensitive supplemental: %+v", m)
	}
	m = matchName(t, []string{"Photo.jpeg", "photo.JSON"}, "Photo.jpeg")
	if !m.Found() || m.FileName != "photo.JSON" {
		t.Fatalf("case-insensitive legacy: %+v", m)
	}
}

func TestMatch_PriorityAndWarnings(t *testing.T) {
	// Exact current format beats legacy when both exist.
	names := []string{"IMG.jpg", "IMG.jpg.json", "IMG.jpg.supplemental-metadata.json"}
	m := matchName(t, names, "IMG.jpg")
	if m.FileName != "IMG.jpg.supplemental-metadata.json" {
		t.Fatalf("priority: got %s", m.FileName)
	}
	// Non-exact selections are flagged.
	m = matchName(t, []string{"A.mov", "A.mov.supplemental-me.json"}, "A.mov")
	if m.Exact {
		t.Fatal("generic truncated selection must be non-exact (warning)")
	}
	// Candidates list records alternatives considered.
	if len(m.Cands) == 0 || m.Cands[0] != "A.mov.supplemental-me.json" {
		t.Fatalf("candidates not recorded: %+v", m.Cands)
	}
}

func TestMatch_LegacyHeuristics(t *testing.T) {
	// -edited stripping
	m := matchName(t, []string{"IMG-edited.jpg", "IMG.jpg.json"}, "IMG-edited.jpg")
	if !m.Found() {
		t.Fatalf("-edited heuristic failed: %+v", m)
	}
	// mp4 photo-sidecar name
	m = matchName(t, []string{"VID.mp4", "PHOTO.jpg.json"}, "VID.mp4")
	if m.Found() {
		t.Fatal("unrelated photo json must not match mp4 without shared stem")
	}
	m = matchName(t, []string{"VID.mp4", "VID.jpg.json"}, "VID.mp4")
	if !m.Found() || m.FileName != "VID.jpg.json" {
		t.Fatalf("mp4 photo-sidecar heuristic: %+v", m)
	}
	// duplicate numbering move IMG(1).jpg -> IMG.jpg(1).json
	m = matchName(t, []string{"IMG(1).jpg", "IMG.jpg(1).json"}, "IMG(1).jpg")
	if !m.Found() || m.FileName != "IMG.jpg(1).json" {
		t.Fatalf("numbering move: %+v", m)
	}
}

func TestMatch_ReverseRenamed(t *testing.T) {
	// After extension fixing, media is .png but sidecar still references .jpg.
	m := matchName(t, []string{"IMG.png", "IMG.jpg.supplemental-metadata.json"}, "IMG.png")
	if !m.Found() || m.FileName != "IMG.jpg.supplemental-metadata.json" || m.Exact {
		t.Fatalf("reverse-renamed: %+v", m)
	}
	// Must not hijack when another medium directly owns the sidecar.
	mt := mkMatcher(t, "IMG.png", "IMG.jpg", "IMG.jpg.supplemental-metadata.json")
	_ = mt.Match("IMG.jpg") // direct claim first
	m = mt.Match("IMG.png")
	if m.Found() {
		t.Fatalf("reverse rule stole claimed sidecar: %+v", m)
	}
}

func TestClassify(t *testing.T) {
	mt := mkMatcher(t, "a.jpg", "a.jpg.supplemental-metadata.json", "b.xmp", "Metadaten.json", "Thumbs.db", ".hidden", "subdir/")
	c := mt.Classify()
	if len(c.Media) != 1 || c.Media[0] != "a.jpg" {
		t.Fatalf("media classification: %+v", c.Media)
	}
	if len(c.Jsons) != 2 {
		t.Fatalf("json classification: %+v", c.Jsons)
	}
}

type fakeDirEntry struct {
	name string
	dir  bool
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.dir }
func (f fakeDirEntry) Type() os.FileMode          { return 0 }
func (f fakeDirEntry) Info() (os.FileInfo, error) { return nil, os.ErrNotExist }
