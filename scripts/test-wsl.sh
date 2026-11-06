#!/usr/bin/env bash
# Workspace-local Go/PostgreSQL; does not read application .env or use live data.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/.tools/pg/root/usr/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
pgbin="$PWD/.tools/pg/root/usr/lib/postgresql/18/bin"
pgshare="$PWD/.tools/pg/root/usr/share/postgresql/18"
if [[ ! -x "$pgbin/initdb" ]] && command -v pg_config >/dev/null; then
  pgbin=$(pg_config --bindir)
  pgshare=$(pg_config --sharedir)
fi
if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  test_data_dir=$(mktemp -d /tmp/novalogistik-go-test.XXXXXX)
  "$pgbin/initdb" -D "$test_data_dir/data" -L "$pgshare" --no-locale --encoding=UTF8 --auth=trust > "$test_data_dir/init.log"
  # Port 0 is not supported by PostgreSQL; use an explicitly configurable test port.
  test_port=${TEST_POSTGRES_PORT:-55439}
  "$pgbin/pg_ctl" -D "$test_data_dir/data" -l "$test_data_dir/server.log" -o "-h 127.0.0.1 -p $test_port -k $test_data_dir -c jit=off" -w start
  trap '"$pgbin/pg_ctl" -D "$test_data_dir/data" -m fast -w stop' EXIT
  export TEST_DATABASE_URL="postgresql://$(id -un)@127.0.0.1:$test_port/postgres?sslmode=disable"
fi
mkdir -p .tools
go test -race ./... -count=1 -coverpkg=novalogistik/backend/... -coverprofile=.tools/coverage-all.out
# Scope the report to this module if dependency counters are also emitted.
awk 'NR == 1 || index($0, "novalogistik/backend/") == 1' .tools/coverage-all.out > coverage.out
go vet ./...
go build -o bin/api-linux ./cmd/api
bash scripts/smoke-process.sh
if command -v golangci-lint >/dev/null; then
  golangci-lint run
else
  echo 'golangci-lint unavailable; lint gate remains open.' >&2
  exit 1
fi
