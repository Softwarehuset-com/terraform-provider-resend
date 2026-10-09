package provider

import (
	"context"
	"io"
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

// retryTransport retries rate limits (429, all methods) and server errors
// (5xx, idempotent methods only) with bounded exponential backoff, honoring
// Retry-After up to a cap. Non-retryable statuses pass through untouched.
type retryTransport struct {
	rt          http.RoundTripper
	maxAttempts int
	baseDelay   time.Duration
}

const retryAfterCap = 30 * time.Second

func newRetryTransport(rt http.RoundTripper, maxAttempts int, baseDelay time.Duration) *retryTransport {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &retryTransport{rt: rt, maxAttempts: maxAttempts, baseDelay: baseDelay}
}

// retryableMethod reports whether replaying the request is safe. POSTs are
// only retried on 429 (rate limiting means the request was not executed).
func retryableMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	}
	return false
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	backoff := t.baseDelay
	var resp *http.Response
	var err error

	for attempt := 1; ; attempt++ {
		resp, err = t.rt.RoundTrip(req)
		if err != nil || !shouldRetry(req, resp) || attempt >= t.maxAttempts {
			return resp, err
		}

		delay := backoff
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, perr := strconv.Atoi(ra); perr == nil && secs >= 0 {
				delay = time.Duration(secs) * time.Second
			}
		}
		// A Retry-After beyond the cap (or the client timeout) means giving
		// up and surfacing the throttled response.
		if delay > retryAfterCap {
			return resp, nil
		}
		// Drain and close so the keep-alive connection is reusable.
		if resp.Body != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}
		if werr := sleepCtx(req.Context(), delay); werr != nil {
			return nil, werr
		}
		// Rebuild the request for the next attempt: transports consume the
		// body, and HTTP/2 does not rewind it automatically.
		if req.GetBody != nil {
			body, berr := req.GetBody()
			if berr != nil {
				return nil, berr
			}
			req = req.Clone(req.Context())
			req.Body = body
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func shouldRetry(req *http.Request, resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode >= 500 && retryableMethod(req.Method)
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
