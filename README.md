# CardSwitch

A card payments network you can run on a laptop. Six services do the jobs of a real card network: a merchant gateway, an authorization switch, an issuer simulator, a PAN tokenization vault, a double-entry ledger and a settlement engine. They talk over HTTP and Kafka and store state in PostgreSQL and Redis.

The goal is to cover the parts of payments that tend to break: duplicate charges under retries, issuers that time out halfway through a request, money that has to balance to the cent, and card numbers that should never reach most of the system.

> **Status:** working end to end. `make up` starts all six services, `scripts/smoke.sh` runs a charge through tokenization, authorization, the ledger and settlement, and the [load](#load-testing) and [failure](#failure-testing) numbers below were measured on a laptop. See [Known limitations](#known-limitations) for what is deliberately simplified.

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
- [Known limitations](#known-limitations)
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
                  │    │ auth.approved / auth.reversed
                  │    ▼
   gateway posts  │  ┌───────────────┐
   AUTH_HOLD ──┐  │  │     Kafka     │
         ┌─────▼────┐└───────┬───────┘
         │  Ledger  │◀──┐    ▼
         │  (Java)  │   │ SETTLEMENT posting
         └────┬─────┘   │
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
| `settlement` | Java | 8085 | Consumes approval and reversal events from Kafka into per-merchant, per-currency, per-window batches. Closes due batches, posts the settlement to the ledger, and reconciles each batch against the ledger's holds. |

Infrastructure: `postgres` (5432), `redis` (compose network only), `kafka` (single-node KRaft, `apache/kafka`).

## Request lifecycle

A successful charge, step by step:

1. **Merchant → Gateway**: `POST /v1/charges` with an `Idempotency-Key` header.
2. **Idempotency check**: the gateway takes a Redis `SET NX` lock on `idem:{merchant}:{key}`, then inserts an `idempotency_keys` row in Postgres with a SHA-256 of the request body.
   - Key already finished: return the stored response, byte for byte, with `Idempotent-Replayed: true`.
   - Key still in progress: return `409 Conflict` so the client retries later.
   - Same key with a different request body (hash mismatch): return `422`.
3. **Tokenize**: if the request has a raw PAN, the gateway calls the vault and keeps only the token, BIN and last 4 digits. The PAN goes no further. The charge is stored as `PENDING`.
4. **Gateway → Switch**: the gateway forwards `{charge_id, merchant_id, token, bin, amount, currency}`.
5. **Routing**: the switch picks the issuer whose BIN prefix matches longest.
6. **Switch → Issuer**: the switch calls the issuer with an 800 ms timeout, through that issuer's circuit breaker, retrying up to 2 times with jittered backoff on transient errors.
7. **Events**: on approval the switch publishes `auth.approved` to Kafka; on an unknown outcome it publishes `auth.reversed` and reverses at the issuer.
8. **Ledger hold**: on approval (`00`), the gateway posts an `AUTH_HOLD` transaction (debit `merchant:receivable`, credit `issuer:payable`) with idempotency key `hold:{charge_id}`.
9. **Response**: the gateway stores the final response against the idempotency key and returns it.
10. **Settlement**: when a batch's window ends, the settlement service closes it, posts a `SETTLEMENT` (debit `merchant:cash`, credit `merchant:receivable`) keyed by batch ID, and reconciles it.

## Design decisions

### Idempotent charging

Retries are normal in payments. Clients time out, load balancers resend requests, and users press "Pay" twice. CardSwitch prevents duplicates at three layers, so a gap in one is caught by the next:

| Layer | Mechanism | Catches |
|---|---|---|
| Gateway (Redis) | `SET NX` with a TTL on `(merchant_id, idem_key)` | Requests that arrive at the same time with the same key |
| Gateway (Postgres) | `idempotency_keys` table with a stored `request_hash` and response | Retries that arrive after a crash or after the Redis TTL expires; key reuse with a different body |
| Ledger | `UNIQUE (idempotency_key)` on `ledger_transactions` plus `INSERT ... ON CONFLICT DO NOTHING` | Duplicate postings from gateway retries |

`loadtest/duplicate_retries.js` sends 50 parallel requests for each of 10 keys. Measured result: 500 requests produced exactly 10 charges and 10 ledger holds, and `scripts/chaos.sh check-ledger` found no charge with two holds and no unbalanced transaction.

If Redis is down, the gateway logs it and carries on with the Postgres record alone: slower under contention, still correct.

### Double-entry ledger

Every movement of money is a `ledger_transaction` made of two or more `ledger_entries`, and the debits must equal the credits. The rule is enforced twice:

- In the application, `Posting.validate()` rejects unbalanced postings, single entries, non-positive amounts and overflow with `422`.
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
- **Failure classes**: every failure is sorted by one question, *did the issuer process it?* Any failure while dialing (DNS, refused, unreachable) and `503` mean **no**. Timeouts, resets and other `5xx` mean **maybe**. A `4xx` means the issuer rejected the request.
- **Retries**: both "no" and "maybe" are retried, up to `ISSUER_MAX_RETRIES` extra attempts with full-jitter exponential backoff. Retrying "maybe" is safe because the issuer dedupes on `charge_id`. Once any attempt was "maybe", the final error stays "maybe", even if later attempts failed differently.
- **Circuit breaker** (`internal/breaker`): one per issuer. After `BREAKER_THRESHOLD` consecutive failures it opens and fails fast for `BREAKER_COOLDOWN_MS`, then lets a single half-open probe through. `4xx` rejections don't count against it.
- **Automatic reversal**: if the outcome is unknown, the merchant gets `91` immediately. A background goroutine, with its own 5-minute deadline, sends `POST /v1/reverse` to the issuer until it is acknowledged, skipping the breaker because releasing money must keep trying. The switch also publishes `auth.reversed`. The issuer releases the hold once, and if the reversal arrives before the authorization, it stores a decline so the late authorization can't take money.
- **Graceful shutdown**: on `SIGTERM` the switch stops taking requests, waits for in-flight reversals, and flushes Kafka.

### PAN tokenization

Only the gateway, briefly, and the vault ever handle a raw card number (PAN). Every other service sees only `token`, `bin` and `last4`.

- **Encryption at rest**: AES-256-GCM with a fresh 96-bit IV per record and a 128-bit tag. The stored layout is `iv || ciphertext || tag` (see `PanCipher.java`).
- **AAD binding**: the token is passed as additional authenticated data, so an attacker with write access to the database cannot move one card's ciphertext onto another token.
- **Deduplication**: `pan_hmac = HMAC-SHA256(pan)` with a separate key lets the vault return the existing token for a card it has seen before without decrypting anything.
- **Key versioning**: `key_version` on every row allows key rotation by re-encrypting in the background.
- **Validation**: PANs must be 12-19 digits and pass the Luhn check, or the vault returns `422`.
- **Access control**: `detokenize` is only allowed for services listed in `VAULT_DETOKENIZE_CALLERS`, identified by the `X-Caller-Service` header. That header is a stand-in for mTLS client identity.

In development the keys come from `VAULT_MASTER_KEY` and `VAULT_HMAC_KEY`; compose ships dev-only defaults so the stack starts without setup. In production they would come from a KMS or HSM.

### Settlement and reconciliation

- The switch publishes `auth.approved` and `auth.reversed`, keyed by `merchant_id` with a hash balancer, so all events for one merchant stay in order on one partition. Writes wait for all in-sync replicas.
- The settlement service adds each approval to the `OPEN` batch for its `(merchant, currency, window)`; windows are fixed and aligned (`SETTLEMENT_WINDOW_MINUTES`, default 1 in compose). A late event for an already-closed window goes into the current one. A reversal removes the charge while its batch is open.
- Both consumers are idempotent (a charge can appear in only one batch line), because Kafka delivers at least once.
- A scheduled job closes due batches under a row lock, posts the `SETTLEMENT` to the ledger keyed by batch ID (so a re-run can't pay twice), and runs the `Reconciler`. It flags lines with no ledger hold, amount drift, and held charges from the same merchant and window that are missing from every batch. The batch is marked `RECONCILED` or `MISMATCH` and the details are stored as JSON.

## Data model

The schema is in [`infra/postgres/`](infra/postgres/): `001_schema.sql` for the tables and the ledger trigger, `002_settlement.sql` for settlement indexes and result columns.

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
| `auth.approved` | `merchant_id` | auth-switch | settlement | charge_id, merchant_id, token, issuer_id, amount_minor, currency, auth_code, occurred_at |
| `auth.reversed` | `merchant_id` | auth-switch | settlement | same fields plus reason (`ISSUER_TIMEOUT`) |

## Getting started

**Requirements:** Docker with Compose v2. That is all: every service builds inside a container, so you do not need Go, Java or Maven installed locally.

```bash
git clone https://github.com/AmeyaMprojects/cardswitch.git
```

```bash
cd cardswitch && make up
```

The first build takes a few minutes (three Maven and three Go images). Dev vault keys are built into `docker-compose.yml`; to use your own, copy `.env.example` to `.env` and fill both keys with the output of `openssl rand -base64 32`.

Check that the services are healthy:

```bash
curl localhost:8080/healthz
```

The Java services expose `/actuator/health` on ports 8083–8085. Then run the end-to-end check:

```bash
make smoke
```

It approves a charge, replays it, reuses the key with a different body (`422`), triggers an insufficient-funds decline, checks the ledger balance, force-closes settlement and confirms the batch reconciled.

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
  "charge_id": "bec2cad7-891e-4ff0-8e30-08500d0938f4",
  "status": "APPROVED",
  "response_code": "00",
  "token": "tok_VYgLrlvE5esBclrb5u0C5rnJ",
  "last4": "1111",
  "amount_minor": 1999,
  "currency": "USD"
}
```

| Status | Meaning |
|---|---|
| `200` | Approved, or a replay of a finished request with the same key |
| `402` | Declined by the issuer (`05`, `13`, `51`), or `91` when the issuer could not be reached or its answer was lost (reversed) |
| `502` | The switch itself did not answer; the charge stays `PENDING` and this response is replayed for the key |
| `409` | A request with the same key is still in progress |
| `422` | Same key sent with a different request body |
| `400` | `Idempotency-Key` missing, body malformed, or card number invalid |

### Internal APIs

| Service | Endpoint |
|---|---|
| vault | `POST /v1/tokens`, `GET /v1/tokens/{token}`, `POST /v1/tokens/{token}/detokenize` |
| switch | `POST /v1/authorize` |
| issuer | `POST /v1/authorize`, `POST /v1/reverse` |
| ledger | `POST /v1/postings`, `GET /v1/accounts/{id}/balance` |
| settlement | `GET /v1/batches?merchant_id=`, `GET /v1/batches/{id}`, `GET /v1/batches/{id}/reconciliation`, `POST /v1/batches/close?force=true` |

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

What each scenario did when measured:

| Scenario | How | Observed |
|---|---|---|
| Issuer crash | `issuer-crash`, then 8 charges | The first charge retried and got `91`; the breaker then answered the rest in about 10 ms. No reversals were sent, because a failed DNS lookup proves nothing reached the issuer. Charges were approved again once the issuer came back and the breaker cooled down. |
| Network delay | `issuer-slow 1500` (above the 800 ms timeout), 50 req/s for 30 s | All 1,501 charges answered `91`. The breaker opened after the first timeouts, so p50 was 4.7 ms and p99 864 ms. 205 charges had an attempt time out and were reversed in the background, each publishing `auth.reversed`. |
| Flaky issuer | `issuer-flaky 0.3`, 100 req/s for 60 s | Retries absorbed the 30% `503`s: 5,846 approved (97.4%) and 155 `91` (2.6%), close to the 0.3³ = 2.7% expected after three tries. No HTTP errors. |
| Duplicate requests | `make dup-test` | 500 requests on 10 keys produced 10 charges and 10 ledger holds; `check-ledger` found no charge with two holds and no unbalanced transaction. |

Finding this broke one assumption: a dead issuer container shows up as a DNS failure, which was first classified as "outcome unknown" and triggered pointless reversals. Every dial-phase failure is now treated as "not sent".

## Load testing

The k6 scripts are in [`loadtest/`](loadtest/) and run through the `grafana/k6` Docker image, so you do not need to install k6.

```bash
make load
```

```bash
make dup-test
```

`charge.js` sends a constant arrival rate of charges with a fresh Luhn-valid PAN and key each time, across 20 merchants. `RATE` and `DURATION` are set with `-e`. Its thresholds are p99 under 250 ms and under 1% errors.

Measured on a MacBook running all nine containers in one 4-CPU, 6 GB Colima VM, with the issuer simulator adding 20 ms per call. The whole path runs on every request: gateway, vault, switch, issuer, Kafka, ledger and Postgres.

| Scenario | Target RPS | Achieved | p50 | p95 | p99 | HTTP errors |
|---|---|---|---|---|---|---|
| Happy path | 100 | 100 | 36 ms | 151 ms | 547 ms | 0% |
| Happy path | 150 | 148 | 89 ms | 545 ms | 1.94 s | 0% |
| Issuer 30% `503`s | 100 | 100 | 46 ms | 172 ms | 226 ms | 0% |
| Issuer +1500 ms (breaker open) | 50 | 50 | 4.7 ms | 849 ms | 864 ms | 0% |

About 150 req/s is this laptop's ceiling; past that the VM's CPUs saturate and the tail grows. All 184 settlement batches created during these runs reconciled with zero mismatches.

Two fixes came out of load testing:
- kafka-go's default 1 s `BatchTimeout` made every approval wait for a batch to fill (p50 around 2 s). It is now 5 ms.
- pgx's default pool of 4 connections throttled the gateway. Compose sets `pool_max_conns=20`.

## Known limitations

These are deliberate simplifications, not oversights:

- **Dual writes.** The switch publishes `auth.approved` after the issuer answers, and the gateway posts the ledger hold after the switch answers. A crash between the two loses the second write. The fix is a transactional outbox: write the event in the same Postgres transaction as the charge, and relay it to Kafka from there.
- **Stuck keys after a crash.** If the gateway dies mid-request, its idempotency row has no response, so retries get `409` until someone resolves the `PENDING` charge. A recovery job would ask the switch for the outcome.
- **Single-instance Redis lock.** Fine here because Postgres is the source of truth; a Redis outage only costs speed.
- **No capture step.** Approvals settle directly; there is no separate capture, void or refund API.
- **In-memory issuer.** The issuer simulator keeps balances in memory, so restarting it resets every card.
- **No mTLS.** Services trust the compose network; the vault's `X-Caller-Service` header stands in for client certificates.

## Repository layout

```
cardswitch/
├── docker-compose.yml
├── Makefile
├── .env.example
├── infra/postgres/                    # schema, ledger trigger, settlement indexes
├── services/
│   ├── gateway/                       # Go – merchant API, idempotency (Redis + Postgres)
│   │   └── internal/{charges,store,clients}
│   ├── auth-switch/                   # Go – BIN routing, breaker, retries, reversals, Kafka
│   │   └── internal/{routing,breaker,issuerclient,reversal,events}
│   ├── issuer-sim/                    # Go – balances, decline codes, reversals, fault injection
│   ├── tokenization-vault/            # Java – Luhn, HMAC dedupe, AES-GCM PAN vault
│   ├── ledger/                        # Java – idempotent double-entry postings
│   └── settlement/                    # Java – Kafka batches, ledger settlement, reconciliation
├── loadtest/                          # k6 load and duplicate-key scripts
└── scripts/                           # smoke.sh end-to-end check, chaos.sh failure injection
```

Run every unit test (Go and JUnit) inside containers, without installing Go or Java:

```bash
make test
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
- [x] End-to-end smoke test, chaos scenarios and recorded k6 results
- [ ] Transactional outbox for events and ledger holds
- [ ] Recovery job for charges left `PENDING`
- [ ] Integration tests with Testcontainers
- [ ] Prometheus metrics and Grafana dashboard (latency, breaker state, reversal rate)
- [ ] mTLS between services

## Disclaimer

This is a learning project. It is not PCI DSS compliant and must never handle real card data. Use only the published test card numbers.
