#!/usr/bin/env bash
# Install tools inside this workspace, without sudo or system service changes.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .tools/bin .tools/pg
go_version=1.27.3
if [[ ! -x .tools/go/bin/go ]]; then
  curl -fsSL "https://go.dev/dl/go${go_version}.linux-amd64.tar.gz" -o .tools/go.tar.gz
  # Verify against the checksum published in the official Go download catalog.
  curl -fsSL 'https://go.dev/dl/?mode=json&include=all' -o .tools/go-downloads.json
  python3 - "$go_version" <<'PY'
import hashlib, json, pathlib, sys
version = "go" + sys.argv[1]
catalog = json.loads(pathlib.Path(".tools/go-downloads.json").read_text())
release = next(item for item in catalog if item["version"] == version)
artifact = next(item for item in release["files"] if item["filename"] == version + ".linux-amd64.tar.gz")
actual = hashlib.file_digest(open(".tools/go.tar.gz", "rb"), "sha256").hexdigest()
if actual != artifact["sha256"]:
    raise SystemExit("Go download checksum mismatch")
PY
  tar -xzf .tools/go.tar.gz -C .tools
fi
export PATH="$PWD/.tools/go/bin:$PATH"
export GOBIN="$PWD/.tools/bin"
if [[ ! -x .tools/bin/golangci-lint ]]; then
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
fi
if [[ ! -x .tools/pg/root/usr/lib/postgresql/18/bin/postgres ]]; then
  (
    cd .tools/pg
    apt download postgresql-18 postgresql-client-18 libpq5 libnuma1 liburing2
    for package in *.deb; do dpkg-deb -x "$package" root; done
  )
fi
go mod download
echo 'WSL tools ready. Run bash scripts/test-wsl.sh.'
