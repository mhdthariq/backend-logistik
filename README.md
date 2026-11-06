# NovaLogistik Go backend

Incremental migration from `../kirimku` to Go + Fiber v3. **141 of 185 legacy HTTP
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
administration with wallet summaries, profit sharing and account management.
The remaining 44 handlers are listed in
[the inventory](docs/route-inventory.csv). Frontend routing still points to Next.js;
implementation status does not mean production traffic has switched.

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
account profile editing/disabling.
All phase 1–6 inventory rows are implemented, including pickup confirmation and delivery
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

## Verify

```bash
bash scripts/test-wsl.sh
go tool cover -func=coverage.out
```

The script starts temporary loopback-only PostgreSQL, creates unique test databases,
applies the actual Prisma SQL history, runs race tests, vet, build and lint, and stops
PostgreSQL even on failure. It never reads `.env`. See [testing](docs/testing.md)
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
