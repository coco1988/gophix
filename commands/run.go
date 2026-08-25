package commands

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexdachin/gophix/meta"
	"github.com/alexdachin/gophix/report"
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
  gophix run [options] <takeout-root> <organized-destination>
  gophix find-duplicates [options] <takeout-root>
  gophix fix [options] <takeout-media-root>
  gophix organize-by-year [options] <source> <destination>
  gophix clean-json [options] <takeout-root>
  gophix version | help

The simple workflow:
  gophix run 'Takeout' 'Organized'
     step 1/3  deduplicate      find exact copies; removes them with --yes, reports otherwise
     step 2/3  correct dates    photo date wins, JSON fills gaps, filename last resort
     step 3/3  restructure      copy into Organized/YYYY/

Options:
  --dry-run                       Plan only; never write, rename, move or delete.
  --verbose                       Detailed per-file output.
  --timezone <IANA-zone|±HH:MM>   Optional precision for JSON/filename dates
                                  (e.g. Europe/Berlin). Photo-embedded dates are used as-is.
  --layout <yyyy|yyyy/mm|yyyy-mm|flat>  organize/run: folder structure (default yyyy).
  --move                          organize: move instead of copy (opt-in).
  --include-unknown-date          organize/run: place undated media into Unknown/.
  --keep-json                     organize/run: also copy matched JSON sidecars.
  --delete (+ --yes)              find-duplicates: remove surplus duplicate copies.
  --format <text|csv|json>, --output <file|->  find-duplicates report format/destination.
  --yes                           Skip confirmation prompts (scripts).
  --jobs <N>                      Parallel ExifTool workers (default: CPU count, max 8).

Examples:
  gophix run --dry-run "Takeout" "Organized"        # plan the whole pipeline
  gophix run "Takeout" "Organized"
  gophix fix --timezone Europe/Berlin "Takeout/Google Fotos"

Always work on a copy of your Takeout export.
`

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
	p, err := parseArgs(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, usage)
		return ExitUsage
	}
	if len(p.positional) == 0 {
		fmt.Fprintf(stderr, "error: missing path argument\n\n%s", usage)
		return ExitUsage
	}

	var zone *time.Location
	zone, err = meta.LoadZone(p.g.Timezone)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitUsage
	}

	needsExiftool := cmd != "find-duplicates"
	if needsExiftool {
		if err := meta.Available(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return ExitNoExiftool
		}
		meta.ConfigureJobs(p.g.Jobs)
	}

	bw := bufio.NewWriter(stdout)

	var code int
	switch cmd {
	case "run":
		if len(p.positional) < 2 {
			fmt.Fprintf(stderr, "error: run needs <takeout-root> <organized-destination>\n\n%s", usage)
			return ExitUsage
		}
		code = runPipeline(pipeCfg{
			src: cleanPathArg(p.positional[0]), dst: cleanPathArg(p.positional[1]),
			layout: p.layout, includeUnknown: p.includeUnk, keepJSON: p.keepJSON,
			yes: p.yes, g: p.g,
		}, zone, stdin, bw)
	case "find-duplicates":
		format, ferr := inferDupFormat(p.output, p.format)
		if ferr != nil {
			fmt.Fprintf(stderr, "error: %v\n\n%s", ferr, usage)
			return ExitUsage
		}
		code = runFindDuplicates(dupCfg{
			root: cleanPathArg(p.positional[0]), format: format, output: p.output,
			delete: p.del, yes: p.yes, g: p.g,
		}, stdin, bw)
	case "fix":
		sum := report.New(bw)
		code = runFix(cleanPathArg(p.positional[0]), &p.g, zone, sum, bw)
		fmt.Fprintln(bw, "")
		sum.Print(bw)
		if code == ExitOK && sum.HasErrors() {
			code = ExitErrors
		}
	case "organize-by-year":
		if len(p.positional) < 2 {
			fmt.Fprintf(stderr, "error: organize-by-year needs <source-path> <destination-path>\n\n%s", usage)
			return ExitUsage
		}
		sum := report.New(bw)
		code = runOrganize(organizeCfg{
			src: cleanPathArg(p.positional[0]), dst: cleanPathArg(p.positional[1]),
			move: p.move, includeUnknown: p.includeUnk, keepJSON: p.keepJSON,
			layout: p.layout, zone: zone, g: p.g,
		}, sum, bw)
		fmt.Fprintln(bw, "")
		sum.Print(bw)
		if code == ExitOK && sum.HasErrors() {
			code = ExitErrors
		}
	case "clean-json":
		sum := report.New(bw)
		code = runCleanJSON(cleanPathArg(p.positional[0]), &p.g, zone, p.yes, stdin, bw, sum)
		fmt.Fprintln(bw, "")
		sum.Print(bw)
		if code == ExitOK && sum.HasErrors() {
			code = ExitErrors
		}
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n\n%s", cmd, usage)
		return ExitUsage
	}

	_ = bw.Flush()
	meta.CloseAll()
	return code
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
	p := &parsedArgs{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !isFlag(a):
			p.positional = append(p.positional, a)
		case a == "--dry-run":
			p.g.DryRun = true
		case a == "--verbose" || a == "-v":
			p.g.Verbose = true
		case a == "--yes":
			p.yes = true
		case a == "--move":
			p.move = true
		case a == "--delete":
			p.del = true
		case a == "--include-unknown-date":
			p.includeUnk = true
		case a == "--keep-json":
			p.keepJSON = true
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
