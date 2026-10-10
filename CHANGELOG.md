# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `resend` provider with `api_key` (env `RESEND_API_KEY`) and `base_url` configuration.
- `resend_domain` resource: domain registration with sending/receiving capabilities, region, TLS, return path and tracking settings; DNS records exposed as computed `records` (DKIM/SPF/MX/CNAME with per-record status and MX priority).
- `resend_domain_verification` resource: triggers verification, polls until terminal status with configurable create timeout, reports failing DNS records on error.
- `resend_webhook` resource: HTTPS endpoint + closed-set event validation (incl. `email.received`), sensitive `signing_secret`.
