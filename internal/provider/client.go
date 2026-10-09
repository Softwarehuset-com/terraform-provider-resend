package provider

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/resend/resend-go/v4"
)

const (
	defaultMaxAttempts = 5
	defaultTimeout     = 60 * time.Second
)

// retryTransport retries idempotent-safe client errors (429) and server
// errors (5xx) with bounded exponential backoff, honoring Retry-After when
// present. Non-retryable statuses pass through untouched.
type retryTransport struct {
	rt          http.RoundTripper
	maxAttempts int
	baseDelay   time.Duration
}

func newRetryTransport(rt http.RoundTripper, maxAttempts int, baseDelay time.Duration) *retryTransport {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &retryTransport{rt: rt, maxAttempts: maxAttempts, baseDelay: baseDelay}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	backoff := t.baseDelay
	var resp *http.Response
	var err error

	for attempt := 1; ; attempt++ {
		// RoundTrip must not mutate the request; the SDK always builds fresh
		// requests with GetBody set (bytes.Buffer body), so re-sending is safe.
		resp, err = t.rt.RoundTrip(req)
		if err != nil || !shouldRetry(resp) || attempt >= t.maxAttempts {
			return resp, err
		}

		delay := backoff
		if resp != nil && resp.Header.Get("Retry-After") != "" {
			if secs, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && secs >= 0 {
				delay = time.Duration(secs) * time.Second
			}
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if werr := sleepCtx(req.Context(), delay); werr != nil {
			return nil, werr
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func shouldRetry(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NewResendClient builds a resend-go client with retrying transport and an
// optional base URL override (used by provider configuration for stub servers).
func NewResendClient(apiKey, baseURL string) *resend.Client {
	httpClient := &http.Client{
		Timeout:   defaultTimeout,
		Transport: newRetryTransport(http.DefaultTransport, defaultMaxAttempts, 250*time.Millisecond),
	}
	client := resend.NewCustomClient(httpClient, apiKey)
	if baseURL != "" && baseURL != defaultBaseURL {
		if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
			if u.Path == "" || u.Path[len(u.Path)-1] != '/' {
				u.Path += "/"
			}
			client.BaseURL = u
		}
	}
	return client
}
