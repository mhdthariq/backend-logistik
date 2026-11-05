# Backend test strategy

## Continuation — delivery tasks and scans (2026-11-05)

Six additional method/path pairs are implemented (91/185 total): GET/POST
deliveries, GET/PUT/DELETE deliveries/[id], and POST deliveries/[id]/scans.
DELETE removes the delivery and cascades its scans, matching the legacy route.
Delivery completion and pickup confirmation with evidence-photo processing remain
planned. No frontend routing, schema or live application database is changed.

Source was rechecked against the current kirimku implementation. Go options now
include active checkpoints ordered by sequence, with routes ordered by name/id,
matching the recent route-reuse update. Return-transport creation remains planned.

Delivery assignment retains origin/destination receipt gates, courier validation,
first-gap DLV-2026- numbering, tracking and audit records. Lists retain warehouse
scope, executor ownership, driver/kenek DIRECT filtering, first-checkpoint badges,
customer fields and explicit per-package scan counts. As in Next.js, a B2B master
scan satisfies detail progress but does not inflate explicit list scan counts.
Scanning never completes a delivery or advances shipment custody.

Intentional changes follow the previous Go pickup slice:

- Assignment and scans enforce shipment scope. Executor detail reads require the
  assigned employee. Accounts with no employee get an empty executor list and
  cannot scan an unassigned task; owners/assignment supervisors keep their override.
- Scan-user relations expose only id/name rather than full account records.
  Proof photos and capture metadata retain proof_photo.view gating; planned task
  coordinates and receiver text remain visible.
- Mutations lock the shipment before the task, serializing Go scans with package
  edits and arrival undo. Task creation and tracking commit atomically. Code
  allocation uses a transaction advisory lock. Audits remain best-effort after commit.

Disposable-PostgreSQL regressions cover CRUD, validation, permissions, warehouse
separation, receipt gates, proof redaction, terminal tasks, courier ownership,
empty-employee/unassigned-task denial, B2C and B2B scans, context/task isolation,
concurrent duplicate scans, injected tracking/scan/delete failures, cascading scan
deletion, DIRECT crew/checkpoint behavior, sparse numbering, route-option ordering,
and two independent tenants sharing identical task/package identifiers.

Validation: the full WSL race and disposable-PostgreSQL suite, route inventory
check, vet, Linux build and golangci-lint passed (zero lint issues). Selected
legacy proof-visibility (shared/API) and resi-privacy suites passed (52 tests).
Graphify was refreshed. Full legacy response fixtures, mixed Node/Go contention,
browser/proxy checks and staging cutover remain acceptance work.

## Continuation — pickup tasks and scans (2026-11-05)

Six additional method/path pairs are implemented (85/185 total): GET/POST
pickups, GET/PUT/DELETE pickups/[id], and POST pickups/[id]/scans. The DELETE
action cancels the task and preserves history. Pickup confirmation with image
decoding, capture metadata and photo watermarking remains on the legacy handler;
delivery, warehouse, transport, notification and finance routes remain planned.

Pickup creation preserves ready-status, positive-price, B2B invoice and courier
validation, the first unused PICK-2026- code, tracking and audit fields. List
responses retain sender snapshot addresses, origin-branch history, per-package
scan counts, driver/kenek DIRECT filtering and first-checkpoint check-in badges.
Scans retain B2B master-resi completion, B2C package requirements, unexpected and
duplicate responses, earliest successful metadata, task/context isolation and
corporate QR checks. Scanning alone never advances pickup or shipment custody.
Payment summaries share the shipment-detail implementation (verified payments,
discounted price and deprecated dpOk=true), without recording a payment.

Intentional changes relative to legacy pickup routes:

- Creation and cancellation now check shipment scope. Executor detail access
  and scans require a matching assigned employee; accounts with no employee
  receive an empty executor list and cannot scan an unassigned task. Supervisors
  with pickup.assign_kurir and owners retain their override. Warehouse scope
  still applies; history lists stay associated with the origin branch.
- PICKED_UP tasks reject edits as well as cancellation so a reassignment/status
  edit cannot reverse physical custody. COMPLETED/CANCELLED edits remain blocked.
- Scan-user relations expose id/name, excluding credentials and other account
  fields. Proof photos and capture metadata retain proof_photo.view gating;
  planned task coordinates remain visible without that permission.
- All mutations lock the parent shipment before the pickup row, consistent with
  Go receipt/cancellation. Concurrent scans classify one success and subsequent
  duplicates. First-gap code generation uses a transaction advisory lock.
- Task, shipment restoration and tracking writes commit atomically; failures
  roll back the entire action. Audits remain best-effort after commit, matching
  the legacy audit service. No database schema changes are needed.

PostgreSQL regressions cover CRUD and validation envelopes, permissions, courier
ownership, warehouse separation, no-employee denial, B2B invoice gating, proof
redaction, DIRECT crew/checkpoint behavior, sparse code allocation, B2C and B2B
scans, terminal states, concurrent duplicate scans, injected tracking/scan
failures, and two independent tenants with identical task/package identifiers.
Mixed Node/Go contention and full legacy response fixtures remain staging gates.

Validation: the final full WSL race and disposable-PostgreSQL suite, inventory
registration check, vet, Linux build and golangci-lint passed (zero lint issues).
The selected legacy proof-visibility (shared/API) and resi-privacy suites passed
(52 tests). Graphify was refreshed. Changes are local; no frontend routing or
live database was modified.

## Continuation — shipment arrival scans, reception and undo (2026-11-04)

Five additional method/path pairs are implemented (79/185 total): GET/POST
arrival-scans and POST arrival-scan-all, arrive and undo-arrival. The receipt
handler supports courier drop-off, walk-in and destination transport reception.
B2B master scans satisfy the entire consignment; B2C still requires each package.
Progress preserves earliest successful scan metadata, explicit null fields and
context isolation, including legacy ARRIVED_AT_GUDANG rows without destReceivedAt.
Unexpected and duplicate scans retain their legacy successful response envelopes.
QR URLs enforce corporate identity; bare scan codes remain supported.

Database regressions cover permissions, warehouse and Marketing scope, inactive
warehouses, incomplete/empty consignments, origin/destination transitions, receipt
timestamps, pickup completion/restoration, legacy origin history fallback, dedicated
undo permission and reason validation, cancelled vs active downstream work and
expenses, scan-context cleanup, repeated actions, concurrent duplicate scans, and
rollback on injected scan, tracking and transactional undo-audit failures. Two
independent databases with identical shipment/package identifiers verify tenant
scan isolation and absence of cross-tenant receipt/reversal writes.

Intentional concurrency changes relative to legacy scan/receipt handlers:

- Arrival reads, single/bulk scans and receipt lock the parent shipment until the
  transaction completes. Concurrent Go scans classify one success and subsequent
  duplicates; bulk scan failures roll back the entire batch.
- Undo runs serializably and retains its mandatory audit inside the transaction.
  Receipt and scan audits remain best-effort after commit, as in the legacy service.
  Concurrent serialization failures return 409 through the existing adapter.
- Reason and QR length limits count UTF-16 units, matching JavaScript.

Selected legacy validation:

```powershell
bun run test tests/app/api/v1/shipments/undo-arrival.test.ts tests/shared/resi-privacy.test.ts
```

Validation record: the final WSL race suite, disposable-PostgreSQL integration
tests, inventory registration check, vet, Linux build and golangci-lint passed
(zero lint issues). Both selected legacy suites passed (21 tests). Graphify was
refreshed. No live database or frontend routing is changed.
Mixed Node/Go scan/receipt contention, full legacy response fixtures, load checks,
frontend/E2E routing and staging rollout remain acceptance work. Pickup/delivery,
transport, notifications and finance handlers remain planned.

## Continuation — shipment ready/cancel and public tracking (2026-11-04)

Four more method/path pairs are implemented (74/185 total): POST ready, POST
cancel, and GET/POST public tracking. GET preserves the legacy 405 response;
POST requires a corporate header and full sender/recipient phone verification.

Database regressions cover ready prerequisites, every lifecycle status accepted or
rejected by these two actions, permission and warehouse/Marketing scope, active
transport/delivery/invoice cancellation blocks, payment and pickup warnings,
B2C company-share escrow, withdrawal reservations, B2B exemption, repeated actions,
ledger conservation, competing debits, and rollback on injected tracking failure.
Two independent tenant databases verify public lookup isolation and cancellation
token isolation. Public fixtures cover sender snapshot precedence, customer-phone
fallback, equivalent Indonesian phone formats, master/resi/child code lookup,
cancelled/unknown shipments, masked contacts, omitted scan/financial/credential
fields, and private no-store responses. Limiter tests cover concurrency, tenant
keys, expiry and the 10,000-key bound.

Intentional changes relative to the legacy mutation routes:

- Status, tracking event, escrow debit/refund and wallet ledger updates commit in
  one serializable transaction. Legacy ready could leave a hold before updating
  status, and cancel committed status before attempting its refund. A failure in
  Go rolls back the complete action; concurrent serialization failures return 409.
- Existing escrow business references are reused before checking available funds,
  allowing recovery from a legacy partial ready operation without another debit.
- Audit writes remain best-effort after commit, consistent with the existing Go
  adapter and legacy audit service; cancellation warnings remain in the audit log.

The tracking limiter is bounded and protected by a mutex per application instance,
with the legacy limit of ten attempts per corporate ID/code in fifteen minutes.
It is not shared between replicas or the legacy and Go processes. Coordinated
limits and mixed Node/Go wallet contention remain staging acceptance work.
Public totals preserve legacy dimension-based volume because the legacy public
package projection omits the direct volumeM3 override.

Selected legacy suites (37 tests):

```powershell
bun run test tests/app/api/v1/shipments/cancellation.test.ts tests/app/api/v1/public/tracking.test.ts tests/shared/resi-privacy.test.ts
```

Validation record: the final WSL disposable-PostgreSQL suite, race detector,
inventory registration check, vet, Linux build and golangci-lint passed (zero
lint issues). All 37 selected legacy tests passed. Graphify was refreshed.

Shipment arrival/scans/undo, notification orchestration, pickup/delivery actions,
transport and finance handlers remain planned. No frontend routing or live schema
changes are part of this continuation.

## Continuation — shipment editing, pricing and documents (2026-11-04)

Seventeen more legacy method/path pairs are implemented (70/185 total). The WSL
runner below exercises shipment create/list/detail/update/delete, package expansion
and batch changes, price snapshots, documents, internal tracking and print logging.
It applies the actual Prisma migration history to disposable databases.

Regression coverage includes warehouse scope through origin/in-transit/destination
states, Marketing ownership, sender defaults, direct-shipment permission denial,
payment-proof and actor-credential redaction, invoice filters, deletion protection
for financial/operational history, concurrent master/package code allocation,
strict batch validation, batch rollback on an injected second-row failure, pricing
rollback on a tracking-event failure, verified-payment repricing locks, Marketing
discount limits and wallet withdrawal reservations, B2B wallet exemption, tariff
fallback excluding other customers, and document type/size/ownership checks.

The pure pricing suite ports legacy actual/volumetric kg, B2B simple kg, B2C simple
kg, koli, cubic, minimums, direct volume and rounding cases, including the floating
point value just below 0.5 where `floor(x + 0.5)` diverges from JavaScript.

Intentional changes in this slice:

- B2B /kg billing follows the shipment customer's type, including generic tariffs
  with no customerType: entered kg (subject to the configured minimum) times rate,
  with no volumetric comparison or weight rounding. Preview and price snapshots
  share this rule; B2C volumetric billing retains its existing behavior.
- Tracking actor objects omit password hashes.
- Shipment/package mutations lock the parent row; batch edits, deletion and pricing
  use serializable transactions. Price snapshots and tracking events commit together.
- Concurrent master creation uses an advisory lock. Daily package allocation shares
  the existing CodeSequence key with Node and commits with package inserts.
- Package expansion is bounded at 10,000 per create request; invalid/nonobject
  inline details return validation errors. Legacy had no finite upper expansion cap.
- The HTTP JSON limit is 16 MiB so the legacy 12,000,000-character document data URL
  limit is reachable after base64 expansion. Document validation keeps its legacy
  MIME-prefix/length checks; it does not validate decoded file signatures.
- List totals and latest tracking events load in batches, avoiding a query per row.

Selected legacy validation command (171 tests):

```powershell
bun run test tests/domain/pricing.test.ts tests/application/pricing/pricing-service.test.ts 'tests/app/api/v1/shipments/[id]/details/batch/route.test.ts' tests/app/api/v1/shipments/delete-and-invoice.test.ts
```

Validation record: the final WSL race suite, disposable PostgreSQL integration
tests, inventory check, vet, Linux build and golangci-lint passed (zero lint issues).
The pricing engine has 100% statement coverage (36/36 statements). All 171 selected
legacy tests passed. Graphify was refreshed. No live database was used.

Remaining acceptance work includes complete legacy response fixtures, mixed Node/Go
write contention, two-tenant shipment scenarios, production-sized workload checks,
frontend/E2E routing and staging rollout. Ready/cancel/arrival, public tracking,
notifications and shipment finance actions remain on the planned inventory rows.
This slice does not change frontend routing or apply live database changes.

## Continuation — identity and master-data APIs (2026-11-04)

Run in WSL from `/mnt/d/Project/NovaLogistik/backend`:

```bash
bash scripts/setup-wsl.sh  # first use: workspace-local Go, lint, PostgreSQL
bash scripts/test-wsl.sh
.tools/go/bin/go tool cover -func=coverage.out
```

The setup script targets the installed Ubuntu/amd64 WSL distribution, uses apt
package downloads without installing services, and keeps binaries under ignored
`.tools/`. Go is 1.27.3, pgx 5.12.0, Fiber 3.6.1 and golangci-lint 2.15.0.
The test runner uses a fresh `/tmp/novalogistik-go-test.*` cluster, loopback port
55439 (override `TEST_POSTGRES_PORT`), and trust auth for that disposable cluster.
It stops the server on success or failure and retains its temporary files for diagnosis.
`TEST_DATABASE_URL` may instead point to a disposable PostgreSQL admin connection;
the tests CREATE/DROP only uniquely named `novalogistik_test_*` databases and require
CREATEDB. Never set it to an account/database intended for application traffic.

Integration tests create actual databases and apply every Prisma SQL migration.
They verify Node password/session fixtures, login validation and normalization,
raw-login vs hydrated permissions, direct permissions and Owner-role inheritance,
expiry/inactive/logout behavior, user/employee lifecycle, partner/wallet provisioning,
profile/password updates, role permission replacement and transaction rollback.
Two independent tenant databases exercise concurrent authentication and cross-tenant
token denial. License tests cover caching, stale fallback, upstream errors, invalid
payloads, deadlines, credential encoding and bounded pool leasing/eviction.

Master-data tests cover warehouse reference protection, concurrent code allocation,
bank validation and invoice-only account selection, checkpoint ordering and replacement,
customer/warehouse/marketing scope, vehicle capacity/photos, tariff date-only expiry,
duplicate identity, method validation and linked-shipment deletion protection, settings,
dropdowns and mobile version policy. A separate migration check compares current Prisma
scalar columns with the migrated database and reapplies the reconciliation migration.
An inventory test checks actual Fiber registration against every CSV method/path status.

Intentional fixes relative to the legacy routes:

- Employee lists strip nested user password hashes.
- Customer search retains warehouse scope instead of replacing it with the search OR.
- Multi-row identity/master-data writes roll back together on invalid dependencies.
- Checkpoint reordering avoids transient unique-sequence collisions; duplicate IDs fail.
- Capacity settings are read per tenant without the legacy process-global cache.
- No implicit RBAC/demo bootstrap runs in Go. Provision the existing permission catalog
  and operator accounts explicitly; schema ownership stays with Prisma.

These regressions do not establish full production parity. Pricing use-case tests,
full legacy response fixture comparisons, frontend/E2E cutover, workload/load tests,
license outage policy review and staging reconciliation remain acceptance work.
Next.js routing and all unmigrated operations remain unchanged.

Validation record for this continuation: the WSL race suite, fresh-database migration
chain and schema-column check, two-tenant concurrency checks, inventory registration
test, `go vet ./...`, Linux binary build and golangci-lint all pass (zero lint issues).
No live application database or license service was used. Full legacy Vitest/UI suites
were not run; TypeScript application code and frontend routing were unchanged.

## Phase 1 automated coverage

- `cmd/api/main_test.go`: invalid process configuration and a real occupied-port
  startup failure, including error propagation to the process boundary.
- `internal/config/config_test.go`: defaults, environment overrides, IPv6/localhost,
  whitespace, invalid hosts, invalid/out-of-range ports, invalid/nonpositive timeouts.
- `internal/httpapi/app_test.go`: legacy greeting, liveness, JSON missing routes and
  unmigrated business routes, success metadata/null data, field validation, license
  error codes, 401, framework errors, internal-error redaction and logging, recovery
  from panic followed by a successful request.

HTTP tests use Fiber `app.Test` with real request/response serialization, no listener,
database, license server, demo seed or frontend dependency. Go tests use `_test.go`
files beside packages, with table-driven subtests. The error-test routes are registered
only by tests and do not exist in the executable.

```powershell
go test ./... '-coverprofile=coverage.out'
go tool cover '-func=coverage.out'
go vet ./...
go build -o bin/api.exe ./cmd/api
go test -race ./...
golangci-lint run
```

Race detection needs CGO and a compatible C compiler; a default Windows Go install
with `CGO_ENABLED=0` cannot run it. Install/configure the compiler before claiming
race validation. `golangci-lint` is a separate tool; absence is not a passing check.
Coverage measures tested logic, not phase completion; startup/signal behavior requires
a process smoke test in addition to unit tests.

## Phase 1 validation record — 2026-10-28

- Go 1.27.3, Fiber v3.6.1 on Windows amd64.
- `go test ./... -coverprofile=coverage.out`: passed; overall statement coverage 90.0%.
  Config and HTTP packages: 100%; command package: 64.3%. `main()` and the successful
  return from the long-running listener are not covered by unit tests.
- `go vet ./...` and executable build: passed.
- Built executable smoke: real HTTP requests to `/api` and `/health/live` passed
  on `127.0.0.1:18080`; the temporary process was stopped afterward. This smoke
  does not establish graceful draining of active requests.
- `go test -race ./...`: unavailable because CGO is disabled; no compatible C compiler
  was found. `golangci-lint run`: unavailable because the executable is not installed.
  These checks remain required before production cutover; they are not marked passed.
- Module download, coverage and local HTTP checks needed elevated execution after
  sandbox cache/socket denials. No database or external license service was accessed.
- Existing TypeScript/UI tests were not rerun: `kirimku/` and frontend routing were
  unchanged. No business API/database parity is claimed for this foundation phase.

## Required for subsequent phases

Use three layers: pure domain/use-case tests, Fiber HTTP contract tests with injected
ports, and real disposable-PostgreSQL tests for repositories and transactions. Add
staging browser tests for the frontend and method/path routing once cutover starts.
See the [phase gates](migration-plan.md#phases-and-acceptance-gates) for module cases.

Retain legacy Vitest suites as executable specifications and port assertions, not
TypeScript mock internals. Explicitly compare status, JSON key presence/nullability,
dates, validation fields, permission/license codes, pagination and persisted effects.
New Go tests do not replace UI tests. Run affected legacy suites after a shared
contract change and the full legacy suite before final cutover.

Tests currently absent from the legacy suite must be added before migrating the
relevant slice: real transaction rollback, two-tenant isolation, concurrent wallet
and sequence writes, license HTTP failures, persisted sessions across runtimes,
and proxy-level HTTP behavior. Never label mocked handler tests as database coverage.

Use synthetic fixtures with explicit IDs, dates and expected amounts. For money and
pricing, capture JS rounding fixtures (including half-unit and decimal edge cases)
before selecting a Go numeric representation. Do not silently change historical
Float columns to integer money during a behavior-preserving migration.
