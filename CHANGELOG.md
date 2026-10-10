# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.0] - 2026-10-10

### Added

- `resend_api_key` resource: full lifecycle with `full_access`/`sending_access` permissions, optional `domain_id` scoping, in-place rename, import; the `token` is sensitive and write-once (null after import).
- `resend_domain` data source: read an existing domain by `id` or `name` (exactly one; ambiguous names error), exposing status, region, capabilities, tracking settings and the full `records` list for DNS wiring.
- `resend_domain_claim` resource: starts a claim to take over a domain verified under another Resend account; exposes the TXT `record` to publish, `status`, `domain_id`, `blocked_reason`, `failure_reason` and `expires_at`. Destroy does not cancel the claim (no API); claims expire server-side.
- `resend_domain_claim_verification` resource: triggers claim verification and polls to `verified`/`completed` with configurable create timeout; failed verifications are retried on the next apply, and blocked/failure reasons surface in errors.

### Fixed

- `resend_domain` failed with "Value Conversion Error: Path: capabilities" when the `capabilities` block was omitted: the Optional+Computed object arrived as unknown at plan time, which the internal model could not represent. Creating a domain without the block now applies API defaults (`sending = enabled`, `receiving = disabled`).

## [1.0.1] - 2026-10-09

### Added

- `resend` provider with `api_key` (env `RESEND_API_KEY`) and `base_url` configuration.
- `resend_domain` resource: domain registration with sending/receiving capabilities, region, TLS, return path and tracking settings; DNS records exposed as computed `records` (DKIM/SPF/MX/CNAME with per-record status and MX priority).
- `resend_domain_verification` resource: triggers verification, polls until terminal status with configurable create timeout, reports failing DNS records on error.
- `resend_webhook` resource: HTTPS endpoint + closed-set event validation (incl. `email.received`), sensitive `signing_secret`.
