# gophix

A tool that restores the original metadata of your Google Photos Takeout export into the media files
themselves — and sorts them into year folders.

## Why

Google Takeout exports photos and videos with **new filesystem timestamps** (the moment of the
export), not the original capture times. Since file browsers and gallery apps sort by capture or
creation time, your media ends up in the wrong chronological order — in Windows Explorer, Android
galleries, and photo-management software alike.

gophix merges the metadata from the Takeout JSON sidecar files back into the media itself:

- Original capture date/time (with correct timezone handling).
- GPS coordinates, altitude, and GPS date/time.
- Description/caption and title.
- Filesystem modification time (and creation time where the OS supports it).
- Fixes wrong media extensions (e.g. PNG data with a `.jpg` extension).

Works with every format [ExifTool](https://exiftool.org) supports; if a format cannot safely hold
embedded metadata, gophix writes an XMP sidecar (`<media>.xmp`) instead. Both photos **and** videos
are supported.

> ⚠️ **Always work on a copy of your Takeout export.** gophix is careful by default, but you are
> repairing irreplaceable originals.

## Sidecar naming: current and legacy

Google changed its Takeout format; gophix understands all variants and picks the best candidate per
media file (case-insensitive, same directory only):

| Priority | Pattern | Era |
|---|---|---|
| 1–7 | `<media>.supplemental-metadata.json` (+ truncations like `.supplemental-metadat.json`, … `.supplemental.json`) | current |
| 8–9 | `<media>.json`, `<basename-without-ext>.json` | legacy |
| fallback | other truncated supplemental names, `-edited`/`(1)` duplicates, long-name truncations, renamed-extension recovery | heuristic |

Album-level files such as `metadata.json` / `Metadaten.json` are never matched, modified, or deleted.

## Metadata mapping overview

| Takeout JSON | Written to |
|---|---|
| `photoTakenTime` → `creationTime` → embedded → filename → mtime | images: `DateTimeOriginal`, `CreateDate`, `ModifyDate` (+ offset tags); videos: QuickTime create dates (UTC) |
| `geoData` / `geoDataExif` (valid values only) | `GPSLatitude`/`Longitude` + N/S/E/W refs, `GPSAltitude` ± sea level |
| capture instant | `GPSDateStamp`/`GPSTimeStamp` — always UTC |
| `description` (non-empty) | EXIF `ImageDescription`, XMP-dc `Description`, IPTC `Caption-Abstract` |
| `title` | XMP-dc `Title` |

Private Google data (account IDs, people labels, URLs, album membership) is never transferred.
Empty JSON descriptions never overwrite existing ones.

### Dates & timezone policy

Takeout timestamps are UTC instants, but EXIF capture dates are local wall-clock times. gophix
therefore resolves an offset in this order: existing embedded offset → reconciled embedded time →
`--timezone` (IANA zone with correct historical DST, or fixed `±01:00`). If none can be determined it
keeps the UTC instant and warns loudly instead of pretending UTC is local time.

```bash
# recommended for a Germany-based archive captured in Germany:
./gophix fix --timezone Europe/Berlin "/data/Takeout/Google Fotos"
```

Photos taken abroad need that country's zone instead — `--timezone` is per-run, so run it separately
per trip if needed. See [PROJECT.md](PROJECT.md) for the full policy and
[filename fallback](PROJECT.md#filename-timestamp-fallback) details.

### Filesystem timestamp limitations

- `FileModifyDate`: set on all platforms.
- `FileCreateDate`: writable via ExifTool on Windows/macOS only. On Linux it is reported as
  *unsupported* per file — this is a filesystem limitation, not an error; metadata repair still succeeds.
- `FileAccessDate` is intentionally never modified.

## Installation (Linux)

```bash
sudo apt update
sudo apt install -y golang-go git-all build-essential libimage-exiftool-perl jq python3
```

Build:

```bash
git clone https://github.com/alexdachin/gophix.git
cd gophix
go build -o gophix .
```

## Installation (Windows)

Install [Go](https://go.dev) and [ExifTool](https://exiftool.org); ensure `go`, `git` and `exiftool`
are on your PATH:

```powershell
go build -o gophix.exe .
```

> Tip: avoid a trailing backslash inside quoted paths (`'...\Fotos von 2015\'`). PowerShell mangles
> it into a stray quote for native programs. gophix auto-repairs this, but the clean form is
> `'...\Fotos von 2015'`.

## Complete walkthrough (with real output)

Every example below was executed against exactly this small demo export; the outputs are gophix's
real ones. Reproduce it anytime — commands are copy-paste ready.

Demo tree:

```
~/gophix-demo/Takeout/
├── Google Fotos/
│   ├── IMG_20220814_153000.jpg                        + .supplemental-metadata.json (GPS Rom, Beschreibung)
│   ├── IMG_20190301_104500.jpg                        + IMG_20190301_104500.json    (legacy sidecar name)
│   ├── IMG_20210102_030405.jpg                        ← PNG data, wrong extension, NO sidecar
│   └── Metadaten.json                                 ← album metadata, must never be touched
└── Album Italien 2022/
    └── P1010001.jpg                                   ← byte copy of the Rome photo (+ own sidecar)
```

### Step 0 — always start from a copy

```bash
cp -r ~/Takeout ~/Takeout-original-backup    # keep an untouched original somewhere safe
```

### Step 1 — check for duplicates *before* repairing

Albums duplicate photos byte-for-byte; hashing is cheapest while copies are still identical.

```bash
./gophix find-duplicates ~/gophix-demo/Takeout
```

```text
1 duplicate families found:

── family 1/1 · sha256 38212e… · 2 copies · 642 B each
 ★ KEEP ~/gophix-demo/Takeout/Album Italien 2022/P1010001.jpg (sidecar, taken 2022-08-14)
        ~/gophix-demo/Takeout/Google Fotos/IMG_20220814_153000.jpg (sidecar, taken 2022-08-14)

This is a report only - nothing was deleted or modified.
1 duplicate families: 1 redundant copies, 642 B reclaimable (4 files in 3 directories, 2 hashed, 2 skipped by unique size) - report above
```

The ★ suggestion is advisory. Delete surplus copies yourself if you want (report-only is a design
decision), or just leave them — `organize-by-year` never overwrites either way.

### Step 2 — plan the repair (nothing is written)

```bash
./gophix fix --dry-run --verbose --timezone Europe/Berlin "$HOME/gophix-demo/Takeout/Google Fotos"
```

```text
📓 processing ~/gophix-demo/Takeout/Google Fotos
   • IMG_20190301_104500.jpg: planned (dry-run) [date source: json-photoTakenTime]
🔁 [dry-run] would rename IMG_20210102_030405.jpg -> IMG_20210102_030405.png
   • IMG_20210102_030405.jpg: planned (dry-run) [pattern: IMG_YYYYMMDD_HHMMSS] [date source: filename-date-time]
   • IMG_20220814_153000.jpg: planned (dry-run) [date source: json-photoTakenTime]
   ℹ️  IMG_20220814_153000.jpg: not transferred (no safe standard mapping): geoData.latitudeSpan, geoData.longitudeSpan

📋 summary
   directories scanned:      1
   media files found:         3
   updated directly:          0
   XMP sidecars written:      0
   already correct:           0
   skipped without sidecar:   1
   failed:                    0
   warnings:                  0
   errors:                    0
result: completed
```

Read: both JSON-backed photos will be repaired from their sidecars; the extension-less PNG gets its
name fixed and its date from the filename pattern; `Metadaten.json` is not even counted as media.

### Step 3 — run the repair

```bash
./gophix fix --timezone Europe/Berlin "$HOME/gophix-demo/Takeout/Google Fotos"
```

```text
📓 processing ~/gophix-demo/Takeout/Google Fotos
🔄 renamed IMG_20210102_030405.jpg -> IMG_20210102_030405.png

📋 summary
   directories scanned:      1
   media files found:         3
   updated directly:          3
   XMP sidecars written:      0
   already correct:           0
   skipped without sidecar:   1
   failed:                    0
   fs modification times set: 3
   fs creation times set:     0
   fs creation unsupported:   3
   date source filename-date-time:      1 file(s)
   date source json-photoTakenTime:     2 file(s)
   warnings:                  0
   errors:                    0
result: completed
```

(`fs creation unsupported` on Linux means exactly that — filesystem limitation, not an error.)

### Step 4 — re-run proves idempotency

```bash
./gophix fix --timezone Europe/Berlin "$HOME/gophix-demo/Takeout/Google Fotos"
```

```text
   updated directly:          0
   XMP sidecars written:      0
   already correct:           3
   skipped without sidecar:   1
   failed:                    0
result: completed
```

### Step 5 — verify independently with ExifTool

```bash
exiftool -time:all -gps:all -XMP-dc:Description \
  "$HOME/gophix-demo/Takeout/Google Fotos/IMG_20220814_153000.jpg"
```

```text
File Modification Date/Time     : 2022:08:14 16:10:00+02:00
Modify Date                     : 2022:08:14 16:10:00
Date/Time Original              : 2022:08:14 16:10:00
Create Date                     : 2022:08:14 16:10:00
Offset Time                     : +02:00
Offset Time Original            : +02:00
Offset Time Digitized           : +02:00
GPS Time Stamp                  : 14:10:00
GPS Date Stamp                  : 2022:08:14
GPS Latitude Ref                : North
GPS Longitude Ref               : East
GPS Altitude Ref                : Above Sea Level
Description                     : Kolosseum in Rom
```

Local time 16:10+02:00 (= 14:10 UTC, the JSON instant), GPS stamps UTC — exactly the documented
policy. The filename-repaired PNG carries `2021:01:02 03:04:05` with `+01:00`.

### Step 6 — organize by year (copies; sources stay untouched)

```bash
./gophix organize-by-year "$HOME/gophix-demo/Takeout" "$HOME/gophix-demo/Organized"
find "$HOME/gophix-demo/Organized" -type f | sort
```

```text
📋 summary
   date source embedded-DateTimeOriginal: 3 file(s)
   date source json-photoTakenTime:     1 file(s)
   copied:                    4
   moved:                     0
   already present (skipped): 0
   collisions resolved:       0
   placed in Unknown/:        0
result: completed

gophix-demo/Organized/2019/IMG_20190301_104500.jpg
gophix-demo/Organized/2021/IMG_20210102_030405.png
gophix-demo/Organized/2022/IMG_20220814_153000.jpg
gophix-demo/Organized/2022/P1010001.jpg
```

### Step 7 — clean up sidecars of repaired media only

```bash
./gophix clean-json --dry-run "$HOME/gophix-demo/Takeout"   # inspect first
./gophix clean-json --yes "$HOME/gophix-demo/Takeout"       # then delete
find "$HOME/gophix-demo/Takeout" -name "*.json"             # what remains:
```

```text
matched & verified JSON sidecars:
   ~/gophix-demo/Takeout/Google Fotos/IMG_20190301_104500.json  (media: IMG_20190301_104500.jpg)
   ~/gophix-demo/Takeout/Google Fotos/IMG_20220814_153000.jpg.supplemental-metadata.json  (media: IMG_20220814_153000.jpg)

[dry-run] would delete 2 file(s)

# after --yes, still present (deliberately):
gophix-demo/Takeout/Album Italien 2022/P1010001.jpg.supplemental-metadata.json   ← that media was never fixed
gophix-demo/Takeout/Google Fotos/Metadaten.json                                  ← generic album metadata
```

Without `--yes`, `clean-json` asks for confirmation interactively (`Really delete these N files? [y/N]`).

## Usage

```
gophix fix [options] <takeout-media-root>
gophix clean-json [options] <takeout-media-root>
gophix organize-by-year [options] <source-path> <destination-path>
gophix find-duplicates [options] <takeout-media-root>
gophix help | version
```

### `fix` — every option in use

```bash
# recommended default: dry-run first, verbose to see decisions
./gophix fix --dry-run --verbose --timezone Europe/Berlin "/data/Takeout/Google Fotos"

# real run (same flags minus --dry-run)
./gophix fix --timezone Europe/Berlin "/data/Takeout/Google Fotos"

# fixed offset instead of IANA zone
./gophix fix --timezone "+01:00" "/data/Takeout/Google Fotos"

# overwrite valid embedded capture times with the JSON time (opt-in)
./gophix fix --force-json-time --timezone Europe/Berlin "/data/Takeout/Google Fotos"

# JSON always wins over embedded times (policy variant)
./gophix fix --time-policy prefer-json --timezone Europe/Berlin "/data/Takeout"
./gophix fix --time-policy json-only  --timezone Europe/Berlin "/data/Takeout"   # no JSON time = no date

# never derive dates from filenames (strictest mode)
./gophix fix --no-filename-fallback --timezone Europe/Berlin "/data/Takeout/Google Fotos"

# date-only filenames like 2019-03-01.jpg get 12:00:00 (default: off, year-only usage)
./gophix fix --assume-noon-for-date-only --timezone Europe/Berlin "/data/Takeout"

# limit parallel ExifTool workers (default: CPU count, max 8; env GOPHIX_JOBS also works)
./gophix fix --jobs 4 --timezone Europe/Berlin "/data/Takeout/Google Fotos"
```

Flag interactions: `--force-json-time` implies JSON-over-embedded for files that have a JSON
timestamp; `--time-policy json-only` also disables the filename fallback implicitly.

### `clean-json` — every option in use

```bash
./gophix clean-json --dry-run "/data/Takeout"          # list what would be deleted; deletes nothing
./gophix clean-json "/data/Takeout"                    # interactive: 'Really delete … ? [y/N]'
./gophix clean-json --yes "/data/Takeout"              # non-interactive (scripts)
./gophix clean-json --yes --verbose "/data/Takeout"    # additionally print why files are KEPT
```

Only sidecars whose media verifies fully correct (dates, GPS, description, FileModifyDate) are ever
deleted — run it **after** `fix`. Generic/unmatched/unfixable JSON always survives.

### `organize-by-year` — every option in use

```bash
# plan first (never writes/moves/deletes anything)
./gophix organize-by-year --dry-run "/data/Takeout/Google Fotos" "/data/Organized"

# default layout yyyy/, copy mode (sources untouched)
./gophix organize-by-year "/data/Takeout/Google Fotos" "/data/Organized"

# month folders, e.g. 2020/12/file.jpg
./gophix organize-by-year --layout yyyy/mm "/data/Takeout" "/data/Organized"

# one folder per month: 2020-12/
./gophix organize-by-year --layout yyyy-mm "/data/Takeout" "/data/Organized"

# everything directly in /data/Organized (collision-safe names when needed)
./gophix organize-by-year --layout flat "/data/Takeout" "/data/Organized"

# move instead of copy (opt-in; sources removed only after byte-verified copy)
./gophix organize-by-year --move "/data/Takeout/Google Fotos" "/data/Organized"

# include undated media under Unknown/ (otherwise they are skipped & reported)
./gophix organize-by-year --include-unknown-date "/data/Takeout" "/data/Organized"

# also copy matched JSON sidecars next to the media
./gophix organize-by-year --keep-json "/data/Takeout" "/data/Organized"

# verbose per-file decisions incl. collision notices
./gophix organize-by-year --verbose "/data/Takeout" "/data/Organized"
```

Flags combine freely, e.g. plan a move with unknowns included:

```bash
./gophix organize-by-year --dry-run --move --include-unknown-date \
  "/data/Takeout" "/data/Organized"
```

### organize-by-year: folder layouts

`--layout` selects the destination structure (folders are built from the resolved **local** capture
date, whatever source provided it):

| `--layout` | Result | |
|---|---|---|
| `yyyy` *(default)* | `2020/IMG.jpg` | one folder per year |
| `yyyy/mm` | `2020/12/IMG.jpg` | nested year/month |
| `yyyy-mm` | `2020-12/IMG.jpg` | one folder per month |
| `flat` | `IMG.jpg` | everything directly in the destination |

Layout-independent behavior: files without a usable capture date are skipped unless
`--include-unknown-date` places them in `Unknown/`; name collisions are resolved deterministically
(the same final name is shared by media, `.xmp` and optional JSON sidecars); nothing is ever
overwritten.

### find-duplicates

Google Photos albums are labels, not folders: a Takeout export contains a **full byte copy** of every
album member inside each album directory. `find-duplicates` reports those exact duplicates — it is a
report only, nothing is ever deleted or modified.

```bash
./gophix find-duplicates "/data/Takeout"                    # human-readable report to stdout
./gophix find-duplicates --output dupes.csv "/data/Takeout" # format inferred from extension
./gophix find-duplicates --output dupes.json --format json "/data/Takeout"
./gophix find-duplicates --format json --output - "/data/Takeout"   # JSON to stdout

# delete the redundant copies (never the ★ keeper):
./gophix find-duplicates --dry-run --delete "/data/Takeout"  # plan first - deletes nothing
./gophix find-duplicates --delete "/data/Takeout"            # asks: Really delete N files? [y/N]
./gophix find-duplicates --delete --yes "/data/Takeout"      # non-interactive
```

Sample text report (from the walkthrough above):

```text
── family 1/1 · sha256 38212e… · 2 copies · 642 B each
 ★ KEEP ~/gophix-demo/Takeout/Album Italien 2022/P1010001.jpg (sidecar, taken 2022-08-14)
        ~/gophix-demo/Takeout/Google Fotos/IMG_20220814_153000.jpg (sidecar, taken 2022-08-14)
```

CSV columns: `hash,path,bytes,is_keep,has_sidecar,capture_date` — exactly one row per copy carries
`is_keep,true`.

- Hashing runs only on files whose byte size collides with another file (most of a large library is
  skipped). Requires no ExifTool.
- Each family lists all copies with a ★ keep suggestion (copy with sidecar first, then shorter path).
- Exit code stays `0` when duplicates are found; `1` only for processing errors (unreadable files).

### Deleting duplicates (`--delete`)

Duplicates are exact byte copies, so removing surplus ones is lossless. Safety rails:

- Only the non-★ copies of each family are removed; the keeper always survives.
- `--dry-run --delete` plans only; `--delete` asks `Really delete N files? [y/N]`; `--yes` skips the
  prompt for scripts.
- A deleted copy's own JSON sidecar and `<media>.xmp` are removed too — no orphans are left.
- Deletion failures (permissions) are reported per file and make the exit code `1`.
- The report/footer states exactly what was deleted and how many bytes were freed.

Recommended order: **find-duplicates → fix → organize-by-year**. Dedupe first while Google's copies
are still byte-identical; after `fix`, repaired files differ from their unfixed album copies and can
no longer be matched by content hashing.

### Windows PowerShell examples

```powershell
.\gophix.exe fix --dry-run --verbose --timezone Europe/Berlin 'D:\Takeout\Google Fotos'

.\gophix.exe fix --timezone Europe/Berlin 'D:\Takeout\Google Fotos'

.\gophix.exe organize-by-year --layout yyyy/mm `
  'D:\Takeout\Google Fotos' `
  'D:\GooglePhotos-Organized'

.\gophix.exe organize-by-year --move --include-unknown-date `
  'D:\Takeout\Google Fotos' `
  'D:\GooglePhotos-Organized'

.\gophix.exe clean-json --dry-run 'D:\Takeout'
.\gophix.exe clean-json --yes 'D:\Takeout'

.\gophix.exe find-duplicates --output D:\dupes.csv 'D:\Takeout'
```

Exit codes: `0` success · `1` processing errors · `2` usage error · `3` ExifTool missing
(`find-duplicates` never needs ExifTool and cannot produce `3`).

## Performance

gophix is built for large archives (tens of thousands of files):

- Persistent ExifTool processes (`-stay_open` batching) instead of one process per operation.
- Parallel workers (`--jobs N`, default: number of CPUs, capped at 8; `GOPHIX_JOBS` env override).
- Minimal invocations per file (extension detection folded into the metadata read,
  filesystem timestamp written within the same call as the metadata).

Measured on a notebook (4 workers, 300 files): first run ≈ 26 ms/file, verified
re-run ≈ 14 ms/file - roughly **20x faster** than process-per-operation mode.
25,000 photos therefore take on the order of 10-15 minutes, not hours.

## Documentation

- [TASK.md](TASK.md) — task tracker, acceptance criteria, test matrix, evidence.
- [PROJECT.md](PROJECT.md) — architecture, matching/mapping specs, timezone policy, safety behavior,
  limitations.
