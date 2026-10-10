# Resend Provider v1.1 — API Keys, Domain Data Source, Domain Claims

Date: 2026-10-10
Status: approved design (this document), pending implementation plan
Parent: docs/superpowers/specs/2026-10-09-resend-provider-design.md

## Goal

Extend the published provider (`registry.terraform.io/softwarehuset-com/resend`, v1.0.1) with three capabilities requested for internal use:

1. `resend_api_key` resource — manage Resend API keys as infrastructure.
2. `data "resend_domain"` data source — read an existing domain (by id or name), including its DNS records, without owning it in the same stack.
3. `resend_domain_claim` resource — start and drive the domain claim flow (take over a domain verified under another Resend account).

All API-surfaced optional properties are in scope for each feature. Release target: **v1.1.0** (minor — new features), through the existing goreleaser + registry pipeline.

## Non-goals (v1.1)

- List data sources (`data "resend_domains"`) — add later on demand.
- Resources for contacts, audiences, broadcasts, templates, automations — unchanged from v1 non-goals.
- Claim **completion** polling (multi-day grace period) — the resource polls to `verified`; later statuses surface via refresh.

## 1. `resend_api_key` resource

SDK surface: `Create(name, permission?, domain_id?) → {id, token}`, `List()`, `Update(id, name)`, `Remove(id)`.

Schema:

| Attribute | Type | Flags | Notes |
|---|---|---|---|
| `name` | String | Required, updatable in place | API supports rename via Update |
| `permission` | String | Optional, RequiresReplace | `"full_access"` (default) or `"sending_access"`; validated with `StringOneOf` |
| `domain_id` | String | Optional, RequiresReplace | Scopes a `sending_access` key to one domain |
| `token` | String | Computed, Sensitive | Returned only by Create; same lifecycle as `signing_secret` in `resend_webhook` |
| `id` | String | Computed | |

Behavior:

- **Create**: POST `api-keys`; persist `id` + `token`. If `permission` omitted, the API applies `full_access`.
- **Read**: List + match by id; mark gone on 404-ish absence. `token` is never refreshed (API never returns it) — preserved in state.
- **Update**: in-place rename only (POST Update). `permission`/`domain_id` changes force replacement.
- **Delete**: Remove by id.
- **Import**: supported (`terraform import resend_api_key.ci <id>`); token is blank after import (documented).
- Drift note: deleting a key outside Terraform → next plan recreates it with a new token (documented in resource description).

## 2. `data "resend_domain"` data source

SDK surface: `Get(id)`, `List()`.

Schema — exactly one of `id` / `name` required (validator):

| Attribute | Type | Notes |
|---|---|---|
| `id` | String, Optional | Direct GET lookup |
| `name` | String, Optional | List + exact-match filter; error if 0 or >1 match |
| — outputs — | | |
| `status` | String | `pending`/`verified`/… |
| `region` | String | |
| `created_at` | String | |
| `capabilities` | Object{sending, receiving} | Same shape as the resource |
| `records` | List of objects | Same element shape as `resend_domain.records` (`record`, `name`, `type`, `ttl`, `priority`, `status`, `value`) so DNS `for_each` wiring is identical |
| `open_tracking` / `click_tracking` / `tracking_subdomain` | Bool/Bool/String | As returned by the API |

Read: resolve by `id` (GET) or `name` (List + filter), map all fields via a shared helper with the resource (no duplicated mapping logic).

## 3. `resend_domain_claim` resource

SDK surface: `Create(name, region?, custom_return_path?, tracking_subdomain?, open_tracking?, click_tracking?) → DomainClaim`, `Get(domainId)`, `Verify(domainId)`.

Claim statuses: `pending → verified → completed` (terminal success); `blocked`, `expired`, `superseded`, `canceled`, `failed` (terminal/attention states).

Schema — **all optional request properties included**:

| Attribute | Type | Flags | Notes |
|---|---|---|---|
| `name` | String | Required, RequiresReplace | Domain to claim |
| `region` | String | Optional, RequiresReplace | Same enum as resend_domain |
| `custom_return_path` | String | Optional, RequiresReplace | Subdomain for bounce return-path |
| `tracking_subdomain` | String | Optional, RequiresReplace | |
| `open_tracking` | Bool | Optional, RequiresReplace | |
| `click_tracking` | Bool | Optional, RequiresReplace | |
| — computed — | | | |
| `status` | String | Computed | Refreshed on every read; documents the full lifecycle |
| `domain_id` | String | Computed | Becomes a normal domain id once completed |
| `record` | Object{type, name, value, ttl} | Computed | The TXT record proving ownership — wire into DNS provider |
| `blocked_reason` / `failure_reason` | String | Computed | Surfaced when status says so |
| `expires_at` | String | Computed | |

Behavior:

- **Create**: POST claim → save claim/domain id → immediately begin polling: every 30s, GET claim; once `record` is present, call `Verify(domainId)` once, keep polling until status == `verified` (default create timeout 10m, configurable via `timeouts`), then finish. Optional-only request attributes (tracking flags etc.) are written at create and never read back — identical convention to `resend_domain.tls`.
- **Read**: GET claim; update `status`, `blocked_reason`, `failure_reason`, `expires_at`. Any status — including terminal-lost (`expired`, `superseded`, `canceled`, `failed`) — is a valid read; refresh never removes state or errors on status alone.
- **Update**: none — every request attribute is RequiresReplace (a changed claim = abandon old, start new; the old claim is NOT deleted server-side on replace — document that replacement leaves the previous claim to expire naturally).
- **Delete**: no server-side cancel API exists; destroy removes it from Terraform state only (documented).
- **Import**: not supported (claim id == domain id; harmless to allow later).

## 4. Testing & docs

- Unit tests: schema shape, validators, mapping helpers, error paths (fake server where cheap).
- Acceptance (real API, TF_ACC + secrets): full CRUD for `resend_api_key`; `data.resend_domain` against a freshly created test domain; `resend_domain_claim` **cannot** be acceptance-tested (requires a domain verified in a *different* Resend account) — unit tests + documented manual checklist instead.
- Docs: templates + examples for all three (`tfplugindocs`), README feature list update.
- Release: version bump v1.1.0, tag through existing pipeline (GPG-signed, registry auto-index).

## Decisions recorded

- API key `token` uses the signing_secret pattern (write-once, Sensitive, blank on import) — established convention in this provider.
- Claim polling stops at `verified`; multi-day completion is observed via refresh, not awaited in a create timeout.
- Claim replace does not attempt server-side cancel (no API) — documented behavior.
- Data source exposes the full SDK Domain struct surface; no new list data source (YAGNI).
