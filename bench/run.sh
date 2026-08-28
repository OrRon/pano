#!/usr/bin/env bash
# Compare direct vs proxied throughput with oha (brew install oha).
# Usage: bench/run.sh [duration] [concurrency]
set -euo pipefail
DUR=${1:-8s}
CONC=${2:-64}
PROXY=${PANO_PROXY:-http://127.0.0.1:9091}
ORIGIN=127.0.0.1:18080
command -v oha >/dev/null || { echo "install oha: brew install oha"; exit 1; }
go build -o /tmp/pano-bench-origin ./bench/origin
/tmp/pano-bench-origin -addr "$ORIGIN" >/dev/null 2>&1 &
OPID=$!
trap 'kill $OPID 2>/dev/null' EXIT
sleep 0.5
run() { oha -z "$DUR" -c "$CONC" --no-tui "$@" 2>&1 | grep -E "Requests/sec|50.00%|99.00%|Success rate"; }
echo "== direct   http://$ORIGIN/small"; run "http://$ORIGIN/small"
echo "== via pano $PROXY (capture as configured)"; run -x "$PROXY" "http://$ORIGIN/small"
