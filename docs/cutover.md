# Go API cutover and rollback

All 185 explicit legacy method/path pairs are registered in Go. Registration and
passing synthetic regressions do not establish full response parity or authorize
production cutover. Prisma remains the schema owner; keep the legacy service and
its migrations available throughout the rollback window.

## Prepare a staging release

1. Apply the reviewed Prisma migration chain using the normal operator workflow.
   Use an isolated staging tenant, never a production database for write tests.
2. Build from `backend/`: `go build -o bin/api-linux ./cmd/api` on Linux/WSL.
   Run the binary under the existing process supervisor with automatic restart
   and SIGTERM shutdown. Load environment variables through that supervisor;
   Go does not load `.env` automatically. Use `backend/.env.example` as the field
   reference, set `NODE_ENV=production`, and configure matching license settings
   for both services. Bind Go to loopback or a private service network.
3. Verify `/health/live`, then verify an authenticated business request. Liveness
   alone does not check database connectivity, migrations, licensing, or RBAC.
4. Set **server-only** `GO_API_ORIGIN=http://127.0.0.1:8080` for the frontend build
   (use the private service hostname if Go runs elsewhere). Build/deploy Next.js
   normally. Rewrites are captured at build time in a standalone deployment.
   Leave the variable blank for the legacy API. No browser API URL changes.

`beforeFiles` rewrites override the existing `/api` and `/api/v1/*` handlers for
every HTTP method. Query strings, request bodies, bearer tokens and corporate
headers travel through the same-origin proxy. An unavailable Go upstream must
fail; do not automatically retry mutations against Next.js. `/uploads/*`, pages,
tracking UI and print UI remain served by Next.js. Preserve its existing upload
storage, old file URLs and CSP/media settings. For selective method/path rollout,
use an upstream reverse proxy with the inventory as its ownership manifest;
`GO_API_ORIGIN` is the complete-API switch, not a per-phase switch.

## Acceptance gates before production

- Run `bash scripts/test-wsl.sh`, frontend routing tests and relevant UI tests.
  Compare decoded legacy/Go JSON against the same restored synthetic snapshot;
  never replay a mutation twice on a shared database. Check every inventory row,
  including error/null/meta/date/number contracts and implicit HEAD/OPTIONS.
- Exercise owner, inherited owner, office admin, warehouse admin/staff, marketing,
  vehicle owner, driver, kenek and kurir through the browser. Use two independent
  tenant databases with overlapping IDs; verify tokens cannot cross tenants.
- Through the deployed proxy, verify login/logout, filters/query strings, dynamic
  IDs, malformed bodies, request size limits, photo capture/upload/old photo URLs,
  public phone-verified tracking, resi/invoice printing and print logging.
- Reconcile read-only dashboard/finance totals. Test same-runtime concurrency,
  then mixed legacy/Go writers if traffic will overlap. Legacy unlocked writers
  can bypass Go's wallet/settlement locking; switch connected write workflows as
  one unit. Measure dashboard/audit latency and database pool saturation using a
  representative restored dataset and expected concurrency, not just empty data.
- Exercise supervisor restart and SIGTERM with requests in flight; confirm clean
  drain and tenant-pool release. Verify backup restoration into an isolated
  database, then run the routing rollback rehearsal below.

## Rollback rehearsal

1. Retain the previous frontend artifact built with `GO_API_ORIGIN` blank. Keep
   its runtime database/license configuration and upload storage ready.
2. Drain API writes at the ingress, switch to that artifact (or revert the
   reverse-proxy ownership rules), and resume traffic on Next.js. This avoids
   in-flight writes overlapping between runtimes. Do not undo schema migrations
   or restore an old database over new writes merely to roll back routing.
3. Verify login, one authenticated read, a synthetic operational write, finance
   reconciliation, old upload URLs and public tracking. Monitor errors, latency,
   ledger consistency and duplicate business references before ending the window.

Record deployment IDs, backup/restore evidence, role/tenant results, load numbers
and rollback results with the staging release. Production routing remains an
explicit operator cutover after these gates pass.
