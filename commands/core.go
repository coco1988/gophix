// Package commands implements the gophix subcommands.
package commands

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/takeout"
)

// Process exit codes.
const (
	ExitOK         = 0
	ExitErrors     = 1
	ExitUsage      = 2
	ExitNoExiftool = 3
)

const version = "2.0.0"

// globalOpts holds flags shared by the subcommands.
type globalOpts struct {
	DryRun   bool
	Verbose  bool
	Timezone string // IANA zone or ±HH:MM; empty = write UTC digits for JSON dates
	Jobs     int
}

// cleanPathArg repairs path arguments mangled by Windows shell quoting
// (PowerShell turns a trailing backslash inside quotes into a stray quote).
func cleanPathArg(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	if p == "" {
		return p
	}
	if len(p) > 1 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') {
		isRoot := strings.HasSuffix(p, ":\\") || p == "/" || p == "\\"
		if !isRoot {
			p = p[:len(p)-1]
		}
	}
	return filepath.Clean(p)
}

// --- directory walking -------------------------------------------------------

// dirContext bundles everything known about one directory during a scan.
type dirContext struct {
	path    string
	matcher *takeout.Matcher
}

// walkDirs visits every directory under root exactly once, depth-first,
// listing its entries a single time (the same enumeration feeds both the
// recursion and the sidecar matcher).
func walkDirs(root string, fn func(dc *dirContext) error) error {
	return walkDirRec(root, fn)
}

func walkDirRec(path string, fn func(dc *dirContext) error) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	dc := &dirContext{path: path, matcher: takeout.NewMatcher(entries)}
	if err := fn(dc); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := walkDirRec(filepath.Join(path, e.Name()), fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- worker pool -------------------------------------------------------------

// runPool distributes indices 0..n-1 to workers and streams results to collect.
func runPool[R any](n int, work func(int) R, collect func(R)) {
	if n == 0 {
		return
	}
	workers := meta.MaxJobs()
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}
	jobCh := make(chan int)
	resCh := make(chan R, workers*2)
	var feed sync.WaitGroup
	var drain sync.WaitGroup

	drain.Add(1)
	go func() {
		defer drain.Done()
		for r := range resCh {
			collect(r)
		}
	}()
	for w := 0; w < workers; w++ {
		feed.Add(1)
		go func() {
			defer feed.Done()
			for i := range jobCh {
				resCh <- work(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobCh <- i
	}
	close(jobCh)
	feed.Wait()
	close(resCh)
	drain.Wait()
}

// bulkRead fills dst[i]/failed[i] for each paths[i] using chunked parallel
// ReadMany invocations. One unreadable file never affects the others.
func bulkRead(paths []string, dst []meta.Info, failed []error) {
	const chunk = meta.ReadChunkSize
	n := len(paths)
	if n == 0 {
		return
	}
	type chunkRes struct {
		infos map[string]meta.Info
		errs  map[string]error
	}
	res := make([]chunkRes, (n+chunk-1)/chunk)
	runPool(len(res),
		func(ci int) struct{} {
			lo, hi := ci*chunk, min(n, (ci+1)*chunk)
			infos, errs := meta.ReadMany(paths[lo:hi])
			res[ci] = chunkRes{infos, errs} // disjoint index per worker
			return struct{}{}
		},
		func(struct{}) {})
	for i, p := range paths {
		if info, ok := res[i/chunk].infos[p]; ok {
			dst[i] = info
		} else if e, ok := res[i/chunk].errs[p]; ok {
			failed[i] = e
		} else if failed[i] == nil {
			failed[i] = fmt.Errorf("metadata read produced no result")
		}
	}
}

// flushIfBuffered makes queued output visible immediately.
func flushIfBuffered(stdout io.Writer) {
	if f, ok := stdout.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

// --- hashing / copying -------------------------------------------------------

const hashLen = 6

func shortHash(s string) string {
	if len(s) <= hashLen {
		return s
	}
	return s[:hashLen]
}

// hashFile streams the SHA-256 of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyVerified copies src to dst and verifies the written bytes via SHA-256.
func copyVerified(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("cannot open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dst, err)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		os.Remove(dst)
		return "", fmt.Errorf("cannot copy %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("cannot write %s: %w", dst, err)
	}

	written, err := hashFile(dst)
	if err != nil {
		return hex.EncodeToString(h.Sum(nil)), fmt.Errorf("cannot verify %s: %w", dst, err)
	}
	want := hex.EncodeToString(h.Sum(nil))
	if written != want {
		return want, fmt.Errorf("copy verification failed for %s (hash mismatch)", dst)
	}
	return want, nil
}

// confirm asks the user to approve an action. It refuses cleanly (with
// actionable guidance) when stdin cannot provide an answer - e.g. inside
// PowerShell ISE, VS Code debug consoles, or any wrapper that closes stdin.
func confirm(stdin io.Reader, stdout io.Writer, action string) (bool, error) {
	if stdin == os.Stdin {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
			return false, fmt.Errorf(
				"stdin is not interactive (no terminal attached); re-run with --yes to skip the confirmation")
		}
	}
	flushIfBuffered(stdout)
	r := bufio.NewReader(stdin)
	fmt.Fprintf(stdout, "Really %s? [y/N] ", action)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf(
			"no interactive confirmation possible (%v); re-run with --yes to skip the prompt", err)
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}
