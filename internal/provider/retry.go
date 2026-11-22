package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryDelays are the waits before the 1st, 2nd and 3rd retry of a throttled
// request (so a request is sent at most 4 times). A Retry-After header
// replaces the delay when present. Variables so tests can shrink them.
var (
	retryDelays   = []time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second}
	maxRetryAfter = 60 * time.Second
)

// retryThrottled runs op until it succeeds, fails with something other than
// throttling, or the retries are used up. op reports whether its failure was
// throttling (HTTP 429 / a provider's throttling error) and the server's
// requested wait (0 = use the backoff). The last error is returned as is. A
// cancelled ctx ends the wait with ctx.Err(). Item errors (4xx other than 429)
// are never retried: op must report them as throttled=false.
func retryThrottled(ctx context.Context, op func() (throttled bool, after time.Duration, err error)) error {
	for attempt := 0; ; attempt++ {
		throttled, after, err := op()
		if err == nil || !throttled || attempt >= len(retryDelays) {
			return err
		}
		wait := retryDelays[attempt]
		if after > 0 {
			wait = min(after, maxRetryAfter)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// parseRetryAfter reads a Retry-After header: delay seconds or an HTTP date.
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.ParseFloat(h, 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	if t, err := http.ParseTime(h); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// errThrottled is what requestRetry's op returns for an HTTP 429.
var errThrottled = errors.New("throttled")

// requestRetry is requestWith that retries HTTP 429 with backoff (honoring
// Retry-After). After the last retry the final 429 is returned as a body-less
// response for the caller's status classification (ErrDown). A cancelled ctx
// returns ctx.Err(), never a stale 429.
func requestRetry(ctx context.Context, hc *http.Client, method, u string, hdr http.Header, body []byte) (*http.Response, error) {
	var resp, stub *http.Response
	err := retryThrottled(ctx, func() (bool, time.Duration, error) {
		r, err := requestWith(ctx, hc, method, u, hdr, body)
		if err != nil {
			return false, 0, err
		}
		if r.StatusCode == http.StatusTooManyRequests {
			after := parseRetryAfter(r.Header.Get("Retry-After"), time.Now())
			io.Copy(io.Discard, io.LimitReader(r.Body, 1<<16))
			r.Body.Close()
			stub = &http.Response{StatusCode: r.StatusCode, Header: r.Header, Body: io.NopCloser(bytes.NewReader(nil))}
			return true, after, errThrottled
		}
		resp = r
		return false, 0, nil
	})
	switch {
	case err == nil:
		return resp, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, errThrottled):
		return stub, nil
	}
	return nil, err
}
