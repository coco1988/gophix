package meta

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Batch execution keeps a small pool of long-running ExifTool processes
// (exiftool -stay_open True -@ -). This removes the Perl interpreter startup
// cost from every call - the dominant bottleneck when processing tens of
// thousands of files. If batch mode cannot start, Exec falls back to spawning
// one process per call.

type batchProc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	seq int
}

func startBatch() (*batchProc, error) {
	cmd := exec.Command("exiftool", "-stay_open", "True", "-@", "-")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr // warnings stay on the terminal, out of parsed data
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &batchProc{cmd: cmd, in: in, out: bufio.NewReader(outPipe)}, nil
}

// run streams one command group and collects stdout up to the {readyN} sentinel.
func (b *batchProc) run(args []string) ([]byte, error) {
	b.seq++
	var sb strings.Builder
	for _, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return nil, fmt.Errorf("batch arguments must not contain newlines")
		}
		sb.WriteString(a)
		sb.WriteByte('\n')
	}
	sentinel := "{ready" + strconv.Itoa(b.seq) + "}"
	sb.WriteString("-execute" + strconv.Itoa(b.seq) + "\n")

	if _, err := io.WriteString(b.in, sb.String()); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	for {
		line, err := b.out.ReadString('\n')
		if err != nil {
			return buf.Bytes(), err
		}
		t := strings.TrimRight(line, "\r\n")
		if t == sentinel {
			break
		}
		buf.WriteString(line)
	}
	return buf.Bytes(), nil
}

func (b *batchProc) close() {
	io.WriteString(b.in, "-stay_open\nFalse\n")
	b.in.Close()
	done := make(chan struct{})
	go func() { _ = b.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		if b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
			<-done
		}
	}
}

func (b *batchProc) alive() bool {
	return b.cmd != nil && b.cmd.Process != nil && b.cmd.ProcessState == nil
}

var pool = struct {
	sync.Mutex
	idle []*batchProc
	sem  chan struct{}
	max  int
}{max: 0} // 0 = not configured yet

// ConfigureJobs sets the maximum number of concurrent ExifTool processes.
// n <= 0 selects an automatic value (number of CPUs, capped at 8).
// GOPHIX_JOBS overrides the automatic value. First configuration wins;
// later calls are no-ops (Exec auto-initializes on first use).
func ConfigureJobs(n int) {
	pool.Lock()
	defer pool.Unlock()
	configureLocked(n)
}

func configureLocked(n int) {
	if pool.max != 0 {
		return // already configured
	}
	if n <= 0 {
		n = runtime.NumCPU()
		if env := strings.TrimSpace(os.Getenv("GOPHIX_JOBS")); env != "" {
			if v, err := strconv.Atoi(env); err == nil && v > 0 {
				n = v
			}
		}
		if n > 8 {
			n = 8
		}
		if n < 1 {
			n = 1
		}
	}
	pool.max = n
	pool.sem = make(chan struct{}, n)
}

// useBatch reports whether stay_open batching is enabled. It can be disabled
// for debugging via GOPHIX_NO_BATCH=1.
func useBatch() bool { return os.Getenv("GOPHIX_NO_BATCH") == "" }

// Exec runs one exiftool invocation (args without the program name) and
// returns combined stdout of the command. An error is returned when exiftool
// reported failure (non-zero exit in spawn mode, "Error:" lines in batch mode).
func Exec(args []string) ([]byte, error) {
	if !useBatch() {
		return runExiftool(args...)
	}
	pool.Lock()
	configureLocked(0)
	sem := pool.sem
	pool.Unlock()

	sem <- struct{}{}
	defer func() { <-sem }()

	bp := takeIdle()
	if bp == nil {
		nb, err := startBatch()
		if err != nil {
			// Graceful degradation: fall back to a plain spawn for this call.
			return runExiftool(args...)
		}
		bp = nb
	}

	out, err := bp.run(args)
	if err != nil || !bp.alive() {
		// Process died mid-command; retry once with a fresh one.
		bp.close()
		nb, serr := startBatch()
		if serr == nil {
			out2, err2 := nb.run(args)
			if err2 == nil && nb.alive() {
				release(nb)
				return judge(out2)
			}
			nb.close()
		}
		if err == nil {
			err = fmt.Errorf("exiftool batch process terminated unexpectedly")
		}
		return out, err
	}
	release(bp)
	return judge(out)
}

func takeIdle() *batchProc {
	pool.Lock()
	defer pool.Unlock()
	if len(pool.idle) > 0 {
		bp := pool.idle[len(pool.idle)-1]
		pool.idle = pool.idle[:len(pool.idle)-1]
		return bp
	}
	return nil
}

func release(bp *batchProc) {
	pool.Lock()
	pool.idle = append(pool.idle, bp)
	pool.Unlock()
}

// judge converts ExifTool's textual failure signals into an error, mirroring
// the exit-code semantics of spawn mode.
func judge(out []byte) ([]byte, error) {
	var errs []string
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Error:") ||
			strings.Contains(t, "files weren't updated due to errors") ||
			strings.Contains(t, "file wasn't updated due to errors") {
			errs = append(errs, t)
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

// MaxJobs returns the configured worker/process limit (after auto-init).
func MaxJobs() int {
	pool.Lock()
	configureLocked(0)
	defer pool.Unlock()
	return pool.max
}

// CloseAll shuts down pooled ExifTool processes. Called on process exit;
// exported for tests.
func CloseAll() {
	pool.Lock()
	defer pool.Unlock()
	for _, bp := range pool.idle {
		bp.close()
	}
	pool.idle = nil
}
