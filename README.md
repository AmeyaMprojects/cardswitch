# CardSwitch

A card payments network you can run on a laptop. Six services do the jobs of a real card network: a merchant gateway, an authorization switch, an issuer simulator, a PAN tokenization vault, a double-entry ledger and a settlement engine. They talk over HTTP and Kafka and store state in PostgreSQL and Redis.

The goal is to cover the parts of payments that tend to break: duplicate charges under retries, issuers that time out halfway through a request, money that has to balance to the cent, and card numbers that should never reach most of the system.

> **Status:** early skeleton. Service layout, interfaces, the database schema, the circuit breaker, BIN routing, AES-GCM PAN encryption, load tests and chaos scripts are in place. Most request handlers return `501 Not Implemented` and are marked with `TODO`s. See [Roadmap](#roadmap).

**Stack:** Java 21 (Spring Boot 3), Go 1.22, Kafka (KRaft), PostgreSQL 16, Redis 7, Docker Compose, k6

---

## Table of contents

- [Architecture](#architecture)
- [Services](#services)
- [Request lifecycle](#request-lifecycle)
- [Design decisions](#design-decisions)
  - [Idempotent charging](#idempotent-charging)
  - [Double-entry ledger](#double-entry-ledger)
  - [Timeouts, retries, circuit breaking and reversals](#timeouts-retries-circuit-breaking-and-reversals)
  - [PAN tokenization](#pan-tokenization)
  - [Settlement and reconciliation](#settlement-and-reconciliation)
- [Data model](#data-model)
- [Kafka topics](#kafka-topics)
- [Getting started](#getting-started)
- [API](#api)
- [Failure testing](#failure-testing)
- [Load testing](#load-testing)
- [Repository layout](#repository-layout)
- [Roadmap](#roadmap)
- [Disclaimer](#disclaimer)

---

## Architecture

```
                   ┌──────────────┐
  Merchant ──────▶ │   Gateway    │ ── Idempotency-Key lock ──▶ Redis
  (HTTP)           │    (Go)      │
                   └──────┬───────┘
              tokenize PAN │            ┌────────────────────┐
                   ┌───────┴──────────▶ │ Tokenization Vault │ ──▶ PostgreSQL
                   │                    │  (Java, AES-GCM)   │     (vault_tokens)
                   │ token only         └────────────────────┘
                   ▼
           ┌──────────────────┐  BIN routing   ┌────────────────┐
           │  Auth Switch     │ ─────────────▶ │ Issuer Sim(s)  │
           │  (Go)            │ ◀───────────── │ (Go, faulty    │
           │  timeout/retry/  │   00/05/51     │  on request)   │
           │  circuit breaker │                └────────────────┘
           └──────┬────┬──────┘
     auth hold /  │    │ auth.approved / auth.captured / auth.reversed
     reversal     ▼    ▼
         ┌──────────┐  ┌───────────────┐
         │  Ledger  │  │     Kafka     │
         │  (Java)  │  └───────┬───────┘
         └────┬─────┘          ▼
              │        ┌───────────────┐
              ▼        │  Settlement   │ ── batch totals vs ledger ──▶ reconciliation report
         PostgreSQL ◀──│   (Java)      │
     (double-entry)    └───────────────┘
```

**Why the language split:** the gateway, switch and issuer are on the hot path, where latency and concurrency matter most, so they are written in Go. The vault, ledger and settlement services handle money and cryptography, where transaction handling, mature JDBC/Kafka libraries and a strong type system matter more, so they are written in Java with Spring Boot.

## Services

| Service | Lang | Port | Responsibility |
|---|---|---|---|
| `gateway` | Go | 8080 | Public merchant API. Validates requests, enforces `Idempotency-Key`, swaps PANs for tokens, forwards to the switch, stores and replays responses. |
| `auth-switch` | Go | 8081 | Routes authorizations to issuers by BIN (longest-prefix match). Wraps each issuer call with a timeout, bounded retries and a per-issuer circuit breaker. Sends a reversal when the outcome is unknown. |
| `issuer-sim` | Go | 8082 | Fake issuer that approves or declines with ISO 8583-style codes (`00`, `05`, `51`). Latency and failure rate are set with env vars for chaos testing. |
| `tokenization-vault` | Java | 8083 | Swaps PANs for opaque tokens. Stores PANs encrypted with AES-256-GCM, with the token bound as AAD. Only issuer-facing callers may detokenize. |
| `ledger` | Java | 8084 | Double-entry ledger. Every posting balances and is idempotent per key. Account balances come from the entries. |
| `settlement` | Java | 8085 | Consumes capture and reversal events from Kafka, builds per-merchant, per-currency batches at cutoff, and reconciles each batch against the ledger. |

Infrastructure: `postgres` (5432), `redis` (6379), `kafka` (9092, single-node KRaft).

## Request lifecycle

A successful charge, step by step:

1. **Merchant → Gateway**: `POST /v1/charges` with an `Idempotency-Key` header.
2. **Idempotency check**: the gateway runs `SET NX` in Redis on `idem:{merchant}:{key}`.
   - Key already finished: return the stored response unchanged.
   - Key still in progress: return `409 Conflict` so the client retries later.
   - Same key with a different request body (hash mismatch): return `422`.
3. **Tokenize**: if the request has a raw PAN, the gateway calls the vault and keeps only the token, BIN and last 4 digits. The PAN goes no further.
4. **Gateway → Switch**: the gateway forwards `{token, bin, amount, currency, charge_id}`.
5. **Routing**: the switch picks the issuer whose BIN prefix matches longest.
6. **Switch → Issuer**: the switch calls the issuer with an 800 ms timeout, through that issuer's circuit breaker, retrying up to 2 times with jittered backoff on transient errors.
7. **Ledger hold**: on approval (`00`), the switch posts an `AUTH_HOLD` transaction (debit `merchant:receivable`, credit `issuer:payable`) using `charge_id` as the idempotency key.
8. **Events**: the switch publishes `auth.approved`, and later `auth.captured`, to Kafka.
9. **Response**: the gateway stores the final response against the idempotency key and returns it.
10. **Settlement**: at cutoff, the settlement service closes the open batch for each merchant and currency, posts `SETTLEMENT` entries and runs reconciliation.

## Design decisions

### Idempotent charging

Retries are normal in payments. Clients time out, load balancers resend requests, and users press "Pay" twice. CardSwitch prevents duplicates at three layers, so a gap in one is caught by the next:

| Layer | Mechanism | Catches |
|---|---|---|
| Gateway (Redis) | `SET NX` with a TTL on `(merchant_id, idem_key)` | Requests that arrive at the same time with the same key |
| Gateway (Postgres) | `idempotency_keys` table with a stored `request_hash` and response | Retries that arrive after a crash or after the Redis TTL expires; key reuse with a different body |
| Ledger | `UNIQUE (idempotency_key)` on `ledger_transactions` plus `INSERT ... ON CONFLICT DO NOTHING` | Duplicate postings from switch retries or replayed Kafka messages |

`loadtest/duplicate_retries.js` sends 50 parallel requests for each of 10 keys. The pass condition is exactly 10 ledger transactions, checked with `scripts/chaos.sh check-ledger`.

### Double-entry ledger

Every movement of money is a `ledger_transaction` made of two or more `ledger_entries`, and the debits must equal the credits. The rule is enforced twice:

- In the application, `LedgerController` rejects unbalanced postings with `422`.
- In the database, a `DEFERRABLE INITIALLY DEFERRED` constraint trigger checks the balance at commit, so a buggy caller cannot commit a half-written transaction.

Balances are never stored as a mutable column. They are computed from the entries, and can later be cached in a projection. Amounts are always `BIGINT` minor units (cents), never floats.

Example postings for a $19.99 charge:

| Event | Debit | Credit |
|---|---|---|
| `AUTH_HOLD` | `merchant:m_1:receivable` 1999 | `issuer:issuer-a:payable` 1999 |
| `REVERSAL` | `issuer:issuer-a:payable` 1999 | `merchant:m_1:receivable` 1999 |
| `SETTLEMENT` | `merchant:m_1:cash` 1999 | `merchant:m_1:receivable` 1999 |

### Timeouts, retries, circuit breaking and reversals

The hardest case in card authorization is when **the issuer may have approved the charge but the switch never got the answer**. If the switch simply reports a decline, the cardholder can end up with a hold on their card for a charge the merchant believes failed.

The switch handles it like this:

- **Timeout**: every issuer call runs under `context.WithTimeout` (`ISSUER_TIMEOUT_MS`, default 800 ms).
- **Retries**: only for errors that are known to be safe to retry (connection refused, `503` before the request was accepted). There are at most `ISSUER_MAX_RETRIES` attempts, with exponential backoff and jitter.
- **Circuit breaker** (`internal/breaker`): one per issuer. After N consecutive failures it opens and fails fast for a cooldown period, then lets a single half-open probe through. This keeps a dead issuer from tying up switch goroutines.
- **Automatic reversal**: if the outcome is unknown (a timeout after the request was sent, or a broken connection mid-response), the switch publishes `auth.reversed`. It also sends a reversal to the issuer, retried until the issuer acknowledges it, and posts a `REVERSAL` to the ledger. The merchant gets a decline with a clear response code, and any hold the issuer placed is released.

### PAN tokenization

Only the gateway, briefly, and the vault ever handle a raw card number (PAN). Every other service sees only `token`, `bin` and `last4`.

- **Encryption at rest**: AES-256-GCM with a fresh 96-bit IV per record and a 128-bit tag. The stored layout is `iv || ciphertext || tag` (see `PanCipher.java`).
- **AAD binding**: the token is passed as additional authenticated data, so an attacker with write access to the database cannot move one card's ciphertext onto another token.
- **Deduplication**: `pan_hmac = HMAC-SHA256(pan)` with a separate key lets the vault return the existing token for a card it has seen before without decrypting anything.
- **Key versioning**: `key_version` on every row allows key rotation by re-encrypting in the background.
- **Access control**: `detokenize` is meant to be reachable only from the issuer-facing path (mTLS or a service allowlist). This is on the roadmap.

In development the master key comes from `VAULT_MASTER_KEY`. In production it would come from a KMS or HSM.

### Settlement and reconciliation

- The switch publishes `auth.captured` and `auth.reversed`. Messages are keyed by `merchant_id`, so all events for one merchant stay in order on the same partition.
- The settlement service adds each capture to the open `settlement_batch` for its `(merchant, currency, window)`. A reversal removes the charge from the open batch, or creates an adjustment line if the batch has already closed.
- At cutoff (a scheduled job), the batch closes and the `Reconciler` compares `SUM(settlement_lines)` with the ledger entries for the same `charge_id`s. It marks the batch `RECONCILED` or `MISMATCH` and lists any missing captures, orphan reversals or amount differences.

## Data model

The full schema is in [`infra/postgres/001_schema.sql`](infra/postgres/001_schema.sql).

```
vault_tokens ──┐
               ▼
idempotency_keys     charges ◀──── ledger_transactions ◀── ledger_entries ──▶ accounts
                       ▲
                       └────────── settlement_lines ──▶ settlement_batches
```

## Kafka topics

| Topic | Key | Producer | Consumer | Payload |
|---|---|---|---|---|
| `auth.approved` | `merchant_id` | auth-switch | (analytics) | charge_id, token, amount, issuer, auth_code |
| `auth.captured` | `merchant_id` | auth-switch | settlement | charge_id, amount, currency, captured_at |
| `auth.reversed` | `merchant_id` | auth-switch | settlement, ledger | charge_id, reason (`ISSUER_TIMEOUT`, `ISSUER_DOWN`, `MERCHANT_VOID`) |

## Getting started

**Requirements:** Docker with Compose v2. That is all: every service builds inside a container, so you do not need Go, Java or Maven installed locally.

```bash
git clone https://github.com/AmeyaMprojects/cardswitch.git
```

```bash
cd cardswitch && cp .env.example .env
```

Generate a development vault key and add it to `.env` as `VAULT_MASTER_KEY`:

```bash
openssl rand -base64 32
```

```bash
make up
```

Check that the services are healthy:

```bash
curl localhost:8080/healthz
```

The Java services expose `/actuator/health` on ports 8083–8085.

Stop everything and remove the volumes:

```bash
make down
```

## API

### `POST /v1/charges` (gateway)

```http
POST /v1/charges
Idempotency-Key: 7f9c2e1a-...
Content-Type: application/json

{
  "merchant_id": "m_demo",
  "amount_minor": 1999,
  "currency": "USD",
  "pan": "4111111111111111",
  "expiry_month": 12,
  "expiry_year": 2030
}
```

```json
{
  "charge_id": "b3c1...",
  "status": "APPROVED",
  "response_code": "00",
  "token": "tok_9QeX..."
}
```

| Status | Meaning |
|---|---|
| `200` | Approved, or a replay of a finished request with the same key |
| `402` | Declined by the issuer, or reversed after an unknown outcome |
| `409` | A request with the same key is still in progress |
| `422` | Same key sent with a different request body |
| `400` | `Idempotency-Key` missing or body malformed |

### Internal APIs

| Service | Endpoint |
|---|---|
| vault | `POST /v1/tokens`, `POST /v1/tokens/{token}/detokenize` |
| switch | `POST /v1/authorize` |
| issuer | `POST /v1/authorize` |
| ledger | `POST /v1/postings`, `GET /v1/accounts/{id}/balance` |

Test card numbers to use: `4111111111111111` routes to issuer-a, `5555555555554444` routes to issuer-b. Never use real card numbers.

## Failure testing

`scripts/chaos.sh` injects faults into the running stack:

```bash
./scripts/chaos.sh issuer-crash
```

```bash
./scripts/chaos.sh issuer-slow 1500
```

```bash
./scripts/chaos.sh issuer-flaky 0.3
```

```bash
./scripts/chaos.sh check-ledger
```

| Scenario | How | Expected |
|---|---|---|
| Issuer crash | `issuer-crash` while load is running | Breaker opens within N failures; in-flight auths are reversed; no orphan holds in the ledger |
| Network delay | `issuer-slow 1500` (above the 800 ms timeout) | Switch times out, sends reversals, merchant gets `402`; p99 stays bounded by the timeout |
| Flaky issuer | `issuer-flaky 0.3` | Retries absorb transient `503`s; every charge ends up with exactly one ledger transaction |
| Duplicate requests | `make dup-test` | One ledger transaction per key; every reply for a key is identical |
| Gateway restart mid-request | `docker compose restart gateway` during load | Postgres idempotency record lets a client retry get the original result |

## Load testing

The k6 scripts are in [`loadtest/`](loadtest/) and run through the `grafana/k6` Docker image, so you do not need to install k6.

```bash
make load
```

```bash
make dup-test
```

`charge.js` sends 500 requests per second at a constant rate for 2 minutes. It fails if p99 latency goes above 250 ms or more than 1% of requests error. Record results here once the handlers are implemented:

| Scenario | RPS | p50 | p95 | p99 | Error rate |
|---|---|---|---|---|---|
| Happy path | – | – | – | – | – |
| Issuer +1500 ms | – | – | – | – | – |
| Issuer 30% 5xx | – | – | – | – | – |

## Repository layout

```
cardswitch/
├── docker-compose.yml
├── Makefile
├── .env.example
├── infra/
│   └── postgres/001_schema.sql        # vault, idempotency, charges, ledger, settlement
├── services/
│   ├── gateway/                       # Go – merchant API, idempotency
│   ├── auth-switch/                   # Go – BIN routing, breaker, retries, reversals
│   │   └── internal/{routing,breaker}
│   ├── issuer-sim/                    # Go – configurable fake issuer
│   ├── tokenization-vault/            # Java – AES-GCM PAN vault
│   ├── ledger/                        # Java – double-entry postings
│   └── settlement/                    # Java – Kafka batches + reconciliation
├── loadtest/                          # k6 scripts
└── scripts/chaos.sh                   # failure injection
```

Run the Go unit tests without installing Go locally:

```bash
docker run --rm -v "$PWD/services/auth-switch":/s -w /s golang:1.22-alpine go test ./...
```

## Roadmap

- [x] Service layout, Dockerfiles, Compose stack
- [x] PostgreSQL schema with a balance-checking trigger
- [x] BIN longest-prefix routing with tests
- [x] Circuit breaker
- [x] Issuer simulator: per-token balances, ISO 8583 decline codes, idempotent replays, fault injection
- [x] AES-256-GCM PAN cipher with AAD binding
- [x] k6 load and duplicate-request scripts, chaos script
- [x] Gateway: Redis lock plus Postgres idempotency record, response replay, request-hash mismatch detection
- [x] Vault: tokenize, metadata lookup and allowlisted detokenize endpoints, HMAC dedupe, Luhn check
- [x] Switch: issuer client with per-attempt timeout, jittered exponential backoff, per-issuer circuit breaker
- [x] Switch: background reversals on unknown outcome (issuer-side idempotent, tombstones late auths), Kafka events keyed by merchant
- [x] Ledger: idempotent transactional postings, balance computed from entries
- [x] Settlement: Kafka consumers, windowed batches, scheduled close, ledger settlement posting, reconciliation
- [ ] Integration tests with Testcontainers
- [ ] Prometheus metrics and Grafana dashboard (latency, breaker state, reversal rate)
- [ ] mTLS between services; detokenize allowlist
- [ ] Recorded k6 results

## Disclaimer

This is a learning project. It is not PCI DSS compliant and must never handle real card data. Use only the published test card numbers.
