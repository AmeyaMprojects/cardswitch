#!/usr/bin/env bash
# End-to-end check of the running stack: charge, replay, key misuse, decline,
# settlement and reconciliation. Run after `make up`.
set -euo pipefail

GW=${GATEWAY_URL:-http://localhost:8080}
LEDGER=${LEDGER_URL:-http://localhost:8084}
SETTLE=${SETTLEMENT_URL:-http://localhost:8085}
M="m_smoke_$RANDOM"
pass() { printf '  \033[32mok\033[0m   %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }

charge() { # key amount -> "status body"
  curl -s -o /tmp/cs_body -w '%{http_code}' "$GW/v1/charges" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $1" \
    -d "{\"merchant_id\":\"$M\",\"amount_minor\":$2,\"currency\":\"USD\",\"pan\":\"4111111111111111\",\"expiry_month\":12,\"expiry_year\":2030}"
}

echo "merchant $M"
code=$(charge k1 1999); body=$(cat /tmp/cs_body)
[[ $code == 200 && $body == *'"APPROVED"'* ]] && pass "approve (200)" || fail "approve: $code $body"
[[ $body != *4111111111111111* ]] && pass "response carries token, not PAN" || fail "PAN leaked: $body"

code=$(charge k1 1999); [[ $code == 200 && $(cat /tmp/cs_body) == "$body" ]] && pass "replay returns identical body" || fail "replay: $code"
code=$(charge k1 2500); [[ $code == 422 ]] && pass "same key, different body (422)" || fail "key reuse: $code"
code=$(charge k2 99999999); [[ $code == 402 && $(cat /tmp/cs_body) == *'"51"'* ]] && pass "insufficient funds (402, 51)" || fail "decline: $code $(cat /tmp/cs_body)"
code=$(charge k3 500); [[ $code == 200 ]] && pass "second approval" || fail "second approval: $code"

bal=$(curl -s "$LEDGER/v1/accounts/merchant:$M:receivable/balance")
[[ $bal == *'"balance_minor":2499'* ]] && pass "ledger receivable = 2499" || fail "ledger balance: $bal"

sleep 3 # let settlement consume the approval events
curl -s -X POST "$SETTLE/v1/batches/close?force=true" > /dev/null
batches=$(curl -s "$SETTLE/v1/batches?merchant_id=$M")
[[ $batches == *'"total_minor":2499'* && $batches == *'"RECONCILED"'* ]] && pass "batch settled and reconciled" || fail "settlement: $batches"
cash=$(curl -s "$LEDGER/v1/accounts/merchant:$M:cash/balance")
[[ $cash == *'"balance_minor":2499'* ]] && pass "merchant cash = 2499 after settlement" || fail "cash: $cash"
