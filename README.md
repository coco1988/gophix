# gophix

Restores original metadata (capture date, GPS, description) from Google Photos Takeout JSON
sidecars into your photos and videos, removes duplicate album copies, and organizes everything
into year folders — so file browsers and gallery apps sort correctly again.

> ⚠️ **Always work on a copy of your Takeout export.**

---

## Step-by-step manual

Two ways to drive gophix — pick whichever fits:

- **Step by step** (Steps 1–4 below): full control, inspect between stages.
- **One stop**: `gophix run "Takeout-work" "Organized"` executes dedupe → repair → organize in
  sequence with the same safety rules (see "All three steps in one command" at the end).

### Step 0 — Prepare

1. Extract your Google Takeout zip.
2. Make a working copy and never touch the original:

```bash
# Linux/macOS                      # Windows (PowerShell)
cp -r Takeout Takeout-work         Copy-Item -Recurse Takeout Takeout-work
```

All following commands run against `Takeout-work` (adjust the paths to yours).

---

### Step 1 — Remove duplicate album copies

Google albums are labels, not folders: every album member is exported as a **full byte copy**
inside each album directory. Remove the surplus copies *before* repairing, while they are still
byte-identical.

```bash
gophix find-duplicates --dry-run "Takeout-work"        # 1a: preview what would go
gophix find-duplicates --delete --yes "Takeout-work"   # 1b: remove them
```

What happens: files with identical SHA-256 form families; each family keeps exactly one copy
(★ suggested keeper), the rest are deleted. Nothing else is touched. Without `--delete` this is
report-only; without `--yes` it asks `[y/N]`.

---

### Step 2 — Repair dates, GPS and descriptions

```bash
gophix fix --dry-run --verbose "Takeout-work/Google Fotos"   # 2a: preview (writes nothing)
gophix fix "Takeout-work/Google Fotos"                       # 2b: repair
```

Which date wins (in order):

1. **The photo's own capture date** — used as-is, never rewritten.
2. The JSON sidecar date (`photoTakenTime`) if the photo has none — add `--timezone Europe/Berlin`
   for correct local times and offset tags (optional but recommended for your home region).
3. Filename patterns like `IMG_20201206_142433.jpg` — last resort only.

Also done automatically: GPS filled from JSON **only when missing**, description/title copied,
wrong extensions fixed (`.png` pretending to be `.jpg`), filesystem modification/creation times set
so Explorer and galleries sort correctly.

Notes: `fix` is idempotent — rerunning reports `already correct` and touches nothing.
Undated photos are reported as `undated (left untouched)` — they stay where they are.
Problem cases can be isolated instead of blocking the run:
`--failed-dir "errors"` copies failed files (with sidecar) aside, `--failed-move` moves them.

Verify any result independently:

```bash
exiftool -time:all -gps:all "Takeout-work/Google Fotos/Fotos von 2022/IMG_xxxx.jpg"
```

---

### Step 3 — Organize into year folders

```bash
gophix organize-by-year "Takeout-work" "Organized"
```

Copies (never moves by default) every dated photo/video into `Organized/YYYY/`, e.g.
`Organized/2022/IMG_20220814_153000.jpg`. Sources stay untouched, nothing is ever overwritten;
name collisions get deterministic suffixed names shared by media + sidecars.

Variants:

```bash
--layout yyyy/mm          # 2022/08/…        --layout yyyy-mm   # 2022-08/…
--layout flat             # all directly in Organized/
--move                    # move instead of copy (sources deleted after verified copy)
--include-unknown-date    # undated media go to Organized/Unknown/ instead of being skipped
```

---

### Step 4 (optional) — Delete the used-up JSON sidecars

Only sidecars whose media verified fully repaired are eligible; album metadata
(`Metadaten.json`, …) always survives.

```bash
gophix clean-json --dry-run "Takeout-work"     # inspect first
gophix clean-json --yes "Takeout-work"
```

---

## All three steps in one command

Prefer letting gophix do the sequence? This runs dedupe → repair → organize:

```bash
gophix run --yes "Takeout-work" "Organized"
```

(`--yes` approves the step-1 deletions; `--dry-run` plans the whole pipeline; `--timezone`,
`--layout`, `--include-unknown-date`, `--keep-json`, `--failed-dir` pass through to the steps.)

---

## Options reference

| Option | Effect |
|---|---|
| `--dry-run` / `--verbose` | Plan without writing · detailed per-file output |
| `--timezone <IANA-zone\|±HH:MM>` | Precision for JSON/filename dates, e.g. `Europe/Berlin` |
| `--layout yyyy\|yyyy/mm\|yyyy-mm\|flat` | Folder structure (default `yyyy`) |
| `--move` | organize: move instead of copy |
| `--include-unknown-date`, `--keep-json` | organize extras |
| `--delete`, `--yes` | duplicate removal controls |
| `--failed-dir <folder>`, `--failed-move` | isolate processing failures |
| `--format text\|csv\|json`, `--output <file>` | duplicate report format/destination |
| `--jobs <N>` | Parallel ExifTool workers (default: CPU count, max 8) |

Exit codes: `0` success · `1` processing errors · `2` usage error · `3` ExifTool missing
(`find-duplicates` needs no ExifTool).

Sidecar naming handled: `<media>.supplemental-metadata.json` (current, incl. truncated variants),
`<media>.json`, `<basename>.json` (legacy) plus recovery heuristics. Album-level files
(`metadata.json`, `Metadaten.json`, …) are never matched, modified, or deleted. Private Google data
(account IDs, people labels, URLs) is never transferred.

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
