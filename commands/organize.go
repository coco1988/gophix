package commands

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/report"
	"github.com/alexdachin/gophix/takeout"
)

type organizeCfg struct {
	src, dst       string
	move           bool
	includeUnknown bool
	keepJSON       bool
	layout         string // yyyy (default), yyyy/mm, yyyy-mm, flat
	zone           *time.Location
	g              globalOpts
}

// layoutDir returns the destination directory for one media file according to
// the configured --layout. local is the resolved local capture time; its
// month is only consulted when the layout uses it.
func layoutDir(layout, dst string, local time.Time) string {
	switch layout {
	case "yyyy/mm":
		return filepath.Join(dst, fmt.Sprintf("%04d", local.Year()), fmt.Sprintf("%02d", int(local.Month())))
	case "yyyy-mm":
		return filepath.Join(dst, fmt.Sprintf("%04d-%02d", local.Year(), int(local.Month())))
	case "flat":
		return dst
	default: // "yyyy"
		return filepath.Join(dst, fmt.Sprintf("%04d", local.Year()))
	}
}

type orgJob struct {
	dc        *dirContext
	mediaName string
	sc        *takeout.Sidecar // nil when no sidecar matched/parsed
	matchVal  takeout.Match
	matched   bool
	cur       meta.Info // pre-read metadata; nil when the policy never consults embedded data
	readErr   error     // non-nil when the bulk read failed for this file
}

type orgResult struct {
	copied, moved, skippedExisting int
	collisions, unknownPlaced      int
	failed                         int
	noSidecar                      bool
	source                         string
	errs                           []string
	lines                          []string // always printed
	vlines                         []string // verbose only
}

// runOrganize copies (or moves, opt-in) media into <dst>/YYYY/ folders using
// the resolved local capture year. It never overwrites and never touches the
// source unless --move was given.
func runOrganize(cfg organizeCfg, sum *report.Summary, stdout io.Writer) int {
	if _, err := os.Stat(cfg.src); err != nil {
		fmt.Fprintf(stdout, "error: cannot access source %s: %v\n", cfg.src, err)
		return ExitErrors
	}
	if err := os.MkdirAll(cfg.dst, 0o755); err != nil {
		fmt.Fprintf(stdout, "error: cannot create destination %s: %v\n", cfg.dst, err)
		return ExitErrors
	}
	if cfg.move && cfg.g.DryRun {
		fmt.Fprintln(stdout, "[dry-run] --move ignored during dry-run; nothing will be removed")
	}

	var jobs []orgJob
	err := walkDirs(cfg.src, func(dc *dirContext) error {
		sum.DirsScanned++
		cls := dc.matcher.Classify()
		for _, mediaName := range cls.Media {
			sum.MediaFound++
			sc, m := matchAndParse(dc, mediaName, &cfg.g, sum)
			if sc == nil && m.Found() {
				sum.Failed++
				continue
			}
			jobs = append(jobs, orgJob{dc: dc, mediaName: mediaName, sc: sc, matchVal: m, matched: m.Found()})
		}
		return nil
	})
	if err != nil {
		sum.Errorf("scan failed: %v", err)
		return ExitErrors
	}

	// One bulk metadata read per file; workers reuse it for planning.
	paths := make([]string, len(jobs))
	for i := range jobs {
		paths[i] = filepath.Join(jobs[i].dc.path, jobs[i].mediaName)
	}
	infos := make([]meta.Info, len(paths))
	fails := make([]error, len(paths))
	bulkRead(paths, infos, fails)
	for i := range jobs {
		jobs[i].cur, jobs[i].readErr = infos[i], fails[i]
	}

	runPool(len(jobs),
		func(i int) orgResult { return processOrgItem(jobs[i], cfg) },
		func(r orgResult) { collectOrgResult(r, cfg, sum, stdout) })

	if sum.HasErrors() {
		return ExitErrors
	}
	return ExitOK
}

// buildOrgResolved resolves the capture date under the v2 rule:
// embedded -> JSON -> filename. Returns nil when nothing works.
func buildOrgResolved(mediaPath string, info meta.Info, sc *takeout.Sidecar, cfg organizeCfg) (*meta.DateResult, string) {
	var taken *int64
	if sc != nil {
		taken = sc.TakenUnix()
	}
	var fname *meta.FileNameDate
	if f, err := meta.ParseFileName(mediaPath); err == nil {
		fname = f
	}
	date, ok := meta.ResolveDate(info, taken, fname, cfg.zone)
	if !ok {
		return nil, ""
	}
	return date, date.Source
}

func resolvedSource(r *meta.DateResult) string {
	if r == nil {
		return "unknown"
	}
	return r.Source
}

// processOrgItem places one media file (plus sidecars) under its year folder.
func processOrgItem(job orgJob, cfg organizeCfg) orgResult {
	res := orgResult{}
	base := filepath.Base(job.mediaName)
	mediaPath := filepath.Join(job.dc.path, job.mediaName)
	ext := filepath.Ext(base)

	resolved, srcLabel := buildOrgResolved(mediaPath, job.cur, job.sc, cfg)
	if job.readErr != nil {
		// A failed metadata read counts as no usable capture date.
		resolved, srcLabel = nil, ""
	}

	xmp := mediaPath + ".xmp"
	if _, err := os.Stat(xmp); err != nil {
		xmp = ""
	}
	item := orgItem{
		mediaPath: mediaPath,
		matchVal:  job.matchVal,
		xmpPath:   xmp,
		resolved:  resolved,
	}

	var dir string
	switch {
	case resolved == nil:
		if !cfg.includeUnknown {
			if cfg.g.Verbose {
				res.vlines = append(res.vlines,
					fmt.Sprintf("⏭  %s: no usable capture date (use --include-unknown-date to include)", base))
			} else {
				res.lines = append(res.lines,
					fmt.Sprintf("⏭  %s: no usable capture date", base))
			}
			return res
		}
		dir = filepath.Join(cfg.dst, "Unknown")
	default:
		dir = layoutDir(cfg.layout, cfg.dst, resolved.Wall)
	}
	res.source = srcLabel

	if err := os.MkdirAll(dir, 0o755); err != nil {
		res.errs = append(res.errs, fmt.Sprintf("cannot create %s: %v", dir, err))
		res.failed++
		return res
	}

	target := filepath.Join(dir, base)
	if _, err := os.Stat(target); err == nil {
		same, _ := sameContent(item.mediaPath, target)
		if same {
			res.skippedExisting++
			if cfg.g.Verbose {
				res.vlines = append(res.vlines,
					fmt.Sprintf("= %s already present with identical content", target))
			}
			if cfg.move && !cfg.g.DryRun {
				// Identical data is already at the target; retiring the
				// source completes an equivalent, verified move.
				retireSources(&item, matchedJSONPath(&item), cfg.keepJSON, &res)
				res.moved++
			}
			return res
		}
		// Deterministic collision name from capture instant + content hash.
		res.collisions++
		srcHash, herr := hashFile(item.mediaPath)
		if herr != nil {
			res.errs = append(res.errs, herr.Error())
			res.failed++
			return res
		}
		stem := strings.TrimSuffix(base, ext)
		ts := "00000000T000000"
		if resolved != nil {
			ts = resolved.Instant.UTC().Format("20060102T150405")
		}
		base = fmt.Sprintf("%s-%s-%s%s", stem, ts, shortHash(srcHash), ext)
		for k := 1; ; k++ {
			cand := filepath.Join(dir, base)
			if _, err := os.Stat(cand); err != nil {
				break
			}
			base = fmt.Sprintf("%s-%s-%s_%d%s", stem, ts, shortHash(srcHash), k, ext)
		}
		target = filepath.Join(dir, base)
		if !cfg.g.DryRun {
			res.lines = append(res.lines,
				fmt.Sprintf("🔀 collision: using %s for %s", base, filepath.Base(item.mediaPath)))
		}
	}

	if cfg.g.DryRun {
		verb := "copy"
		if cfg.move {
			verb = "move"
		}
		res.vlines = append(res.vlines,
			fmt.Sprintf("[dry-run] would %s %s -> %s [date source: %s]", verb, filepath.Base(item.mediaPath), target, srcLabel))
		return res
	}

	// Copy with race-safe target creation: another worker may materialize
	// the same destination name between our stat and our O_EXCL create.
	// On a lost creation race we re-classify against the real file instead
	// of failing - and never delete a file we do not own.
	copied := false
	for attempt := 0; attempt < 128; attempt++ {
		if _, err := copyVerified(item.mediaPath, target); err != nil {
			// Note: os.IsExist cannot see through %w-wrapped errors; use
			// errors.Is against fs.ErrExist.
			if !errors.Is(err, fs.ErrExist) {
				// We created this (partial) file ourselves; clean it up.
				os.Remove(target)
				res.errs = append(res.errs, err.Error())
				res.failed++
				return res
			}
			// Target appeared meanwhile. Identical bytes -> nothing to add;
			// different bytes -> derive the next collision name and retry.
			if same, _ := sameContent(item.mediaPath, target); same {
				res.skippedExisting++
				return res
			}
			srcHash, herr := hashFile(item.mediaPath)
			if herr != nil {
				res.errs = append(res.errs, herr.Error())
				res.failed++
				return res
			}
			stem := strings.TrimSuffix(base, ext)
			ts := "00000000T000000"
			if resolved != nil {
				ts = resolved.Instant.UTC().Format("20060102T150405")
			}
			base = fmt.Sprintf("%s-%s-%s_%d%s", stem, ts, shortHash(srcHash), attempt+1, ext)
			target = filepath.Join(dir, base)
			continue
		}
		copied = true
		break
	}
	if !copied {
		res.errs = append(res.errs, fmt.Sprintf("cannot place %s: collision naming exhausted", item.mediaPath))
		res.failed++
		return res
	}
	res.copied++

	sidecarCopies := 0
	if item.xmpPath != "" {
		xmpTarget := filepath.Join(dir, base+".xmp")
		if _, err := os.Stat(xmpTarget); err == nil {
			res.skippedExisting++
		} else if _, err := copyVerified(item.xmpPath, xmpTarget); err != nil {
			if errors.Is(err, fs.ErrExist) { // concurrent job won the create race
				res.skippedExisting++
			} else {
				res.errs = append(res.errs, fmt.Sprintf("cannot copy XMP sidecar %s: %v", item.xmpPath, err))
			}
		} else {
			sidecarCopies++
		}
	}

	srcJSON := matchedJSONPath(&item)
	jsonMoved := false
	if cfg.keepJSON && srcJSON != "" {
		suffix := strings.TrimPrefix(filepath.Base(srcJSON), filepath.Base(item.mediaPath))
		if suffix == "" || suffix == filepath.Base(srcJSON) {
			suffix = filepath.Ext(srcJSON) // legacy fallback
		}
		jsonTarget := filepath.Join(dir, base+suffix)
		if _, err := os.Stat(jsonTarget); err == nil {
			srcJSON = "" // destination exists; leave everything untouched
		} else if _, err := copyVerified(srcJSON, jsonTarget); err != nil {
			res.errs = append(res.errs, fmt.Sprintf("cannot copy JSON sidecar %s: %v", srcJSON, err))
		} else {
			sidecarCopies++
			jsonMoved = true
		}
	}

	if cfg.move {
		removeOK := true
		for _, p := range []string{item.mediaPath, item.xmpPath} {
			if p == "" {
				continue
			}
			if err := os.Remove(p); err != nil {
				res.errs = append(res.errs, fmt.Sprintf("cannot remove source %s after copy: %v", p, err))
				removeOK = false
			}
		}
		if jsonMoved && srcJSON != "" && removeOK {
			os.Remove(srcJSON)
		}
		if removeOK {
			res.moved++
			res.copied--
			if cfg.g.Verbose {
				res.vlines = append(res.vlines,
					fmt.Sprintf("➡️  moved %s -> %s", filepath.Base(item.mediaPath), target))
			}
		}
	} else if cfg.g.Verbose {
		res.vlines = append(res.vlines,
			fmt.Sprintf("📄 copied %s -> %s [date source: %s]", filepath.Base(item.mediaPath), target, srcLabel))
	}
	_ = sidecarCopies
	return res
}

func collectOrgResult(r orgResult, cfg organizeCfg, sum *report.Summary, stdout io.Writer) {
	sum.Copied += r.copied
	sum.Moved += r.moved
	sum.SkippedExisting += r.skippedExisting
	sum.CollisionsResolved += r.collisions
	sum.PlacedUnknown += r.unknownPlaced
	sum.Failed += r.failed
	if r.noSidecar {
		sum.NoSidecar++
	}
	if r.source != "" {
		sum.Source(r.source)
	}
	for _, e := range r.errs {
		sum.Errorf("%s", e)
	}
	for _, l := range r.lines {
		fmt.Fprintln(stdout, l)
	}
	if cfg.g.Verbose {
		for _, l := range r.vlines {
			fmt.Fprintln(stdout, "   "+l)
		}
	}
}

// retireSources removes the original media (+ sidecars) after a verified move.
func retireSources(item *orgItem, srcJSON string, alsoJSON bool, res *orgResult) {
	removeOK := true
	for _, p := range []string{item.mediaPath, item.xmpPath} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil {
			res.errs = append(res.errs, fmt.Sprintf("cannot remove source %s: %v", p, err))
			removeOK = false
		}
	}
	if alsoJSON && removeOK && srcJSON != "" {
		os.Remove(srcJSON)
	}
}

type orgItem struct {
	mediaPath string
	matchVal  takeout.Match
	xmpPath   string
	resolved  *meta.DateResult
}

func matchedJSONPath(item *orgItem) string {
	if !item.matchVal.Found() {
		return ""
	}
	return filepath.Join(filepath.Dir(item.mediaPath), item.matchVal.FileName)
}

// sameContent compares two files by size first, then by hash.
func sameContent(a, b string) (bool, string) {
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	if ea != nil || eb != nil || sa.Size() != sb.Size() {
		return false, ""
	}
	ha, erra := hashFile(a)
	hb, errb := hashFile(b)
	if erra != nil || errb != nil {
		return false, ha
	}
	return ha == hb, hb
}
