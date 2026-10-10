# Resend Terraform Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A registry-publishable Terraform provider managing Resend domains (with receiving/MX), domain verification, and inbound-email webhooks.

**Architecture:** `terraform-plugin-framework` provider, one file per resource under `internal/provider/`, talking to the Resend REST API through the official `resend-go/v4` SDK wrapped in a retrying HTTP client. DNS records are exposed as computed attributes (users create them in their DNS provider); verification is a separate poll-until-verified resource.

**Tech Stack:** Go 1.27, terraform-plugin-framework v1.19.x, terraform-plugin-testing (latest stable), resend-go/v4 v4.8.2, tfplugindocs, goreleaser.

**Spec:** `docs/superpowers/specs/2026-10-09-resend-provider-design.md`

## Global Constraints

- Module path: `github.com/Softwarehuset-com/terraform-provider-resend` (rename at release if org differs).
- Provider registry address in `main.go`: `registry.terraform.io/Softwarehuset-com/resend`.
- SDK facts locked by source inspection (resend-go v4.8.2):
  - `CreateDomainRequest`: `Name, Region, CustomReturnPath, TrackingSubdomain, OpenTracking *bool, ClickTracking *bool, Capabilities *DomainCapabilities` — **no `tls`**.
  - `UpdateDomainRequest` (PATCH): `OpenTracking, ClickTracking` (use `SetOpenTracking`/`SetClickTracking` to send explicit false), `Tls`, `TrackingSubdomain` only → `name`, `region`, `custom_return_path`, `capabilities` changes are **RequiresReplace**.
  - `DomainStatus` values: `pending, verified, failed, not_started, partially_verified, partially_failed`.
  - `Domain`/`CreateDomainResponse` share `Id, Name, Status, Records []Record, Region, OpenTracking, ClickTracking, TrackingSubdomain, Capabilities`.
  - `Record`: `Record, Name, Type, Ttl, Status, Value string; Priority json.Number` (absent for TXT/CNAME).
  - Webhooks: `CreateWebhookRequest{Endpoint, Events}`, `UpdateWebhookRequest{Endpoint *string, Events, Status *string}`, create response has `SigningSecret`; GET does not return it.
  - Errors: 429 → `*resend.RateLimitError` (has `RetryAfter` header string); everything else → `errors.New("[ERROR]: <message>")` — no status code in the error. 404 must be detected by `IsNotFound` heuristic: message contains "404" or "not found" (case-insensitive).
  - `NewCustomClient(httpClient, apiKey)`; `Client.BaseURL` is an exported `*url.URL` (override after construction for `base_url`).
- Every resource implements `resource.ResourceWithConfigure`, `ImportStatePassthroughID`, and uses `resp.State.Raw`/framework types (no SDKv2).
- `signing_secret` is `Sensitive`; import must not fail when it is null (`ImportStateVerifyIgnore` in tests).
- Unit tests must not hit the network. Acceptance tests run only when `RESEND_API_KEY` is set (`t.Skip` otherwise).
- Commit after every green test cycle. Conventional Commits.

## Review Focus

Five input classes the task tests below do not already pin, each with its owning test (folded into tasks):

1. **Missing/empty `api_key`** — configure must fail with a clear summary, not a 401 per call → Task 1 Step 3 test.
2. **API 400/422 (e.g. duplicate domain, invalid name)** — diag must surface Resend's message verbatim → Task 2 Step 3 test (`TestDiagFromErr`).
3. **`priority` absent on TXT/CNAME records** — must map to null, not "0" → Task 3 Step 4 test (`TestMapRecords` with TXT fixture).
4. **Domain deleted out-of-band while verification resource exists** — read drops it from state (no error) → Task 4 Step 2 test with fake getter.
5. **Typo'd webhook event name** (`"email.recieved"`) — rejected at plan time by validator → Task 5 Step 4 test.

---

### Task 1: Module scaffold, provider skeleton, configure guard

**Files:**
- Create: `go.mod`, `main.go`, `internal/provider/provider.go`, `internal/provider/provider_test.go`, `Makefile`, `.gitignore`, `LICENSE` (MPL-2.0), `version/version.go`

**Interfaces:**
- Produces: `provider.New(version string) func() provider.Provider` (framework convention); package `provider` with `Version string` field on the struct; `resp.DataSourceData`/`ResourceData` set to `*resend.Client` (nil until configured).

- [ ] **Step 1: Init module + fetch deps**

Run: `go mod init github.com/Softwarehuset-com/terraform-provider-resend && go get github.com/hashicorp/terraform-plugin-framework@v1.19.0 github.com/hashicorp/terraform-plugin-testing@latest github.com/resend/resend-go/v4@v4.8.2`
Record resolved `terraform-plugin-testing` version in the commit message.

- [ ] **Step 2: Write failing provider tests** in `internal/provider/provider_test.go`

Two tests, plain `go test` (no TF CLI):
- `TestProviderSchema`: call `p := (provider.New("test")()).(*provider.Provider)`; `s := &tfprotov6...` — simpler: build `diag := tfsdk.Schema`... use framework directly: `req := provider.SchemaRequest{}; p.Schema(ctx, &req, resp)` then assert: `api_key` exists, `String`, `Required`, `Sensitive`, `Description` mentions `RESEND_API_KEY`; `base_url` optional with default `https://api.resend.com`.
- `TestProviderConfigureMissingKey`: `Configure` with unknown-absent env and empty config → expect `resp.Diagnostics.HasError()` and a summary containing "RESEND_API_KEY".

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/provider/ -run 'TestProvider' -v`
Expected: FAIL — `provider.New` undefined.

- [4] **Step 4: Implement** `main.go` (`providerserver.Serve`, `Address: "registry.terraform.io/Softwarehuset-com/resend"`, `-debug` flag, `version` var) and `internal/provider/provider.go`:
- `Schema`: `api_key` (string, required, sensitive, schema.StringDefault none — `Description: "Resend API key. Defaults to the RESEND_API_KEY environment variable."`), `base_url` (string, optional, `schema.StringDefault("https://api.resend.com")`).
- `Configure`: read env `RESEND_API_KEY` as fallback; if the combined value is `""` → `diag.AddError("Invalid or missing RESEND_API key", ...)` (mentions `RESEND_API_KEY`); else `NewResendClient(key, baseURL)` (Task 2 provides it — until then, inline `resend.NewClient(key)` and refactor in Task 2) into `resp.ResourceData`.
- Resources registered: `NewDomainResource`, `NewDomainVerificationResource`, `NewWebhookResource` — create stub files returning `resource.Resource` that fail loudly if used before Tasks 3–5 (schema with just `id`), so the provider compiles.

- [ ] **Step 5: Run tests to verify they pass; `go vet ./...` clean**

- [ ] **Step 6: Commit** `feat: provider scaffold with api_key auth and configure guard`

### Task 2: Client construction, retry transport, error mapping

**Files:**
- Create: `internal/provider/client.go`, `internal/provider/errors.go`, `internal/provider/client_test.go`, `internal/provider/errors_test.go`

**Interfaces:**
- Consumes: nothing (independent).
- Produces: `NewResendClient(apiKey, baseURL string) *resend.Client` (retrying transport, `BaseURL` overridden when `baseURL != ""` and `!= "https://api.resend.com"`); `IsNotFound(err error) bool`; `diagFromErr(summary string, err error) diag.Diagnostics`.

- [ ] **Step 1: Write failing tests** in `client_test.go` / `errors_test.go`:
- `TestRetryTransport`: `httptest.Server` returns 429 w/ `Retry-After: 0` once, then 200 `{}`; round-trip through the internal `retryTransport` (export `newRetryTransport` for the test); assert 2 total requests seen, final status 200.
- `TestRetryTransportGivesUp`: server always 500 → after `MaxAttempts` (3 in test config) returns last error.
- `TestIsNotFound`: `IsNotFound(errors.New("[ERROR]: 404 Not Found")) == true`; `IsNotFound(errors.New("[ERROR]: Domain not found")) == true`; `IsNotFound(errors.New("[ERROR]: missing_permissions")) == false`.
- `TestDiagFromErr` (Review Focus 2): returns exactly one `diag` with severity ERROR whose Detail contains the original message text.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/provider/ -run 'TestRetry|TestIsNotFound|TestDiag' -v` → FAIL (undefined).

- [ ] **Step 3: Implement**:
- `client.go`: `type retryTransport struct { rt http.RoundTripper; maxAttempts int }` — retry on 429 + 5xx, sleep `Retry-After` (parse seconds) else backoff 250ms·2^n, cap 5 attempts, `ctx` cancellation checked between attempts; `NewResendClient` builds `&http.Client{Timeout: 60s, Transport: newRetryTransport(http.DefaultTransport)}` → `resend.NewCustomClient(...)` → override `BaseURL` if needed.
- `errors.go`: `IsNotFound` (case-insensitive `strings.Contains` for `"404"`, `"not found"`); `diagFromErr(summary, err)` → `diag.Diagnostics{{Severity: diag.Error, Summary: summary, Detail: err.Error()}}`.

- [ ] **Step 4: Wire provider.go's Configure to `NewResendClient`** (replace inline construction).

- [ ] **Step 5: Tests pass** — `go test ./... ./internal/...`; commit `feat: retrying resend client and error mapping`

### Task 3: `resend_domain` resource

**Files:**
- Create: `internal/provider/resource_domain.go` (replace stub), `internal/provider/resource_domain_test.go`

**Interfaces:**
- Consumes: `diagFromErr`, `IsNotFound`, `*resend.Client` from `ConfigureData` (assert-and-cast pattern: `client, ok := req.ProviderData.(*resend.Client)` — error if nil).
- Produces: `NewDomainResource() resource.Resource`; schema and state model `domainResourceModel` (exported fields used only within the file); `mapRecords([]resend.Record) []recordModel` tested in isolation.

- [ ] **Step 1: Write failing schema/CRUD-unit tests**:
- `TestDomainSchema`: attributes present with right type/optionality: `name` (required, string, `RequiresReplace` via `RequiresReplacePlanModifier`), `region` (optional, string, validators incl. one-of `us-east-1,eu-west-1,sa-east-1,ap-northeast-1`, RequiresReplace), `capabilities` (optional, object `sending`+`receiving` string attrs w/ enabled/disabled validators, RequiresReplace), `custom_return_path` (optional, RequiresReplace), `tls` (optional, validator opportunistic/enforced), `tracking_subdomain`, `open_tracking`, `click_tracking` (bools, optional); computed: `id`, `status`, `created_at`, `records` (list of object: `record,name,type,ttl,status,value` strings + `priority` number, computed).
- `TestMapRecords` (Review Focus 3): input `[]resend.Record{{Record:"SPF", Name:"send", Type:"TXT", Ttl:"Auto", Status:"pending", Value:"v=spf1 ..."}, {Record:"SPF", Name:"send", Type:"MX", Priority: json.Number("10"), Value:"feedback-smtp.us-east-1.amazonses.com", ...}}` → first has null priority, second has `types.NumberValue` 10.

- [ ] **Step 2: Verify failure** — undefined `NewDomainResource`.

- [ ] **Step 3: Implement** `resource_domain.go`:
- `Create`: `client.Domains.CreateWithContext(ctx, &resend.CreateDomainRequest{...})` from plan; on success, if plan `tls == "enforced"` immediately `UpdateWithContext(..., &resend.UpdateDomainRequest{Tls: resend.Enforced})` (SDK create cannot send tls); then `GetWithContext` to refresh full state (records incl. MX for receiving).
- `Read`: `GetWithContext`; on `IsNotFound` → `resp.State.RemoveResource(ctx)`; else map to state (records, status).
- `Update`: build `UpdateDomainRequest` from plan diff fields (`SetOpenTracking`/`SetClickTracking` when plan values differ from state, `Tls`, `TrackingSubdomain`); then GET-refresh.
- `Delete`: `RemoveWithContext`; `IsNotFound` → success.
- `Import`: `resource.ImportStatePassthroughID` on `id`.
- `mapRecords` maps `json.Number` → `types.NumberValue` when present, `types.NumberNull` when `""`.

- [ ] **Step 4: Tests pass; commit** `feat: resend_domain resource with records output`

### Task 4: `resend_domain_verification` resource

**Files:**
- Create: `internal/provider/resource_domain_verification.go` (replace stub), `internal/domaincheck/domaincheck.go`, `internal/domaincheck/domaincheck_test.go`

**Interfaces:**
- Consumes: `*resend.Client`, `IsNotFound`, `diagFromErr`.
- Produces: `domaincheck.Getter` interface `{ GetWithContext(context.Context, string) (resend.Domain, error) }` (satisfied by `*resend.DomainsSvcImpl` — reached via `client.Domains`); `domaincheck.WaitForVerified(ctx, g Getter, domainID string, tick, timeout time.Duration, now func() time.Time, sleep func(context.Context, time.Duration) error) (resend.Domain, error)` — exported for the resource; returns `ErrNotVerified` after timeout with failing-record detail built by `FailingRecords(resend.Domain) []string`.

- [ ] **Step 1: Write failing tests** (`internal/domaincheck`):
- `TestWaitForVerifiedSuccess`: fake `Getter` returns `pending` twice then `verified` (record calls); tick 1ms; assert returned status `verified`, 3 calls.
- `TestWaitForVerifiedPartially`: returns `partially_verified` → treated as success (documented: with `receiving` enabled not every record set may fully verify).
- `TestWaitForVerifiedTimeout` (Review Focus 4, adjacent): always `pending`, timeout 5ms → `errors.Is(err, domaincheck.ErrNotVerified)` and `err.Error()` contains the failing record names from `FailingRecords`.
- `TestWaitForVerifiedGone`: getter returns the SDK-shaped error `errors.New("[ERROR]: 404 Not Found")` → `domaincheck.IsGone(err)` true; resource treats as gone (state removed) — resource-level test asserts via fake.

- [ ] **Step 2: Verify failure** — package missing.

- [ ] **Step 3: Implement** `domaincheck.go` with the signature above; terminal set = `verified, partially_verified` (success), `failed, partially_failed` (immediate error listing failing records), else poll. `FailingRecords` returns `"name (type): status"` strings for records whose status is not `verified`. `IsGone` delegates to `provider.IsNotFound` (import cycle note: put `IsNotFound` here instead — `domaincheck.IsGone(err)` string-matches; `provider/errors.go` calls `domaincheck.IsGone`).

- [ ] **Step 4: Implement `resource_domain_verification.go`:**
- Schema: `domain_id` (required, string, RequiresReplace), `status` (computed, string); `Timeouts` with `Create: 30 * time.Minute` default (only `create`).
- `Create`: `VerifyWithContext` once (transient errors during verify call → `diagFromErr`); then `domaincheck.WaitForVerified(ctx, r.client.Domains, ...)` with tick `10s` growing to `60s`; on `ErrNotVerified`/terminal-failure → `diag.AddError` including `FailingRecords` output (this is the "apply tells you which DNS records fail" contract). Save `status`.
- `Read`: `GetWithContext`; gone or status ∈ {`not_started`, `pending`, `failed`, `partially_failed`} → `resp.State.RemoveResource(ctx)`; else refresh `status`.
- `Update`: never called (no mutable args) — implement as passthrough copy.
- `Delete`: no-op.
- `Import`: passthrough on `domain_id` (ID = domain ID).

- [ ] **Step 5: Tests pass; commit** `feat: resend_domain_verification with dns-record failure reporting`

### Task 5: `resend_webhook` resource

**Files:**
- Create: `internal/provider/resource_webhook.go` (replace stub), `internal/provider/resource_webhook_test.go`

**Interfaces:**
- Consumes: `diagFromErr`, `IsNotFound`, `*resend.Client`.
- Produces: `NewWebhookResource() resource.Resource`.

- [ ] **Step 1: Write failing tests**:
- `TestWebhookSchema`: `endpoint` required string w/ URL-ish validator; `events` required `schema.SetOfString` w/ `stringvalidator.OneOf` covering all SDK event constants (`resend.EventEmailReceived` … use the exported consts, not literals); computed: `id` (string), `signing_secret` (string, **Sensitive**), `status`, `created_at`.
- `TestWebhookEventValidator` (Review Focus 5): unknown event `"email.recieved"` → validator returns error at schema validation level (`ValidateAttribute` on the response, or unit-test the validator function directly with `"email.received"` ok / `"email.recieved"` error).

- [ ] **Step 2: Verify failure.**

- [ ] **Step 3: Implement**:
- `Create`: `CreateWithContext` → state: `id`, `signing_secret` (Sensitive), then GET-refresh for `status`/`created_at`/`events`.
- `Read`: `GetWithContext`; gone → remove; `signing_secret` stays as-is in state (GET doesn't return it — don't overwrite with empty).
- `Update`: `UpdateWebhookRequest{Endpoint: &ep, Events: evs}` → GET-refresh.
- `Delete`: `Remove`; gone → success.
- `Import`: passthrough on `id`.

- [ ] **Step 4: Tests pass; commit** `feat: resend_webhook resource with sensitive signing secret`

### Task 6: Acceptance tests

**Files:**
- Create: `internal/provider/resource_domain_test.go` (append `TestAccDomain*`), `resource_webhook_test.go` (append `TestAccWebhook*`), `resource_domain_verification_test.go` (`TestAccDomainVerification`)

**Interfaces:**
- Consumes: `terraform-plugin-testing` `resource.Test`+`resource.UnitTest` is NOT used — these are `resource.Test` with `ProtoV6ProviderFactories` (or `ProviderFactories` via `providerserver`) and `TF_ACC` env gate + `RESEND_API_KEY` gate.

- [ ] **Step 1: Write the tests** (each `t.Skip` unless `RESEND_API_KEY` set):
- Domain: create with `capabilities = {sending="enabled", receiving="enabled"}` on `tfacc-<runID>.example.com` → assert `records` contains a `type = "MX"` entry (inbound MX present); update `open_tracking` → plan shows in-place change; import + `ImportStateVerify`; destroy.
- Verification: domain under test has no real DNS → expect the apply to fail with an error matching `DNS|record` within a short `timeouts = { create = "1m" }` — asserts graceful failure listing records, and is intentionally a "fails-then-destroy" `ExpectError` test.
- Webhook: `events = ["email.received"]` → `signing_secret` non-empty (check via `resource.TestCheckResourceAttrSet`), update events to `["email.received","email.sent"]`, `ImportStateVerifyIgnore: schema.Schema{ signing_secret }`-style `resource.ImportStateVerifyIgnore([]string{"signing_secret"})`, destroy.

- [ ] **Step 2: Verify they compile** (`go vet ./...`) and skip cleanly with no env key: `go test ./internal/provider/ -run TestAcc -v` → SKIP.

- [ ] **Step 3: Run for real if user provides `RESEND_API_KEY`** (manual gate — ask). Record results. Commit `test: acceptance coverage for domain, verification, webhook`.

### Task 7: Docs, CI, release skeleton

**Files:**
- Create: `examples/provider/provider.tf`, `examples/resources/resend_domain/resource.tf`, `examples/resources/resend_domain/import.sh`, `examples/resources/resend_domain_verification/resource.tf`, `examples/resources/resend_webhook/resource.tf` + `import.sh`, `templates/index.md.tmpl`, `templates/resources/{resend_domain,resend_domain_verification,resend_webhook}.md.tmpl`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.goreleaser.yml`, `.terraform-provider-release.yml`, `CHANGELOG.md`, `Makefile` targets (`docs`, `testacc`, `build`)

- [ ] **Step 1: Examples** — `resend_domain` example uses `automated.appcompany.com` with receiving enabled + a commented Cloudflare `for_each` over `resend_domain.records` showing MX/DKIM/SPF wiring; webhook example posts `email.received` to `https://api.appcompany.com/hooks/resend`.

- [ ] **Step 2: Templates** — per-resource pages documenting every schema attribute (copy table content from the spec's resource sections), `index.md.tmpl` with provider auth docs.

- [ ] **Step 3: CI** — on push/PR: `go vet`, `go test ./...`, `gofmt -l` check, build. Release — on tag `v*`: goreleaser (GOOS/GOARCH matrix linux/darwin/windows × amd64/arm64), `GPG_FINGERPRINT` secret signs, `.terraform-provider-release.yml` present for registry ingestion.

- [ ] **Step 4: `tfplugindocs generate` locally** (if `tfplugindocs` available via `go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest generate`), verify `docs/` output renders; commit `docs: examples, templates, CI and release skeleton`
