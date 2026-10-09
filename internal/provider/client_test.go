package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTransportRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := newRetryTransport(srv.Client().Transport, 3, time.Millisecond)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
}

func TestRetryTransportGivesUp(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := newRetryTransport(srv.Client().Transport, 3, time.Millisecond)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls (max attempts), got %d", got)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected last 500 to surface, got %d", resp.StatusCode)
	}
}

func TestRetryTransportDoesNotRetry4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	rt := newRetryTransport(srv.Client().Transport, 3, time.Millisecond)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 call (no retry on 400), got %d", got)
	}
}

func TestNewResendClientBaseURLOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data": []}`))
	}))
	defer srv.Close()

	client := NewResendClient("re_test", srv.URL)

	if got := client.BaseURL.String(); got != srv.URL+"/" {
		t.Errorf("expected BaseURL %q, got %q", srv.URL+"/", got)
	}
}
