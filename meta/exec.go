// Package meta reads and writes media metadata through ExifTool and
// implements gophix's timezone policy.
package meta

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Available verifies that the exiftool executable can be found via PATH.
func Available() error {
	path, err := exec.LookPath("exiftool")
	if err != nil {
		return fmt.Errorf("exiftool not found in PATH; install it with 'sudo apt install libimage-exiftool-perl' (Windows/macOS: https://exiftool.org) and make sure 'exiftool' is on PATH")
	}
	_ = path
	return nil
}

func runExiftool(args ...string) ([]byte, error) {
	cmd := exec.Command("exiftool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("exiftool %s: %w\noutput: %s", args[0], err, string(out))
	}
	return out, nil
}

// FileTypeExtension returns the canonical extension ExifTool assigns to the
// file's detected format (e.g. "jpg" for JPEG data).
//
// Only stdout is used: ExifTool prints warnings (e.g. "[minor] unrecognized
// makernotes") to stderr, and those must never leak into the detected value.
func FileTypeExtension(path string) (string, error) {
	cmd := exec.Command("exiftool", "-q", "-q", "-p", ".$FileTypeExtension", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(out))
		}
		if detail != "" {
			return "", fmt.Errorf("cannot determine file type of %s: %v: %s", path, err, detail)
		}
		return "", fmt.Errorf("cannot determine file type of %s: %w", path, err)
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.TrimSpace(line), nil
}

// saneExtRe guards against polluted detection values.
var saneExtRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// SaneExtension reports whether s looks like a plain file extension that is
// safe to use for renaming ("jpg", "mp4", ...).
func SaneExtension(s string) bool {
	return saneExtRe.MatchString(s)
}
