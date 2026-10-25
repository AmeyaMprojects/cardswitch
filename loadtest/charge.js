import http from 'k6/http';
import { check } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

// RATE (requests/s) and DURATION are overridable: k6 run -e RATE=200 -e DURATION=1m
export const options = {
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 500),
      timeUnit: '1s',
      duration: __ENV.DURATION || '2m',
      preAllocatedVUs: 200,
      maxVUs: 1000,
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<250'],
    http_req_failed: ['rate<0.01'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';

// A Luhn-valid test PAN on the 411111 BIN, different per call, so load
// spreads over many cards instead of draining one card's limit.
function randomPan() {
  const digits = '411111'.split('').map(Number);
  while (digits.length < 15) digits.push(Math.floor(Math.random() * 10));
  let sum = 0;
  for (let i = 0; i < 15; i++) {
    let d = digits[14 - i];
    if (i % 2 === 0) { d *= 2; if (d > 9) d -= 9; }
    sum += d;
  }
  digits.push((10 - (sum % 10)) % 10);
  return digits.join('');
}

export default function () {
  const res = http.post(`${BASE}/v1/charges`, JSON.stringify({
    merchant_id: `m_load_${__VU % 20}`,
    amount_minor: 1999,
    currency: 'USD',
    pan: randomPan(),
    expiry_month: 12,
    expiry_year: 2030,
  }), {
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': uuidv4() },
    responseCallback: http.expectedStatuses(200, 402),
  });
  check(res, { 'approved or declined': (r) => r.status === 200 || r.status === 402 });
}
