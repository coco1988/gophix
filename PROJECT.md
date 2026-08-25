# PROJECT.md — gophix technical documentation

Durable technical documentation for gophix **as implemented** (post-modernization). Everything here
describes the actual code in this repository.

## Purpose and target user problem

Google Photos Takeout exports contain media files plus JSON sidecars holding original metadata
(capture time, GPS, description). The export process stamps files with new filesystem timestamps, so
file browsers and gallery apps (Windows Explorer, Android galleries, photo tools) sort media in the
wrong chronological order. gophix restores the original metadata **into the media itself** (or XMP
sidecars where embedding is unsafe), so consumers sort and display correctly again.

## Design principles

1. **JSON is authoritative only for what it explicitly provides** — known fields are mapped explicitly;
   unknown/opaque keys are never copied into EXIF/IPTC/XMP.
2. **Safety first** — dry-run everywhere, copy-by-default organization, never overwrite, never delete
   unmatched/generic/invalid JSON, source Takeout untouched unless explicitly requested (`--move`).
3. **ExifTool-outcome-driven format support** — no hard-coded media allowlist decides writability.
   gophix attempts an embedded write and verifies by reading back; failure falls back to an XMP sidecar.
4. **Deterministic behavior** — priority-ordered sidecar matching, sorted directory iteration,
   content-hash collision names, stable summaries.
5. **Idempotency** — desired values are computed first and compared against current values; equal means
   skip (no rewrite).
6. **Honest time handling** — Takeout epochs are UTC instants; local display times require a resolved
   offset; unresolved cases warn instead of silently mislabeling.

## Architecture

```
main.go                  CLI entry: exiftool availability gate, exit codes, tzdata embedding
takeout/
  model.go               typed Takeout JSON model + effective-value resolution
  sidecar.go             sidecar matching engine + directory classification
meta/
  exec.go                exiftool exec wrapper, availability check
  batch.go               stay_open process pool (`Exec`, job limiting)
  read.go                metadata read-back (exiftool -j -n -G1) → Info map
  time.go                clock/timezone resolution engine + formatting helpers
  filename.go            strict filename timestamp parser
  apply.go               plan builder (desired tags + expected values) & writer
commands/
  run.go                 subcommand dispatch, flag parsing, usage text
  fix.go                 fix command (+ shared directory walker)
  cleanjson.go           clean-json command
  organize.go            organize-by-year command
  dupfind.go             find-duplicates command (report-only, no ExifTool)
  extfix.go              media extension correction via ExifTool detection
report/summary.go        counters, warnings/errors collection, summary printing
```

Data flow (`fix`): walk tree → classify entries per directory → match sidecars → parse sidecar JSON →
read current media metadata → build plan (resolve times/GPS/description) → compare (skip if satisfied) →
write via exiftool → read back to verify → on failure retry as XMP sidecar → summarize.

## Sidecar matching specification

For media file `<M>` in a directory, candidates are looked up **in that directory only**,
case-insensitively, evaluated in order; the first existing candidate wins:

1–7. `<M>.supplemental-metadata.json` plus truncations `-metadat`, `-metada`, `-metad`, `-meta`,
     `-met`, `` (exact suffix list from the task spec)
8. `<M>.json` (legacy full name)
9. `<basename-without-extension>.json` (legacy basename)
10. **Generic truncated rule**: any entry starting with `<M>.supplemental` (case-insensitive) and ending
    `.json` whose remaining middle part is empty or starts with `-` followed only by ASCII `a–z`
    (i.e. a prefix-truncation of `-metadata`). Rejects anything else (`-metadata-old`, digits, etc.).
11. Legacy heuristics: `-edited` stripped name (one level), MP4→`.jpg/.jpeg/.heic` photo-sidecar names,
    `(n)` duplicate-suffix moves (`IMG(1).jpg` ↔ `IMG.jpg(1).json`), 46-char filename truncation.
12. **Reverse-renamed rule**: `<stem>.<ext>.supplemental-metadata.json` whose stem-minus-extension equals
    the media stem-minus-extension and whose extension differs but is a plausible media extension —
    recovers association after `fix` renamed the media extension. Only applied when no direct candidate
    matched and the sidecar is not claimed by another media file's higher-priority match.

Selections from tiers 10–12 are non-exact: they emit warnings. If multiple valid candidates exist all
candidates are logged and the selected one named. Claimed sidecars are recorded per directory so two
media files cannot share one sidecar silently.

Never matched (hard exclusions, case-insensitive): `metadata.json`, `metadaten.json`, `album.json`,
`shared_album_comments.json`; plus any JSON not derived from a media filename. Junk files
(`Thumbs.db`, `desktop.ini`, `.DS_Store`) and `.xmp` files are ignored during classification.

Worked example — directory listing:

```text
IMG_20220814_153000.jpg
IMG_20220814_153000.jpg.supplemental-metadata.json   ← priority 1 for IMG_20220814_153000.jpg
IMG_20190301_104500.jpg
IMG_20190301_104500.json                            ← priority 9 (<basename>.json) for IMG_20190301_104500.jpg
P1010001.jpg
P1010001.jpg.supplemental-metadat.json              ← priority 2 (truncated) for P1010001.jpg, warns
Metadaten.json                                      ← excluded for everyone
```

## Takeout JSON model

```go
type Geo struct{ Lat, Lon, Alt float64 }
type Sidecar struct {
    Title, Description string
    PhotoTaken, Creation *int64 // unix seconds
    Geo, GeoExif *Geo
}
```

- Effective capture instant: `photoTakenTime.timestamp` → else `creationTime.timestamp`.
- Effective GPS: `geoData` if coordinates valid → else `geoDataExif` if valid → else none.
  Validity rejects the redacted placeholder `(0,0)` and out-of-range values (|lat| ≤ 90, |lon| ≤ 180).
- Altitude is written only when ≠ 0.0 (Takeout uses 0.0 both as value and as placeholder; ambiguity is
  resolved conservatively). Negative altitude = below sea level.
- Unparsable timestamps or malformed JSON make the sidecar invalid: logged as error, never deleted by
  `clean-json`.

### Metadata mapping (implemented)

| JSON field | Destination tags (via ExifTool) |
|---|---|
| `photoTakenTime.timestamp` / `creationTime.timestamp` | images: EXIF `DateTimeOriginal`, `CreateDate`, `ModifyDate`; XMP `exif:DateTimeOriginal`, `xmp:CreateDate` on fallback; offset tags `OffsetTimeOriginal`, `OffsetTimeDigitized`, `OffsetTime`. videos: QuickTime `CreateDate`, `ModifyDate` (**UTC**, container spec) |
| effective geo lat/lon | `GPSLatitude`+`GPSLatitudeRef`, `GPSLongitude`+`GPSLongitudeRef` (absolute value + N/S/E/W ref); XMP signed decimals on fallback |
| effective geo alt (≠0) | `GPSAltitude` + `GPSAltitudeRef` (0 above / 1 below sea level) |
| capture instant (when GPS written) | `GPSDateStamp`, `GPSTimeStamp` — always UTC |
| `description` (non-empty) | EXIF `ImageDescription`, XMP-dc `Description`, IPTC `Caption-Abstract` (Caption-Abstract verified leniently: checked only when present) |
| `title` (non-empty) | XMP-dc `Title` |

Deliberately **not** transferred (no safe standard mapping / private data): `imageViews`, `url`,
`googlePhotosOrigin.*`, `appSource.*`, `geoData*.latitudeSpan/longitudeSpan`, account identifiers,
people/face data, album membership. In verbose mode present-but-skipped known fields are reported.

Empty JSON description never overwrites an existing non-empty embedded description (description is only
written when the JSON provides one).

### Timezone policy

See TASK.md §"Timezone and timestamp policy" (binding spec). Summary of implementation:

- Source priority: embedded-with-offset → embedded-reconciled (≤26 h delta vs JSON instant) →
  `photoTakenTime` → `creationTime` → **filename** (see next section) → mtime. Under `prefer-json`,
  `json-only` or `--force-json-time`, JSON moves to the front of the chain.
- Offset chain: existing offset tags → reconciled embedded time → `--timezone` (IANA zone with
  historical DST via Go `time.LoadLocation`, or fixed `±HH:MM`) → UTC + prominent warning.
- Local wall clock goes to image/XMP date fields (+offset tags, no `Z`). GPS stamps stay UTC.
  QuickTime container dates are written as **UTC verbatim** — ExifTool 13.x stores write-inputs for
  QuickTime tags without timezone conversion; gophix feeds the UTC clock digits directly.
- `FileModifyDate` = resolved instant with offset; `FileCreateDate` attempted separately so unsupported
  platforms report cleanly without failing the repair. `FileAccessDate` is never touched.

Worked example (real run, `--timezone Europe/Berlin`, JSON `photoTakenTime.timestamp = "1660486200"`):

```text
JSON instant            2022-08-14T14:10:00Z   (no embedded offset present)
resolved local          2022-08-14 16:10:00 +02:00        (Europe/Berlin, CEST summer time)
written image tags      DateTimeOriginal/CreateDate/ModifyDate = 2022:08:14 16:10:00
                        OffsetTimeOriginal/Digitized/Offset    = +02:00     (no Z suffix)
written GPS tags        GPSDateStamp = 2022:08:14, GPSTimeStamp = 14:10:00  (always UTC)
written filesystem      FileModifyDate = 2022:08:14 16:10:00+02:00          (same instant)
winter counterpart      same instant in January would resolve to +01:00 (CET) — historical DST applies
```

The same JSON without `--timezone` and without any embedded offset keeps UTC clock digits and emits
the documented warning instead of inventing an offset.

### Filename timestamp fallback

When JSON and valid embedded metadata are unavailable, the capture date is parsed from the media
filename using strict patterns (validated through `time.Parse`, so impossible dates/hours fail):

| Pattern | Example | Result | Source label |
|---|---|---|---|
| `IMG_YYYYMMDD_HHMMSS` | `IMG_20201206_142433.jpg` | 2020-12-06 14:24:33 | `filename-date-time` |
| `VID_YYYYMMDD_HHMMSS` | `VID_20201206_142433.mp4` | 2020-12-06 14:24:33 | `filename-date-time` |
| `PXL_YYYYMMDD_HHMMSS…` | `PXL_20201206_142433123.jpg` | 2020-12-06 14:24:33 (extra digits ignored) | `filename-date-time` |
| `YYYYMMDD_HHMMSS` anywhere | `trip_20201206_142433.png` | 2020-12-06 14:24:33 | `filename-date-time` |
| `YYYY-MM-DD_at_HH.MM.SS` | `WhatsApp_Image_2020-12-06_at_14.24.33.jpeg` | full timestamp | `filename-date-time` |
| `YYYY-MM-DD` / `YYYYMMDD` date-only | `2019-03-01.jpg` | year/date only, no invented time | `filename-date-only` |

Rules implemented:

- Filename parsing is a pure fallback; it never overrides JSON or valid embedded times.
- Timestamp-shaped names that fail validation (`IMG_20201332_142433`, hour 25, non-leap Feb 29,
  ambiguous `IMG_12345678`) are rejected outright instead of yielding a partial date.
- Two different valid timestamps in one name → skip with a verbose warning, never guess.
- A date-only match never shadows a full timestamp in the same name and never populates EXIF times;
  it selects the `organize-by-year` year only. `--assume-noon-for-date-only` opts into 12:00:00.
- Known collision suffixes `(1)`, `_1`, `-2` after the date/time are tolerated; extensions are never parsed.
- Filename times carry no timezone. With `--timezone` they gain a real offset (GPS stamps writable);
  without one, clock digits are written without an offset claim plus a verbose warning.
- `--no-filename-fallback` disables the mechanism entirely.

### Embedded vs XMP-sidecar behavior

gophix does not decide writability from extensions. It builds the tag set, runs ExifTool against the
media, then re-reads and compares expected values. Verified success → *updated directly*.
Verification failure or ExifTool refusal → same tags written as an XMP sidecar `<media>.xmp`
(created with `-o` when absent, updated in place otherwise) → *written to XMP sidecar*.
Files ExifTool cannot identify at all are reported as failed rather than given sidecars.

## CLI reference

```
gophix fix [--dry-run] [--verbose] [--timezone <zone|+01:00>] [--force-json-time]
           [--time-policy preserve-existing|prefer-json|json-only]
           [--no-filename-fallback] [--assume-noon-for-date-only] <takeout-media-root>

gophix clean-json [--dry-run] [--yes] [--verbose] <takeout-media-root>

gophix organize-by-year [--dry-run] [--verbose] [--move] [--include-unknown-date]
                        [--keep-json] [--timezone <zone|+01:00>] [--force-json-time]
                        [--time-policy ...] [--no-filename-fallback]
                        [--assume-noon-for-date-only]
                        [--layout yyyy|yyyy/mm|yyyy-mm|flat] <source-path> <destination-path>

gophix find-duplicates [--format text|csv|json] [--output <file>|-] [--verbose]
                       [--delete [--yes]] <takeout-media-root>

gophix version | help
```

Exit codes: `0` success (warnings allowed; for `find-duplicates` also "duplicates found"/deletions
completed) ·
`1` at least one processing error · `2` usage error · `3` ExifTool not found
(`find-duplicates` never requires ExifTool and can therefore not exit `3`).

Path arguments are sanitized: stray quotes and trailing separators produced by Windows PowerShell
quoting (`'C:\dir\'` → received as `C:\dir"`) are repaired before use.

Summaries count scanned directories, media found, per-outcome results (updated directly / XMP sidecar /
already correct / skipped-no-sidecar / failed), filesystem timestamp attempted/succeeded/unsupported,
date sources used, collisions resolved, copies/moves/skips, deleted/kept JSON, warnings and errors.
An empty tree prints an explicit "nothing to do" note (not a false success).

## Safety behavior

- `fix --dry-run`: reads and plans only; zero writes, renames, deletions, timestamp changes.
- `clean-json`: stateless — re-runs matching live, then deletes a sidecar only when its media exists,
  parses fine, and the media's metadata is fully verified correct (dates, GPS, description,
  FileModifyDate). Generic/unmatched/invalid/failed-related JSON is kept. Interactive confirmation
  required unless `--yes`; refuses in non-interactive sessions without `--yes`.
- `organize-by-year`: copy-only by default; destination files never overwritten (identical-content
  targets are skipped as already-present; differing ones get deterministic
  `<stem>-<captureUTC>-<hash6>` names shared by media + its sidecars). `--move` deletes sources only
  after byte-verified copies (size + SHA-256). Unknown-date media skipped unless
  `--include-unknown-date` places them under `<dest>/Unknown/` (independent of `--layout`).
  JSON not copied unless `--keep-json`.
- `--layout` selects the destination structure from the resolved local capture date:
  `yyyy` (default) → `<dst>/2020/f.jpg`; `yyyy/mm` → `<dst>/2020/12/f.jpg`;
  `yyyy-mm` → `<dst>/2020-12/f.jpg`; `flat` → `<dst>/f.jpg`. Invalid values are rejected at
  argument-parse time (exit code 2). Collision handling, Unknown placement, copy/move semantics and
  sidecar naming are identical across layouts.
- `find-duplicates`: scans with the same media detection as the other commands, hashes only files
  whose byte size collides with at least one other file (streaming SHA-256, parallel workers), and
  groups equal digests into families. Each family carries a deterministic keep suggestion (copy with
  matched sidecar first, then shorter path, then lexicographic) marked ★; the suggestion is advisory.
  Reports render as text (default), CSV (`hash,path,bytes,is_keep,is_deleted,has_sidecar,capture_date`)
  or JSON (summary + families); `--output` infers the format from the file extension unless `--format`
  overrides it. Exact byte duplicates only — `-edited` variants or perceptual near-duplicates are out
  of scope. Without `--delete` the command never modifies anything.
- `find-duplicates --delete`: removes exactly the non-★ copies of each family. Confirmation prompt
  unless `--yes`; `--dry-run` plans without touching anything; the deleted copy's own JSON sidecar and
  `<media>.xmp` are removed with it (no orphans); per-file failures are warnings and fail the run;
  report and footer state what was removed and the bytes freed.
- Extension fixing may rename media (`png` masquerading as `jpg`); association survives across runs via
  the reverse-renamed matching rule.

## Performance model

ExifTool is a Perl program; spawning it costs 100-300 ms per call. gophix therefore:

1. keeps up to `--jobs` (default CPU count, max 8) long-lived `-stay_open True -@ -`
   processes alive and streams commands through their stdin, synchronizing via the
   `{readyN}` sentinel (`GOPHIX_NO_BATCH=1` forces legacy spawn behavior);
2. reads metadata in **chunks** (`meta.ReadMany`, up to 32 files per `-execute`,
   results keyed by ExifTool's `SourceFile`; a bad file fails alone, never the batch);
3. issues at most two batch operations per file on a first run (write incl.
   FileModifyDate, verify read-back) and **zero** further operations when everything
   is already correct — the bulk-read Info is reused for planning, the already-correct
   check and `Apply` (re-read only after an actual write or when not supplied);
4. under `--time-policy prefer-json|json-only` or `--force-json-time`, skips the
   embedded-metadata read in `organize-by-year` entirely whenever the sidecar carries
   a JSON timestamp (the resolution chain cannot consult embedded data then);
5. runs per-file work in a worker pool fed by a cheap sequential scan/match phase,
   with one directory listing per directory shared by recursion and matcher.

Read commands pass `-q` so stay_open mode's per-execute progress summary stays off
stderr. Fallbacks are automatic: if a pooled process dies mid-command the call is
retried once on a fresh process; if batch mode cannot start at all, Exec falls back
to plain spawning. Pooled processes are shut down before the CLI exits
(`meta.CloseAll`). Batch arguments must not contain newlines (paths never do in
practice).

Linux (Debian/Ubuntu):

```bash
sudo apt update
sudo apt install -y golang-go git-all build-essential libimage-exiftool-perl jq python3 just
go build -o gophix .
./gophix help
```

Windows (PowerShell): install Go and ExifTool, ensure `go`, `git`, `exiftool` on PATH:

```powershell
go build -o gophix.exe .
```

Notes: gophix embeds `time/tzdata`, so IANA zones like `Europe/Berlin` work without OS tz packages on
any platform. `FileCreateDate` is writable by ExifTool on Windows/macOS; on Linux it reports
unsupported and everything else proceeds.

Quality gates:

```bash
gofmt -w <changed .go files>
go vet ./...
go test ./...
go build -o gophix .
```

Integration tests invoke the real `exiftool` binary and are skipped automatically when it is absent.
Video-container tests additionally honor `GOPHIX_FIXTURE_DIR` (a directory containing a real `.mp4`);
they skip when unset. Tests always work in temp directories on synthetic or copied fixtures.

## Verified remaining limitations

- No GPS→timezone inference (needs offline tz lookup DB; use `--timezone` instead).
- `FileCreateDate` unsupported on Linux (reported per-file, non-fatal).
- Takeout fractional-second timestamps are integers today; sub-second precision is preserved only if
  Google ever provides it and the target tag supports it.
- Altitude 0.0 treated as "no altitude" (placeholder ambiguity) — real sea-level-altitude captures
  would not get an altitude tag written.
- IPTC `Caption-Abstract` write success is verified leniently (checked when present) because some
  containers accept IPTC only conditionally; EXIF/XMP description coverage remains strict.
- QuickTime/MP4 dates are written as UTC clock digits verbatim (ExifTool 13.x stores QuickTime write
  inputs without conversion); consumers that assume local time in MP4 atoms will display a shifted
  time for videos — an industry-wide ambiguity, documented as a deliberate choice.
- `json-only` time policy intentionally also skips the filename fallback (JSON sources only).
- Filename patterns are strict by design: names like `-edited` variants of timestamp files or exotic
  camera schemes not listed above yield no date rather than a guess.
