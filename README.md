# liniget

A small, wget-like file grabber for Linux, written in Go (standard library
only — no external dependencies to fetch).

## Build

```bash
go build -o liniget .
sudo mv liniget /usr/local/bin/   # optional: put it on your PATH
```

Requires Go 1.20+ (uses `io.OffsetWriter`, added in 1.20).

## First-time setup

```bash
liniget -stp
```

Walks you through your defaults and saves them to
`~/.config/liniget/config.json`:

- default thread count
- default download directory
- default speed limit
- what to do when the destination file already exists (skip / overwrite / rename)
- retries on failure
- per-request timeout

You can rerun `liniget -stp` any time to change these; it shows your
current values as the default so you can just press Enter to keep them.

## Downloading a file

```bash
liniget grab <url> [-f flag flag=value ...]
```

Everything after `-f` (space or comma separated) is a flag for that one
download; anything not set is taken from your saved config.

| Flag              | Meaning                                                        |
|-------------------|------------------------------------------------------------------|
| `skip`            | Skip the download if the destination file already exists        |
| `overwrite`       | Overwrite the destination if it already exists                  |
| `rename`          | Save as `name (1).ext` if the destination already exists         |
| `resume`          | Resume a partially-downloaded file (single-thread downloads only) |
| `threads=N`       | Number of concurrent connections for this download              |
| `limit=RATE`      | Speed cap, e.g. `limit=500k`, `limit=2m`, `limit=1.5g`           |
| `retries=N`       | Retry attempts on failure                                        |
| `timeout=SECS`    | How long to wait for the server to start responding             |
| `out=PATH`        | Output file path, or a directory to save into                   |
| `name=FILENAME`   | Override just the output filename                               |

### Examples

```bash
# Plain download using your saved defaults
liniget grab https://example.com/file.iso

# 8 parallel connections, capped at 2 MB/s
liniget grab https://example.com/file.iso -f threads=8 limit=2m

# Skip if already downloaded, save into a specific folder
liniget grab https://example.com/file.iso -f skip out=/data/isos/

# Resume an interrupted single-connection download
liniget grab https://example.com/big.zip -f resume threads=1
```

## How it works

- `liniget` sends a `HEAD` request first to check the file size and
  whether the server supports byte-range requests (`Accept-Ranges: bytes`).
- If it does, and more than one thread is requested, the file is split
  into that many byte ranges and downloaded concurrently, with each
  chunk written straight to its offset in the pre-sized output file.
- If the server doesn't support ranges (or you asked for 1 thread), it
  falls back to a plain single-stream download. `resume` only applies to
  this path — it Range-requests from wherever the partial file left off.
- The speed limit, if set, is shared across all active threads via a
  simple token-bucket rate limiter, so `limit=2m` means 2 MB/s total,
  not per thread.

## Known limitations

- `resume` isn't supported for multi-threaded downloads — an interrupted
  multi-threaded download restarts from scratch. Use `threads=1 resume`
  for downloads you expect to need resuming.
- No recursive/mirror mode (no `-r` equivalent) — this grabs one file
  per invocation, wget-style but single-target.
