# TASK.md — Google Photos Takeout Compatibility and Year Organization

Task tracker for the current change. Status legend: `[ ]` open, `[x]` done and verified, `[~]` partially done.
Evidence for every completed item is recorded in **Evidence/results** below. Nothing is marked complete
before it actually passes.

## Objective

Modernize gophix so that it fully processes **current** Google Photos Takeout exports
(`<media>.supplemental-metadata.json` sidecars, including truncated suffix variants) while keeping all
legacy sidecar formats working, restoring original capture metadata (date/time, GPS, description) into
the media itself, and adding a new `organize-by-year` command that sorts media into `YYYY/` folders.

## Background / user problem

Google Takeout exports media files plus JSON sidecars containing original metadata. The exported files
often receive **new filesystem creation/modification timestamps** (the time of the export/download), not
the original capture times. File browsers and gallery apps commonly sort by capture/creation time, so in
Windows Explorer, Android gallery apps and photo-management software the photos/videos appear in the
wrong chronological order.

Regression trigger for this task: Google changed the sidecar naming scheme. Legacy Takeout used
`<media>.json`; current Takeout uses `<media>.supplemental-metadata.json`. The previous gophix matcher
only knew the legacy patterns and aborted on current exports.

## Scope

- Sidecar matching: new + truncated + legacy patterns, case-insensitive, deterministic, priority-ordered.
- Metadata model: typed representation of Takeout JSON; explicit, documented tag mappings only.
- Metadata writing via ExifTool: local-time date fields with offsets, GPS with refs, description/title,
  filesystem timestamps, idempotency, XMP sidecar fallback based on ExifTool outcome.
- Timezone policy implementation (`--timezone`, `--force-json-time`, `--time-policy`) as specified below.
- Filename timestamp fallback (`IMG_/VID_/PXL_/YYYYMMDD_HHMMSS`, WhatsApp and date-only forms,
  `--no-filename-fallback`, `--assume-noon-for-date-only`) per the capture-date fallback spec.
- CLI: extended `fix`, hardened `clean-json`, new `organize-by-year`.
- Safety: dry-run everywhere, never overwrite/delete source data by default, safe repeated runs.
- Tests: unit + integration covering the required matrix.
- Docs: this file, PROJECT.md, README.md.

## Non-goals

- No GUI, no daemon, no cloud interaction.
- No transfer of private Google data (account identifiers, internal IDs, face recognition/people labels,
  album membership, URLs) into metadata.
- No automatic timezone inference from GPS coordinates (deliberately unsupported; see limitations).
- No renaming of JSON sidecars on disk; no asking users to rename them.
- No editing of media pixels; bytes are preserved.

## Input naming variants

For a media file `<M>` (full filename incl. extension), candidates searched **only in the same directory**,
case-insensitively, in this priority order:

| Priority | Pattern | Notes |
|---|---|---|
| 1 | `<M>.supplemental-metadata.json` | current Google format |
| 2 | `<M>.supplemental-metadat.json` | truncated |
| 3 | `<M>.supplemental-metada.json` | truncated |
| 4 | `<M>.supplemental-metad.json` | truncated |
| 5 | `<M>.supplemental-meta.json` | truncated |
| 6 | `<M>.supplemental-met.json` | truncated |
| 7 | `<M>.supplemental.json` | truncated |
| 8 | `<M>.json` | legacy full-name |
| 9 | `<basename-without-ext>.json` | legacy basename |
| 10 | generic truncated rule | starts with `<M>.supplemental`, ends `.json`, middle is a prefix-truncation of `-metadata` (ASCII lowercase only); warns |
| 11 | legacy heuristics | `-edited` stripped name, mp4→jpg/jpeg/heic sidecar names, `(n)` duplicate-suffix moves, 46-char truncation; warns |
| 12 | reverse-renamed match | sidecar of a differently-named-extension file with identical stem (after extension fixing); warns |

Never matched: `metadata.json`, `Metadaten.json`, `album.json`, `shared_album_comments.json` and any JSON
not derived from a media filename. All non-exact fallback selections emit warnings.

## Timezone and timestamp policy (binding spec)

Google Takeout JSON Unix timestamps are UTC instants. They must not be blindly written as UTC clock
times: repaired media must display/sort according to the intended local capture time.

Date source priority:
1. Valid existing embedded local capture time **with** offset → preserve unless explicitly forced.
2. Valid existing embedded local capture time **without** offset → preserve if reconcilable with the JSON
   instant (≤ 26 h difference); ambiguity reported in verbose mode.
3. `photoTakenTime.timestamp` (primary JSON fallback).
4. `creationTime.timestamp` (secondary JSON fallback).
5. Filesystem modification time — last fallback, only for `organize-by-year`, clearly reported.

Offset resolution when JSON must supply the time:
1. Existing `OffsetTimeOriginal` / `OffsetTimeDigitized` / `OffsetTime` (or equivalent video metadata).
2. Existing embedded local time reconcilable with the JSON instant.
3. Explicit `--timezone <IANA-zone|±HH:MM>` (recommended; IANA zones apply historical DST correctly).
4. GPS-based IANA inference: **not implemented** (would require an offline tz-lookup DB; see limitations).
5. Otherwise keep the UTC instant and emit a prominent warning; never label UTC as local time.

Writing rules:
- Local human-facing fields: `DateTimeOriginal`, `CreateDate`, `ModifyDate` (+ XMP equivalents), plus
  `OffsetTimeOriginal`, `OffsetTimeDigitized`, `OffsetTime`. No `Z` suffix on EXIF local fields.
- Video containers: QuickTime `CreateDate`/`ModifyDate` are written as **UTC** (container-spec semantics);
  documented in PROJECT.md.
- GPS `GPSDateStamp`/`GPSTimeStamp` always **UTC** from the same absolute instant.
- `FileModifyDate` set to the resolved instant (with offset). `FileCreateDate` attempted only where the OS
  supports it; unsupported results reported without failing the repair.

CLI controls:
- `--timezone <IANA-zone-or-offset>`, e.g. `Europe/Berlin` or `+01:00`
- `--force-json-time` (explicit opt-in to overwrite valid existing embedded times)
- `--time-policy preserve-existing|prefer-json|json-only` (default `preserve-existing`)
- `--dry-run` shows: JSON timestamp (UTC), detected/existing offset, selected timezone source, resolved
  local capture time, target fields, and warnings for uncertain decisions.

## Acceptance criteria

- [x] Current `.supplemental-metadata.json` sidecars are matched and processed.
- [x] Truncated supplemental suffixes are matched robustly (generic rule + exact list), with warnings.
- [x] Legacy `<media>.json` and `<basename>.json` still work.
- [x] Generic album JSON (`Metadaten.json`, `metadata.json`, …) is never matched, never modified, never deleted.
- [x] Capture date/time, GPS, description restored per mapping tables in PROJECT.md.
- [x] Timezone policy implemented exactly as specified above; CET/CEST verified by tests.
- [x] GPS `(0,0)` placeholders and out-of-range coordinates are never written.
- [x] Unicode descriptions survive round-trip (umlauts tested).
- [x] `fix` is idempotent; second run reports "already correct" and rewrites nothing.
- [x] `fix --dry-run` performs zero writes/renames/timestamp changes.
- [x] Extension fixing still works and sidecars remain associated afterwards (reverse-renamed rule).
- [x] `clean-json` deletes only matched-and-verified sidecars; requires confirmation or `--yes`.
- [x] `organize-by-year` creates `YYYY/` folders from resolved local capture year, copy-only by default,
      never overwrites, collision-safe naming shared by media + sidecars, Unknown handling opt-in.
- [x] Missing ExifTool → clear actionable error, distinct exit code.
- [x] Meaningful summaries and exit codes (0 ok / 1 processing errors / 2 usage / 3 no exiftool).
- [x] Quality gates pass: gofmt, go vet, go test ./..., go build.
- [x] Real-fixture smoke test executed on a **copy** only.

## Implementation checklist (dependency order)

1. [x] Documentation scaffolds (TASK.md, PROJECT.md)
2. [x] `takeout` package: typed JSON model + effective-value resolution
3. [x] `takeout` package: sidecar matcher (priority list, generic truncation rule, exclusions, legacy heuristics, reverse-renamed rule)
4. [x] `meta` package: ExifTool exec wrapper + availability check
5. [x] `meta` package: metadata read-back (`-j -n -G1`)
6. [x] `meta` package: timezone/clock resolution engine
7. [x] `meta` package: plan builder (desired tags + expected values) & writer (embedded → verify → XMP fallback)
8. [x] `report` package: summary counters
9. [x] `commands`: `fix [--dry-run] [--verbose] [--timezone] [--force-json-time] [--time-policy]`
10. [x] `commands`: `clean-json [--dry-run] [--yes] [--verbose]`
11. [x] `commands`: `organize-by-year [--dry-run|--move|--include-unknown-date|--keep-json|...]`
12. [x] `main.go` CLI dispatch + exit codes + tzdata embedding
13. [x] Remove superseded `utils/` packages
14. [x] Unit tests: matching table, model parsing, clock resolution
15. [x] Integration tests (real exiftool): fix flows, clean-json protections, organize flows, exit codes
16. [x] README.md rewrite
17. [x] Quality gates + fixture smoke tests + evidence recording
18. [x] Performance pass 2: `meta.ReadMany` chunked bulk reads (fix + organize pre-read)
19. [x] Performance pass 2: reuse cached Info in `meta.Apply` (no duplicate read); dedupe double read in `clean-json` verification
20. [x] Performance pass 2: organize fast path — skip embedded read when policy makes JSON authoritative
21. [x] Fix: description expectation used wrong `-G1` group (`EXIF:` vs `IFD0:`), forcing needless XMP fallback for every description-bearing file
22. [x] Output hygiene: buffered stdout, all progress routed through the injected writer, `-q` on reads silences stay_open stderr chatter
23. [x] `organize-by-year --layout yyyy|yyyy/mm|yyyy-mm|flat` (folder-structure option; default unchanged)
24. [x] `find-duplicates`: report-only exact-duplicate detection (`--format text|csv|json`, `--output`),
       size-prefiltered parallel hashing, deterministic keep suggestion, no ExifTool required
25. [x] Fix organize-by-year concurrent-copy race (lost O_EXCL create race misreported as error and
       deleted another worker's target; `os.IsExist` blind to `%w`-wrapped errors → `errors.Is(fs.ErrExist)`)
26. [x] Documentation pass: README gained a fully executed walkthrough (demo fixture under `~/gophix-demo`,
       every output block captured from real runs), per-option examples for all four commands, expanded
       Windows section; PROJECT.md gained worked matching + timezone examples; dry-run rename line now
       worded as "would rename" (was past tense during dry-run)

## Test matrix

Unit tests (`go test`): U1–U14. Integration tests (real ExifTool, temp dirs): I1–I16. Manual fixture
checks (copies of real Takeout data): M1–M5.

| # | Requirement (instructions §9 + timezone §) | Kind | Test |
|---|---|---|---|
| 1 | Legacy `<media>.json` matching | unit | `TestMatch_LegacyFullName` |
| 2 | Legacy `<basename>.json` matching | unit | `TestMatch_LegacyBasename` |
| 3 | `.supplemental-metadata.json` matching | unit | `TestMatch_SupplementalMetadata` |
| 4 | Truncated supplemental suffix | unit | `TestMatch_TruncatedSuffixes`, `TestMatch_GenericTruncated` |
| 5 | `Metadaten.json`/`metadata.json` never matched | unit | `TestMatch_GenericExcluded` |
| 6 | Case-insensitive matching | unit | `TestMatch_CaseInsensitive` |
| 7 | Candidate priority + warning output | unit | `TestMatch_PriorityAndWarnings` |
| 8 | Invalid JSON → visible error + non-zero exit | integration | `I8_InvalidJSONError` |
| 9 | `photoTakenTime` precedence over `creationTime` | unit | `TestModel_TimePrecedence` |
| 10 | `creationTime` fallback | unit | `TestModel_TimePrecedence` |
| 11 | `geoData` precedence over `geoDataExif` | unit | `TestModel_GeoPrecedence` |
| 12 | GPS fallback to `geoDataExif` | unit | `TestModel_GeoPrecedence` |
| 13 | Missing GPS writes no placeholder coords | integration | `I13_NoGPSNoPlaceholder` |
| 14 | Unicode description survives | integration | `I14_UnicodeDescription` |
| 15 | Image scenario | integration | `I15_ImageFix` |
| 16 | Video scenario | manual+integration | M2 / `I16_VideoFix` (fixture-gated) |
| 17 | XMP sidecar fallback scenario | integration | `I17_XMPFallback` |
| 18 | Second `fix` run idempotent | integration | `I18_IdempotentSecondRun` |
| 19 | `fix --dry-run` changes nothing | integration | `I19_DryRunNoChange` |
| 20 | `clean-json` protects generic/unmatched/invalid/failed | integration | `I20_CleanJsonProtections` |
| 21 | `clean-json --dry-run` changes nothing | integration | `I21_CleanJsonDryRun` |
| 22 | organize creates expected year folder | integration | `I22_OrganizeYearFolder` |
| 23 | Year derives per source priority | unit+integration | `TestClock_SourcePriority`, `I23_OrganizeYearPriority` |
| 24 | Unknown date requires `--include-unknown-date` | integration | `I24_UnknownDateOptIn` |
| 25 | Collisions never overwrite | integration | `I25_CollisionSafety` |
| 26 | Default organization copies, source unchanged | integration | `I26_CopyDefaultSourceUntouched` |
| 27 | `--move` opt-in and safe | integration | `I27_MoveOptIn` |
| 28 | Sidecars follow collision-resolved names | integration | `I28_SidecarsFollowRename` |
| 29 | Missing ExifTool → actionable error | integration | `I29_MissingExiftool` |
| 30 | Empty tree → useful summary, no false success | integration | `I30_EmptyTree` |
| T1 | CET winter `+01:00` correct | unit | `TestClock_BerlinWinterSummer` |
| T2 | CEST summer `+02:00` correct | unit | `TestClock_BerlinWinterSummer` |
| T3 | Existing local time+offset preserved by default | integration | `T3_F7_PreserveExisting` |
| T4 | `--force-json-time` overwrites only when requested | integration | `T3_F7_PreserveExisting` (second phase) |
| T5 | GPS date/time stays UTC | unit+integration | `TestFormatting_GPSUTC`, `I15_ImageFix` |
| T6 | Unresolved timezone → warning, UTC not faked as local | integration | `T6_UnresolvedTimezoneWarning` |
| T7 | Video container-specific time behavior | manual/integration | M2 / `I16_VideoFix` |
| T8 | organize year from resolved local time (not shifted UTC) | integration | `T8_OrganizeLocalYearBoundary` |
| F1 | `IMG_20201206_142433.jpg` → `2020-12-06 14:24:33` | unit | `TestFilename_IMGPattern` |
| F2 | `VID_20201206_142433.mp4` → same | unit | `TestFilename_VIDPattern` |
| F3 | `PXL_20201206_142433123.jpg` ignores extra digits | unit | `TestFilename_PXLExtraDigits` |
| F4 | Invalid calendar/time values rejected | unit | `TestFilename_InvalidValuesRejected`, `TestFilename_LeapYear` |
| F5 | Two different valid timestamps → no guessing | unit | `TestFilename_AmbiguousSkipped` |
| F6 | JSON capture time overrides filename time | integration | `F6_JSONBeatsFilename` |
| F7 | Valid existing DateTimeOriginal overrides filename (default policy) | integration | `T3_F7_PreserveExisting` |
| F8 | Filename date-time overrides FileModifyDate | integration | `F8_FilenameOverridesFileModifyDate` |
| F9 | Date-only organizes into its year, invents no time | unit+integration | `TestClock_DateOnlyOrganizeYearNoInventedTime`, `F9_DateOnlyOrganizes` |
| F10 | `--no-filename-fallback` skips filename selection | unit+integration | `TestClock_FilenameResolution`, `F10_NoFilenameFallbackFlag` |
| F11 | Second run idempotent incl. filename-sourced files | integration | `I18_IdempotentSecondRun` |
| P1 | `ReadMany` splits results/errors per file | unit (exiftool) | `TestReadMany_SplitsResultsAndErrors` |
| P2 | One bad file does not poison a read chunk | unit (exiftool) | `TestReadMany_BadFileDoesNotPoisonChunk` |
| P3 | Organize fast path == slow path year result (preserve-existing / prefer-json / force-json-time) | integration | `TestOrganize_FastPathMatchesSlowPath` |
| P4 | json-only without JSON timestamp stays unknown; Unknown/ opt-in preserved | integration | `TestOrganize_JSONOnly_NoTimestampStaysUnknown` |
| P5 | fix idempotent through cached-Info `Apply` path | integration | `TestFix_CachedInfoPathIdempotent` |
| L1 | Each layout produces its documented structure | integration | `TestOrganize_Layouts` (yyyy, yyyy/mm, yyyy-mm, flat) |
| L2 | Default layout stays `yyyy` | integration | `TestOrganize_LayoutDefaultIsYear` |
| L3 | flat: same-named files never overwrite; both survive | integration | `TestOrganize_FlatCollisionNeverOverwrites` |
| L4 | Unknown/ placement independent of layout | integration | `TestOrganize_LayoutUnknownStillOptIn` |
| L5 | Invalid layout value rejected with exit 2 | integration | `TestOrganize_LayoutInvalidValueRejected` |
| D1 | Duplicate families detected across directories; unique files untouched | integration | `TestDupFind_FamiliesAcrossDirs` |
| D2 | Equal size + different content never flagged (prefilter soundness) | integration | `TestDupFind_SizePrefilterSoundness` |
| D3 | Keep suggestion: sidecar copy wins over shorter path; capture date shown | integration | `TestDupFind_RankingSidecarFirst` |
| D4 | CSV and JSON reports written & parseable; format inferred from extension | integration | `TestDupFind_CSVAndJSONOutput` |
| D5 | Unreadable file → warning, others processed, exit 1 | integration | `TestDupFind_UnreadableFileWarnsAndFails` (root-skipped) |
| D6 | Empty tree → explicit note, exit 0 | integration | `TestDupFind_EmptyTree` |
| D7 | Invalid `--format` rejected with exit 2 | integration | `TestDupFind_InvalidFormatRejected` |
| R1 | Concurrent same-name organize copies never fail or lose data | stress | `TestOrganize_FlatCollisionNeverOverwrites -count=30` |

## Build / test / smoke commands

```bash
# quality gates
gofmt -l . && go vet ./... && go test ./... && go build -o gophix .

# smoke test on a COPY of real takeout data (never run against the original)
cp -r /path/to/Takeout/Google\ Fotos /tmp/opencode/gophix-smoke && \
./gophix fix --dry-run --verbose --timezone Europe/Berlin /tmp/opencode/gophix-smoke && \
./gophix fix --timezone Europe/Berlin /tmp/opencode/gophix-smoke && \
./gophix organize-by-year --dry-run /tmp/opencode/gophix-smoke /tmp/opcode-organized
```

## Evidence/results

All commands executed on 2026-08-24, Go 1.26.0 / ExifTool 13.50 / Linux.

Quality gates (all pass):

```
$ gofmt -l .                      -> (no output)
$ go vet ./...                    -> exit 0
$ go test ./...                   -> ok  github.com/alexdachin/gophix/commands
                                     ok  github.com/alexdachin/gophix/meta
                                     ok  github.com/alexdachin/gophix/takeout
$ go build -o gophix .            -> binary builds, `./gophix help` works
```

Fixture smoke test on a COPY (`/tmp/opencode/final`, real Takeout data: 3 jpeg + 1 jpg with GPS + 1 mp4 +
`Metadaten.json`), never the original:

```
$ ./gophix fix --dry-run --verbose --timezone Europe/Berlin <copy>
   5 media found, all "planned (dry-run)", zero writes (verified via file snapshot)
$ ./gophix fix --timezone Europe/Berlin <copy>
   updated directly: 5 | fs modification times set: 5 | fs creation unsupported: 5 (Linux)
   date source embedded-DateTimeOriginal: 4, embedded-video-date: 1
$ exiftool -time:all -gps:all <image-fixture>.jpg
   Date/Time Original 2022:09:20 12:41:48 + OffsetTime +02:00 (= 10:41:48Z)
   GPS Latitude/Longitude Ref N/E, GPS Date Stamp 2022:09:20, GPS Time Stamp 10:41:48 (UTC) ✓
$ exiftool -time:all <video-fixture>.mp4
   QuickTime CreateDate/ModifyDate + Track/Media dates = 2019:02:09 23:29:27 (UTC per container spec)
   File Modification Date/Time = 2019:02:10 00:29:27+01:00 ✓ consistent instant
$ ./gophix fix ... (second run)   -> already correct: 5, failed: 0 (idempotent)
$ ./gophix organize-by-year --dry-run ... -> planned 2015/, 2019/, 2022/
$ ./gophix organize-by-year --timezone Europe/Berlin <copy> <out>
   copied: 5 into 2015/, 2019/, 2022/; sources intact
$ ./gophix clean-json --dry-run <copy>
   matched & verified: exactly the 5 media sidecars; Metadaten.json kept
```

Manual video fixture check M2 was performed using a real MP4 from the local Takeout copy (see above);
the automated variant `I16_VideoFix` runs when `GOPHIX_FIXTURE_DIR` points to a directory containing an
`.mp4`.

Performance work (same day, after user reported slowness on ~25k photos):

```
300-file synthetic tree, 4 workers:
  spawn-per-op (legacy path, GOPHIX_NO_BATCH=1): 2m36s  (~520 ms/file)
  stay_open batch + parallel + merged calls:      7.9s  (~26 ms/file)  -> ~20x
  idempotent second run:                          4.1s  (~14 ms/file)
micro-benchmark, single Read op: spawn 234 ms vs batch 7.6 ms
extrapolation for 25k photos: first run ~10-15 min (was ~3.5 h)
quality gates re-run after change: gofmt/vet/test/build all pass;
full integration suite green (18.5 s, down from 44 s).
```

Performance pass 2 (same day; same synthetic 300-file tree, 4 workers,
identical machine — baseline binary vs new binary):

```
                              baseline    pass-2     delta
fix first run:                 8.30 s      5.73 s     -31%
fix second run (idempotent):   8.74 s      5.69 s     -35%   (run-to-run noise ±0.9 s)
organize preserve-existing:    1.17 s      1.18 s      =  (embedded reads inherent to policy)
organize prefer-json:          1.13 s      0.09 s     ~12x   (fast path: zero embedded reads)

changes responsible:
  1. meta.ReadMany: <=32 files per -execute for pre-reads (was 1 call/file)
  2. Apply(plan, cur, opts): reuses the planning Info; duplicate per-file read removed
     (clean-json verification had a literal double Read - also removed)
  3. organize skips the embedded read when policy makes JSON authoritative
  4. BUG FIX: description expectation key "EXIF:ImageDescription" never matched the
     -G1 read-back group "IFD0:ImageDescription", so every description-bearing file
     failed embedded verification and fell into the XMP-sidecar fallback (extra write +
     extra verify + .xmp creation). Baseline reproduced the mismatch; after the one-line
     fix descriptions embed directly and second runs report "already correct".
  5. hygiene: buffered stdout, single os.ReadDir per directory, -q on reads
     (silences "N image files read" stderr chatter), all progress via injected writer

quality gates re-run: gofmt clean, vet clean, go test ./... ok (commands 15.5 s incl.
new P1-P5 tests), build ok.
```

`--layout` feature (same day):

```
new flag: --layout yyyy|yyyy/mm|yyyy-mm|flat (default yyyy = previous behavior)
implementation: layoutDir() in commands/organize.go; month/day from the resolved
  local capture time (all date sources already populate it, incl. date-only names);
  invalid values rejected at parse time (exit 2)
smoke (synthetic tree, ts -> local 2019-03):
  --layout yyyy    -> 2019/IMG.jpg
  --layout yyyy/mm -> 2019/03/IMG.jpg
  --layout yyyy-mm -> 2019-03/IMG.jpg
  --layout flat    -> IMG.jpg directly in destination
  Unknown/ placement + collision handling verified identical across layouts;
  two same-named files with different capture times both survive in flat mode
tests added: TestOrganize_Layouts, TestOrganize_LayoutDefaultIsYear,
  TestOrganize_FlatCollisionNeverOverwrites, TestOrganize_LayoutUnknownStillOptIn,
  TestOrganize_LayoutInvalidValueRejected
quality gates: gofmt clean, vet clean, go test ./... ok, build ok
```

Bugs found & fixed while implementing this: Exec<->runExiftool mutual recursion (stack overflow in
no-batch fallback), FileModifyDate append after slice assignment (silently dropped from write args),
applyFS claiming success without writing when metadata was already satisfied but fsModInMain was set,
organize move/skip-existing branch emptied by refactor, orgJob.matchVal never populated,
organize concurrent same-name copies (worker losing an O_EXCL create race reported "file exists" as a
processing error AND its cleanup deleted the winner's in-progress target; root cause: `os.IsExist`
does not look through `fmt.Errorf("%w")` chains — replaced with `errors.Is(err, fs.ErrExist)` and
race-safe re-classification retry; found via `-count=20` test stress).

## Known limitations

See PROJECT.md §"Verified remaining limitations" (kept in sync).

## Future work (parked ideas, not scheduled)

- Duplicate finder: ~~content-hash report~~ **implemented as `find-duplicates`**; possible follow-ups:
  `--delete` with keep-policy + confirmation, perceptual/`-edited` near-duplicate matching.
- Include/exclude glob filters for `organize-by-year` to organize subsets without staging copies.
- Summary export (`--report json|csv`) for large-run bookkeeping.
- Day-level layout variant (`yyyy/mm/dd`) for `organize-by-year`.
