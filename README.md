# gophix

Restores original metadata (capture date, GPS, description) from Google Photos Takeout JSON
sidecars into your photos and videos, removes duplicate album copies, and organizes everything
into year folders — so file browsers and gallery apps sort correctly again.

> ⚠️ **Always work on a copy of your Takeout export.**

## The workflow

```bash
gophix run 'Takeout' 'Organized'
```

That single command performs the three steps:

| Step | What happens |
|---|---|
| **1/3 deduplicate** | Albums duplicate photos byte-for-byte. Exact copies are found (SHA-256); surplus copies are removed — never the suggested keeper. Asks `[y/N]`; add `--yes` for scripts, `--dry-run` plans only. |
| **2/3 correct dates** | The photo's own capture date wins. If it has none, the JSON sidecar date is used (with `--timezone` applied when given). Filename patterns (`IMG_20201206_142433.jpg`, WhatsApp, …) are the last resort. GPS is filled from JSON only when the photo has none — existing GPS is never overwritten. Filesystem modification time follows the chosen date. |
| **3/3 restructure** | Media is **copied** into `Organized/YYYY/` (sources stay untouched). Undated files are reported; `--include-unknown-date` places them under `Unknown/`. Name collisions get deterministic suffixed names shared with their sidecars; nothing is ever overwritten. |

Every step also works alone:

```
gophix find-duplicates [--delete] <root>
gophix fix [options] <takeout-media-root>
gophix organize-by-year [options] <source> <destination>
gophix clean-json [--yes] <root>        # optional step 4: delete sidecars of verified-repaired media
```

Problem cases (unreadable media, invalid sidecars, write failures) can be isolated during `fix`:
`--failed-dir <folder>` copies them — with their sidecar — into one place for inspection; add
`--failed-move` to relocate instead. The run continues, the summary counts them, and the exit code
stays `1` so scripts notice.

## Options

| Option | Effect |
|---|---|
| `--dry-run` / `--verbose` | Plan without writing · detailed per-file output |
| `--timezone <IANA-zone\|±HH:MM>` | Optional precision for JSON/filename dates, e.g. `Europe/Berlin`. Photo-embedded dates are used exactly as they are. |
| `--layout yyyy\|yyyy/mm\|yyyy-mm\|flat` | Folder structure (default `yyyy`) |
| `--move` | organize: move instead of copy (sources deleted only after byte-verified copy) |
| `--include-unknown-date`, `--keep-json` | organize extras |
| `--delete`, `--yes` | find-duplicates removal controls |
| `--failed-dir <folder>` (+ `--failed-move`) | fix/run: isolate files whose processing failed into this folder (with their sidecar) — copy by default, move with the flag. Never overwrites; undated-but-valid media is not quarantined. |
| `--format text\|csv\|json`, `--output <file>` | duplicate report format/destination |
| `--jobs <N>` | Parallel ExifTool workers (default: CPU count, max 8) |

Sidecar naming: current `<media>.supplemental-metadata.json` (incl. truncated variants) plus legacy
`<media>.json` / `<basename>.json`. Album-level files (`metadata.json`, `Metadaten.json`, …) are
never matched, modified, or deleted. Private Google data (account IDs, people labels, URLs) is
never transferred.

Exit codes: `0` success · `1` processing errors · `2` usage error · `3` ExifTool missing
(`find-duplicates` needs no ExifTool).

Performance: persistent ExifTool processes + parallel workers ≈ 20x faster than process-per-call;
25,000 photos in roughly 10–15 minutes.

## Install

Linux (Debian/Ubuntu):

```bash
sudo apt install -y golang-go git-all build-essential libimage-exiftool-perl
git clone https://github.com/coco1988/gophix.git && cd gophix && go build -o gophix .
```

Windows/macOS: install [Go](https://go.dev) + [ExifTool](https://exiftool.org), then
`go build -o gophix.exe .`
