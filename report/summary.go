// Package report collects per-run counters, warnings and errors and prints
// the final summary.
package report

import (
	"fmt"
	"io"
	"sort"
)

// Summary aggregates run statistics.
type Summary struct {
	w io.Writer // output target for live warnings/errors

	DirsScanned int
	MediaFound  int

	Updated        int // metadata written directly into media
	SidecarXMP     int // written to XMP sidecar
	AlreadyCorrect int
	NoSidecar      int
	Undated        int // media without any usable date source (left untouched)
	Quarantined    int // failed media copied/moved to the error folder
	Failed         int
	JunkIgnored    int

	FSModSet            int
	FSCreateSet         int
	FSCreateUnsupported int

	DateSources map[string]int

	Copied             int
	Moved              int
	SkippedExisting    int
	CollisionsResolved int
	PlacedUnknown      int

	JSONDeleted        int
	JSONKeptGeneric    int
	JSONKeptUnmatched  int
	JSONKeptInvalid    int
	JSONKeptUnverified int

	warnings []string
	errors   []string
}

// New creates a summary that streams warnings/errors to w.
func New(w io.Writer) *Summary {
	if w == nil {
		w = io.Discard
	}
	return &Summary{DateSources: map[string]int{}, w: w}
}

func (s *Summary) Source(label string) {
	if label == "" {
		return
	}
	s.DateSources[label]++
}

func (s *Summary) Warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.warnings = append(s.warnings, msg)
	fmt.Fprintln(s.w, "⚠️  "+msg)
}

func (s *Summary) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.errors = append(s.errors, msg)
	fmt.Fprintln(s.w, "🚨 "+msg)
}

func (s *Summary) HasErrors() bool { return len(s.errors) > 0 }

// Print renders the final summary. The header line states plainly what
// happened; an empty run is never reported as a success.
func (s *Summary) Print(w io.Writer) {
	fmt.Fprintln(w, "📋 summary")
	fmt.Fprintf(w, "   directories scanned:      %d\n", s.DirsScanned)
	fmt.Fprintf(w, "   media files found:         %d\n", s.MediaFound)
	if s.JunkIgnored > 0 {
		fmt.Fprintf(w, "   junk files ignored:        %d\n", s.JunkIgnored)
	}
	fmt.Fprintf(w, "   updated directly:          %d\n", s.Updated)
	fmt.Fprintf(w, "   XMP sidecars written:      %d\n", s.SidecarXMP)
	fmt.Fprintf(w, "   already correct:           %d\n", s.AlreadyCorrect)
	fmt.Fprintf(w, "   without JSON sidecar:      %d\n", s.NoSidecar)
	if s.Undated > 0 {
		fmt.Fprintf(w, "   undated (left untouched):  %d\n", s.Undated)
	}
	if s.Quarantined > 0 {
		fmt.Fprintf(w, "   moved to error folder:     %d\n", s.Quarantined)
	}
	fmt.Fprintf(w, "   failed:                    %d\n", s.Failed)
	if s.FSModSet > 0 || s.FSCreateSet > 0 || s.FSCreateUnsupported > 0 {
		fmt.Fprintf(w, "   fs modification times set: %d\n", s.FSModSet)
		fmt.Fprintf(w, "   fs creation times set:     %d\n", s.FSCreateSet)
		fmt.Fprintf(w, "   fs creation unsupported:   %d\n", s.FSCreateUnsupported)
	}
	if len(s.DateSources) > 0 {
		keys := make([]string, 0, len(s.DateSources))
		for k := range s.DateSources {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "   date source %-24s %d file(s)\n", k+":", s.DateSources[k])
		}
	}
	if s.Copied > 0 || s.Moved > 0 || s.SkippedExisting > 0 || s.CollisionsResolved > 0 || s.PlacedUnknown > 0 {
		fmt.Fprintf(w, "   copied:                    %d\n", s.Copied)
		fmt.Fprintf(w, "   moved:                     %d\n", s.Moved)
		fmt.Fprintf(w, "   already present (skipped): %d\n", s.SkippedExisting)
		fmt.Fprintf(w, "   collisions resolved:       %d\n", s.CollisionsResolved)
		fmt.Fprintf(w, "   placed in Unknown/:        %d\n", s.PlacedUnknown)
	}
	if s.JSONDeleted > 0 || s.JSONKeptGeneric > 0 || s.JSONKeptUnmatched > 0 ||
		s.JSONKeptInvalid > 0 || s.JSONKeptUnverified > 0 {
		fmt.Fprintf(w, "   json sidecars deleted:     %d\n", s.JSONDeleted)
		fmt.Fprintf(w, "   json kept (generic):       %d\n", s.JSONKeptGeneric)
		fmt.Fprintf(w, "   json kept (unmatched):     %d\n", s.JSONKeptUnmatched)
		fmt.Fprintf(w, "   json kept (invalid):       %d\n", s.JSONKeptInvalid)
		fmt.Fprintf(w, "   json kept (not verified):  %d\n", s.JSONKeptUnverified)
	}

	fmt.Fprintf(w, "   warnings:                  %d\n", len(s.warnings))
	fmt.Fprintf(w, "   errors:                    %d\n", len(s.errors))

	switch {
	case s.MediaFound == 0 && s.Copied == 0 && s.Moved == 0:
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "nothing to do: no media files were found in the given path")
	case s.HasErrors():
		fmt.Fprintln(w, "result: completed WITH ERRORS - see messages above")
	default:
		fmt.Fprintln(w, "result: completed")
	}
}
