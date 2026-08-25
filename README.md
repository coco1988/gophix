# gophix

Restores original metadata (capture date/time, GPS, description) from Google Photos Takeout JSON
sidecars back into your photos and videos — so file browsers and gallery apps sort them correctly
again — and can organize everything into year/month folders.

Works with every format [ExifTool](https://exiftool.org) supports (photos *and* videos); formats
that cannot hold embedded metadata get an XMP sidecar (`<media>.xmp`) instead.

> ⚠️ **Always work on a copy of your Takeout export.**

## Quick start

```bash
cp -r Takeout Takeout-backup                                        # work on a copy!

gophix find-duplicates --dry-run Takeout                            # albums duplicate photos;
gophix find-duplicates --delete Takeout                             # remove surplus copies (asks [y/N])
gophix fix --dry-run --verbose --timezone Europe/Berlin "Takeout/Google Fotos"
gophix fix --timezone Europe/Berlin "Takeout/Google Fotos"          # writes dates, GPS, description
gophix organize-by-year --dry-run Takeout ~/Pictures/Organized      # plan first …
gophix organize-by-year Takeout ~/Pictures/Organized                # copies into YYYY/ folders
```

Order matters: dedupe **before** `fix` (afterwards repaired files are no longer byte-identical).
`fix` is idempotent — running it twice reports `already correct`. `clean-json --yes Takeout` can
later delete sidecars whose media was verified repaired; `Metadaten.json` & co. are never touched.

Windows: identical commands in PowerShell, e.g. `.\gophix.exe fix --timezone Europe/Berlin 'D:\Takeout\Google Fotos'`.

## Commands

| Command | Purpose |
|---|---|
| `fix <root>` | Merge sidecar metadata into media (dates, GPS, description); fixes wrong extensions |
| `find-duplicates <root>` | Report exact byte duplicates (album copies). `--delete` removes surplus copies |
| `organize-by-year <src> <dst>` | Copy (or `--move`) media into date-based folders |
| `clean-json <root>` | Delete sidecars of verified-repaired media only |

## Options

| Option | Applies to | Effect |
|---|---|---|
| `--timezone <IANA\|±HH:MM>` | fix, organize | Resolve local capture times, e.g. `Europe/Berlin`. Recommended. |
| `--dry-run` / `--verbose` | all | Plan without writing / detailed per-file output |
| `--force-json-time` | fix, organize | Let the JSON timestamp overwrite valid embedded times |
| `--time-policy <p>` | fix, organize | `preserve-existing` (default) \| `prefer-json` \| `json-only` |
| `--no-filename-fallback` | fix, organize | Never derive dates from filenames like `IMG_20201206_142433.jpg` |
| `--assume-noon-for-date-only` | fix, organize | Date-only names (`2019-03-01.jpg`) get 12:00 instead of year-only |
| `--jobs <N>` | fix, clean-json | Parallel ExifTool workers (default: CPU count, max 8) |
| `--layout yyyy\|yyyy/mm\|yyyy-mm\|flat` | organize | Folder structure: `2020/` · `2020/12/` · `2020-12/` · destination root |
| `--move` | organize | Move instead of copy (sources deleted only after byte-verified copy) |
| `--include-unknown-date` | organize | Place undated media under `Unknown/` instead of skipping |
| `--keep-json` | organize | Also copy matched JSON sidecars |
| `--delete` (+ `--yes`) | find-duplicates | Delete non-★ duplicate copies; confirmation unless `--yes` |
| `--format <text\|csv|json>`, `--output <file>` | find-duplicates | Report format/destination (format inferred from extension) |

Never overwrites anything anywhere; collisions get deterministic suffixed names shared by media +
sidecars. Undated files are skipped unless `--include-unknown-date`.

## What gets written

| Takeout JSON | Destination tags |
|---|---|
| `photoTakenTime` → `creationTime` → embedded → filename → mtime | images: `DateTimeOriginal`, `CreateDate`, `ModifyDate` + offset tags · videos: QuickTime dates (UTC) |
| `geoData` / `geoDataExif` | `GPSLatitude/Longitude` (+N/S/E/W), `GPSAltitude`, GPS date/time (UTC) |
| `description`, `title` | EXIF/XMP/IPTC description, XMP `Title` |

Private Google data (account IDs, people labels, URLs, album membership) is never transferred.
Sidecars recognized: `<media>.supplemental-metadata.json` (current, incl. truncated variants),
`<media>.json` and `<basename>.json` (legacy), plus recovery heuristics. Album-level files
(`metadata.json`, `Metadaten.json`, …) are never matched, modified, or deleted.

## Dates & timezones

Takeout timestamps are UTC instants; image tags receive the **local wall clock** plus offset tags,
GPS stamps stay UTC. Offset resolution: existing embedded offset → reconciled embedded time →
`--timezone`. Without any of these, UTC digits are written with a loud warning rather than faked
locality. Photos from different countries: run `fix` separately per trip's timezone.
`FileCreateDate` is only writable on Windows/macOS (reported unsupported on Linux — not an error);
`FileAccessDate` is never touched.

## Installation

```bash
sudo apt install -y golang-go git-all build-essential libimage-exiftool-perl   # Debian/Ubuntu
git clone https://github.com/coco1988/gophix.git && cd gophix && go build -o gophix .
```

Windows/macOS: install [Go](https://go.dev) + [ExifTool](https://exiftool.org), then
`go build -o gophix.exe .`

Exit codes: `0` success · `1` processing errors · `2` usage error · `3` ExifTool missing.

Performance: persistent ExifTool processes + parallel workers ≈ 20x faster than process-per-call;
25k photos in roughly 10–15 minutes.

More detail: [PROJECT.md](PROJECT.md) (architecture, matching/mapping specs, safety behavior,
limitations) · [TASK.md](TASK.md) (task tracker, test matrix, evidence).
