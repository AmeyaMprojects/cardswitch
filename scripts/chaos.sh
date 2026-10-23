#!/usr/bin/env bash
# Failure-injection helpers for CardSwitch.
set -euo pipefail

case "${1:-}" in
  issuer-crash)
    docker compose kill issuer-sim
    echo "issuer-sim killed; expect breaker to open and reversals on in-flight auths" ;;
  issuer-restore)
    docker compose up -d issuer-sim ;;
  issuer-slow)
    ISSUER_LATENCY_MS="${2:-1500}" docker compose up -d --force-recreate issuer-sim
    echo "issuer latency set to ${2:-1500}ms (switch timeout is 800ms)" ;;
  issuer-flaky)
    ISSUER_FAIL_RATE="${2:-0.3}" docker compose up -d --force-recreate issuer-sim ;;
  check-ledger)
    docker compose exec -T postgres psql -U cardswitch -c \
      "SELECT idempotency_key, COUNT(*) FROM ledger_transactions GROUP BY 1 HAVING COUNT(*) > 1;" ;;
  *)
    echo "usage: $0 {issuer-crash|issuer-restore|issuer-slow [ms]|issuer-flaky [rate]|check-ledger}"; exit 1 ;;
esac
