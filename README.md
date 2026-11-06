# NovaLogistik Go backend

Incremental migration from `../kirimku` to Go + Fiber v3. **185 of 185 legacy HTTP
handlers are implemented**: the greeting, identity/access management, master-data
routes, mobile capability discovery, shipment CRUD, package CRUD/batches,
pricing, documents, internal tracking, print logging, pickup submission,
cancellation, arrival scans/reception/undo, phone-verified public tracking, and
pickup list/detail, assignment, editing, cancellation, package scans and confirmation;
delivery list/detail, assignment, editing, deletion, package scans and completion;
shipment payment history/recording, operational expenses and marketing notifications;
and the gudang
workspace with arrival queues, walk-ins and held inventory; fleet capacity
overview, capacity change trends and owner-only bulk koli configuration;
transport planning, execution, checkpoint/activity evidence, multi-drop approval,
operational expenses/reversals, return tasks and atomic vehicle-owner settlement;
payment verification/rejection, the warehouse-scoped B2C unpaid list, and partner
administration with wallet summaries, profit sharing and account management; B2B invoices,
commission release/reversal, wallet ledger/history/adjustments, top-up verification,
withdrawal reservations and processing, repair deductions/refunds/history, partner
self-service dashboards/vehicles/transports, company finance reports, the
main/driver/kurir dashboards and warehouse-scoped audit-log queries.
[The inventory](docs/route-inventory.csv) records every handler. Frontend routing
defaults to Next.js; opt in with the build-time `GO_API_ORIGIN` setting only after
the [cutover gates](docs/cutover.md) pass. Production traffic has not switched.

## Run

Requires Go 1.27.3 or later (the initialized and tested toolchain). Fiber is pinned in
`go.mod` and `go.sum`; see the [official Fiber documentation](https://docs.gofiber.io/).
From `backend/` (WSL):

```bash
go mod download
go run ./cmd/api
```

Defaults: `HOST=127.0.0.1`, `PORT=8080`, `SHUTDOWN_TIMEOUT=10s`.
Set process environment variables to override them. `.env.example` documents settings;
the executable does not automatically load `.env` files. Use `HOST=0.0.0.0` for a
container listener. Interrupt/SIGTERM initiates bounded graceful shutdown.

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/api` | Preserves the original `{"message":"Hello, world!"}` response |
| GET | `/health/live` | New process liveness endpoint: `{"data":{"status":"ok"}}` |

Liveness does not test PostgreSQL or licensing. Business routes activate when
`DATABASE_URL` or `LICENSE_API_URL` is configured. Without either, only foundation
routes run. Unknown/unmigrated routes return JSON 404; there is no legacy proxy.

Implemented `/api/v1` modules: `auth/login`, `auth/logout`, `auth/me`, `profile`,
`users`, `roles` (including permissions), `employees`, `customers`, `warehouses`,
`vehicles`, `routes` (including checkpoints), `bank-accounts`, `tariffs`, `settings`,
`options`, `mobile/config`, shipment CRUD/details/batches, pricing, documents,
internal tracking, print logs, ready/cancel actions, arrival scan progress/single/bulk
scans, origin/destination receipt and undo, phone-verified public tracking, pickup
CRUD/cancellation/scans, delivery CRUD/scans, the gudang workspace, and fleet
capacity overview/bulk koli configuration and capacity change trends, transport
CRUD/lifecycle/activities/check-ins/drops/expenses/settlement, return tasks,
payment verification/rejection, unpaid B2C shipments, and partner listing/settings/
account profile editing/disabling, invoices/lines/settlements/print/send, commissions,
wallet/topups/withdrawals, repairs/logs/vehicle choices, partner dashboard/vehicles/
transports, and finance summary/operational-expense reports.
All phase 1–8 inventory rows are implemented, including dashboards, audit logs, pickup confirmation and delivery
completion with evidence photos, shipment payments/expenses and marketing notices.
The inventory records exact method/path pairs.

Pickup/delivery evidence processing runs in Go with bundled fonts. GPS photos
receive the company/capture watermark; validated photos without GPS are retained
unchanged. Optional address lookup uses `NOMINATIM_URL` (default OpenStreetMap
Nominatim) only when the device supplied no address, with a 3-second deadline.
See [the parity notes](docs/testing.md) for rendering differences and rollout gates.

Prisma remains schema owner. Apply its migration chain through the normal deployment
workflow before using business endpoints. Migration
`20261104120000_capacity_settings_schema_parity` supplies `Vehicle.maxKoli` and
`SystemSetting`, already present in schema.prisma but missing from SQL history.
The Go server never migrates or seeds databases or bootstraps RBAC; use the existing
operator provisioning workflow for accounts and the permission catalog.

Authentication preserves Node scrypt hashes, SHA-256 session hashes, 12-hour bearer
sessions, owner-role inheritance and direct permissions. Development ignores corporate
headers; preview/production resolve them through the license service. Missing corporate
headers still select `DATABASE_URL`, matching legacy behavior. Licenses cache for one
minute with legacy stale-on-upstream-error behavior. Pools are bounded and request-leased;
in-use pools are never evicted and shutdown closes the registry after HTTP drains.

## Deploy from GitHub to Vercel

This repository uses the [Fiber Vercel recipe](https://docs.gofiber.io/recipes/vercel/):
`api/index.go` adapts the existing application to `net/http`, and `vercel.json`
routes requests to it. The app and tenant pools are reused for warm invocations.
The explicit `public` output directory prevents project files from becoming static output.

1. Commit and push this setup to `mhdthariq/backend-logistik`.
2. In Vercel, choose **Add New > Project**, import that GitHub repository, and
   leave **Root Directory** at the repository root (`.`). Choose **Other** as
   the framework; the checked-in configuration supplies routing and output settings.
   Leave build/install commands at their repository defaults.
3. Add these **server-side Environment Variables** for Production and Preview,
   copying the four license values from your local `.env`:

   | Variable | Value |
   | --- | --- |
   | `NODE_ENV` | `production` |
   | `PREVIEW_MODE` | `false` |
   | `LICENSE_API_URL` | Your license-header endpoint |
   | `LICENSE_API_KEY` | Your existing Key value |
   | `LICENSE_API_TOKEN` | Your existing Token value |
   | `LICENSE_APLIKASI` | Your registered application value |
   | `NEXT_PUBLIC_COMPANY_NAME` | `NovaLogistik` (or your company name) |
   | `TENANT_MAX_POOLS` | `4` initially |
   | `TENANT_MAX_CONNECTIONS` | `2` initially |

   Vercel does not import your ignored local `.env`. Do not prefix license secrets
   with `NEXT_PUBLIC_`. `DATABASE_URL` is optional and selects the fallback tenant
   for requests without `x-corp-id`; omit it if all business requests supply a
   Corporate ID. Do not copy `NODE_ENV=development` to Vercel: that mode bypasses
   license lookup. When `NODE_ENV` is absent, Vercel defaults to production mode.
4. Deploy. Verify `https://YOUR-BACKEND.vercel.app/health/live` and `/api`.
   Check license routing with a request containing `x-corp-id: RJL2507`, for example:

   ```bash
   curl -i https://YOUR-BACKEND.vercel.app/api/v1/auth/me -H 'x-corp-id: RJL2507'
   ```

   Without a bearer token, an active configured tenant returns 401. License errors
   identify lookup/configuration failures; this request alone does not prove a
   database connection. Complete a normal login to check database/authentication access.
5. To connect the existing Next.js frontend, set its build-time `GO_API_ORIGIN`
   to `https://YOUR-BACKEND.vercel.app` and redeploy it after the cutover gates pass.
   Its existing same-origin proxy forwards corporate and authorization headers.

Vercel must be able to reach the license service and each tenant's PostgreSQL host.
Apply the existing Prisma migrations separately before serving business traffic.
Pool limits apply **per function instance**, so size database connection limits for
multiple instances. Vercel's request/response payload limits also apply to evidence
photo uploads, even though Fiber accepts up to 16 MiB locally.

## Verify

```bash
bash scripts/test-wsl.sh
go tool cover -func=coverage.out
```

The script starts temporary loopback-only PostgreSQL, creates unique test databases,
applies the actual Prisma SQL history, runs race tests, vet, build and lint, and stops
PostgreSQL even on failure. It also tests binary restart and SIGTERM shutdown.
It never reads `.env`. See [testing](docs/testing.md)
for workspace-local tools and remaining acceptance gates.

## Layout

```text
cmd/api/             process startup and graceful shutdown
internal/config/     validated environment and tenant settings
internal/httpapi/    Fiber handlers, contracts and PostgreSQL regressions
internal/identity/   Node-compatible password/session primitives
internal/persistence/ pgx adapters, transactions, UTC serialization
internal/tenant/     license lookup and bounded tenant pool registry
docs/                migration plan, endpoint inventory, test strategy
scripts/             inventory regeneration
```

Tests live beside Go packages using `_test.go`. SQL identifiers and update columns
come only from application-defined maps; all client values use query parameters.
