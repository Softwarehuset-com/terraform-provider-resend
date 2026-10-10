package domaincheck

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	resend "github.com/resend/resend-go/v4"
)

type fakeGetter struct {
	statuses []string
	err      error
	calls    int
}

func (f *fakeGetter) GetWithContext(_ context.Context, _ string) (resend.Domain, error) {
	if f.err != nil {
		return resend.Domain{}, f.err
	}
	idx := f.calls
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	}
	f.calls++
	return resend.Domain{Status: f.statuses[idx]}, nil
}

func testWaiter() (func(context.Context, time.Duration) error, func() int) {
	sleeps := 0
	sleep := func(ctx context.Context, d time.Duration) error {
		sleeps++
		return nil
	}
	count := func() int { return sleeps }
	return sleep, count
}

func TestWaitForVerifiedSuccess(t *testing.T) {
	g := &fakeGetter{statuses: []string{resend.DomainStatusPending, resend.DomainStatusPending, resend.DomainStatusVerified}}
	sleep, sleeps := testWaiter()

	dom, err := WaitForVerified(context.Background(), g, "dom-1", time.Millisecond, time.Minute, sleep)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if dom.Status != resend.DomainStatusVerified {
		t.Errorf("expected verified, got %s", dom.Status)
	}
	if g.calls != 3 {
		t.Errorf("expected 3 get calls, got %d", g.calls)
	}
	if sleeps() != 2 {
		t.Errorf("expected 2 sleeps, got %d", sleeps())
	}
}

func TestWaitForVerifiedPartiallyVerified(t *testing.T) {
	g := &fakeGetter{statuses: []string{resend.DomainStatusPartiallyVerified}}
	sleep, _ := testWaiter()

	dom, err := WaitForVerified(context.Background(), g, "dom-1", time.Millisecond, time.Minute, sleep)
	if err != nil {
		t.Fatalf("partially_verified should be success, got: %s", err)
	}
	if dom.Status != resend.DomainStatusPartiallyVerified {
		t.Errorf("expected partially_verified, got %s", dom.Status)
	}
}

func TestWaitForVerifiedTerminalFailure(t *testing.T) {
	g := &fakeGetter{statuses: []string{resend.DomainStatusFailed}}
	sleep, _ := testWaiter()

	_, err := WaitForVerified(context.Background(), g, "dom-1", time.Millisecond, time.Minute, sleep)
	if err == nil {
		t.Fatal("expected error on terminal failure status")
	}
	if !errors.Is(err, ErrVerificationFailed) {
		t.Errorf("expected ErrVerificationFailed, got %v", err)
	}
}

func TestWaitForVerifiedTimeout(t *testing.T) {
	g := &fakeGetter{statuses: []string{resend.DomainStatusPending}}
	sleep, _ := testWaiter()

	_, err := WaitForVerified(context.Background(), g, "dom-1", time.Millisecond, 5*time.Millisecond, sleep)
	if !errors.Is(err, ErrNotVerified) {
		t.Fatalf("expected ErrNotVerified, got %v", err)
	}
}

func TestFailingRecords(t *testing.T) {
	dom := resend.Domain{
		Status: resend.DomainStatusPending,
		Records: []resend.Record{
			{Record: "DKIM", Name: "resend._domainkey", Type: "TXT", Status: resend.DomainRecordStatusVerified, Value: "p=..."},
			{Record: "MX", Name: "automated.appcompany.com", Type: "MX", Status: resend.DomainRecordStatusPending, Value: "inbound.resend.io"},
			{Record: "SPF", Name: "send", Type: "TXT", Status: resend.DomainRecordStatusFailed, Value: "v=spf1 ..."},
		},
	}

	got := FailingRecords(dom)
	if len(got) != 2 {
		t.Fatalf("expected 2 failing records, got %d: %v", len(got), got)
	}
	want := "automated.appcompany.com (MX): pending"
	if got[0] != want {
		t.Errorf("got %q, want %q", got[0], want)
	}
	want = "send (TXT): failed"
	if got[1] != want {
		t.Errorf("got %q, want %q", got[1], want)
	}
}

func TestFailingRecordsEmptyOnVerified(t *testing.T) {
	dom := resend.Domain{Status: resend.DomainStatusVerified}
	if got := FailingRecords(dom); len(got) != 0 {
		t.Errorf("expected no failing records, got %v", got)
	}
}

func TestIsGone(t *testing.T) {
	if !IsGone(errors.New("[ERROR]: 404 Not Found")) {
		t.Error("404 should be gone")
	}
	if !IsGone(errors.New("[ERROR]: Domain not found")) {
		t.Error("not found should be gone")
	}
	if IsGone(errors.New("[ERROR]: missing_permissions")) {
		t.Error("other errors are not gone")
	}
	if IsGone(nil) {
		t.Error("nil is not gone")
	}
}

// --- domain claim polling ---

type fakeClaimGetter struct {
	calls  int
	states []resend.DomainClaim
	err    error
}

func (f *fakeClaimGetter) GetWithContext(_ context.Context, _ string) (resend.DomainClaim, error) {
	defer func() { f.calls++ }()
	if f.err != nil {
		return resend.DomainClaim{}, f.err
	}
	if f.calls >= len(f.states) {
		return f.states[len(f.states)-1], nil
	}
	return f.states[f.calls], nil
}

func noopSleep(context.Context, time.Duration) error { return nil }

func TestWaitForClaimVerified(t *testing.T) {
	pending := resend.DomainClaim{Status: resend.DomainClaimStatusPending, DomainId: "d-1"}
	verified := resend.DomainClaim{Status: resend.DomainClaimStatusVerified, DomainId: "d-1"}

	g := &fakeClaimGetter{states: []resend.DomainClaim{pending, pending, verified}}
	got, err := WaitForClaimVerified(context.Background(), g, "d-1", time.Second, time.Minute, noopSleep)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got.Status != resend.DomainClaimStatusVerified {
		t.Errorf("expected verified, got %q", got.Status)
	}
	if g.calls != 3 {
		t.Errorf("expected 3 polls, got %d", g.calls)
	}
}

func TestWaitForClaimVerifiedCompletedAlsoSucceeds(t *testing.T) {
	g := &fakeClaimGetter{states: []resend.DomainClaim{
		{Status: resend.DomainClaimStatusCompleted, DomainId: "d-1"},
	}}
	if _, err := WaitForClaimVerified(context.Background(), g, "d-1", time.Second, time.Minute, noopSleep); err != nil {
		t.Fatalf("completed should be a success state, got %v", err)
	}
}

func TestWaitForClaimVerifiedBlockedSurfacesReason(t *testing.T) {
	g := &fakeClaimGetter{states: []resend.DomainClaim{
		{Status: resend.DomainClaimStatusBlocked, BlockedReason: resend.DomainClaimBlockedReasonGracePeriod},
	}}
	_, err := WaitForClaimVerified(context.Background(), g, "d-1", time.Second, time.Minute, noopSleep)
	if err == nil || !strings.Contains(err.Error(), "grace_period") {
		t.Fatalf("expected blocked error mentioning grace_period, got %v", err)
	}
}

func TestWaitForClaimVerifiedFailedSurfacesReason(t *testing.T) {
	g := &fakeClaimGetter{states: []resend.DomainClaim{
		{Status: resend.DomainClaimStatusFailed, FailureReason: "TXT record mismatch"},
	}}
	_, err := WaitForClaimVerified(context.Background(), g, "d-1", time.Second, time.Minute, noopSleep)
	if err == nil || !strings.Contains(err.Error(), "TXT record mismatch") {
		t.Fatalf("expected failure reason surfaced, got %v", err)
	}
}

func TestWaitForClaimVerifiedTimeout(t *testing.T) {
	g := &fakeClaimGetter{states: []resend.DomainClaim{
		{Status: resend.DomainClaimStatusPending},
	}}
	_, err := WaitForClaimVerified(context.Background(), g, "d-1", time.Second, 0, noopSleep)
	if err == nil || !strings.Contains(err.Error(), "not verified before timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
