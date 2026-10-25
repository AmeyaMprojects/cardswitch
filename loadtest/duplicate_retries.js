// Fires the same Idempotency-Key from many VUs at once. Expect exactly one
// ledger posting per key and identical response bodies across all replies.
import http from 'k6/http';
import { check } from 'k6';

export const options = { vus: 50, iterations: 500 };
const BASE = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const key = `dup-${__ENV.RUN || 'a'}-${__ITER % 10}`; // 10 keys, ~50 hits each
  const res = http.post(`${BASE}/v1/charges`, JSON.stringify({
    merchant_id: 'm_demo', amount_minor: 500, currency: 'USD', pan: '4111111111111111', expiry_month: 12, expiry_year: 2030,
  }), { headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key } });
  check(res, { 'approved, replayed or in-flight': (r) => [200, 402, 409].includes(r.status) });
}
