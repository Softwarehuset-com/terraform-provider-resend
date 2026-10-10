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

// Verifier is the subset of the resend-go domains service that can
// (re-)trigger a DNS re-check. Resend only re-checks a domain's DNS records
// when the verify endpoint is called, so the poller re-triggers on a cadence
// while a domain stays pending. *resend.DomainsSvcImpl satisfies it via
// client.Domains.
type Verifier interface {
	VerifyWithContext(ctx context.Context, domainId string) (bool, error)
}

// ClaimGetter is the subset of the resend-go domain claims service the
// claim poller needs. *resend.DomainClaimsSvcImpl satisfies it via
// client.DomainClaims.
type ClaimGetter interface {
	GetWithContext(ctx context.Context, domainId string) (resend.DomainClaim, error)
}

// ClaimVerifier is the subset of the resend-go domain claims service that
// can (re-)trigger the claim TXT-record check. *resend.DomainClaimsSvcImpl
// satisfies it via client.DomainClaims.
type ClaimVerifier interface {
	VerifyWithContext(ctx context.Context, domainID string) (resend.DomainClaim, error)
}

// ErrClaimNotVerified is returned when the timeout elapses before a domain
// claim reaches verified/completed.
var ErrClaimNotVerified = errors.New("domain claim not verified before timeout")

// WaitForClaimVerified polls a domain claim until it reaches verified or
// completed, a terminal failure state, or the timeout elapses. Failure
// states surface their blocked/failure reason in the error message.
// reverifyEvery re-triggers the claim verification on a cadence while the
// claim stays pending (Resend only re-checks the TXT record when verify is
// called); <= 0 disables re-triggering.
func WaitForClaimVerified(ctx context.Context, c ClaimGetter, v ClaimVerifier, domainID string, tick, timeout, reverifyEvery time.Duration, sleep func(context.Context, time.Duration) error) (resend.DomainClaim, error) {
	deadline := time.Now().Add(timeout)
	interval := tick
	if interval > time.Minute {
		interval = time.Minute
	}
	nextReverify := time.Now().Add(reverifyEvery)

	for {
		claim, err := c.GetWithContext(ctx, domainID)
		if err != nil {
			return claim, fmt.Errorf("reading domain claim during verification: %w", err)
		}

		switch claim.Status {
		case resend.DomainClaimStatusVerified, resend.DomainClaimStatusCompleted:
			return claim, nil
		case resend.DomainClaimStatusBlocked:
			return claim, fmt.Errorf("domain claim blocked (status: %s): %s", resend.DomainClaimStatusBlocked, claim.BlockedReason)
		case resend.DomainClaimStatusFailed:
			return claim, fmt.Errorf("domain claim failed: %s", claim.FailureReason)
		case resend.DomainClaimStatusExpired, resend.DomainClaimStatusSuperseded, resend.DomainClaimStatusCanceled:
			return claim, fmt.Errorf("domain claim reached terminal status %q", claim.Status)
		}

		if time.Now().After(deadline) {
			return claim, ErrClaimNotVerified
		}

		if reverifyEvery > 0 && !time.Now().Before(nextReverify) {
			if _, err := v.VerifyWithContext(ctx, domainID); err != nil {
				return claim, fmt.Errorf("re-triggering claim verification: %w", err)
			}
			nextReverify = time.Now().Add(reverifyEvery)
		}

		if err := sleep(ctx, interval); err != nil {
			return claim, fmt.Errorf("waiting for domain claim verification: %w", err)
		}
		if interval *= 2; interval > time.Minute {
			interval = time.Minute
		}
	}
}

// WaitForVerified polls the domain until verification reaches a terminal
// status, the timeout elapses, or ctx is cancelled. tick is the initial poll
// interval (it grows up to a minute); sleep is injected so tests run without
// real delays. reverifyEvery re-triggers the verify endpoint on a cadence
// while the domain stays pending (Resend only re-checks DNS when verify is
// called); <= 0 disables re-triggering.
func WaitForVerified(ctx context.Context, g Getter, v Verifier, domainID string, tick, timeout, reverifyEvery time.Duration, sleep func(context.Context, time.Duration) error) (resend.Domain, error) {
	deadline := time.Now().Add(timeout)
	interval := tick
	if interval > time.Minute {
		interval = time.Minute
	}
	nextReverify := time.Now().Add(reverifyEvery)

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

		if reverifyEvery > 0 && !time.Now().Before(nextReverify) {
			if _, err := v.VerifyWithContext(ctx, domainID); err != nil {
				return dom, fmt.Errorf("re-triggering verification: %w", err)
			}
			nextReverify = time.Now().Add(reverifyEvery)
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
