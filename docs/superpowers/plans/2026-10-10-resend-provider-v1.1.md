# Resend Provider v1.1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `resend_api_key` resource, `data "resend_domain"` data source, and `resend_domain_claim` resource; release as v1.1.0.

**Architecture:** Three new provider objects following the established conventions of `resource_domain.go`/`resource_webhook.go` (tfsdk models, `types.*` attributes, diag-based errors, SDK client from `ConfigureData`). Domain field mapping is shared with the resource via extracted helpers. Claim polling reuses the domaincheck poller style.

**Tech Stack:** terraform-plugin-framework v1.19.0, resend-go/v4 v4.8.2, terraform-plugin-testing v1.16.0, tfplugindocs v0.24.

**Spec:** `docs/superpowers/specs/2026-10-10-resend-provider-v1.1-design.md`

## Global Constraints

- Module path `github.com/Softwarehuset-com/terraform-provider-resend`; provider address `registry.terraform.io/Softwarehuset-com/resend`.
- Example domains in code/docs: `*.appcompany.com` only (never real domains).
- API key `token` is Sensitive, Computed, write-once (signing_secret pattern, `resource_webhook.go:105,159-171` is the reference).
- All SDK request optionals for claims are exposed: `region`, `custom_return_path`, `tracking_subdomain`, `open_tracking`, `click_tracking` — Optional + RequiresReplace, never read back.
- Unit tests run without network; acceptance tests skip unless `TF_ACC` + `RESEND_API_KEY` + `TF_ACC_DOMAIN_BASE` set (see `testAccPreCheck` / `testAccDomainBase` in `resource_domain_test.go`).
- Commit style: conventional commits (`feat:`, `test:`, `docs:`), one logical unit each.

## Review Focus

1. **api_key token leakage** — token must be `Sensitive`; terraform output/show must redact. Test: schema unit test asserts `IsSensitive() && IsComputed()`.
2. **data source ambiguous lookup** — both `id` and `name` set, or neither → clean schema validation error, no API call. Test: unit test on the exactly-one validator.
3. **data source name ambiguity** — 0 matches and >1 matches must both error diagnostically (never empty success). Test: acceptance step with a nanosecond-unique name expects 0-match error.
4. **api_key drift** — key deleted outside Terraform → Read must mark resource gone so plan recreates (no eternal error). Test: acceptance test deletes via SDK between steps? No — cost; instead unit test Read with empty List response asserting `State.RemoveResource` path.
5. **claim failure surfaces reason** — Verify returning `blocked`/`failed` must error with `blocked_reason`/`failure_reason` text inside the create timeout. Test: unit test with fake claim status `failed` + failure_reason asserting the error contains the reason.

---

### Task 1: `resend_api_key` resource

**Files:**
- Create: `internal/provider/resource_api_key.go`
- Test: `internal/provider/resource_api_key_test.go`
- Modify: `internal/provider/provider.go` (Resources list)

**Interfaces:**
- Consumes: `*resend.Client` from `ConfigureData` (pattern at `resource_domain.go:214-224`), `diagFromErr` (errors.go), SDK `ApiKeysSvc` (`Create/CreateWithContext`, `List`, `Update`, `Remove`).
- Produces: `NewApiKeyResource() resource.Resource`; model `apiKeyResourceModel` with `tfsdk` tags `name`, `permission`, `domain_id`, `token`, `id`; registered as `"resend_api_key"`.

- [ ] **Step 1: Write failing unit tests** — schema shape (name Required, permission Optional+StringOneOf["full_access","sending_access"], domain_id Optional, token Computed+Sensitive, id Computed); mapping of `CreateApiKeyResponse{Id, Token}` into model; Update path calls SDK Update with name; Read drift: List response without our id → `resp.State.RemoveResource` (plan recreates, no error).

- [ ] **Step 2: Run** `go test ./internal/provider/ -run TestApiKey -v` — expect compile FAIL (undefined symbols).

- [ ] **Step 3: Implement `resource_api_key.go`** — full CRUD+Import per spec §1: Create POSTs and stores token; Read Lists and matches by id (absent → `resp.State.RemoveResource(ctx)`); Update in-place rename only; Delete Remove; `permission`/`domain_id` have `RequiresReplaceIfConfigured`; import via `resource.ImportStatePassthroughID`.

- [ ] **Step 4: Register** in `provider.go` Resources as `"resend_api_key": NewApiKeyResource`.

- [ ] **Step 5: Write failing acceptance test** `TestAccApiKeyBasic` — create with `permission="sending_access"` + `domain_id` of a throwaway domain; check `token` non-empty + sensitive; rename in place (1 changed); change permission → replacement; import with token blank; destroy. Verify account clean after.

- [ ] **Step 6: Run** unit tests PASS; `RESEND_API_KEY=… TF_ACC=1 TF_ACC_DOMAIN_BASE=… go test ./internal/provider/ -run TestAccApiKey -v -timeout 10m` PASS.

- [ ] **Step 7: Commit** `feat: resend_api_key resource with token lifecycle and import`

### Task 2: `data "resend_domain"` data source

**Files:**
- Create: `internal/provider/data_source_domain.go`
- Test: `internal/provider/data_source_domain_test.go`
- Modify: `internal/provider/provider.go` (DataSources list — new method, first data source), `internal/provider/resource_domain.go` (extract capabilities mapping)

**Interfaces:**
- Consumes: `mapRecords(records []resend.Record) (types.List, diag.Diagnostics)` (`resource_domain.go:446`).
- Produces: `NewDomainDataSource() datasource.DataSource`; extracted shared helper `mapDomainCapabilities(caps *resend.DomainCapabilities) *domainCapabilitiesModel` used by both resource and data source; model `domainDataSourceModel` with `tfsdk` tags `id`, `name` (lookups) + outputs `status`, `region`, `created_at`, `capabilities`, `records`, `open_tracking`, `click_tracking`, `tracking_subdomain`.

- [ ] **Step 1: Refactor `resource_domain.go`** — move the capabilities-fill block from `mapDomainToState` (`resource_domain.go:377-401` region) into `mapDomainCapabilities`; resource behavior unchanged; run `go test ./...` PASS.

- [ ] **Step 2: Write failing unit tests** — exactly-one validator (`id` xor `name`): both-set error, neither-set error, one-set OK; mapping of a full `resend.Domain` (records + capabilities + tracking fields) to outputs.

- [ ] **Step 3: Run** — expect FAIL (undefined).

- [ ] **Step 4: Implement `data_source_domain.go`** — schema with validator; `Read(ctx, req, resp)`: id-path → `client.Domains.GetWithContext`; name-path → `List` + exact filter, error `no domain named %q` on 0, `multiple (%d)` on >1; map all outputs via shared helpers.

- [ ] **Step 5: Register** in `provider.go` `DataSources` as `"resend_domain": NewDomainDataSource`.

- [ ] **Step 6: Acceptance test** `TestAccDomainDataSource` — create throwaway domain, `data` lookup by `name`, check `records` length > 0, MX present, `status` attribute; second config with nanosecond-unique nonexistent name expecting error match `no domain named`.

- [ ] **Step 7: Run** unit + acceptance PASS; commit `feat: resend_domain data source with id/name lookup`

### Task 3: `resend_domain_claim` resource

**Files:**
- Create: `internal/provider/resource_domain_claim.go`
- Test: `internal/provider/resource_domain_claim_test.go`
- Modify: `internal/provider/provider.go` (Resources list)

**Interfaces:**
- Consumes: SDK `DomainClaimsSvc` (`Create`, `Get`, `Verify`), `diagFromErr`, timeouts module usage pattern from `resource_domain_verification.go`.
- Produces: `NewDomainClaimResource() resource.Resource`; model `domainClaimResourceModel` tags: inputs `name`, `region`, `custom_return_path`, `tracking_subdomain`, `open_tracking`, `click_tracking`; computed `status`, `domain_id`, `record` (single nested object: `type`,`name`,`value`,`ttl`), `blocked_reason`, `failure_reason`, `expires_at`, `id`.

- [ ] **Step 1: Write failing unit tests** — schema: all inputs Optional except `name` Required, all RequiresReplace; `record` computed object shape; request-building sends non-empty optionals only (mirror of domain request builder test style); poller: claim `pending` with record present → Verify called once → `verified` finishes; `failed`+reason → error contains reason (Review Focus #5); `blocked` → error with `blocked_reason`.

- [ ] **Step 2: Run** — expect FAIL.

- [ ] **Step 3: Implement** — Create: POST claim, save ids, poll loop (30s tick) calling Get; when `Record != nil` and not yet verified → `Verify(domainId)` once; finish when status `verified`; default create timeout `10m` via timeouts block (`timeouts = { create = ... }` convention). Read: Get + update computed fields for any status. Update: none (all RequiresReplace). Delete: no API call (state-only; add Description note). No Import.

- [ ] **Step 4: Register** `"resend_domain_claim": NewDomainClaimResource`.

- [ ] **Step 5: Run** unit tests PASS; full `go test ./...` PASS; `gofmt`/`go vet` clean.

- [ ] **Step 6: Commit** `feat: resend_domain_claim resource with verify polling`

### Task 4: Docs, examples, v1.1.0 release

**Files:**
- Create: `templates/resources/api_key.md.tmpl`, `templates/resources/domain_claim.md.tmpl`, `templates/data-sources/domain.md.tmpl`
- Create: `examples/resources/resend_api_key/resource.tf`, `examples/resources/resend_domain_claim/resource.tf`, `examples/data-sources/resend_domain/data-source.tf`
- Modify: `examples/resources/resend_domain/resource.tf` (unchanged ok), `README.md` (feature list), generated `docs/` via tfplugindocs

**Interfaces:**
- Consumes: tfplugindocs v0.24 conventions — templates use short names, example dirs use full prefixed names (`examples/data-sources/resend_domain/`).

- [ ] **Step 1: Write examples** using `appcompany.com` domains only; claim example shows DNS wiring comment for `record` output.

- [ ] **Step 2: Write templates** with `.SchemaMarkdown` + `codefile` include, matching existing `templates/resources/domain.md.tmpl` structure.

- [ ] **Step 3: Regenerate docs** — `tfplugindocs generate` (or `make docs` if GNUmakefile target exists); verify `docs/` contains `resources/api_key.md`, `resources/domain_claim.md`, `data-sources/domain.md`.

- [ ] **Step 4: README feature list** — add the three new objects to the capabilities table/list.

- [ ] **Step 5: Full gate** — `gofmt -l .` empty, `go vet ./...`, `go test ./...` PASS, full acceptance suite PASS with secrets.

- [ ] **Step 6: Commit + push + tag `v1.1.0`** — conventional commit `feat: v1.1.0 — api keys, domain data source, domain claims`; watch release workflow success; verify SHA256SUMS signature locally; confirm registry indexes `1.1.0`; from-scratch `terraform init` with `version = "1.1.0"`.
