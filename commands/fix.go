package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/report"
	"github.com/alexdachin/gophix/takeout"
)

// matchAndParse finds the sidecar for mediaName in dc and parses it.
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

type fixJob struct {
	mediaPath   string
	sc          *takeout.Sidecar // nil = no sidecar matched/parsed
	sidecarPath string           // matched sidecar file ("" when none)
	matched     bool
	preErr      error // detected during scan (e.g. invalid sidecar)
	cur         meta.Info
	readErr     error
}

type fixResult struct {
	path          string
	outcome       meta.Outcome
	fsRes         meta.FSResult
	source        string // date source label
	err           error
	warnings      []string
	noSidecar     bool
	undated       bool
	renameLn      string
	quarantineMsg string
	quarantined   int
}

func runFix(root string, g *globalOpts, zone *time.Location, sum *report.Summary, stdout io.Writer) int {
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
		flushIfBuffered(stdout)

		cls := dc.matcher.Classify()
		for _, mediaName := range cls.Media {
			sum.MediaFound++
			mediaPath := filepath.Join(dc.path, mediaName)
			sc, m := matchAndParse(dc, mediaName, g, sum)
			if sc == nil && m.Found() {
				// Sidecar matched but unreadable/invalid: a real processing
				// error for THIS media - routed through the normal failure
				// path so --failed-dir quarantine applies.
				jobs = append(jobs, fixJob{
					mediaPath:   mediaPath,
					sidecarPath: sidecarPathFor(dc.path, m),
					matched:     true,
					preErr:      fmt.Errorf("sidecar could not be used (unreadable or invalid JSON)"),
				})
				continue
			}
			jobs = append(jobs, fixJob{
				mediaPath:   mediaPath,
				sc:          sc,
				sidecarPath: sidecarPathFor(dc.path, m),
				matched:     m.Found(),
			})
		}
		return nil
	})
	if err != nil {
		sum.Errorf("scan failed: %v", err)
		return ExitErrors
	}

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
		func(i int) fixResult { return processFixJob(jobs[i], g, zone, stdout) },
		func(r fixResult) { collectFixResult(r, g, sum, stdout) })

	if sum.MediaFound == 0 && sum.Failed == 0 && sum.Undated == 0 {
		fmt.Fprintln(stdout, "no media files found - nothing was changed")
	}
	if sum.HasErrors() {
		return ExitErrors
	}
	return ExitOK
}

func processFixJob(job fixJob, g *globalOpts, zone *time.Location, stdout io.Writer) fixResult {
	res := fixResult{path: job.mediaPath, outcome: meta.OutcomeFailed, noSidecar: !job.matched}

	if job.preErr != nil || job.readErr != nil {
		if job.preErr != nil {
			res.err = job.preErr
		} else {
			res.err = job.readErr
		}
		if g.FailedDir != "" {
			q := quarantineFailed(job.mediaPath, job.sidecarPath, g, stdout)
			res.quarantineMsg = q.msg
			res.quarantined = q.count
		}
		return res
	}
	curInfo := job.cur

	newPath, plannedRename, warn := fixExtensionFromInfo(job.mediaPath, curInfo, g, stdout)
	if warn != "" {
		res.warnings = append(res.warnings, warn)
	}
	mediaPath := newPath
	if plannedRename != "" {
		res.renameLn = fmt.Sprintf("%s -> %s", filepath.Base(job.mediaPath), filepath.Base(plannedRename))
	}

	opts := meta.Options{DryRun: g.DryRun, Verbose: g.Verbose, Out: stdout}
	plan, err := meta.BuildPlan(mediaPath, curInfo, job.sc, zone)
	if err != nil {
		if _, undated := err.(*meta.UndatableError); undated {
			res.undated = true
			res.outcome = meta.OutcomeAlreadyCorrect // counted separately via Undated
			return res
		}
		res.err = fmt.Errorf("cannot build metadata plan: %v", err)
		return res
	}
	res.source = plan.Date.Source

	// curInfo stays valid even after a rename: tag values don't depend on names.
	outcome, fsRes, err := meta.Apply(plan, curInfo, opts)
	res.outcome, res.fsRes, res.err = outcome, fsRes, err

	if res.err != nil && g.FailedDir != "" {
		q := quarantineFailed(mediaPath, job.sidecarPath, g, stdout)
		res.quarantineMsg = q.msg
		res.quarantined = q.count
	}
	return res
}

// sidecarPathFor resolves the matched sidecar's full path.
func sidecarPathFor(dir string, m takeout.Match) string {
	if !m.Found() {
		return ""
	}
	return filepath.Join(dir, m.FileName)
}

type quarantineOutcome struct {
	msg   string // human-readable line ("" = nothing done)
	count int    // number of files actually/planned relocated
}

// quarantineFailed relocates a failed media file (+ its own matched sidecar
// and any .xmp) to the error folder. Copy is the default; --failed-move moves.
// Never overwrites: existing targets get a numeric suffix. Undated-but-valid
// media is never quarantined - only actual processing errors are.
func quarantineFailed(mediaPath, sidecarPath string, g *globalOpts, stdout io.Writer) quarantineOutcome {
	if g.DryRun {
		target := uniqueTarget(filepath.Join(g.FailedDir, filepath.Base(mediaPath)))
		verb := "copied"
		if g.FailedMove {
			verb = "moved"
		}
		return quarantineOutcome{msg: fmt.Sprintf("[dry-run] would %s failed file to %s", verb, target), count: 1}
	}
	if err := os.MkdirAll(g.FailedDir, 0o755); err != nil {
		return quarantineOutcome{msg: fmt.Sprintf("cannot create error folder %s: %v", g.FailedDir, err)}
	}
	target := uniqueTarget(filepath.Join(g.FailedDir, filepath.Base(mediaPath)))
	verb := "copied"
	if g.FailedMove {
		verb = "moved"
	}

	if g.FailedMove {
		if err := os.Rename(mediaPath, target); err != nil {
			return quarantineOutcome{msg: fmt.Sprintf("cannot move %s to error folder: %v", mediaPath, err)}
		}
	} else if _, err := copyVerified(mediaPath, target); err != nil {
		os.Remove(target)
		return quarantineOutcome{msg: fmt.Sprintf("cannot copy %s to error folder: %v", mediaPath, err)}
	}
	count := 1

	// sidecar + xmp follow under the (possibly suffixed) media name.
	baseOld := filepath.Base(mediaPath)
	baseNew := filepath.Base(target)
	for _, extra := range []string{sidecarPath, mediaPath + ".xmp"} {
		if extra == "" {
			continue
		}
		if _, err := os.Stat(extra); err != nil {
			continue
		}
		suffix := strings.TrimPrefix(filepath.Base(extra), baseOld)
		extraTarget := uniqueTarget(filepath.Join(g.FailedDir, baseNew+suffix))
		if g.FailedMove {
			if err := os.Rename(extra, extraTarget); err == nil {
				count++
			}
		} else if _, err := copyVerified(extra, extraTarget); err == nil {
			count++
		}
	}
	return quarantineOutcome{msg: fmt.Sprintf("%s to error folder: %s (%s)", verb, target, filepath.Base(mediaPath)), count: count}
}

// uniqueTarget returns path, or a variant with _N before the extension that
// does not exist yet (deterministic order, never overwrites).
func uniqueTarget(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

func collectFixResult(r fixResult, g *globalOpts, sum *report.Summary, stdout io.Writer) {
	sum.Quarantined += r.quarantined
	base := filepath.Base(r.path)
	for _, w := range r.warnings {
		sum.Warnf("%s: %s", base, w)
	}
	if r.renameLn != "" {
		if g.DryRun {
			fmt.Fprintf(stdout, "🔁 [dry-run] would rename %s\n", r.renameLn)
		} else {
			fmt.Fprintf(stdout, "🔄 renamed %s\n", r.renameLn)
		}
	}

	switch {
	case r.err != nil:
		sum.Errorf("could not update %s: %v", r.path, r.err)
		sum.Failed++
		if r.quarantineMsg != "" {
			fmt.Fprintf(stdout, "   📦 %s\n", r.quarantineMsg)
		}
	case r.undated:
		sum.Undated++
	case r.outcome == meta.OutcomeAlreadyCorrect:
		sum.AlreadyCorrect++
	case r.outcome == meta.OutcomeSidecar:
		sum.SidecarXMP++
	case r.outcome == meta.OutcomeUpdated:
		sum.Updated++
	}

	if r.noSidecar {
		sum.NoSidecar++
	}
	if r.fsRes.ModSet {
		sum.FSModSet++
	}
	switch r.fsRes.CreateState {
	case "set":
		sum.FSCreateSet++
	case "unsupported":
		sum.FSCreateUnsupported++
	}

	if !g.Verbose {
		return
	}
	line := fmt.Sprintf("   • %s: %s", base, outcomeText(r))
	if r.source != "" {
		line += fmt.Sprintf(" [date source: %s]", r.source)
	}
	fmt.Fprintln(stdout, line)
	if r.fsRes.CreateState == "unsupported" {
		fmt.Fprintf(stdout, "   ℹ️  FileCreateDate unsupported here for %s (platform/filesystem limitation)\n", base)
	}
}

func outcomeText(r fixResult) string {
	switch {
	case r.undated:
		return "undated - left untouched (no embedded/JSON/filename date)"
	case r.outcome == meta.OutcomeDryRunPlanned:
		return "planned (dry-run)"
	case r.outcome == meta.OutcomeSidecar:
		return "written to XMP sidecar (" + r.path + ".xmp)"
	default:
		return r.outcome.String()
	}
}
