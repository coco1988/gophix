package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/report"
	"github.com/alexdachin/gophix/takeout"
)

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

// matchAndParse finds the sidecar for mediaName in dc and parses it.
// Diagnostics are emitted here, during the sequential scan phase.
func matchAndParse(dc *dirContext, mediaName string, g *globalOpts, sum *report.Summary) (*takeout.Sidecar, takeout.Match) {
	m := dc.matcher.Match(mediaName)
	if !m.Found() {
		return nil, m
	}
	if !m.Exact && g.Verbose {
		sum.Warnf("non-exact sidecar selection for %s: %s", mediaName, m.Describe())
	}
	data, err := os.ReadFile(filepath.Join(dc.path, m.FileName))
	if err != nil {
		sum.Errorf("cannot read sidecar %s: %v", filepath.Join(dc.path, m.FileName), err)
		return nil, m
	}
	sc, err := takeout.Parse(data)
	if err != nil {
		sum.Errorf("invalid sidecar %s (in %s): %v", m.FileName, dc.path, err)
		return nil, m
	}
	return sc, m
}

// --- fix pipeline ------------------------------------------------------------
//
// Cheap scanning/matching runs sequentially; the ExifTool-heavy per-file work
// is distributed over a bounded worker pool.

type fixJob struct {
	mediaPath string
	sc        *takeout.Sidecar // nil = repair dates only from embedded/filename/mtime
	matched   bool             // a sidecar file was matched
	cur       meta.Info        // metadata pre-read in bulk before the worker pool
	readErr   error            // non-nil when the bulk read failed for this file
}

type fixResult struct {
	path          string
	outcome       meta.Outcome
	fsRes         meta.FSResult
	source        string
	pattern       string
	err           error
	warnings      []string
	noSidecar     bool
	untransferred []string
	renameLine    string // informational rename notice
}

func runFix(root string, g *globalOpts, clock *meta.Clock, sum *report.Summary, stdout io.Writer) int {
	if fi, err := os.Stat(root); err != nil {
		sum.Errorf("cannot access %s: %v", root, err)
		return ExitErrors
	} else if !fi.IsDir() {
		sum.Errorf("%s is not a directory", root)
		return ExitErrors
	}

	var jobs []fixJob
	err := walkDirs(root, func(dc *dirContext) error {
		sum.DirsScanned++
		fmt.Fprintf(stdout, "📓 processing %s\n", dc.path)

		cls := dc.matcher.Classify()
		for _, mediaName := range cls.Media {
			sum.MediaFound++
			mediaPath := filepath.Join(dc.path, mediaName)
			sc, m := matchAndParse(dc, mediaName, g, sum)
			if sc == nil && m.Found() {
				// Invalid/unreadable sidecar already reported above.
				sum.Failed++
				continue
			}
			jobs = append(jobs, fixJob{mediaPath: mediaPath, sc: sc, matched: m.Found()})
		}

		for _, j := range dc.matcher.Unclaimed() {
			sum.JSONKeptUnmatched++
			if g.Verbose {
				fmt.Fprintf(stdout, "   unmatched sidecar (left alone): %s\n", filepath.Join(dc.path, j))
			}
		}
		return nil
	})
	if err != nil {
		sum.Errorf("scan failed: %v", err)
		return ExitErrors
	}

	// One bulk metadata read per file before the write phase; workers reuse
	// it for planning and the already-correct check.
	paths := make([]string, len(jobs))
	for i := range jobs {
		paths[i] = jobs[i].mediaPath
	}
	infos := make([]meta.Info, len(jobs))
	readFails := make([]error, len(jobs))
	bulkRead(paths, infos, readFails)
	for i := range jobs {
		jobs[i].cur, jobs[i].readErr = infos[i], readFails[i]
	}

	runPool(len(jobs),
		func(i int) fixResult { return processFixJob(jobs[i], g, clock, stdout) },
		func(r fixResult) { collectFixResult(r, sum, stdout, g) })

	if sum.MediaFound == 0 && sum.Failed == 0 {
		fmt.Fprintln(stdout, "no media files found - nothing was changed")
	}
	if sum.HasErrors() {
		return ExitErrors
	}
	return ExitOK
}

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

func processFixJob(job fixJob, g *globalOpts, clock *meta.Clock, stdout io.Writer) fixResult {
	res := fixResult{path: job.mediaPath, outcome: meta.OutcomeFailed}
	mediaPath := job.mediaPath
	res.noSidecar = !job.matched

	if job.readErr != nil {
		res.err = job.readErr
		return res
	}
	curInfo := job.cur

	newPath, plannedRename, warn := fixExtensionFromInfo(mediaPath, curInfo, g, stdout)
	if warn != "" {
		res.warnings = append(res.warnings, warn)
	}
	mediaPath = newPath
	if plannedRename != "" {
		res.renameLine = fmt.Sprintf("%s -> %s", filepath.Base(job.mediaPath), filepath.Base(plannedRename))
	}

	opts := meta.Options{DryRun: g.DryRun, Verbose: g.Verbose, Out: stdout}
	plan, err := meta.BuildPlan(mediaPath, curInfo, job.sc, clock, opts)
	if err != nil {
		res.err = fmt.Errorf("cannot build metadata plan: %v", err)
		return res
	}
	res.source = plan.SourceLabel()
	res.pattern = plan.FilenamePattern()
	res.warnings = append(res.warnings, plan.Warnings()...)
	if job.sc != nil {
		res.untransferred = job.sc.PresentUntransferred()
	}

	// curInfo stays valid even when extension fixing renamed the file:
	// metadata values do not depend on the file name.
	outcome, fsRes, err := meta.Apply(plan, curInfo, opts)
	res.outcome, res.fsRes, res.err = outcome, fsRes, err
	return res
}

func collectFixResult(res fixResult, sum *report.Summary, stdout io.Writer, g *globalOpts) {
	base := filepath.Base(res.path)
	for _, w := range res.warnings {
		sum.Warnf("%s: %s", base, w)
	}
	if res.renameLine != "" {
		if g.DryRun {
			fmt.Fprintf(stdout, "🔁 [dry-run] would rename %s\n", res.renameLine)
		} else {
			fmt.Fprintf(stdout, "🔄 renamed %s\n", res.renameLine)
		}
	}

	switch {
	case res.err != nil:
		sum.Errorf("could not update %s: %v", res.path, res.err)
		sum.Failed++
		return
	case res.outcome == meta.OutcomeAlreadyCorrect:
		sum.AlreadyCorrect++
	case res.outcome == meta.OutcomeSidecar:
		sum.SidecarXMP++
		sum.Source(res.source)
	case res.outcome == meta.OutcomeUpdated:
		sum.Updated++
		sum.Source(res.source)
	case res.outcome == meta.OutcomeDryRunPlanned:
		// counted via verbose line below; dry-run performs no writes
	}

	if res.noSidecar {
		sum.NoSidecar++
	}

	switch res.fsRes.ModSet {
	case true:
		sum.FSModSet++
	}
	switch res.fsRes.CreateState {
	case "set":
		sum.FSCreateSet++
	case "unsupported":
		sum.FSCreateUnsupported++
	}

	if !g.Verbose {
		return
	}
	line := fmt.Sprintf("   • %s: %s", base, outcomeText(res))
	if res.pattern != "" {
		line += fmt.Sprintf(" [pattern: %s]", res.pattern)
	}
	if res.source != "" {
		line += fmt.Sprintf(" [date source: %s]", res.source)
	}
	fmt.Fprintln(stdout, line)
	if len(res.untransferred) > 0 {
		fmt.Fprintf(stdout, "   ℹ️  %s: not transferred (no safe standard mapping): %s\n",
			base, strings.Join(res.untransferred, ", "))
	}
	if res.fsRes.CreateState == "unsupported" {
		fmt.Fprintf(stdout, "   ℹ️  FileCreateDate unsupported here for %s (platform/filesystem limitation)\n", base)
	}
}

func outcomeText(res fixResult) string {
	switch res.outcome {
	case meta.OutcomeDryRunPlanned:
		return "planned (dry-run)"
	case meta.OutcomeSidecar:
		return "written to XMP sidecar (" + res.path + ".xmp)"
	default:
		return res.outcome.String()
	}
}
