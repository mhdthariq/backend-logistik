# Incremental migration: Kirimku to Go + Fiber

## Scope and current status

Requested target: `backend/`, using Go + Fiber, migrated in reviewable phases with
tests. The continuations implement identity/access, master-data routes, mobile
capability discovery, shipment editing/pricing/documents, ready/cancel actions,
arrival scans/reception/undo, phone-verified public tracking, shipment payments,
expenses and marketing notification, pickup/delivery management/scans/completion,
the gudang workspace, and fleet capacity overview/trends/bulk koli configuration.
No production cutover,
frontend move, live database mutation,
schema reset or demo seeding is included. An additive Prisma migration reconciles
two missing schema items; only disposable tests apply it here.

Source of truth is the code under `../../kirimku/`, not historical API examples.
The refreshed audit finds 185 exported handlers: 104 implemented and 81 planned.
Phase 2 has 18 route implementations, Phase 3 has 33, and Phase 8 additionally
has mobile/config. Phase 4 has 33 implementations: shipment CRUD, package CRUD and
batch changes, pricing, documents, internal tracking, print logging, pickup submission,
cancellation, arrival scan progress/single/bulk scans, origin/destination reception,
undo-arrival, phone-verified public tracking, marketing notifications, shipment
payment history/recording and operational expense lifecycle. Their remaining
acceptance work is documented in testing.md. Phase 5 has fifteen implementations:
delivery list/detail, assignment, editing, deletion and scans, plus pickup
list/detail, creation, reassignment/editing, cancellation and scans, pickup
confirmation, delivery completion with evidence-photo processing, and the gudang
workspace (courier/transport arrivals, walk-ins and held inventory). All inventory
rows in phases 1–5 are implemented; acceptance/cutover gates remain open.
Phase 6 has three implementations: capacity trend, fleet capacity overview and
owner-only bulk koli configuration. Transport and the remaining
finance handlers remain planned.
The exact methods, paths, source
files, assigned phases, and status are in [route-inventory.csv](route-inventory.csv).
Regenerate after source changes with:

```powershell
pwsh -File scripts/update-route-inventory.ps1
```

Inventory covers explicit method exports in `src/app/api/**/route.ts`. Dynamic
segments retain Next.js `[id]` notation; translate them to Fiber `:id` on migration.
Implicit framework HEAD/OPTIONS behavior and static uploads require separate parity
checks. The new `/health/live` endpoint is not a legacy route.

## Compatibility decisions

- Keep `/api/v1` paths, method semantics, status codes, camelCase keys, null fields,
  pagination metadata, ISO date strings, Indonesian messages, and number behavior.
  Tests must compare decoded JSON, not object key order.
- Success is `{data, meta?}`. Errors are `{message, errors?, code?}` from
  `src/composition/api-helpers.ts`; the README's nested error example is inaccurate.
  `/api` is a separate legacy greeting without the success envelope.
- Preserve PostgreSQL tables and Prisma-created column names, constraints and IDs.
  Prisma remains schema/migration owner until an explicit ownership handover. Use
  `pgx/v5` repositories with explicit SQL once Phase 2 begins; no competing auto-migrate.
- Existing passwords are Node scrypt with a hex salt **passed as text**, and sessions
  are random bearer tokens stored as SHA-256 hex with a 12-hour expiry. Preserve them;
  do not replace sessions with JWTs. Test cross-runtime fixtures before auth cutover.
- Tenant resolution uses `x-corp-id`, license lookup and tenant connection URLs.
  Current development bypasses licensing; current missing-header behavior can use the
  default database even outside development. Capture and explicitly review that
  behavior in Phase 2 rather than silently changing it or copying a misleading comment.
- Permissions include Owner-role inheritance, user permissions and warehouse/partner
  scope. Login and subsequent session hydration have differing permission assembly;
  characterize both before agreeing on a consistent migration contract.
- Preserve transaction boundaries, audit records, sequence allocation, money rounding,
  scan idempotency and reversal behavior. Never compare mutation handlers by writing
  to the same database twice.

## Phases and acceptance gates

| Phase | Deliverable | Required tests and exit gate |
| --- | --- | --- |
| **1 — Foundation (implemented)** | Standalone Fiber module, validated HTTP config, graceful shutdown, legacy `GET /api`, liveness, JSON errors and panic recovery; plan and inventory | Table-driven config/HTTP tests, redacted 500s, panic recovery, success/meta/null envelopes, missing-route behavior; build and vet. Race/lint availability recorded separately. No business traffic switches. |
| **2 — Persistence, tenants, identity** | Request context, bounded tenant pool registry, license client with deadlines, pgx repositories, login/logout/me/profile, RBAC, users/roles/employees | Real disposable PostgreSQL with existing migrations; Node-to-Go scrypt and session fixtures, expiry/logout/inactive users, direct and inherited permissions, owner role, warehouse/partner identity. Two-tenant isolation under concurrency, license errors/timeouts/cache expiry, pool cleanup, no implicit seeding. Route-contract tests from legacy login suite. |
| **3 — Master data and pricing** | Customers, warehouses, vehicles, routes/checkpoints, tariffs, bank accounts, settings/options; pure pricing + repository port | Port pricing domain/application suites, tariff lifecycle and customer scope suites. B2B actual-weight vs B2C volumetric, simple kg, koli/cubic, direct volume, minimums and rounding including JS edge cases. PostgreSQL constraints, filters, pagination, roles and deletion conflicts. Options may need subresource slices while later modules remain legacy. |
| **4 — Shipments and public tracking** | Shipment CRUD/details, server pricing, documents, lifecycle, arrival/undo, tracking, print logs and notification orchestration | Port cancellation, delete/invoice, batch detail, arrival/undo, payment-related shipment and privacy suites. Test every allowed/denied transition, warehouse/marketing scope, public verification and redaction, proof/document limits, transaction rollback and concurrent sequence creation. Finance dependencies remain on legacy paths until Phase 7. |
| **5 — Pickup, warehouse, delivery** | Pickup assignment/confirmation/scans, gudang receiving and delivery completion | Port delivery completion and related domain/service suites. Courier ownership, duplicate scans, partial handover, wrong warehouse, missing proof, terminal state conflicts, payment prerequisites, rollback on audit/proof failure. Test GPS/photo payload behavior and package-by-package flow. |
| **6 — Transport and capacity** | Transport creation/departure/check-ins/arrival/completion, activities, expenses, drops, return tasks, fleet capacity | Port all transport route suites plus capacity/domain/business-rule tests. Completed transport regressions, warehouse staff scope, multi-drop, proof arrays, checkpoint order, undo-arrival invariants, expense approvals/corrections/reversal, capacity contention and rollback. Settlement writes remain legacy until Phase 7. |
| **7 — Finance and partners** | Payments/unpaid, invoices/lines/settlements, wallet/topups/withdrawals, commissions, partners and partner views, repairs, finance reports | Port invoice settlement/bank, shipment payment, customer income, operational expense report, repair lifecycle/finance, reversal and finance scope suites. Test duplicate/concurrent actions, exact rounding, partial payments, PPh/adjustments, wallet reservations, ledger conservation, rollback, append-only action logs and reconciliation totals. |
| **8 — Aggregates and cutover** | Dashboard variants, audit-log queries, full parity review, deployment and frontend routing | Full contract suite plus disposable-DB integration, UI/E2E smoke tests for every role, two-tenant scenarios, upload/print/tracking checks, load tests, race detector, restart/shutdown smoke. Reconcile read-only old/new reports, verify backup/restore and route rollback in staging before full cutover. |

Phases 4–7 contain cross-module dependencies: start by extracting shared Go use cases
and reviewing the complete transaction graph. HTTP route ownership does not mean
isolated table ownership. Migrate connected writes as a transaction-sized slice or
leave the entire action on Next.js; do not call legacy HTTP inside a Go SQL transaction.
Each phase may span multiple changes. Further implementation is authorized by the
user's continuation request; production routing remains a separate cutover step.

## Per-slice workflow

1. Select method/path pairs from the inventory and list their transitive helpers,
   schema tables, permissions, tenant context, side effects and existing tests.
2. Capture request/response fixtures from the real legacy handler on synthetic data.
   Add regressions for malformed/null JSON, unknown IDs, denied access, dependency
   errors and boundary numbers. Decide intentional fixes explicitly.
3. Implement pure rules and use cases, then explicit repository ports/adapters,
   then Fiber handlers. Pass `context.Context` through every blocking dependency.
   Do not retain Fiber request buffers or context beyond the request lifetime.
4. Run Go unit/HTTP tests and real PostgreSQL integration tests; keep the existing
   TypeScript suites and run affected legacy suites. Mocks alone cannot prove SQL parity.
5. Review fixtures and schema diff (normally empty), run lint/vet/build/race, then
   mark the specific method/path rows ready. Record validation and rollout notes.
6. In staging, explicitly route only approved method/path pairs to Go. Keep all other
   traffic on Next.js. Include query strings, auth and corporate headers, body limits,
   OPTIONS/CORS and dynamic-path precedence in routing tests. Use a same-origin proxy
   initially so browser storage/auth and the relative client URL need no changes.
7. Enable the approved slice after review. Roll back routing to Next.js if parity or
   reconciliation fails. Retire a legacy route only after its rollback window closes.

## Database and rollout constraints

Before Phase 2, provision a **separate disposable PostgreSQL database** and verify the
existing Prisma migration chain against it. Source docs report a historical local
database/provider mismatch; this change does not read credentials or repair that state.
Use synthetic fixtures, not production tenant data. No `db push --accept-data-loss`,
reset, schema rewrite or seed belongs in the Go startup path.

Sharing a database temporarily requires preserving IDs, defaults, UTC timestamps,
foreign keys, Decimal/Float behavior and update/audit semantics. Isolate integration
tests per database/schema; tenant tests need two independent databases. Every write
test must verify persisted side effects and rollback, not just status codes.

During phased deployment, keep a method/path ownership manifest and reconcile any
shared tables touched by both services. Shadow only safe reads with tenant isolation
and redacted logs. Defer Go schema ownership and removal of Prisma/Next.js backend
code until all relevant routes, jobs, file serving and operational checks have passed.
