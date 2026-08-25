package commands

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/report"
	"github.com/alexdachin/gophix/takeout"
)

// runCleanJSON deletes only sidecars matched to existing media whose metadata
// verifies fully correct. Stateless: matching and verification run live at
// deletion time, so behavior stays safe across separate program runs.
func runCleanJSON(root string, g *globalOpts, clock *meta.Clock, yes bool, stdin io.Reader, stdout io.Writer, sum *report.Summary) int {
	if _, err := os.Stat(root); err != nil {
		fmt.Fprintf(stdout, "error: cannot access %s: %v\n", root, err)
		return ExitErrors
	}

	type pair struct{ mediaPath, sidecarPath string }
	var candidates []pair

	err := walkDirs(root, func(dc *dirContext) error {
		sum.DirsScanned++
		cls := dc.matcher.Classify()
		sum.MediaFound += len(cls.Media)

		for _, j := range cls.Jsons {
			if takeout.IsGenericSidecar(j) {
				sum.JSONKeptGeneric++
			}
		}

		for _, mediaName := range cls.Media {
			m := dc.matcher.Match(mediaName)
			if !m.Found() {
				continue
			}
			sidecarPath := filepath.Join(dc.path, m.FileName)
			mediaPath := filepath.Join(dc.path, mediaName)

			data, err := os.ReadFile(sidecarPath)
			if err != nil {
				sum.JSONKeptInvalid++
				if g.Verbose {
					fmt.Fprintf(stdout, "   keeping %s (unreadable: %v)\n", sidecarPath, err)
				}
				continue
			}
			if _, perr := takeout.Parse(data); perr != nil {
				sum.JSONKeptInvalid++
				if g.Verbose {
					fmt.Fprintf(stdout, "   keeping %s (invalid JSON: %v)\n", sidecarPath, perr)
				}
				continue
			}
			candidates = append(candidates, pair{mediaPath, sidecarPath})
		}

		for _, j := range dc.matcher.Unclaimed() {
			if takeout.IsGenericSidecar(j) {
				continue
			}
			sum.JSONKeptUnmatched++
			if g.Verbose {
				fmt.Fprintf(stdout, "   unmatched sidecar (kept): %s\n", filepath.Join(dc.path, j))
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stdout, "error: scan failed: %v\n", err)
		return ExitErrors
	}

	var deletable []pair
	runPool(len(candidates),
		func(i int) struct {
			p   pair
			ok  bool
			msg string
		} {
			p := candidates[i]
			msg := verifySidecarDeletable(p.mediaPath, p.sidecarPath, clock)
			return struct {
				p   pair
				ok  bool
				msg string
			}{p, msg == "", msg}
		},
		func(r struct {
			p   pair
			ok  bool
			msg string
		}) {
			if r.ok {
				deletable = append(deletable, r.p)
				return
			}
			sum.JSONKeptUnverified++
			if g.Verbose {
				fmt.Fprintf(stdout, "   keeping %s (%s)\n", r.p.sidecarPath, r.msg)
			}
		})

	sort.Slice(deletable, func(i, j int) bool { return deletable[i].sidecarPath < deletable[j].sidecarPath })

	if len(deletable) == 0 {
		fmt.Fprintln(stdout, "no deletable JSON sidecars found")
		return ExitOK
	}

	fmt.Fprintln(stdout, "matched & verified JSON sidecars:")
	for _, d := range deletable {
		fmt.Fprintf(stdout, "   %s  (media: %s)\n", d.sidecarPath, filepath.Base(d.mediaPath))
	}

	if g.DryRun {
		fmt.Fprintf(stdout, "\n[dry-run] would delete %d file(s)\n", len(deletable))
		return ExitOK
	}

	if !yes {
		ok, confirmErr := confirm(stdin, stdout, fmt.Sprintf("delete these %d files", len(deletable)))
		if confirmErr != nil {
			fmt.Fprintf(stdout, "error: %v (re-run with --yes for non-interactive use)\n", confirmErr)
			return ExitErrors
		}
		if !ok {
			fmt.Fprintln(stdout, "aborted - nothing was deleted")
			return ExitOK
		}
	}

	for _, d := range deletable {
		if err := os.Remove(d.sidecarPath); err != nil {
			fmt.Fprintf(stdout, "error: cannot delete %s: %v\n", d.sidecarPath, err)
			continue
		}
		sum.JSONDeleted++
		if g.Verbose {
			fmt.Fprintf(stdout, "   🗑  deleted %s\n", d.sidecarPath)
		}
	}
	return ExitOK
}

// verifySidecarDeletable checks that the paired media carries fully correct
// metadata (dates, GPS, description and FileModifyDate). Returns "" when the
// sidecar may be deleted, otherwise the reason why it is kept.
func verifySidecarDeletable(mediaPath, sidecarPath string, clock *meta.Clock) string {
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return fmt.Sprintf("unreadable: %v", err)
	}
	sc, err := takeout.Parse(data)
	if err != nil {
		return fmt.Sprintf("invalid JSON: %v", err)
	}
	info, err := meta.Read(mediaPath)
	if err != nil {
		return fmt.Sprintf("media unreadable: %v", err)
	}
	plan, err := meta.BuildPlan(mediaPath, info, sc, clock, meta.Options{})
	if err != nil {
		return fmt.Sprintf("cannot plan: %v", err)
	}
	if !plan.MetaSatisfied(info) {
		return "metadata not synchronized yet - run fix first"
	}
	if !plan.FSSatisfied(info) {
		return "filesystem timestamp not synchronized yet - run fix first"
	}
	return ""
}

func confirm(stdin io.Reader, stdout io.Writer, action string) (bool, error) {
	if f, ok := stdout.(interface{ Flush() error }); ok {
		_ = f.Flush() // make buffered prompts visible before blocking on stdin
	}
	r := bufio.NewReader(stdin)
	fmt.Fprintf(stdout, "Really %s? [y/N] ", action)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("no interactive confirmation possible")
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}
