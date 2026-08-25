package meta

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Info holds raw metadata values read back from a media file, keyed by
// "GROUP1:Tag" (exiftool -G1). Values are strings or numbers rendered as
// strings; date fields keep their literal tag representation.
type Info map[string]string

// readTags are the fields requested from ExifTool for verification and
// planning, using exact family-1 group names. Missing tags are simply
// omitted from the JSON output. GPS values come back as signed decimals
// because of the -n flag.
var readTags = []string{
	"-ExifIFD:DateTimeOriginal",
	"-ExifIFD:CreateDate",
	"-ExifIFD:OffsetTimeOriginal",
	"-ExifIFD:OffsetTimeDigitized",
	"-ExifIFD:OffsetTime",
	"-IFD0:ModifyDate",
	"-IFD0:ImageDescription",
	"-GPS:GPSLatitude",
	"-GPS:GPSLatitudeRef",
	"-GPS:GPSLongitude",
	"-GPS:GPSLongitudeRef",
	"-GPS:GPSAltitude",
	"-GPS:GPSDateStamp",
	"-GPS:GPSTimeStamp",
	"-IPTC:Caption-Abstract",
	"-XMP-dc:Description",
	"-XMP-dc:Title",
	"-XMP-exif:DateTimeOriginal",
	"-XMP-xmp:CreateDate",
	"-QuickTime:CreateDate",
	"-QuickTime:ModifyDate",
	"-Track1:MediaCreateDate",
	"-Track1:MediaModifyDate",
	"-Track1:TrackCreateDate",
	"-Track1:TrackModifyDate",
	"-File:FileType",
	"-File:FileTypeExtension",
	"-System:FileModifyDate",
}

// Read loads the relevant metadata of path via exiftool.
func Read(path string) (Info, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("cannot read metadata of %s: %w", path, err)
	}
	args := []string{"-j", "-n", "-G1", "-q"}
	args = append(args, readTags...)
	args = append(args, path)

	out, err := Exec(args)
	if err != nil {
		return nil, fmt.Errorf("cannot read metadata of %s: %v", path, err)
	}

	var docs []map[string]any
	if err := json.Unmarshal(out, &docs); err != nil {
		return nil, fmt.Errorf("cannot parse exiftool output for %s: %w", path, err)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("exiftool returned no metadata for %s", path)
	}

	info := Info{}
	for k, v := range docs[0] {
		info[k] = stringify(v)
	}
	return info, nil
}

// ReadChunkSize is the number of files one ReadMany ExifTool invocation
// processes. Chunking amortizes the per-call round-trip over many files
// without inflating a single call's output beyond comfortable parsing size.
const ReadChunkSize = 32

// ReadMany reads the relevant metadata of several files with one ExifTool
// invocation and returns one Info per successfully read file, keyed by the
// exact path string that was passed in (ExifTool echoes it as "SourceFile").
//
// One problematic file never poisons the batch: unreadable or erroring files
// are reported individually in errs and simply do not appear in infos.
func ReadMany(paths []string) (map[string]Info, map[string]error) {
	infos := make(map[string]Info, len(paths))
	errs := make(map[string]error)
	want := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			errs[p] = fmt.Errorf("cannot read metadata of %s: %w", p, err)
			continue
		}
		want = append(want, p)
	}
	if len(want) == 0 {
		return infos, errs
	}

	args := make([]string, 0, len(readTags)+len(want)+4)
	// -q suppresses ExifTool's per-execute "N image files read" progress
	// summary, which stay_open mode emits on stderr; data output is unaffected.
	args = append(args, "-j", "-n", "-G1", "-q")
	args = append(args, readTags...)
	args = append(args, want...)

	out, execErr := Exec(args)

	var docs []map[string]any
	unmarshalErr := json.Unmarshal(out, &docs)
	if unmarshalErr != nil || (execErr != nil && len(docs) == 0) {
		cause := execErr
		if unmarshalErr != nil {
			cause = unmarshalErr
		}
		for _, p := range want {
			errs[p] = fmt.Errorf("cannot read metadata of %s: %v", p, cause)
		}
		return infos, errs
	}

	seen := make(map[string]struct{}, len(docs))
	for _, doc := range docs {
		sf, _ := doc["SourceFile"].(string)
		if sf == "" {
			continue
		}
		if e, ok := doc["Error"]; ok && stringify(e) != "" {
			errs[sf] = fmt.Errorf("exiftool error for %s: %s", sf, stringify(e))
			continue
		}
		info := Info{}
		for k, v := range doc {
			if k == "SourceFile" {
				continue
			}
			info[k] = stringify(v)
		}
		infos[sf] = info
		seen[sf] = struct{}{}
	}
	for _, p := range want {
		if _, ok := seen[p]; !ok {
			if _, already := errs[p]; !already {
				errs[p] = fmt.Errorf("exiftool returned no metadata for %s", p)
			}
		}
	}
	return infos, errs
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// Str returns the value for key (any group) when present.
func (i Info) Str(key string) (string, bool) {
	v, ok := i[key]
	return v, ok && v != ""
}

// Float returns a numeric value for key.
func (i Info) Float(key string) (float64, bool) {
	s, ok := i.Str(key)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// FileType returns the ExifTool file type (e.g. JPEG, PNG, MP4).
func (i Info) FileType() string {
	ft, _ := i.Str("File:FileType")
	return ft
}

// videoTypes are container types whose creation dates live in QuickTime atoms.
var videoTypes = map[string]struct{}{
	"MP4": {}, "M4V": {}, "MOV": {}, "3GP": {}, "3G2": {}, "WMV": {}, "FLV": {},
}

// IsVideo reports whether the file type uses QuickTime-style date atoms.
func (i Info) IsVideo() bool {
	_, ok := videoTypes[i.FileType()]
	return ok
}
