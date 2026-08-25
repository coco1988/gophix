package commands

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexdachin/gophix/meta"
)

// compatibleExt pairs extensions considered equal for renaming purposes
// (gophix keeps names stable whenever possible).
var compatibleExt = map[string]string{
	".jpg": ".jpeg",
	".tif": ".tiff",
	".m4v": ".mp4",
	".mov": ".mp4",
}

// fixExtensionFromInfo decides on an extension correction using the format
// information already read by meta.Read (File:FileTypeExtension) - no extra
// ExifTool invocation. Returns the effective path, the planned rename target
// when a rename would happen (dry-run never renames) and an optional warning.
//
// An unreliable detection means the file keeps its name; metadata processing
// continues regardless.
func fixExtensionFromInfo(mediaPath string, info meta.Info, opts *globalOpts, stdout io.Writer) (string, string, string) {
	currentExt := filepath.Ext(mediaPath)
	detected, _ := info.Str("File:FileTypeExtension")
	detected = strings.ToLower(strings.TrimSpace(detected))
	if !meta.SaneExtension(detected) {
		return mediaPath, "", "could not reliably determine the file type; keeping its name"
	}
	newExt := "." + detected
	if extsCompatible(currentExt, newExt) {
		return mediaPath, "", ""
	}

	base := strings.TrimSuffix(mediaPath, currentExt)
	target := uniqueName(base + newExt)
	if opts.DryRun {
		// No print here: the collector reports the planned rename uniformly.
		return mediaPath, target, ""
	}
	if err := os.Rename(mediaPath, target); err != nil {
		return mediaPath, "", fmt.Sprintf("cannot rename: %v", err)
	}
	fmt.Fprintf(stdout, "🔄 renamed %s -> %s\n", filepath.Base(mediaPath), filepath.Base(target))
	return target, "", ""
}

func extsCompatible(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	for k, v := range compatibleExt {
		if (strings.EqualFold(a, k) && strings.EqualFold(b, v)) ||
			(strings.EqualFold(a, v) && strings.EqualFold(b, k)) {
			return true
		}
	}
	return false
}

// uniqueName returns name, or a variant with a random suffix if it exists.
func uniqueName(name string) string {
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 10; i++ {
		cand := fmt.Sprintf("%s-%s%s", base, randomSuffix(5), ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return name
}

func randomSuffix(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
