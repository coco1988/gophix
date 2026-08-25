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

const usage = `gophix - restore Google Photos Takeout metadata into your media

Usage:
  gophix fix [options] <takeout-media-root>
  gophix clean-json [options] <takeout-media-root>
  gophix organize-by-year [options] <source-path> <destination-path>
  gophix version | help

Commands:
  fix               Merge sidecar metadata (dates, GPS, description) into media.
  clean-json        Delete matched & verified JSON sidecars (safe by default).
  organize-by-year  Copy or move media into date folders by capture date.

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
  --no-filename-fallback          Never derive capture dates from filenames.
  --jobs <N>                      Parallel ExifTool workers (default: number of CPUs, max 8).
  --assume-noon-for-date-only     Use 12:00:00 for date-only filename matches (default: off).

Examples:
  gophix fix --dry-run --timezone Europe/Berlin "/data/Takeout/Google Fotos"
  gophix fix --timezone Europe/Berlin "/data/Takeout/Google Fotos"
  gophix organize-by-year --dry-run "/data/Takeout/Google Fotos" "/data/Organized"
  gophix organize-by-year --layout yyyy/mm "/data/Takeout/Google Fotos" "/data/Organized"
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

	if err := meta.Available(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitNoExiftool
	}
	meta.ConfigureJobs(p.g.Jobs)

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
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n\n%s", cmd, usage)
		return ExitUsage
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
