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

## Usage

```
gophix fix [options] <takeout-media-root>
gophix clean-json [options] <takeout-media-root>
gophix organize-by-year [options] <source-path> <destination-path>
```

Common options: `--dry-run`, `--verbose`, `--timezone <IANA-zone|+HH:MM>`, `--force-json-time`,
`--time-policy preserve-existing|prefer-json|json-only` (default `preserve-existing`),
`--no-filename-fallback`, `--assume-noon-for-date-only`; organize adds `--move`, `--include-unknown-date`,
`--keep-json`, `--layout`; clean-json adds `--yes`.

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

Safe examples (Linux):

```bash
./gophix fix --dry-run --verbose --timezone Europe/Berlin "/data/Takeout/Google Fotos"

./gophix fix --timezone Europe/Berlin "/data/Takeout/Google Fotos"

./gophix organize-by-year --dry-run \
  "/data/Takeout/Google Fotos" \
  "/data/GooglePhotos-Organized"

./gophix organize-by-year --layout yyyy/mm \
  "/data/Takeout/Google Fotos" \
  "/data/GooglePhotos-Organized"

./gophix clean-json --dry-run "/data/Takeout/Google Fotos"
```

Windows PowerShell examples:

```powershell
.\gophix.exe fix --dry-run 'D:\Takeout\Google Fotos'

.\gophix.exe organize-by-year `
  'D:\Takeout\Google Fotos' `
  'D:\GooglePhotos-Organized'
```

Exit codes: `0` success · `1` processing errors · `2` usage error · `3` ExifTool missing.

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
