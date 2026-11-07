# Pagination and performance review — 7 November 2026

## Scope and findings

Reviewed the Go API and React/Vite application, their shared data loaders, list
consumers, warehouse access rules, and Prisma migration history. The original
Next.js application remains the schema owner and migration reference; its API
has not been given the new pagination contract. These are code findings and
local measurements, not a production load-test or a guarantee of capacity.

The largest history screens loaded all matching rows, expanded their relations,
then filtered and rendered in the browser. Route modules were already loaded
with React.lazy; splitting them again would not fix the database or payload cost.

## Implemented

- Shipments, transports, pickups, deliveries and invoices now request 25 rows.
  The API validates page/pageSize and caps a page at 100 rows.
- Search, status, customer type, fulfillment mode, courier/crew assignment and
  warehouse filters run in SQL before IDs are selected for a page. Stable order
  is createdAt DESC, id DESC, including when timestamps tie.
- A materialized ID page precedes expensive JSON relations and package totals.
  Package and tracking loads for shipments use only that page's shipment IDs.
- Counts and status summaries use the entire filtered, authorized result set,
  not the visible page. Summary cards now describe the active filters.
- Changing filters resets the page; deleting the last row clamps the request to
  the final available page. Empty results have page=1, pages=1, total=0.
- Search waits 300 ms. Obsolete browser requests are aborted and stale responses
  cannot replace newer results. Loading/error states hide obsolete row actions;
  errors offer retry instead of displaying a misleading empty result.
- Options supports an allowlisted include parameter. The five list screens
  fetch the option groups they use; shipment/invoice form groups and pickup/
  delivery shipment choices load when their dialogs open. Shipment history no
  longer eagerly fetches the entire warehouse scan workspace.
- Transport CSV exports request all filtered pages in batches of 100 on demand.
  A changing total or a clamped page aborts the export with a retry message.
- The measured package lookup lacks a foreign-key index. Migration
  20261107090000_package_shipment_lookup_index adds DetailShipment(masterId)
  concurrently, and schema.prisma records the index.

No dependencies, cache, schema denormalization or background services were added.

## API contract

```
GET /api/v1/shipments?page=1&pageSize=25&status=CREATED&search=Sender&warehouseId=10
```

The five routes continue to return an array in data. When page or pageSize is
present, meta contains page, pageSize, pages, total, statuses and unpriced.
The latter is meaningful for shipments. page must be 1–1,000,000; pageSize must
be 1–100; defaults are 1 and 25. Invalid values return 422. warehouseId must be
a positive PostgreSQL integer on the four warehouse-aware lists. It narrows
the actor's access; it cannot grant access to another warehouse.

Requests omitting both pagination parameters retain their full-array contract
for existing integrations and assignment pickers. Legacy unpaginated transport
history still overrides status, while paginated history honors status, matching
the screen's previous browser filtering.

```
GET /api/v1/options?include=employees,warehouses
```

Omitting include returns the original full options object. Unknown groups return
422. company remains available on selective requests.

## Measured baseline

TestPaginationQueryPlanPostgres uses a disposable PostgreSQL database with
1,000 shipments and 5,000 packages, ANALYZE and EXPLAIN (ANALYZE, BUFFERS).
The representative query builds shipment/package JSON. It illustrates the
relation-expansion cost; it is not an end-to-end API latency benchmark.

The final local run measured each change separately:

| Representative query | Execution time | Package aggregate loops | Shared buffer hits |
| --- | ---: | ---: | ---: |
| Unbounded baseline | 328.706 ms | 1,000 | 57,019 |
| Page of 25, before index | 8.393 ms | 25 | 1,463 |
| Page of 25, with package index | 2.675 ms | 25 | 213 |

The test also verifies the migrated index is valid, temporarily drops it only
inside its disposable database to reproduce the baseline, then compares paging
alone with paging plus the index. Timing varies by machine and cache state;
there are no flaky timing thresholds. The indexed plan uses a bitmap index scan
on DetailShipment_masterId_idx. Full plans are emitted with go test -v; the
measured plans are saved in [performance-plans.txt](performance-plans.txt).

## Remaining bottlenecks, ordered by likely growth risk

| Area | Evidence in current code | Next change when this area grows |
| --- | --- | --- |
| Warehouse workspace | gudang.go loads active shipments, packages and scans for the tenant before building several queues; nested pickup/transport lookups per shipment | Separate queue requests with their own filters/pages; keep warehouse inventory totals separate |
| Financial histories | wallet.go topups/withdrawals, repairs.go, return_tasks.go and commercial list handlers still return arrays; partners.go computes wallet/ledger/count relations per partner | Extend pagination per consumer; compute money totals over all authorized matches before limiting rows |
| Existing hard caps | Wallet transactions and vehicle-owner transport history use LIMIT 200 | Replace silent truncation with navigable pages; preserve earnings and ledger totals independently |
| Dashboard | dashboard.go loads shipments for the selected date period and active trips for counts, scope and capacity calculations | SQL aggregates for summary cards, bounded recent lists, date-range limits and query-plan measurements |
| Master data and pickers | Customers/vehicles/routes/tariffs/access lists remain full arrays; lazy dialogs still fetch all eligible choices | Searchable paged selectors, keeping a separately fetched selected record for editing |
| Options outside the five screens | include is opt-in; older consumers still request every option group and the B2B eligible shipment list | Migrate each consumer to include, then paginate large customer/shipment choices |
| Large individual records | Invoice lists still expand lines/settlements for up to 25 invoices; shipment/transport/delivery detail responses can contain many packages or evidence images | Aggregate-only list projections; package pagination and on-demand evidence at detail level, preserving print/scan completeness |
| Deep pages and broad search | Exact counts and OFFSET still scan matching IDs; substring search and warehouse-city expressions are not ordinary B-tree lookups | Measure representative tenant plans; add proven filter/order indexes or cursor navigation before very deep histories |
| JavaScript | Production build still contains a substantial main bundle and chart-heavy dashboard; lazy pages already split | Profile startup/download cost, then defer individual expensive charts/camera/print features |

Finance operational-expense reports and audit logs already have database limits/
pagination. Fleet capacity already batches package aggregates rather than issuing
one database request per vehicle, although its response grows with the fleet.
Counts and aggregate reports must never be calculated from one page of rows.

## Deployment and validation

1. Apply the Prisma index migration through the usual schema deployment workflow.
   CREATE INDEX CONCURRENTLY must run outside a transaction. It permits ordinary
   writes but still consumes I/O; schedule according to the tenant's traffic.
2. Verify pg_index.indisvalid for DetailShipment_masterId_idx, and ANALYZE
   DetailShipment after deployment. If concurrent creation fails, inspect/remove
   the invalid index and resolve the failed migration before retrying.
3. Deploy the Go backend before the frontend. The new frontend requires page
   metadata and displays an explicit backend-update error if it is absent.
4. Check owner, warehouse, marketing and assigned courier/crew access in staging;
   search across page boundaries, change tabs, delete a final-page record, and
   export a result containing more than 100 trips.
5. Compare per-route latency percentiles, response bytes, timeouts, pool wait and
   slow-query plans on representative data. This change did not access production
   data or change live database indexes, pool sizes or timeout settings.

Automated coverage includes all five page contracts, validation, full page traversal,
warehouse membership versus existing output, assignment restrictions, direct-manager
access, legacy history compatibility, query plans, cancellation, debouncing, filter
resets (including revisiting a previous filter), retry, and a 101-row CSV export.

Validation completed: 328 frontend tests, frontend production build/typecheck,
Prisma schema validation, and backend race/integration tests, go vet, binary build,
process start/restart/SIGTERM smoke checks and golangci-lint (zero issues).

Pagination bounds parent rows, not the number of packages on a single shipment.
Offset pages are not a cross-request snapshot: concurrent inserts/deletes can shift
rows. CSV checks detect changed totals, but not every concurrent edit. Browser CSV
generation retains the CSV text in memory; very large exports need a server stream.
