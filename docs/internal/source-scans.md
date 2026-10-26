# Claude and Codex source scans

The watcher scans Claude transcripts, Codex rollouts, and Codex title indexes
for metadata changes. These scans back up native fsnotify/FSEvents watching,
which can miss events or run out of watch slots. They replace whole-root
provider polling for those scopes. Other providers keep their polling. The
scanner has no persistent database.

## How the scanner works

Each cached file costs one fixed 16-byte row: a 64-bit hash of its basename and
a 64-bit hash of its size, modification time, and file identity. Rows are sorted
per directory, and each directory path is stored once. Hashes use a per-process
seed and are never persisted. A basename collision only repeats dispatch; a
signature collision can hide one change with odds of 2^-64. The cache holds up
to 1,048,576 rows and 8 MiB of directory paths.

On Windows, size, write time, and identity come from one fresh file handle.
Directory listings can report stale sizes while a writer keeps the file open.
Scanner file and directory handles share delete access, so agents can rename or
remove sources and folders while a scan holds them.

Coverage runs 30 seconds after the previous pass completes. Scans read at most
256 entries at a time outside the native event loop. Changes enter the existing
serialized dispatcher; bounded scan pages bypass the native-event dispatch
floor. Successful archive acknowledgement advances signatures. Failed callbacks
let scans continue to other roots while watcher retries remain pending. Missing
roots defer reconciliation. Providers still supply complete discovery before
disappearance can affect archive state.

Dangling project links are skipped. An unreadable subtree keeps its cached rows,
marks the pass incomplete, and does not stop coverage of its siblings.

When the cache is full, the scanner cannot compare uncached files. It does not
resubmit them every pass. Instead, provider reconciliation of the affected scope
audits them every tenth pass, about every five minutes. Cached files keep
30-second coverage. Saturation is recomputed each pass; once the collection fits
again, one final reconciliation covers any uncached deletions.

Logs report scan duration, files examined, changed paths, retained rows,
directory bytes, saturation, and cumulative failures. `Watcher.SourceScanStats`
exposes the last completed pass. These diagnostics contain counts only.
Production Mac and Windows runs, sustained bursts, and multi-hour retention
remain qualification work.

## Scanner memory

`BenchmarkSourceScan50K` measures 50,000 synthetic files in 196 directories:

| Cache layout                                      | Retained bytes | Bytes per file | Unchanged pass |
| ------------------------------------------------- | -------------: | -------------: | -------------: |
| Per-file Go maps with basename strings (rejected) |      7,647,912 |            153 |          84 ms |
| Hashed 16-byte rows (current)                     |        857,432 |             17 |          77 ms |

At the 1,048,576-row limit, the current layout extrapolates to about 17 MiB.
Each pass still allocates about 21 MiB temporarily, mostly directory entries
returned by `ReadDir`.

```bash
CGO_ENABLED=1 go test -tags fts5 ./internal/sync -run '^$' \
  -bench '^BenchmarkSourceScan50K$' -benchtime=3x -benchmem
```
