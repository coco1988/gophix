// Package commands implements the gophix subcommands.
package commands

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/report"
)

// cleanPathArg repairs path arguments mangled by Windows shell quoting.
// A PowerShell call like:  gophix.exe fix 'C:\dir name\'
// marshals the argument as "C:\dir name\" and the Windows C runtime parses
// the trailing \" as an escaped quote, so the program receives
// 'C:\dir name"'. Strip stray quotes and any resulting trailing separator,
// then normalize.
func cleanPathArg(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	if p == "" {
		return p
	}
	// Drop one trailing separator unless it is a filesystem/volume root.
	if len(p) > 1 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') {
		isRoot := strings.HasSuffix(p, ":\\") || p == "/" || p == "\\"
		if !isRoot {
			p = p[:len(p)-1]
		}
	}
	return filepath.Clean(p)
}

const version = "1.1.0"

// Process exit codes.
const (
	ExitOK         = 0
	ExitErrors     = 1
	ExitUsage      = 2
	ExitNoExiftool = 3
)

// validLayouts lists the accepted --layout values and what they produce.
var validLayouts = []string{"yyyy", "yyyy/mm", "yyyy-mm", "flat"}

func isValidLayout(v string) bool {
	for _, l := range validLayouts {
		if l == v {
			return true
		}
	}
	return false
}

var validDupFormats = []string{"text", "csv", "json"}

func isValidDupFormat(v string) bool {
	for _, f := range validDupFormats {
		if f == v {
			return true
		}
	}
	return false
}

// inferDupFormat picks the report format from the output file extension
// (.csv/.json); anything else defaults to text. An explicit --format wins.
func inferDupFormat(output, explicit string) (string, error) {
	if explicit != "" {
		if !isValidDupFormat(explicit) {
			return "", fmt.Errorf("invalid --format %q (want %s)", explicit, strings.Join(validDupFormats, ", "))
		}
		return explicit, nil
	}
	switch strings.ToLower(filepath.Ext(output)) {
	case ".csv":
		return "csv", nil
	case ".json":
		return "json", nil
	default:
		return "text", nil
	}
}

const usage = `gophix - restore Google Photos Takeout metadata into your media

Usage:
  gophix fix [options] <takeout-media-root>
  gophix clean-json [options] <takeout-media-root>
  gophix organize-by-year [options] <source-path> <destination-path>
  gophix find-duplicates [options] <takeout-media-root>
  gophix version | help

Commands:
  fix               Merge sidecar metadata (dates, GPS, description) into media.
  clean-json        Delete matched & verified JSON sidecars (safe by default).
  organize-by-year  Copy or move media into date folders by capture date.
  find-duplicates   Report exact duplicate content (report-only, never deletes).

Options:
  --dry-run                       Plan only; never write, rename, move or delete.
  --verbose                       Detailed per-file output.
  --timezone <IANA-zone|+01:00>   Timezone for local capture times (e.g. Europe/Berlin).
                                  Recommended; without it unresolved times stay UTC with a warning.
  --force-json-time               Overwrite valid embedded capture times with the JSON time.
  --time-policy <policy>          preserve-existing (default) | prefer-json | json-only
  --yes                           clean-json only: skip the confirmation prompt.
  --move                          organize-by-year only: move instead of copy (opt-in).
  --include-unknown-date          organize-by-year only: place undated media into Unknown/.
  --keep-json                     organize-by-year only: also copy matched JSON sidecars.
  --layout <layout>               organize-by-year only: folder structure under <destination-path>.
                                  yyyy      2020/file.jpg           (default)
                                  yyyy/mm   2020/12/file.jpg
                                  yyyy-mm   2020-12/file.jpg
                                  flat      file.jpg                (all in one)
  --format <text|csv|json>        find-duplicates only: report format (default: inferred
                                  from --output extension, else text).
  --output <file|->               find-duplicates only: write the report to a file
                                  instead of stdout ("-" = stdout).
  --delete                        find-duplicates only: delete the redundant copies of each
                                  duplicate family (never the ★ keeper). Asks for confirmation
                                  unless --yes; --dry-run only plans. Also removes a deleted
                                  copy's own JSON sidecar / .xmp.
  --no-filename-fallback          Never derive capture dates from filenames.
  --jobs <N>                      Parallel ExifTool workers (default: number of CPUs, max 8).
  --assume-noon-for-date-only     Use 12:00:00 for date-only filename matches (default: off).

Examples:
  gophix fix --dry-run --timezone Europe/Berlin "/data/Takeout/Google Fotos"
  gophix fix --timezone Europe/Berlin "/data/Takeout/Google Fotos"
  gophix organize-by-year --dry-run "/data/Takeout/Google Fotos" "/data/Organized"
  gophix organize-by-year --layout yyyy/mm "/data/Takeout/Google Fotos" "/data/Organized"
  gophix find-duplicates "/data/Takeout/Google Fotos"
  gophix find-duplicates --output dupes.csv "/data/Takeout/Google Fotos"
  gophix clean-json --yes "/data/Takeout/Google Fotos"

Always work on a copy of your Takeout export.
`

// globalOpts holds flags shared by the subcommands.
type globalOpts struct {
	DryRun                bool
	Verbose               bool
	Timezone              string
	ForceJSON             bool
	TimePolicy            string
	NoFilenameFallback    bool
	AssumeNoonForDateOnly bool
	Jobs                  int
}

func (g *globalOpts) clock() (*meta.Clock, error) {
	policy, err := meta.ParseTimePolicy(g.TimePolicy)
	if err != nil {
		return nil, err
	}
	return meta.NewClock(meta.ClockConfig{
		Policy:                policy,
		ForceJSON:             g.ForceJSON,
		Timezone:              g.Timezone,
		NoFilenameFallback:    g.NoFilenameFallback,
		AssumeNoonForDateOnly: g.AssumeNoonForDateOnly,
	})
}

func isFlag(a string) bool { return strings.HasPrefix(a, "-") && a != "-" }

func flagValue(args []string, i *int, name string) (string, bool) {
	a := args[*i]
	if strings.HasPrefix(a, name+"=") {
		return strings.TrimPrefix(a, name+"="), true
	}
	if a == name {
		if *i+1 >= len(args) || isFlag(args[*i+1]) {
			return "", false
		}
		*i++
		return args[*i], true
	}
	return "", false
}

type parsedArgs struct {
	positional []string
	g          globalOpts
	yes        bool
	move       bool
	includeUnk bool
	keepJSON   bool
	layout     string
	format     string
	output     string
	del        bool
}

func parseArgs(args []string) (*parsedArgs, error) {
	p := &parsedArgs{g: globalOpts{TimePolicy: "preserve-existing"}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !isFlag(a):
			p.positional = append(p.positional, a)
			continue
		case a == "--dry-run":
			p.g.DryRun = true
		case a == "--verbose" || a == "-v":
			p.g.Verbose = true
		case a == "--force-json-time":
			p.g.ForceJSON = true
		case a == "--yes":
			p.yes = true
		case a == "--delete":
			p.del = true
		case a == "--move":
			p.move = true
		case a == "--include-unknown-date":
			p.includeUnk = true
		case a == "--keep-json":
			p.keepJSON = true
		case a == "--no-filename-fallback":
			p.g.NoFilenameFallback = true
		case a == "--assume-noon-for-date-only":
			p.g.AssumeNoonForDateOnly = true
		default:
			if v, ok := flagValue(args, &i, "--jobs"); ok {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("invalid --jobs %q", v)
				}
				p.g.Jobs = n
				continue
			}
			if v, ok := flagValue(args, &i, "--timezone"); ok {
				p.g.Timezone = v
				continue
			}
			if v, ok := flagValue(args, &i, "--time-policy"); ok {
				p.g.TimePolicy = v
				continue
			}
			if v, ok := flagValue(args, &i, "--layout"); ok {
				if !isValidLayout(v) {
					return nil, fmt.Errorf("invalid --layout %q (want %s)", v, strings.Join(validLayouts, ", "))
				}
				p.layout = v
				continue
			}
			if v, ok := flagValue(args, &i, "--format"); ok {
				p.format = v
				continue
			}
			if v, ok := flagValue(args, &i, "--output"); ok {
				p.output = v
				continue
			}
			return nil, fmt.Errorf("unknown option %q", a)
		}
	}
	return p, nil
}

// Run dispatches a subcommand and returns the process exit code.
func Run(args []string, stdout io.Writer, stderr io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return ExitOK
	case "version", "--version":
		fmt.Fprintf(stdout, "gophix %s\n", version)
		return ExitOK
	}

	cmd := args[0]
	rest := args[1:]

	p, err := parseArgs(rest)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, usage)
		return ExitUsage
	}
	if len(p.positional) == 0 {
		fmt.Fprintf(stderr, "error: missing path argument\n\n%s", usage)
		return ExitUsage
	}

	clock, err := p.g.clock()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitUsage
	}

	// find-duplicates is pure filesystem work and runs without ExifTool.
	needsExiftool := cmd != "find-duplicates"
	if needsExiftool {
		if err := meta.Available(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return ExitNoExiftool
		}
		meta.ConfigureJobs(p.g.Jobs)
	}

	// Buffer the hot per-file output: tens of thousands of small writes would
	// otherwise cost one syscall each. confirm() flushes before blocking on
	// stdin; everything else is flushed once at the end.
	bw := bufio.NewWriter(stdout)
	sum := report.New(bw)

	var code int
	switch cmd {
	case "fix":
		code = runFix(cleanPathArg(p.positional[0]), &p.g, clock, sum, bw)
	case "clean-json":
		code = runCleanJSON(cleanPathArg(p.positional[0]), &p.g, clock, p.yes, stdin, bw, sum)
	case "organize-by-year":
		if len(p.positional) < 2 {
			fmt.Fprintf(stderr, "error: organize-by-year needs <source-path> <destination-path>\n\n%s", usage)
			return ExitUsage
		}
		code = runOrganize(organizeCfg{
			src: cleanPathArg(p.positional[0]), dst: cleanPathArg(p.positional[1]),
			move: p.move, includeUnknown: p.includeUnk, keepJSON: p.keepJSON,
			layout: p.layout,
			g:      p.g, clock: clock,
		}, sum, bw)
	case "find-duplicates":
		format, ferr := inferDupFormat(p.output, p.format)
		if ferr != nil {
			fmt.Fprintf(stderr, "error: %v\n\n%s", ferr, usage)
			return ExitUsage
		}
		code = runFindDuplicates(dupCfg{
			root:   cleanPathArg(p.positional[0]),
			format: format,
			output: p.output,
			delete: p.del,
			yes:    p.yes,
			g:      p.g,
		}, stdin, bw)
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n\n%s", cmd, usage)
		return ExitUsage
	}

	// find-duplicates renders its own summary/footer.
	if cmd == "find-duplicates" {
		_ = bw.Flush()
		return code
	}

	fmt.Fprintln(bw, "")
	sum.Print(bw)
	if code == ExitOK && sum.HasErrors() {
		code = ExitErrors
	}
	_ = bw.Flush()
	meta.CloseAll() // shut down pooled stay_open exiftool processes
	return code
}
