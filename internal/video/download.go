package video

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// chunkSize is the per-request granularity of the ranged download path.
const chunkSize = 4 << 20 // 4 MiB

// mediaRequest builds one bare media request. Media URLs carry their own
// signature; only the defensive Referer is attached, never session state.
func (c *Client) mediaRequest(ctx context.Context, rawURL string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", resourceManageUIURL)
	for key, values := range header {
		for _, value := range values {
			req.Header.Set(key, value)
		}
	}
	resp, err := c.media.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("media GET %s: unexpected status %s", redactMediaURL(rawURL), resp.Status)
	}
	return resp, nil
}

// redactMediaURL strips the query string: the media signature lives there.
func redactMediaURL(rawURL string) string {
	if i := strings.Index(rawURL, "?"); i >= 0 {
		return rawURL[:i] + "?…"
	}
	return rawURL
}

// ProbeMedia issues a Range: bytes=0-0 probe and reports the total size and
// whether ranged requests are honored. A zero size means unknown.
func (c *Client) ProbeMedia(ctx context.Context, rawURL string) (size int64, ranges bool, err error) {
	resp, err := c.mediaRequest(ctx, rawURL, http.Header{"Range": {"bytes=0-0"}})
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	ranges = resp.StatusCode == http.StatusPartialContent ||
		resp.Header.Get("Content-Range") != "" ||
		strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes")

	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil && n > 0 {
				return n, ranges, nil
			}
		}
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > 0 {
			return n, ranges, nil
		}
	}
	return 0, ranges, nil
}

// OpenMedia streams the whole media object; the caller closes the body.
// This is the cat path: sequential, no probe, no parallelism.
func (c *Client) OpenMedia(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	resp, err := c.mediaRequest(ctx, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// FetchRange streams bytes [begin, endInclusive]; the caller closes the
// body. Both 200 and 206 are accepted: some hosts answer a full-body 200 to
// a ranged request, which the download loop tolerates by trusting the
// bytes actually read.
func (c *Client) FetchRange(ctx context.Context, rawURL string, begin, endInclusive int64) (io.ReadCloser, error) {
	header := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", begin, endInclusive)}}
	resp, err := c.mediaRequest(ctx, rawURL, header)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Download fetches the whole media object into dst. With range support it
// runs NumCPU workers over contiguous segments at chunkSize granularity;
// without, it falls back to one sequential stream. progress, when non-nil,
// is invoked after every chunk with (bytesDone, bytesTotal).
func (c *Client) Download(ctx context.Context, rawURL string, dst io.WriterAt, progress func(done, total int64)) (int64, error) {
	size, ranges, err := c.ProbeMedia(ctx, rawURL)
	if err != nil {
		return 0, err
	}
	if size <= 0 {
		return 0, fmt.Errorf("media size unknown for %s; cannot download", redactMediaURL(rawURL))
	}
	if progress != nil {
		progress(0, size)
	}
	if !ranges {
		return c.downloadSequential(ctx, rawURL, dst, size, progress)
	}
	return DownloadRanged(ctx, size, func(ctx context.Context, begin, end int64) (io.ReadCloser, error) {
		return c.FetchRange(ctx, rawURL, begin, end)
	}, dst, progress)
}

// RangeFetcher streams bytes [begin, endInclusive]; the caller closes the
// body.
type RangeFetcher func(ctx context.Context, begin, endInclusive int64) (io.ReadCloser, error)

// DownloadRanged is the transport-generic parallel download engine: size
// bytes via fetch into dst, NumCPU workers over contiguous segments at
// chunkSize granularity, each request's right endpoint clamped to its
// segment end. progress, when non-nil, is invoked after every chunk with
// (bytesDone, bytesTotal).
func DownloadRanged(ctx context.Context, size int64, fetch RangeFetcher, dst io.WriterAt, progress func(done, total int64)) (int64, error) {
	if progress != nil {
		progress(0, size)
	}
	return downloadRanged(ctx, size, fetch, dst, progress)
}

// offsetWriter adapts a WriterAt to a sequential Writer starting at off.
type offsetWriter struct {
	dst io.WriterAt
	off int64
}

func (w *offsetWriter) Write(p []byte) (int, error) {
	n, err := w.dst.WriteAt(p, w.off)
	w.off += int64(n)
	return n, err
}

// downloadSequential is the no-range fallback: one GET, streamed through.
func (c *Client) downloadSequential(ctx context.Context, rawURL string, dst io.WriterAt, size int64, progress func(done, total int64)) (int64, error) {
	body, err := c.OpenMedia(ctx, rawURL)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	w := &offsetWriter{dst: dst}
	buf := make([]byte, 64<<10)
	var done int64
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return done, err
			}
			done += int64(n)
			if progress != nil {
				progress(done, size)
			}
		}
		if readErr == io.EOF {
			return done, nil
		}
		if readErr != nil {
			return done, readErr
		}
	}
}

// downloadRanged splits [0, size) into one contiguous segment per worker;
// each worker walks its segment at chunkSize granularity with the request's
// right endpoint CLAMPED to the segment end (the blueprint requests
// begin+chunkSize unconditionally, overshooting the segment — not ported).
func downloadRanged(ctx context.Context, size int64, fetch RangeFetcher, dst io.WriterAt, progress func(done, total int64)) (int64, error) {
	workers := runtime.NumCPU()
	if maxUseful := int((size + chunkSize - 1) / chunkSize); workers > maxUseful {
		workers = maxUseful
	}
	if workers < 1 {
		workers = 1
	}
	segSize := (size + int64(workers) - 1) / int64(workers)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex // serializes WriteAt for non-atomic destinations
	var done int64
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}

	for i := 0; i < workers; i++ {
		segBegin := int64(i) * segSize
		segEnd := segBegin + segSize - 1
		if segEnd > size-1 {
			segEnd = size - 1
		}
		if segBegin > segEnd {
			break
		}
		wg.Add(1)
		go func(begin, end int64) {
			defer wg.Done()
			for cur := begin; cur <= end; {
				// Clamp the request's right endpoint to the segment end.
				reqEnd := cur + chunkSize - 1
				if reqEnd > end {
					reqEnd = end
				}
				body, err := fetch(ctx, cur, reqEnd)
				if err != nil {
					fail(err)
					return
				}
				chunk, err := io.ReadAll(body)
				body.Close()
				if err != nil {
					fail(err)
					return
				}
				if len(chunk) == 0 {
					fail(fmt.Errorf("empty range response at offset %d", cur))
					return
				}
				mu.Lock()
				_, err = dst.WriteAt(chunk, cur)
				mu.Unlock()
				if err != nil {
					fail(err)
					return
				}
				read := int64(len(chunk))
				cur += read
				errMu.Lock()
				done += read
				total := done
				errMu.Unlock()
				if progress != nil {
					progress(total, size)
				}
			}
		}(segBegin, segEnd)
	}
	wg.Wait()
	errMu.Lock()
	defer errMu.Unlock()
	if firstErr != nil {
		return done, firstErr
	}
	return done, nil
}
