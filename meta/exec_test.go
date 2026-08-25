package meta

import "testing"

func TestSaneExtension(t *testing.T) {
	for _, ok := range []string{"jpg", "jpeg", "mp4", "3gp", "heic", "tif"} {
		if !SaneExtension(ok) {
			t.Errorf("%q must be accepted", ok)
		}
	}
	// Polluted values from ExifTool warnings or multi-line output.
	for _, bad := range []string{
		"warning: [minor] unrecognized makernotes [x2] - c:/users/x/img.jpg",
		"jpg\nwarning: something",
		"",
		".",
		"too-long-extension",
		"has space",
	} {
		if SaneExtension(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
