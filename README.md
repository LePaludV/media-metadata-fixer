# media-metadata-fixer

A Go CLI tool to re-inject metadata from a **Google Takeout** export (photos / videos) by reading the JSON sidecar files (`*.supplemental-metadata.json`) and writing the original capture date and GPS coordinates back into the media files themselves.

## The problem

When you export your photos from Google Photos via [Google Takeout](https://takeout.google.com), you get two kinds of files:

- The media themselves (`IMG_xxx.jpg`, `VID_xxx.mp4`, etc.)
- Companion JSON files containing the original metadata (capture time, GPS, etc.)

**The issue**: exported media files carry the date of the export, not the date the photo was taken. That information lives only inside the JSON. To re-import them into another photo library (Apple Photos, Immich, etc.), the correct EXIF metadata has to be written back into each file.

This tool automates that work at scale (tested on Takeout exports of 100 GB+).

## What the script does

1. Recursively walks a `Takeout/` directory (including every subfolder, e.g. `Corbeille/`, album folders, multi-archive splits)
2. Identifies every media file and its matching JSON sidecar (handles truncated names, edit suffixes like `~2`, album duplicates, split archives)
3. Copies each media file into an output directory reorganized by **year/month** (based on the real capture date)
4. Uses `exiftool` to inject:
   - **Capture date** → `DateTimeOriginal`, `CreateDate`, `ModifyDate` (and QuickTime equivalents for videos)
   - **GPS coordinates** → `GPSLatitude*`, `GPSLongitude*`, `GPSAltitude*` tags
   - **Filesystem timestamps** → `FileModifyDate` everywhere, `FileCreateDate` on macOS/Windows (so Finder/Explorer show the right date)
5. Produces a CSV report listing every file processed (status, error if any)

## Requirements

- **Go 1.21+** to build
- **exiftool** on your `PATH`
  - macOS: `brew install exiftool`
  - Linux: `apt install libimage-exiftool-perl` (Debian/Ubuntu) or equivalent
  - Windows: [official download](https://exiftool.org)

## Installation

```bash
git clone https://github.com/<your-user>/media-metadata-fixer
cd media-metadata-fixer
go build -o media-metadata-fixer .
```

## Usage

### Dry-run (recommended first)

Scans and logs what would happen but writes nothing:

```bash
./media-metadata-fixer \
  --source ./Takeout \
  --output ./out \
  --dry-run \
  --report dry.csv
```

### Real run

```bash
./media-metadata-fixer \
  --source ./Takeout \
  --output ./out \
  --report final.csv
```

### CLI options

| Flag        | Default          | Description                         |
| ----------- | ---------------- | ----------------------------------- |
| `--source`  | (required)       | Takeout root directory              |
| `--output`  | (required)       | Output directory                    |
| `--report`  | `report.csv`     | Path to the CSV report              |
| `--workers` | `0` (= `NumCPU`) | Number of parallel workers          |
| `--dry-run` | `false`          | Analyze only, do not write any file |
| `--verbose` | `false`          | Log every file processed            |

### Resume after interruption

Idempotence is handled by file size: if the destination already contains a file with the same size as the source, it is **skipped** (status `skipped` in the report). You can safely re-run the command after a Ctrl-C or crash.

## Real-world example

A full run on a real Google Takeout export, copying the output to an **external USB HDD** :

| Metric                  | Value                                       |
| ----------------------- | ------------------------------------------- |
| Media files scanned     | 20 549                                      |
| JSON sidecars scanned   | 20 484                                      |
| Media matched to a JSON | 20 131                                      |
| Workers                 | 10                                          |
| Output target           | External USB HDD                            |
| Throughput              | ~2 files/s (disk-bound by the external HDD) |
| **Total duration**      | **~3 h 35 min**                             |

> **Note on throughput**: ~2 files/s is far below what the tool can do on a local SSD (see [Known limitations](#known-limitations)). Writing to an external USB HDD is the bottleneck — every file is fully copied, so the run is limited by the drive's sustained write speed, not CPU. On a local SSD the same volume completes in well under an hour.

## Output structure

```
out/
├── 2021/
│   ├── 11/
│   │   ├── IMG_20211105_080826.jpg
│   │   └── IMG_20211105_080826~2.jpg
│   └── ...
├── 2023/
│   └── 11/
│       └── IMG_20231103_114717.jpg
├── 2026/
│   └── 04/
│       └── PXL_20260408_222036290.mp4
└── _unknown/          # media with no JSON sidecar
    └── orphan.jpg
```

## CSV report

Columns: `source_path`, `output_path`, `status`, `json_path`, `date_taken`, `gps`, `error`

Possible statuses:

- `ok`: media copied and metadata injected successfully
- `skipped`: already present at destination (idempotence)
- `no_match`: no JSON found for this media (or orphan JSON)
- `error`: failed (see `error` column)
- `dry_run`: would have been processed (`--dry-run` mode)

---

# Architecture

The project is deliberately simple: a single `main` package, each file with a clear responsibility.

```
media-metadata-fixer/
├── go.mod / go.sum     # Go dependencies
├── main.go             # CLI flags + top-level orchestration
├── walker.go           # walk the Takeout, classify JSON vs media
├── matcher.go          # JSON ↔ media pairing (handles truncation, ~N, splits)
├── types.go            # SidecarJSON struct + helpers
├── exiftool.go         # exiftool subprocess wrapper
├── processor.go        # worker pool, copy + exiftool + idempotence
└── report.go           # thread-safe CSV writer
```

### Data flow

```
                   ┌──────────┐
                   │  main.go │
                   └────┬─────┘
                        │
                        ▼
              ┌─────────────────┐
              │   walker.go     │  WalkResult { MediaFiles, JSONFiles }
              └────────┬────────┘
                       │
                       ▼
              ┌─────────────────┐
              │  matcher.go     │  []Pair (Media + Sidecar)
              └────────┬────────┘
                       │
                       ▼
              ┌─────────────────┐         ┌────────────────┐
              │ processor.go    │ ──────▶ │   exiftool.go  │
              │ (N workers)     │         │  (subprocess)  │
              └────────┬────────┘         └────────────────┘
                       │
                       ▼
              ┌─────────────────┐
              │   report.go     │   report.csv
              └─────────────────┘
```

---

# File-by-file walkthrough

## `main.go` — entry point

Parses CLI flags, wires components together, orchestrates the pipeline (walk → match → process → report).

Key points:

- Clean `Ctrl-C` handling via `signal.Notify` + `context.WithCancel`: in-flight `exiftool` calls get cancelled cleanly
- Checks `exiftool` is available at startup (`CheckExiftool`) — fail fast instead of failing per file
- Creates the output directory (`os.MkdirAll`) when not in dry-run
- Prints a final summary (`ok=X skipped=Y no_match=Z error=W`)

CLI variables: `source`, `output`, `report`, `workers`, `dryRun`, `verbose`.

## `types.go` — Takeout JSON model

Defines the `SidecarJSON` struct that maps Google's `.supplemental-metadata.json` files. Only the fields we actually use are parsed:

```go
type SidecarJSON struct {
    Title          string         // original media filename, e.g. "IMG_xxx.jpg"
    Description    string
    CreationTime   timestampField // Google upload time (ignored for EXIF)
    PhotoTakenTime timestampField // real capture time (USED)
    GeoData        geoData        // GPS computed by Google
    GeoDataExif    geoData        // GPS read from the original EXIF (preferred)
}
```

Helpers:

- `PhotoTaken() time.Time`: converts the Unix timestamp string → UTC `time.Time`
- `BestGeo() (lat, lon, alt, ok)`: returns `geoDataExif` if non-zero, else `geoData`, else `ok=false`
- `LoadSidecar(path)`: reads and parses a JSON file from disk

## `walker.go` — Takeout walker

A single public function: `Walk(root) (*WalkResult, error)`. It uses `filepath.WalkDir` (faster than `Walk` because it skips `Lstat` per entry) and classifies each file:

- Extension `.json` → `JSONFiles`
- Extension in `mediaExtensions` (jpg, jpeg, png, heic, mp4, mov, gif, webp, …) → `MediaFiles`
- Otherwise → `Skipped`

No parsing at this stage — it's just fast indexing.

## `matcher.go` — JSON ↔ media pairing

The trickiest part. Two real-world challenges:

### Truncated names

Google truncates JSON filenames to fit a ~51-character limit:

```
IMG_20241002_175132.jpg.supplemental-metadata.json   (full name)
IMG_20241002_175132~2.jpg.supplemental-metadat.json  (truncated by 1 char)
PXL_20260408_222036290.mp4.supplemental-met.json     (truncated by 5 chars)
```

### Split archives & album folders

A large Takeout export gets split into many `Takeout 1/`, `Takeout 2/`, …, `Takeout 63/` archives. The same media can also appear duplicated in album folders. As a result, a media file may live in a different directory than its JSON.

### Matching strategy

Instead of trying to guess the truncation, we exploit the fact that **every JSON contains a `title` field with the exact media filename**. Two indexes are built:

- **Primary** — `(json_directory, title) → entry`: strongest signal, no ambiguity
- **Fallback** — `title → entry`: used when media and JSON live in different directories

For each media file, lookups are tried in this order:

1. Same directory + exact filename
2. Same directory + filename with `~N` edit suffix stripped
3. Global index by name only (any directory)
4. Global index by stripped name

### `~N` fallback

For edits like `IMG_xxx~2.jpg` that reuse the original's JSON (`IMG_xxx.jpg.supplemental-metadata.json`):

1. Direct lookup `IMG_xxx~2.jpg` → miss
2. `stripEditedSuffix("IMG_xxx~2.jpg")` → `"IMG_xxx.jpg"`
3. Lookup with the stripped name → hit

The regex `~\d+(\.[^.]+)$` captures `~2`, `~3`, etc. just before the extension.

### Album JSON filtering

Files like `métadonnées.json` or `commentaires_albums_partagés.json` also have a `title` field, but it doesn't point to a media file. We discard them with `IsMedia(side.Title)`: if the `title` doesn't have a known media extension, we skip.

### Outputs

```go
pairs        []Pair               // every media + its sidecar (nil if unmatched)
orphanJSONs  []string             // JSONs parsed successfully but matched nothing
parseErrors  map[string]error     // JSONs that failed to parse (corrupted)
```

## `exiftool.go` — exiftool invocation

Minimal wrapper around the `exiftool` binary (called as a subprocess). Three functions:

### `BuildExiftoolArgs(target, sidecar) []string`

Builds the exiftool command line based on the file type.

Common args:

- `-overwrite_original`: no `.jpg_original` backup file (the source is already preserved in the input directory)
- `-AllDates="YYYY:MM:DD HH:MM:SS"`: shortcut for `DateTimeOriginal`, `CreateDate`, `ModifyDate`
- `-FileModifyDate`: filesystem mtime (every OS)
- `-FileCreateDate`: filesystem birth time (macOS / Windows only — so Finder's "Date created" column matches)

GPS (when present):

- Negative latitude/longitude → `LatitudeRef=S` / `LongitudeRef=W`, else `N` / `E`
- Negative altitude → `AltitudeRef=1` (below sea level)

Video-specific (mp4, mov, m4v, 3gp):

- `-api QuickTimeUTC=1` so players interpret the timezone correctly
- Explicit QuickTime tags: `CreateDate`, `ModifyDate`, `TrackCreateDate`, `TrackModifyDate`, `MediaCreateDate`, `MediaModifyDate`

### `RunExiftool(ctx, args) error`

Runs exiftool with a `context.Context` (cancellable on Ctrl-C). On failure, returns the error **including the combined stdout+stderr** to make debugging easier.

### `CheckExiftool(ctx) error`

Called once at startup: runs `exiftool -ver`. On failure, prints a clear message explaining how to install it.

### `shortTimeout(parent)`

2-minute timeout per exiftool call, to keep a stuck subprocess from hanging the whole pool.

## `processor.go` — worker pool

The heart of parallelism.

### Architecture

```go
jobs := make(chan Pair, workers*2)
for i := 0; i < workers; i++ {
    go worker(jobs, ...) // each consumes from the channel
}
for _, pair := range pairs { jobs <- pair }
close(jobs)
wg.Wait()
```

Buffered channel sized at `workers*2`: enough to keep goroutines busy without holding everything in memory at once.

### Per-file pipeline (`handle`)

1. **If no sidecar**: copy to `out/_unknown/` (idempotent), status `no_match`
2. **Otherwise**:
   - Compute `destDir = out/YYYY/MM/` from `PhotoTaken()`
   - **Idempotence**: if destination exists with the same size as source → `skipped`
   - Otherwise copy (`copyWithDedup`)
   - Run exiftool on the copy
   - Status `ok` or `error`

### Filename collisions (`copyWithDedup`)

If `out/2024/10/IMG_xxx.jpg` already exists but with a different size (so it's a different file), we increment: `IMG_xxx (1).jpg`, `IMG_xxx (2).jpg`, …

### Streaming I/O (`copyFile`)

`io.Copy` with the default buffer (32 KiB): even a multi-GB video keeps a constant memory footprint.

### Progress bar

[`schollz/progressbar/v3`](https://github.com/schollz/progressbar), thread-safe, throttled at 200 ms so it doesn't saturate the terminal.

## `report.go` — CSV report

CSV writer with a `sync.Mutex` for concurrent safety (multiple workers write at the same time).

Structures:

- `Status` (enum): `ok` / `skipped` / `no_match` / `error` / `dry_run`
- `ReportRow`: one CSV line
- `Reporter`: wraps `csv.Writer` + mutex + per-status counters

API:

- `NewReporter(path)`: creates the file, writes the header
- `Write(row)`: appends + bumps counter (thread-safe)
- `Summary()`: returns `"ok=N skipped=N no_match=N error=N dry_run=N"`
- `Close()`: flush + close

---

# Handled edge cases

| Case                                                                  | Handling                                              |
| --------------------------------------------------------------------- | ----------------------------------------------------- |
| Truncated JSON name (`.supplemental-met.json`)                        | Match through the JSON's `title` field                |
| Edit `IMG_xxx~2.jpg` reusing the original JSON                        | Fallback strips `~N` and retries                      |
| Media and JSON in different directories (album dupes, split archives) | Global name-only fallback index                       |
| `métadonnées.json`, `commentaires_albums_partagés.json`               | Ignored (title has no media extension)                |
| Media with no JSON                                                    | Copied to `out/_unknown/`, status `no_match`          |
| Corrupted JSON                                                        | Logged in `parseErrors`, JSON ignored                 |
| GPS = (0, 0)                                                          | Treated as invalid → no GPS write                     |
| Filename collisions at destination                                    | Suffix ` (1)`, ` (2)`, …                              |
| Resume after interruption                                             | Skipped if destination size matches source            |
| `Ctrl-C` during a run                                                 | `context.Cancel` → in-flight exiftool calls cancelled |
| `Corbeille/` folder                                                   | Processed like any other folder                       |

---

# Known limitations

- **No checksum**: idempotence relies on file size only. If two different files happen to share the same size (rare), detection is fooled. Acceptable for this use case.
- **HEIC**: recognized as media, but EXIF support depends on the exiftool version installed.
- **Live Photos (Pixel `.MP.jpg` + `.mp4`)**: both files are processed independently. The Live Photo pairing between them is lost (but that's a broader Google Photos export limitation).
- **Performance**: 100% of files are copied (so disk usage roughly doubles). On an SSD with ~10 cores, expect 30–60 minutes for 100 GB.

---

# Recommended workflow

1. **Back up** your original Takeout before anything else (just in case).
2. **Install exiftool**: `brew install exiftool`.
3. **Build**: `go build -o media-metadata-fixer .`
4. **Dry-run on the full set**:
   ```bash
   ./media-metadata-fixer --source ./Takeout --output ./out --dry-run --report dry.csv
   ```
5. **Inspect `dry.csv`**:
   - How many `no_match`? Acceptable?
   - Any visible errors in the scan phase?
6. **Test on a subset**: copy a couple of `Takeout/2024/` folders into a separate test directory and run for real. Open the files in a viewer (Apple Photos, exiftool read-only) to confirm the EXIF is correct.
7. **Run on the full set**:
   ```bash
   ./media-metadata-fixer --source ./Takeout --output ./out --report final.csv
   ```
8. **Check `final.csv`**: count statuses, handle `error` rows manually if needed.

---

# Possible improvements (not implemented)

- Option to preserve the original Takeout folder structure (`--keep-structure`)
- "Move" mode instead of "copy" to save disk space
- Album support (read `métadonnées.json` to create per-album subfolders)
- SHA hashing for strict idempotence (at the cost of performance)
- Resume via a `.state` file rather than a full re-scan

---

# License

MIT — feel free to fork, adapt, and reuse.
