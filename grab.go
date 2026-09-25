package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// grabOptions is the fully-resolved set of options for one download,
// after merging the persisted Config with any -f flags on the command
// line (flags always win).
type grabOptions struct {
	OutPath    string // final resolved file path
	Threads    int
	SpeedLimit int64 // bytes/sec, 0 = unlimited
	OnExisting string
	Retries    int
	Timeout    time.Duration
	Resume     bool
}

// runGrab is the entry point for `liniget grab <url> [-f ...]`.
func runGrab(rawURL string, flagArgs []string, cfg Config) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("'%s' doesn't look like a valid URL", rawURL)
	}

	flags := parseFlagList(flagArgs)

	opts, err := resolveOptions(u, flags, cfg)
	if err != nil {
		return err
	}

	// Handle the destination file already existing, per -f skip / -f
	// overwrite / -f rename, falling back to the config default.
	if _, statErr := os.Stat(opts.OutPath); statErr == nil {
		switch opts.OnExisting {
		case "skip":
			fmt.Printf("Skipping, file already exists: %s\n", opts.OutPath)
			return nil
		case "overwrite":
			// fall through, we'll truncate it below
		case "rename":
			opts.OutPath = nextAvailableName(opts.OutPath)
		default:
			return fmt.Errorf("unknown on-existing behavior %q (expected skip, overwrite, or rename)", opts.OnExisting)
		}
	}

	if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o755); err != nil {
		return fmt.Errorf("couldn't create output directory: %w", err)
	}

	// No overall client.Timeout: a hard deadline would cut off large
	// downloads. ResponseHeaderTimeout instead bounds how long we wait
	// for the server to start responding.
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: opts.Timeout,
		},
	}

	size, acceptsRanges, err := probe(client, rawURL)
	if err != nil {
		fmt.Printf("Warning: couldn't probe file (%v); falling back to a plain single-stream download.\n", err)
		acceptsRanges = false
		size = -1
	}

	limiter := NewRateLimiter(opts.SpeedLimit)

	fmt.Printf("Downloading: %s\n", rawURL)
	if size > 0 {
		fmt.Printf("Size: %s\n", humanBytes(size))
	} else {
		fmt.Println("Size: unknown")
	}
	fmt.Printf("Destination: %s\n", opts.OutPath)

	start := time.Now()
	var downloaded int64

	stopProgress := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reportProgress(&downloaded, size, stopProgress)
	}()

	var dlErr error
	if acceptsRanges && size > 0 && opts.Threads > 1 {
		dlErr = downloadMultiThreaded(client, rawURL, opts, size, limiter, &downloaded)
	} else {
		dlErr = downloadSingleStream(client, rawURL, opts, limiter, &downloaded)
	}

	close(stopProgress)
	wg.Wait()
	fmt.Println()

	if dlErr != nil {
		return fmt.Errorf("download failed: %w", dlErr)
	}

	elapsed := time.Since(start)
	avgSpeed := float64(downloaded) / elapsed.Seconds()
	fmt.Printf("Done: %s in %s (avg %s/s)\n", humanBytes(downloaded), elapsed.Round(time.Second), humanBytes(int64(avgSpeed)))
	return nil
}

// resolveOptions merges config defaults with the per-download -f flags.
func resolveOptions(u *url.URL, flags map[string]string, cfg Config) (grabOptions, error) {
	opts := grabOptions{
		Threads:    cfg.Threads,
		SpeedLimit: cfg.SpeedLimit,
		OnExisting: cfg.OnExisting,
		Retries:    cfg.Retries,
		Timeout:    time.Duration(cfg.TimeoutSecs) * time.Second,
	}

	if _, ok := flags["skip"]; ok {
		opts.OnExisting = "skip"
	}
	if _, ok := flags["overwrite"]; ok {
		opts.OnExisting = "overwrite"
	}
	if _, ok := flags["rename"]; ok {
		opts.OnExisting = "rename"
	}
	if _, ok := flags["resume"]; ok {
		opts.Resume = true
	}

	if v, ok := flags["threads"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return opts, fmt.Errorf("invalid threads=%q (expected a positive integer)", v)
		}
		opts.Threads = n
	}
	if v, ok := flags["limit"]; ok {
		bps, err := parseSize(v)
		if err != nil {
			return opts, fmt.Errorf("invalid limit=%q: %w", v, err)
		}
		opts.SpeedLimit = bps
	}
	if v, ok := flags["retries"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return opts, fmt.Errorf("invalid retries=%q (expected a non-negative integer)", v)
		}
		opts.Retries = n
	}
	if v, ok := flags["timeout"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return opts, fmt.Errorf("invalid timeout=%q (expected seconds, e.g. timeout=30)", v)
		}
		opts.Timeout = time.Duration(n) * time.Second
	}

	// Work out the destination path.
	name := filepath.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		name = "download"
	}
	if v, ok := flags["name"]; ok {
		name = v
	}

	outPath := filepath.Join(cfg.DownloadDir, name)
	if v, ok := flags["out"]; ok {
		if fi, statErr := os.Stat(v); statErr == nil && fi.IsDir() {
			outPath = filepath.Join(v, name)
		} else if strings.HasSuffix(v, "/") {
			outPath = filepath.Join(v, name)
		} else {
			outPath = v
		}
	}
	opts.OutPath = outPath

	if opts.Threads < 1 {
		opts.Threads = 1
	}

	return opts, nil
}

// parseFlagList turns ["skip", "limit=500k", "threads=8"] into a map.
// Bare flags (no "=") map to "true".
func parseFlagList(args []string) map[string]string {
	out := make(map[string]string)
	for _, raw := range args {
		// Also allow a single comma-separated arg, e.g. -f "skip,limit=500k"
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if idx := strings.Index(part, "="); idx >= 0 {
				out[part[:idx]] = part[idx+1:]
			} else {
				out[part] = "true"
			}
		}
	}
	return out
}

// parseSize parses sizes like "500k", "2m", "1.5g", or a plain byte
// count, and returns bytes/sec. Suffixes are base-1024.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty value")
	}
	multiplier := int64(1)
	suffix := s[len(s)-1]
	numPart := s
	switch suffix {
	case 'k':
		multiplier = 1024
		numPart = s[:len(s)-1]
	case 'm':
		multiplier = 1024 * 1024
		numPart = s[:len(s)-1]
	case 'g':
		multiplier = 1024 * 1024 * 1024
		numPart = s[:len(s)-1]
	case 'b':
		numPart = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("expected a number optionally followed by k/m/g, got %q", s)
	}
	return int64(f * float64(multiplier)), nil
}

// probe sends a HEAD request to find the file size and whether the
// server supports byte-range requests (needed for multi-threading and
// resume).
func probe(client *http.Client, rawURL string) (int64, bool, error) {
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return -1, false, err
	}
	req.Header.Set("User-Agent", "liniget/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return -1, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return -1, false, fmt.Errorf("server returned %s", resp.Status)
	}

	acceptsRanges := strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes")
	size := resp.ContentLength // -1 if unknown
	return size, acceptsRanges, nil
}

// downloadSingleStream does a plain, one-connection download, with
// optional resume from a partially-downloaded file.
func downloadSingleStream(client *http.Client, rawURL string, opts grabOptions, limiter *RateLimiter, downloaded *int64) error {
	var attempt int
	for {
		err := trySingleStream(client, rawURL, opts, limiter, downloaded)
		if err == nil {
			return nil
		}
		attempt++
		if attempt > opts.Retries {
			return err
		}
		fmt.Printf("\nRetrying (%d/%d) after error: %v\n", attempt, opts.Retries, err)
		time.Sleep(time.Second * time.Duration(attempt))
	}
}

func trySingleStream(client *http.Client, rawURL string, opts grabOptions, limiter *RateLimiter, downloaded *int64) error {
	var startOffset int64
	flags := os.O_CREATE | os.O_WRONLY
	if opts.Resume {
		if fi, err := os.Stat(opts.OutPath); err == nil {
			startOffset = fi.Size()
			flags |= os.O_APPEND
			atomic.StoreInt64(downloaded, startOffset)
		}
	} else {
		flags |= os.O_TRUNC
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "liniget/1.0")
	if startOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startOffset))
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if startOffset > 0 && resp.StatusCode != http.StatusPartialContent {
		// Server ignored our Range request; start over from scratch.
		startOffset = 0
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		atomic.StoreInt64(downloaded, 0)
	}

	f, err := os.OpenFile(opts.OutPath, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := newThrottledReader(resp.Body, limiter)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := reader.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			atomic.AddInt64(downloaded, int64(n))
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// downloadMultiThreaded splits the file into N byte ranges and fetches
// them concurrently, writing each chunk directly to its offset in the
// pre-sized output file.
func downloadMultiThreaded(client *http.Client, rawURL string, opts grabOptions, size int64, limiter *RateLimiter, downloaded *int64) error {
	f, err := os.OpenFile(opts.OutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return err
	}

	chunkSize := size / int64(opts.Threads)
	if chunkSize < 1 {
		chunkSize = size
	}

	var wg sync.WaitGroup
	errCh := make(chan error, opts.Threads)

	for i := 0; i < opts.Threads; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == opts.Threads-1 {
			end = size - 1 // last chunk soaks up any remainder
		}
		if start > end {
			continue
		}

		wg.Add(1)
		go func(start, end int64) {
			defer wg.Done()
			err := downloadChunkWithRetry(client, rawURL, opts, f, start, end, limiter, downloaded)
			if err != nil {
				errCh <- err
			}
		}(start, end)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func downloadChunkWithRetry(client *http.Client, rawURL string, opts grabOptions, f *os.File, start, end int64, limiter *RateLimiter, downloaded *int64) error {
	var attempt int
	for {
		err := downloadChunk(client, rawURL, f, start, end, limiter, downloaded)
		if err == nil {
			return nil
		}
		attempt++
		if attempt > opts.Retries {
			return fmt.Errorf("chunk [%d-%d]: %w", start, end, err)
		}
		time.Sleep(time.Second * time.Duration(attempt))
	}
}

func downloadChunk(client *http.Client, rawURL string, f *os.File, start, end int64, limiter *RateLimiter, downloaded *int64) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "liniget/1.0")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", resp.Status)
	}

	writer := io.NewOffsetWriter(f, start)
	reader := newThrottledReader(resp.Body, limiter)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := reader.Read(buf)
		if n > 0 {
			if _, werr := writer.Write(buf[:n]); werr != nil {
				return werr
			}
			atomic.AddInt64(downloaded, int64(n))
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// nextAvailableName turns "file.zip" into "file (1).zip", "file (2).zip",
// etc. until it finds one that doesn't exist.
func nextAvailableName(path string) string {
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)

	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// reportProgress prints a simple in-place progress line until stopped.
func reportProgress(downloaded *int64, total int64, stop <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	start := time.Now()

	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			cur := atomic.LoadInt64(downloaded)
			elapsed := now.Sub(start).Seconds()
			var speed float64
			if elapsed > 0 {
				speed = float64(cur) / elapsed
			}

			if total > 0 {
				pct := float64(cur) / float64(total) * 100
				fmt.Printf("\r%6.2f%%  %s / %s  %s/s   ", pct, humanBytes(cur), humanBytes(total), humanBytes(int64(speed)))
			} else {
				fmt.Printf("\r%s downloaded  %s/s   ", humanBytes(cur), humanBytes(int64(speed)))
			}
		}
	}
}

func humanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), units[exp])
}
