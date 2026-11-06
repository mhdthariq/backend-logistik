# Backend test strategy

## Phase 7 — completed finance migration (2026-11-06)

All 47 phase 7 method/path pairs are implemented (181/185 overall). The inventory
test rejects any unregistered phase 1–7 route. The remaining four planned handlers
belong to phase 8. Earlier entries below describe intermediate migration states.

Invoice CRUD, lines, print logging, sending, and settlement create/update/delete
preserve B2B-only billing, arrived-transport eligibility, one invoice per shipment,
bank snapshots, PPh annotation, and zero-total settlement behavior. Settlement
mutations use serializable transactions for payment/status/commission/ledger
changes. Full payment releases a pending commission; reducing payment reverses it
only when the wallet can cover the reversal and active withdrawal reservations.
Draft edits recalculate pending commission values and attribution while retaining
the recorded rates for the same partner. Unlike the legacy edit paths, invalid
negative line amounts/totals and mixed marketing attribution are rejected.

Wallet writes lock the wallet row and append immutable ledger entries in the
same transaction as balance and workflow changes. Withdrawal creation locks the
wallet before checking reservations. Top-up verification and withdrawal completion
use stable business references and locked status transitions. Repairs record
ownership at creation, debit/refund only partner-owned vehicles, and retain
transactional action logs after deletion. Company repairs never touch wallets.

Company reports preserve operational-expense filtering/pagination and unfiltered
totals, customer receipts independent of wallets, partial invoice receipts,
partner workflow summaries, and monthly ledger groups. Invoice payment monthly
buckets retain the legacy behavior of grouping only the eight recent settlements.
Partner self-service views scope wallets, commissions, vehicles, transports,
repairs and their logs to the authenticated partner. Financial DateTime values
serialize to UTC milliseconds. Top-up bank information accepts the same
NEXT_PUBLIC_COMPANY_BANK_* process environment variables as the legacy service.

Phase 7 regressions cover route authentication/permissions, ownership separation,
state transitions, proof/input validation, immutable bank snapshots, commission
release/reversal with reservation guards, zero invoices, rollback on ledger
failure, repair corrections/refunds/deleted history, report projections, concurrent
withdrawal reservation, exactly-once top-up/completion, concurrent wallet credits,
and settlement overpayment prevention.

Validation: the full disposable-PostgreSQL suite passed with race detection;
the final focused phase 7/inventory suite and financial-date tests also passed.
Go vet, Linux build, golangci-lint (zero issues), and whitespace checks passed.

Frontend routing and live data are unchanged. Browser/proxy acceptance, production
cutover, full response fixture comparison, and mixed Node/Go writer contention
remain rollout gates; Go row locks cannot make legacy unlocked writers safe.

## Phase 7 — partner administration (2026-11-06)

Four more handlers are implemented (141/185 overall): partner listing, partner
settings, profile editing and account disabling. Company-side permissions remain
independent: partner.view, partner.update, user.update and user.disable. Partner
listing includes inactive partners and lazily creates missing wallets with a
conflict-safe insert. Its response preserves ledger earnings by type/direction,
withdrawal reservations, clamped available balances, relation counts, warehouse
alignment, bank information and rounded combined earnings. No ledger entry or
balance adjustment is created by listing.

Settings validate positive percentages summing to 100 within the legacy tolerance
and active warehouse IDs, preserve empty-note/invalid-number fallback behavior,
and change only future settlement percentages. Profile updates preserve the
legacy no-change response, raw password hashing, masked password audit entries,
and JavaScript Boolean behavior for isActive. Account-disable and session-token
deletion commit atomically; DELETE protects Owner accounts and keeps the partner,
wallet and financial history intact. Invalid warehouse IDs use Go's existing
422 validation convention. Audit remains best-effort after commit.

Disposable PostgreSQL regressions cover wallet creation under concurrent listing,
ledger/reservation totals, filters/nulls, permissions, warehouse validation,
protected account fields, raw password verification, redacted response/audit data,
session invalidation and rollback on revocation failure. Existing settlement and
tenant tests now verify historical share snapshots, future share changes and
same-ID partner isolation across tenants. Partner self-service, remaining finance
routes, full legacy response fixtures, browser acceptance and mixed-runtime
contention remain open. Frontend routing and live databases are unchanged.

Validation: the full Go regression suite passed with race detection and disposable
PostgreSQL, as did focused partner/settlement/tenant/inventory tests, vet, Linux
build and golangci-lint (zero issues). The adjacent legacy login/customer suites
passed 39 tests; no dedicated legacy partner-administration suite was present.

## Phase 7 — payment review and unpaid list (2026-11-06)

Three phase 7 handlers are implemented (137/185 overall): payment verification,
payment rejection and the B2C unpaid list. Both review actions use conditional
RECORDED transitions inside serializable transactions, locking the shipment first
to coordinate with Go payment recording. Verification counts only VERIFIED money,
uses finalPriceAmount with priceAmount fallback, rejects B2B/cancelled/unpriced
shipments and overpayments, and retains the legacy serialization-conflict message.
Rejection preserves optional trimmed audit reasons and permits cancelled/B2B
shipments. Audit remains best-effort after commit, as in the legacy service.

Unpaid uses warehouse scope without the DIRECT-management or marketing ownership
exceptions of individual shipment endpoints. It preserves the legacy projection,
discount fallback, payment history and pending/verified totals, including priced
shipments with zero remaining balance. Go adds ID ordering to break timestamp ties.

Disposable PostgreSQL tests cover permissions, warehouse separation, trusted actor
attribution, terminal transitions, invalid amounts, rollback on failed payment
updates, rejection audit reasons, concurrent overpayment prevention, repeated review,
discount/pending math, history projection and same-ID two-tenant isolation. Legacy
payment tests pass (35 tests). Mixed Node/Go contention, full response fixtures,
browser/proxy acceptance and production cutover remain open. Other phase 7 routes
remain planned; frontend routing and live databases are unchanged.

Validation: focused payment/unpaid/tenant/inventory tests passed with the race
detector and disposable PostgreSQL; vet, Linux build and golangci-lint passed
(zero issues). The full regression run passed all other tests; the unpaid test's
fixture/field-count assertions were corrected and verified in the focused rerun.

## Phase 6 — transport and return tasks (2026-11-05)

All 33 phase 6 method/path pairs are implemented (134/185 overall). The inventory
test now rejects any planned route in phases 1–6. Transport settlement is included
because its route belongs to phase 6; wallet credit and settlement creation commit
atomically. Remaining phase 7–8 routes and frontend routing are unchanged.

Disposable PostgreSQL regressions cover planning validation, vehicle/crew conflicts,
simultaneous planning, shipment loading/tracking rollback, executor and warehouse
access, warehouse staff assignment, capacity projections, checkpoint radius and
duplicate checks, automatic departure/arrival, DIRECT pickup/delivery creation,
activity GPS/capture/photo validation and monotonic last location, multi-drop
assignment/unloading/approval, completion and undo, return task approval/reversed
route reuse/replanning/status synchronization, expense proof arrays/corrections/
approval/cancellation/removal, durable reversal audit rollback, settlement rollback,
duplicate settlements and multiple credits to the same owner wallet.

Go takes transaction-scoped locks for mutable transports and serializes planning
checks/code allocation. Linked shipment/task/tracking writes stay in that transaction.
The settlement path also locks the owner wallet before reading its balance.
These locks coordinate Go writers; mixed Node/Go contention remains a staging gate.
Go enforces warehouse scope on planning, print and settlement actions as well as
operational routes, and checks resource availability when creating a return leg.
Image content must match its declared JPEG/PNG format; expense corrections reject
null types/amounts rather than persisting an invalid type or zero amount after a
fallback validation. The existing Go watermark
renderer now adds the trusted vehicle plate/description and transport shipment count.
Its font/rendering differences from Sharp remain documented below.

The selected legacy transport/capacity/domain suites ran 231 tests: 230 passed;
`completed-consumers.test.ts` fails its finance-summary case because its database
mock omits `payment.groupBy`. No legacy source or tests were changed to mask it.
Full response fixtures, browser/visual signoff, mixed-runtime/load testing and
production cutover remain acceptance work. No live data or schema was changed.

Validation: the full WSL race/disposable-PostgreSQL suite, inventory registration,
vet, Linux build and golangci-lint passed (zero lint issues). A final focused race
run of transport, evidence processing and handover completion tests also passed,
followed by vet, build and lint after the photo storage-limit adjustment.

## Continuation — remaining phase 1–5 routes (2026-11-05)

The nine remaining phase 4–5 method/path pairs are implemented, bringing the
inventory to 104/185. Phases 1–3 already had no planned rows. The inventory test
now rejects any unmigrated phase 1–5 row. This completes route implementation,
not staging acceptance or production cutover.

- Shipment payment history and recording preserve verified-only remaining-balance
  checks, discounted totals, recorded status, actor attribution, optional proof,
  B2B invoice-only billing, and the scoped Admin Gudang B2C permission exception.
  Payments remain RECORDED until the phase 7 verify/reject routes run on legacy.
- Operational expense creation requires a received B2B non-Marketing shipment.
  Reports retain the restricted legacy projection and approved/pending totals.
  Pending deletion, approval, cancellation and approved/cancelled removal preserve
  their independent permissions. Reversal/deletion reasons are audited in the
  same transaction; audit failure rolls back the reversal or deletion.
- Marketing notification records a tracking event and best-effort audit, with
  verified-payment balance checks and the existing Indonesian messages.
- Pickup confirmation and delivery completion require all packages scanned,
  assigned courier/supervisor access and proof photos. B2B pickup retains its
  invoice gate. Pickup stays PICKED_UP until warehouse reception; delivery moves
  the shipment to DELIVERED. Legacy payment payloads are ignored. DIRECT tracking
  uses the latest recorded checkpoint on the selected transport.

Go mutations lock shipment before task/expense, including notification and payment
recording. Pickup task, shipment and tracking now commit atomically. Shipment scope
is enforced for pickup confirmation and marketing notifications, consistent with
the existing Go task/warehouse routes. Expense code allocation uses the existing
transaction advisory-lock pattern. Payment recording remains serializable;
serialization failures use the existing Go retryable 409 envelope.

Evidence processing uses golang.org/x/image with Go JPEG/PNG decoders: actual-image
validation, 20-million-pixel limit, capture metadata/freshness/GPS validation,
EXIF orientation, resize to 1600px, JPEG storage cap, tenant/company branding,
Indonesian capture date/time/zone, GPS/accuracy, address, origin/destination,
assigned courier/operator, mock-location warning and By NovaLogistik footer.
Planned task coordinates are never treated as capture coordinates; without GPS,
the validated original photo is retained. The optional address fallback honors
NOMINATIM_URL and a 3-second context deadline with process-local throttling/cache.
Go renders the same information/layout with bundled Go fonts and flat panels;
glyphs, rounded corners, gradients and JPEG bytes are not pixel-identical to Sharp.
Browser/visual signoff remains a staging gate. No Node or system font dependency.

Regression tests exercise permissions, warehouse scope, forged actor/status fields,
balances, B2B gates, expense lifecycle and durable reversal reasons, injected
rollback, concurrent expense allocation/approval and delivery completion, scan
requirements, proof validation/metadata/stamping, EXIF orientation and address
fallback/cache. Two independent tenant databases exercise financial isolation.
The selected seven legacy suites passed (143 tests). Full legacy response fixtures,
mixed Node/Go contention, production-sized images/load, browser/proxy/E2E checks
and staging cutover remain acceptance work. No live database/schema or frontend
routing changes are included.

Validation: full WSL race/disposable-PostgreSQL suite, inventory registration,
vet, Linux build and golangci-lint passed (zero lint issues). The watermark was
visually checked with synthetic data, and Graphify was refreshed. The seven
selected legacy suites passed all 143 tests.

## Continuation — capacity change trends (2026-11-05)

GET /api/v1/capacity/trend is implemented (95/185 total). It requires vehicle.view
and reads only vehicle audit logs from the request's tenant. It preserves the
default 14-day window, 7–90-day clamp, fractional-day behavior, Indonesian labels,
zero-filled buckets, and JavaScript truthiness for capacityConfigChanged.
Malformed/null audit JSON is ignored. The legacy server-local midnight window and
UTC date keys are retained; Node and Go must use the same TZ during rollout.
In WIB, today's UTC audit dates can fall beyond the last bucket, matching legacy.

Tests cover range bounds/defaults, fractional ranges, month boundaries, WIB date
keys, empty history, malformed and false-valued audits, inclusive start/excluded
older logs, unrelated entities, future logs outside buckets, authentication,
permissions, database failure and two-tenant isolation after bulk configuration.
No dedicated legacy trend test suite exists. Browser/proxy checks, complete legacy
response fixtures and staging rollout remain acceptance gates.

Validation: full WSL race/disposable-PostgreSQL suite, inventory registration,
vet, Linux build and golangci-lint passed (zero lint issues). Date-window fixtures
were checked against Node in UTC and Asia/Jakarta. Graphify was refreshed.

## Continuation — fleet capacity overview and bulk koli configuration (2026-11-05)

GET /api/v1/fleet-capacity and POST /api/v1/fleet-capacity/bulk-configure-koli are
implemented (94/185 total). The overview requires vehicle.view; bulk configuration
requires vehicle.update and owner status (including inherited Owner-role status).
Capacity remains informational and never blocks shipment or transport operations.
Capacity trend and transport routes remain planned. No schema, live database or
frontend routing changes are included.

The pure internal/capacity package ports independent weight/volume/koli dimensions,
legacy epsilon rounding, utilization, headroom, overage, unconfigured null fields,
and overall precedence (overlimit, warning, unconfigured/partial, OK). A positive
maximum that rounds to zero keeps its legacy status/null utilization behavior.

The overview includes all vehicles and combines PLANNED/DEPARTED transports only,
including empty active transports. It uses actual package weight, not billing
weight. SQL detail aggregates round per shipment before summing, preserve repeated
masters across active transports, and use dimensions only because the current legacy
detailAggregates SQL omits direct volumeM3. Idle load/capacity fields remain null;
summary counts and unconfiguredVehicles (maxKoli=null only) match legacy behavior.
Threshold reads use the existing Go tenant-local settings helper, without the legacy
process-global cache. No photos or private owner/account objects are returned.

Bulk configuration preserves weight-band suggestions, positive override rounding,
the suggested flag's null/absent semantics, configured-row exclusion, inactive vehicle
inclusion and one capacity-change audit per updated vehicle. Intentional changes:
rows lock in ID order and the batch commits atomically, so update/validation failures
roll back all earlier updates and concurrent Go actions cannot configure the same
row twice. Positive overrides beyond a PostgreSQL 32-bit integer return a field-level
422 instead of a database error. Audits remain best-effort after commit.

Tests cover dimension boundaries, custom thresholds, precision and overall status;
HTTP permissions/ownership, empty fleet, active/idle/terminal transports, repeated
masters, volume/weight parity, owner names, summary counts, threshold updates,
suggestion boundaries, override rounding/fallback, idempotency, audit payloads,
injected mid-batch rollback, concurrent bulk requests, integer overflow and two
independent tenants sharing vehicle identifiers. The selected legacy capacity
suite passed (29 tests).

Validation: the full WSL race and disposable-PostgreSQL suite, inventory check,
vet, Linux build and golangci-lint passed (zero lint issues). Graphify was refreshed.
Full legacy response fixtures, mixed Node/Go contention, browser/proxy checks and
staging cutover remain gates.

## Continuation — gudang workspace (2026-11-05)

GET /api/v1/gudang is implemented (92/185 total), matching the current kirimku
warehouse workspace. It requires shipment.view and uses the existing tenant and
warehouse scope helpers. The response retains scope, arrivals, transportArrivals,
walkIns and warehouses, including empty arrays and explicit nullable fields.
No frontend routing, schema or live database changes are included.

The port preserves several details specific to this legacy endpoint:

- Courier and walk-in queues use shipment scope (explicit IDs plus active city
  matches). Transport queues and physical inventory prioritize arrivedWarehouseId,
  then the relevant branch ID, then case-insensitive city matching. An inactive
  warehouse is omitted from warehouse tabs/names, while an explicit matching ID
  can still expose a transport queue row to its assigned staff.
- Successful and duplicate detail scan records both count, without deduplication
  or expansion from a B2B master scan. Pickup scans and the other arrival context
  do not inflate the queue count. Unknown methods count as TYPED.
- Verified payments alone reduce the discounted final price. Null prices retain
  null remaining amounts; overpayments clamp remaining amounts to zero. dpOk=true
  and unpaidCount=0 remain compatibility fields. This endpoint does not write
  payments or ledger entries.
- Totals use dimensions only because the legacy queries omit direct volumeM3.
  Package weight remains unrounded until the warehouse sum is rounded to two
  decimals. Recent pickup/transport associations and active courier names match
  legacy selection; no photos, capture metadata or account relations are returned.

Disposable-PostgreSQL regressions cover authentication/permissions, empty workspaces,
all queue/held lifecycle statuses, company-name fallback, verified/pending payments,
discounts, overpayments, totals/rounding, duplicate/context scan counts, latest
pickup/transport selection, nullable associations, warehouse/city precedence,
inactive warehouses, scoped and unbound staff, and independent tenant databases
with identical shipment identifiers.

Validation: the full WSL race and disposable-PostgreSQL suite, route inventory
check, vet, Linux build and golangci-lint passed (zero lint issues). Graphify was
refreshed. No dedicated legacy gudang endpoint test suite exists; the full legacy
suite was not rerun. Full legacy response fixtures, browser/proxy checks and staging
cutover remain acceptance work.

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
