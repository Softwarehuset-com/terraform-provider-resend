// Package domaincheck polls a Resend domain until its verification reaches a
// terminal state. It is kept free of provider dependencies so the resource
// layer and tests share one implementation.
package domaincheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	resend "github.com/resend/resend-go/v4"
)

// ErrNotVerified is returned when the timeout elapses before verification
// completes. Its message includes the records still failing verification.
var ErrNotVerified = errors.New("domain not verified before timeout")

// ErrVerificationFailed is returned when Resend reports a terminal failure
// status for the domain.
var ErrVerificationFailed = errors.New("domain verification failed")

// Getter is the subset of the resend-go domains service the poller needs.
// *resend.DomainsSvcImpl satisfies it via client.Domains.
type Getter interface {
	GetWithContext(ctx context.Context, domainId string) (resend.Domain, error)
}

// WaitForVerified polls the domain until verification reaches a terminal
// status, the timeout elapses, or ctx is cancelled. tick is the initial poll
// interval (it grows up to a minute); sleep is injected so tests run without
// real delays.
func WaitForVerified(ctx context.Context, g Getter, domainID string, tick, timeout time.Duration, sleep func(context.Context, time.Duration) error) (resend.Domain, error) {
	deadline := time.Now().Add(timeout)
	interval := tick
	if interval > time.Minute {
		interval = time.Minute
	}

	for {
		dom, err := g.GetWithContext(ctx, domainID)
		if err != nil {
			return dom, fmt.Errorf("reading domain during verification: %w", err)
		}

		switch dom.Status {
		case resend.DomainStatusVerified, resend.DomainStatusPartiallyVerified:
			return dom, nil
		case resend.DomainStatusFailed, resend.DomainStatusPartiallyFailed:
			return dom, fmt.Errorf("%w: %s", ErrVerificationFailed, strings.Join(FailingRecords(dom), "; "))
		}

		if time.Now().After(deadline) {
			return dom, fmt.Errorf("%w: %s", ErrNotVerified, strings.Join(FailingRecords(dom), "; "))
		}

		if err := sleep(ctx, interval); err != nil {
			return dom, fmt.Errorf("waiting for verification: %w", err)
		}
		if interval *= 2; interval > time.Minute {
			interval = time.Minute
		}
	}
}

// FailingRecords describes every DNS record whose verification status is not
// verified, formatted "name (type): status".
func FailingRecords(dom resend.Domain) []string {
	out := []string{}
	for _, r := range dom.Records {
		if r.Status != resend.DomainRecordStatusVerified {
			out = append(out, fmt.Sprintf("%s (%s): %s", r.Name, r.Type, r.Status))
		}
	}
	return out
}

// IsGone reports whether err represents a 404/not-found from resend-go. The
// SDK flattens errors to plain strings, so this matches on the status text.
func IsGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "404") || strings.Contains(msg, "not found")
}
