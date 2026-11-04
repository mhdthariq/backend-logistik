# Backend test strategy

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
