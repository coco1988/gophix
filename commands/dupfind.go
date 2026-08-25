package commands

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/alexdachin/gophix/takeout"
)

// find-duplicates reports exact byte-level duplicates across the scanned
// tree (Takeout album directories commonly hold full copies of library
// photos). Report-only: nothing is ever deleted or modified. No ExifTool
// involved - pure filesystem work.

type dupCfg struct {
	root   string
	format string // "text" | "csv" | "json"
	output string // "" or "-" -> stdout, else file (format inferred from extension)
	g      globalOpts
}

type dupFile struct {
	path    string
	size    int64
	sidecar string // matched JSON sidecar path, "" when none matched
}

type dupCopy struct {
	path        string
	hasSidecar  bool
	captureDate string // "YYYY-MM-DD" from the sidecar when available
	isKeep      bool
}

type dupFamily struct {
	hash   string
	size   int64
	copies []dupCopy // copies[0] is the suggested keeper
}

type dupStats struct {
	dirsScanned     int
	filesScanned    int
	hashed          int
	skippedUniquest int
	failed          int
	families        int
	redundant       int
	reclaimable     int64
}

func runFindDuplicates(cfg dupCfg, stdout io.Writer) int {
	if fi, err := os.Stat(cfg.root); err != nil {
		fmt.Fprintf(stdout, "error: cannot access %s: %v\n", cfg.root, err)
		return ExitErrors
	} else if !fi.IsDir() {
		fmt.Fprintf(stdout, "error: %s is not a directory\n", cfg.root)
		return ExitErrors
	}

	var files []dupFile
	var dirsScanned int
	err := walkDirs(cfg.root, func(dc *dirContext) error {
		dirsScanned++
		cls := dc.matcher.Classify()
		for _, name := range cls.Media {
			fi, err := os.Stat(filepath.Join(dc.path, name))
			if err != nil {
				continue // vanished mid-scan; stat errors surface during hashing anyway
			}
			f := dupFile{path: filepath.Join(dc.path, name), size: fi.Size()}
			if m := dc.matcher.Match(name); m.Found() {
				f.sidecar = filepath.Join(dc.path, m.FileName)
			}
			files = append(files, f)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stdout, "error: scan failed: %v\n", err)
		return ExitErrors
	}

	st := dupStats{dirsScanned: dirsScanned, filesScanned: len(files)}

	// Size prefilter: a file whose byte size is unique cannot have an exact
	// duplicate - only sizes shared by >= 2 files are worth hashing.
	bySize := make(map[int64][]int, len(files))
	for i, f := range files {
		bySize[f.size] = append(bySize[f.size], i)
	}
	var survivors []int
	for _, idxs := range bySize {
		if len(idxs) > 1 {
			survivors = append(survivors, idxs...)
		}
	}
	sort.Ints(survivors)
	st.skippedUniquest = len(files) - len(survivors)

	digests := make([]string, len(survivors))
	hashFails := make([]error, len(survivors))
	runPool(len(survivors),
		func(i int) struct{} {
			d, err := hashFile(files[survivors[i]].path)
			if err != nil {
				hashFails[i] = err
				return struct{}{}
			}
			digests[i] = d
			return struct{}{}
		},
		func(struct{}) {})
	for i, e := range hashFails {
		if e != nil {
			st.failed++
			fmt.Fprintf(stdout, "warning: cannot read %s: %v\n", files[survivors[i]].path, e)
		} else {
			st.hashed++
		}
	}

	byDigest := make(map[string][]int)
	for i := range survivors {
		d := digests[i]
		if d == "" {
			continue
		}
		byDigest[d] = append(byDigest[d], survivors[i])
	}

	var fams []dupFamily
	for d, idxs := range byDigest {
		if len(idxs) < 2 {
			continue
		}
		copies := make([]dupCopy, len(idxs))
		for k, fi := range idxs {
			c := dupCopy{path: files[fi].path, hasSidecar: files[fi].sidecar != ""}
			if c.hasSidecar {
				c.captureDate = sidecarCaptureDate(files[fi].sidecar, cfg.g.Verbose, stdout)
			}
			copies[k] = c
		}
		sortCopiesForKeep(copies)
		copies[0].isKeep = true
		fams = append(fams, dupFamily{
			hash:   d,
			size:   files[idxs[0]].size,
			copies: copies,
		})
		st.families++
		st.redundant += len(copies) - 1
		st.reclaimable += files[idxs[0]].size * int64(len(copies)-1)
	}
	sort.Slice(fams, func(a, b int) bool { return fams[a].copies[0].path < fams[b].copies[0].path })

	target := stdout
	closer := func() {}
	if cfg.output != "" && cfg.output != "-" {
		f, ferr := os.Create(cfg.output)
		if ferr != nil {
			fmt.Fprintf(stdout, "error: cannot write %s: %v\n", cfg.output, ferr)
			return ExitErrors
		}
		target = f
		closer = func() { f.Close() }
	}

	switch cfg.format {
	case "csv":
		writeDupCSV(target, fams)
	case "json":
		writeDupJSON(target, fams, st)
	default:
		writeDupText(target, fams, st)
	}
	closer()

	printDupFooter(stdout, st, cfg.output)

	if st.failed > 0 {
		return ExitErrors
	}
	return ExitOK
}

// sortCopiesForKeep orders copies so index 0 is the suggested keeper:
// sidecar present first, then shorter path, then lexicographically smaller.
func sortCopiesForKeep(copies []dupCopy) {
	sort.SliceStable(copies, func(a, b int) bool {
		if copies[a].hasSidecar != copies[b].hasSidecar {
			return copies[a].hasSidecar
		}
		if len(copies[a].path) != len(copies[b].path) {
			return len(copies[a].path) < len(copies[b].path)
		}
		return copies[a].path < copies[b].path
	})
}

// sidecarCaptureDate extracts the capture date (YYYY-MM-DD) from a matched
// sidecar. Unreadable or invalid sidecars degrade to "" without failing.
func sidecarCaptureDate(sidecarPath string, verbose bool, stdout io.Writer) string {
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return ""
	}
	sc, err := takeout.Parse(data)
	if err != nil {
		if verbose {
			fmt.Fprintf(stdout, "   (ignored invalid sidecar %s)\n", sidecarPath)
		}
		return ""
	}
	ts := sc.TakenUnix()
	if ts == nil {
		return ""
	}
	return time.Unix(*ts, 0).UTC().Format("2006-01-02")
}

func printDupFooter(stdout io.Writer, st dupStats, outputFile string) {
	if st.filesScanned == 0 {
		fmt.Fprintln(stdout, "nothing to do: no media files were found in the given path")
		return
	}
	loc := "report above"
	if outputFile != "" && outputFile != "-" {
		loc = "written to " + outputFile
	}
	if st.families == 0 {
		fmt.Fprintf(stdout,
			"no duplicates found (%d files in %d directories, %d hashed, %d skipped by unique size) - %s\n",
			st.filesScanned, st.dirsScanned, st.hashed, st.skippedUniquest, loc)
		return
	}
	fmt.Fprintf(stdout,
		"%d duplicate families: %d redundant copies, %s reclaimable (%d files in %d directories, %d hashed, %d skipped by unique size) - %s\n",
		st.families, st.redundant, humanBytes(st.reclaimable),
		st.filesScanned, st.dirsScanned, st.hashed, st.skippedUniquest, loc)
}

func writeDupText(w io.Writer, fams []dupFamily, st dupStats) {
	if st.filesScanned == 0 {
		return
	}
	if len(fams) == 0 {
		fmt.Fprintln(w, "no exact duplicate content detected.")
		return
	}
	fmt.Fprintf(w, "%d duplicate families found:\n", len(fams))
	for i, fam := range fams {
		fmt.Fprintf(w, "\n── family %d/%d · sha256 %s… · %d copies · %s each\n",
			i+1, len(fams), shortHash(fam.hash), len(fam.copies), humanBytes(fam.size))
		for _, c := range fam.copies {
			meta := ""
			switch {
			case c.hasSidecar && c.captureDate != "":
				meta = fmt.Sprintf(" (sidecar, taken %s)", c.captureDate)
			case c.hasSidecar:
				meta = " (sidecar)"
			}
			if c.isKeep {
				fmt.Fprintf(w, " ★ KEEP %s%s\n", c.path, meta)
			} else {
				fmt.Fprintf(w, "        %s%s\n", c.path, meta)
			}
		}
	}
	fmt.Fprintln(w, "\nThis is a report only - nothing was deleted or modified.")
}

func writeDupCSV(w io.Writer, fams []dupFamily) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"hash", "path", "bytes", "is_keep", "has_sidecar", "capture_date"})
	for _, fam := range fams {
		for _, c := range fam.copies {
			_ = cw.Write([]string{
				fam.hash, c.path, fmt.Sprintf("%d", fam.size),
				fmt.Sprintf("%t", c.isKeep), fmt.Sprintf("%t", c.hasSidecar),
				c.captureDate,
			})
		}
	}
	cw.Flush()
}

type dupJSONCopy struct {
	Path        string `json:"path"`
	IsKeep      bool   `json:"is_keep"`
	HasSidecar  bool   `json:"has_sidecar"`
	CaptureDate string `json:"capture_date,omitempty"`
}

type dupJSONFamily struct {
	Hash   string        `json:"hash"`
	Bytes  int64         `json:"bytes"`
	Keep   string        `json:"keep"`
	Copies []dupJSONCopy `json:"copies"`
}

type dupJSONReport struct {
	Summary  map[string]any  `json:"summary"`
	Families []dupJSONFamily `json:"families"`
}

func writeDupJSON(w io.Writer, fams []dupFamily, st dupStats) {
	rep := dupJSONReport{
		Summary: map[string]any{
			"directories_scanned": st.dirsScanned,
			"files_scanned":       st.filesScanned,
			"hashed":              st.hashed,
			"skipped_unique_size": st.skippedUniquest,
			"read_errors":         st.failed,
			"families":            st.families,
			"redundant_copies":    st.redundant,
			"reclaimable_bytes":   st.reclaimable,
		},
		Families: make([]dupJSONFamily, 0, len(fams)),
	}
	for _, fam := range fams {
		jf := dupJSONFamily{Hash: fam.hash, Bytes: fam.size, Keep: fam.copies[0].path}
		for _, c := range fam.copies {
			jf.Copies = append(jf.Copies, dupJSONCopy{
				Path: c.path, IsKeep: c.isKeep, HasSidecar: c.hasSidecar, CaptureDate: c.captureDate,
			})
		}
		rep.Families = append(rep.Families, jf)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n >= k*k*k*k:
		return fmt.Sprintf("%.1f TiB", float64(n)/(k*k*k*k))
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GiB", float64(n)/(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.1f MiB", float64(n)/(k*k))
	case n >= k:
		return fmt.Sprintf("%.1f KiB", float64(n)/k)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
