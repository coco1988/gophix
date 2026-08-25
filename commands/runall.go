package commands

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/alexdachin/gophix/report"
)

// runPipeline executes the canonical three-step workflow:
//
//	1/3 deduplicate   find exact copies, remove surplus ones (asks [y/N])
//	2/3 correct dates fix: embedded date wins, JSON fills gaps, filename last resort
//	3/3 restructure   copy media into <dst>/YYYY (or --layout variant)
//
// Each step prints its own detailed output and summary. A failing step aborts
// the pipeline (dates before structure; structure needs the final bytes).
type pipeCfg struct {
	src, dst       string
	layout         string
	includeUnknown bool
	keepJSON       bool
	yes            bool
	g              globalOpts
}

func runPipeline(cfg pipeCfg, zone *time.Location, stdin io.Reader, stdout io.Writer) int {
	if _, err := os.Stat(cfg.src); err != nil {
		fmt.Fprintf(stdout, "error: cannot access %s: %v\n", cfg.src, err)
		return ExitErrors
	}

	worst := ExitOK

	fmt.Fprintf(stdout, "━━━ step 1/3 · deduplicate ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	flushIfBuffered(stdout)
	// Step 1 removes duplicates only on explicit consent (--yes); without it
	// the phase degrades gracefully to report-only so the pipeline always runs.
	del := cfg.yes
	if !del {
		fmt.Fprintln(stdout, "(report-only: pass --yes to remove duplicate copies)")
	}
	c1 := runFindDuplicates(dupCfg{
		root:   cfg.src,
		format: "text",
		delete: del,
		yes:    cfg.yes,
		g:      cfg.g,
	}, stdin, stdout)
	if c1 != ExitOK {
		worst = c1
		fmt.Fprintln(stdout, "(step 1 reported errors - continuing with the remaining steps)")
	}

	fmt.Fprintf(stdout, "\n━━━ step 2/3 · correct dates ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	flushIfBuffered(stdout)
	sum := report.New(stdout)
	c2 := runFix(cfg.src, &cfg.g, zone, sum, stdout)
	fmt.Fprintln(stdout, "")
	sum.Print(stdout)
	if c2 != ExitOK || sum.HasErrors() {
		fmt.Fprintf(stdout, "\nstep 2 reported errors - skipping restructure; fix them and re-run.\n")
		return maxExit(worst, ExitErrors)
	}

	fmt.Fprintf(stdout, "\n━━━ step 3/3 · restructure ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	flushIfBuffered(stdout)
	sum3 := report.New(stdout)
	c3 := runOrganize(organizeCfg{
		src:            cfg.src,
		dst:            cfg.dst,
		includeUnknown: cfg.includeUnknown,
		keepJSON:       cfg.keepJSON,
		layout:         cfg.layout,
		zone:           zone,
		g:              cfg.g,
	}, sum3, stdout)
	fmt.Fprintln(stdout, "")
	sum3.Print(stdout)
	if c3 != ExitOK {
		return maxExit(worst, c3)
	}
	return worst
}

func maxExit(a, b int) int {
	if a > b {
		return a
	}
	return b
}
