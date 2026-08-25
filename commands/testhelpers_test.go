package commands

import (
	"bytes"
	"os"
	"testing"

	"github.com/alexdachin/gophix/meta"
)

// run executes a command with captured process stdout (some progress lines
// are written via fmt to os.Stdout by deep helpers).
func run(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()

	var in bytes.Buffer
	in.WriteString(stdin)
	code := Run(args, w, w, &in)
	t.Cleanup(func() { meta.CloseAll() }) // reap any pooled exiftool holding test pipes

	w.Close()
	out := <-done
	os.Stdout = old
	return code, out
}

func requireOK(t *testing.T, out string, code int) {
	t.Helper()
	if code != ExitOK {
		t.Fatalf("exit=%d output:\n%s", code, out)
	}
}

func readInfo(t *testing.T, path string) meta.Info {
	t.Helper()
	info, err := meta.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func hasExiftool(t *testing.T) {
	t.Helper()
	if err := meta.Available(); err != nil {
		t.Skip("exiftool not available")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
