# Terraform Provider for Resend — Design Spec

Date: 2026-10-09
Status: Approved (design), pending implementation

## Goal

A registry-published Terraform provider (`terraform-provider-resend`) that manages
Resend's inbound-email pipeline in Terraform:

1. **Domain registration** — create a domain (e.g. `automated.appcompany.com`) with the
   `receiving` capability so Resend serves MX records for it.
2. **MX record setup** — expose the domain's required DNS record set as computed
   attributes so users can create the records with their DNS provider
   (Cloudflare, Route53, …) in the same Terraform config.
3. **Mail → HTTP** — a `resend_webhook` resource subscribed to `email.received`
   that makes Resend POST parsed inbound emails to an HTTP endpoint.

The first concrete deployment target is `automated.appcompany.com`.

## Non-goals (v1)

- No resources for API keys, inboxes, contacts, segments, topics, templates,
  broadcasts, automations, or suppressions. (API key used by the provider itself
  is an input, not a managed resource.)
- No domain **claim** flow (for domains registered under another Resend team).
- No data sources in v1.
- The provider does not create DNS records itself; it only outputs them.

## Technology choices

| Piece | Choice |
|---|---|
| Language | Go 1.27 (installed: go1.27.1 darwin/arm64) |
| Framework | `github.com/hashicorp/terraform-plugin-framework` v1.19.x (latest, protocol 6) |
| API client | `github.com/resend/resend-go/v4` (v4.8.x) via `NewCustomClient` with a retrying `http.Client` |
| Testing | `github.com/hashicorp/terraform-plugin-testing` |
| Docs | `tfplugindocs` from `templates/` + `examples/` |
| Release | goreleaser + `.terraform-provider-release.yml` + GPG-signed binaries, GitHub Actions |
| License | MPL-2.0 |

`terraform-plugin-framework` is HashiCorp's recommended successor to SDKv2;
SDKv2 is legacy. Protocol 6 works with Terraform ≥1.3 (installed: 1.14.9).

## Architecture

```
terraform-provider-resend/
├── main.go                          # providerserver.Serve, address registry.terraform.io/<org>/resend
├── internal/provider/
│   ├── provider.go                  # schema: api_key (sensitive, env RESEND_API_KEY), base_url (optional, testing)
│   ├── client.go                    # builds resend-go client w/ retry policy
│   ├── resource_domain.go
│   ├── resource_domain_verification.go
│   ├── resource_webhook.go
│   └── errors.go                    # API error → diag mapping
├── examples/                        # per-resource runnable configs
├── templates/                       # tfplugindocs sources (index + per-resource)
├── .github/workflows/               # ci.yml, release.yml
├── .goreleaser.yml + .terraform-provider-release.yml
├── Makefile                         # build, test, testacc, docs
└── CHANGELOG.md
```

Resource ID semantics: Resend object UUIDs are the Terraform resource IDs.
The verification resource's ID is the domain ID (1:1 with its domain).

## Provider schema

| Attribute | Type | Notes |
|---|---|---|
| `api_key` | string, required, sensitive | default from `RESEND_API_KEY` |
| `base_url` | string, optional | default `https://api.resend.com`; lets acceptance tests hit a stub server |

## Resource: `resend_domain`

Maps `POST/GET/PATCH/DELETE /domains[/{id}]`.

### Arguments

| Attribute | Type | Default | Modifiers |
|---|---|---|---|
| `name` | string, required | — | RequiresReplace |
| `region` | string, optional | `us-east-1` | RequiresReplace; validator: `us-east-1, eu-west-1, sa-east-1, ap-northeast-1` |
| `capabilities` | optional object | `sending=enabled, receiving=disabled` | update-in-place where API allows |
| `capabilities.sending` | string | `enabled` | validator: enabled/disabled |
| `capabilities.receiving` | string | `disabled` | validator: enabled/disabled |
| `custom_return_path` | string, optional | `send` | RequiresReplace |
| `tls` | string, optional | `opportunistic` | validator: opportunistic/enforced |
| `tracking_subdomain` | string, optional | — | |
| `open_tracking` | bool, optional | — | |
| `click_tracking` | bool, optional | — | |

### Computed

| Attribute | Type | Notes |
|---|---|---|
| `id` | string | Resend domain UUID |
| `status` | string | `not_started`, `pending`, `verified`, `failed` |
| `created_at` | string | |
| `records` | list of object | each: `record` (DKIM/SPF/MX/Tracking), `name`, `type` (TXT/MX/CNAME), `value` (sensitive=false, long DKIM keys OK in state), `ttl`, `priority` (nullable), `status` |

The `records` list is the MX-setup contract: users `for_each` over it in their
DNS provider config. Inbound MX records appear once `capabilities.receiving = "enabled"`.

Behavior notes:
- Region changes replace the domain (new region = new records/DKIM).
- On update, send PATCH with changed fields; refresh `records`/`status` from GET.

## Resource: `resend_domain_verification`

Drives `POST /domains/{id}/verify` + polls `GET /domains/{id}`.

| Attribute | Type | Notes |
|---|---|---|
| `domain_id` | string, required | RequiresReplace |
| `status` | string, computed | terminal status of the domain after polling |

- **Create**: call verify, then poll Get with exponential backoff
  (initial ~10s, max interval ~60s) until the domain `status` is `verified`
  (success) or `failed` (error out, listing every DNS record still not passing
  from `records[].status` — the apply error must say *what* is wrong).
- **Read**: if domain status regressed to `not_started`/`pending`, remove the
  verification resource from state → next apply re-verifies.
- **Update**: no mutable fields → replace.
- **Delete**: no-op (no remote object).
- **Timeouts**: `create` (default 30m) bound to the poll loop via
  `resource.ResourceWithTimeouts`; context cancellation respected throughout.
- Domain gone (404 on read) → remove from state, standard framework pattern.

## Resource: `resend_webhook`

Maps `POST/GET/PATCH/DELETE /webhooks[/{id}]`.

| Attribute | Type | Notes |
|---|---|---|
| `endpoint` | string, required | must be https URL (validator) |
| `events` | set of string, required | validator against known Resend event types (email.received, email.sent, email.delivered, email.bounced, …) |
| `id` | string, computed | |
| `signing_secret` | string, computed, **sensitive** | Svix secret for verifying inbound HTTP deliveries |

Update-in-place via PATCH when `endpoint`/`events` change.

## Error handling

- Retry transport around `resend-go`: retry 429 and 5xx, honor `Retry-After`,
  capped attempts (5), bounded backoff; never retry non-idempotent failures blindly.
- Map API errors to `diag` diagnostics carrying Resend's message and code.
- 404 on read ⇒ resource removed from state; 404 on delete ⇒ success (already gone).

## Testing

1. **Unit tests** (no network): schema shape (attrs, types, defaults, validators,
   sensitive flags, plan modifiers), error mapping, retry-policy logic. Run in CI.
2. **Acceptance tests** (real API, gated by `RESEND_API_KEY`):
   - `resend_domain`: create → records present incl. inbound MX when receiving
     enabled → update tracking/tls → import → delete.
   - `resend_domain_verification`: against a domain whose DNS we cannot control
     in CI, expect polling to reach the timeout/failure path gracefully
     (documented limitation; full happy path is validated manually with a real
     domain like automated.appcompany.com).
   - `resend_webhook`: create with `email.received` → update events → delete;
     `signing_secret` present and marked sensitive.
   - Importer coverage: `ImportStateVerifyIgnore` for `signing_secret` (secret
     only returned on create).
3. Manual smoke test checklist with `automated.appcompany.com` (real DNS) before v1 tag.

## Docs & release

- `examples/` + `templates/` → `tfplugindocs generate`; registry pages.
- GitHub Actions CI: golangci-lint, `go test`, build.
- Release workflow on tag `v*`: goreleaser builds for linux/darwin/windows
  (amd64+arm64), GPG-signed, with `.terraform-provider-release.yml` so the
  registry ingests it. `CHANGELOG.md` maintained in go-changelog format.
- Repo published as `github.com/<org>/terraform-provider-resend`, provider
  address `registry.terraform.io/<org>/resend` (org decided at release time).

## Open items (resolved at implementation time)

- Exact PATCH semantics of `/domains/{id}` (which fields are updatable) —
  confirm against SDK request struct; treat unsupported changes as RequiresReplace.
- Confirm domain status enum values from `resend-go` `DomainStatus` constants.
- Org/namespace for the registry address + GPG key provisioning (release step).
