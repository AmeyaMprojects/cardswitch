import http from 'k6/http';
import { check } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

export const options = {
  scenarios: {
    steady: { executor: 'constant-arrival-rate', rate: 500, timeUnit: '1s', duration: '2m', preAllocatedVUs: 100 },
  },
  thresholds: {
    http_req_duration: ['p(99)<250'],
    http_req_failed: ['rate<0.01'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const res = http.post(`${BASE}/v1/charges`, JSON.stringify({
    merchant_id: 'm_demo',
    amount_minor: 1999,
    currency: 'USD',
    pan: '4111111111111111',
    expiry_month: 12,
    expiry_year: 2030,
  }), { headers: { 'Content-Type': 'application/json', 'Idempotency-Key': uuidv4() } });
  check(res, { 'approved or declined': (r) => r.status === 200 || r.status === 402 });
}
