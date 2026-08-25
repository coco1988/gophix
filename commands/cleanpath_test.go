package commands

import (
	"runtime"
	"testing"
)

func TestCleanPathArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Users\laptop\Fotos von 2015"`, `C:\Users\laptop\Fotos von 2015`},
		{`"C:\Users\laptop\Fotos von 2015"`, `C:\Users\laptop\Fotos von 2015`},
		{`C:\dir\`, `C:\dir`},
		{"/tmp/dir/", "/tmp/dir"},
		{"  /tmp/dir  ", `/tmp/dir`},
		{`C:\`, `C:\`}, // volume root preserved
		{"/", "/"},     // filesystem root preserved
	}
	for _, c := range cases {
		if got := cleanPathArg(c.in); got != c.want {
			t.Errorf("cleanPathArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if runtime.GOOS == "windows" {
		// Separator normalization is a native-Clean behavior; verify on Windows.
		if got := cleanPathArg(`C:\a\b\..\c`); got != `C:\a\c` {
			t.Errorf("windows normalize: %q", got)
		}
	}
}
