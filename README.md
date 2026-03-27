# Distributed Event-Driven Notification System

A highly scalable, fault-tolerant, and high-throughput event-driven notification relay system built with **Go**, **Kafka**, **PostgreSQL**, and **Redis**. 

Designed to handle millions of notifications (SMS, Email, Push) with strict idempotency, multi-layered rate limiting, and robust retry mechanisms, ensuring zero data loss and maximum resilience in production environments.

## Key Engineering Features

* **Transactional Outbox Pattern:** Guarantees 100% reliable event publishing to Kafka without the "dual-write" problem, utilizing PostgreSQL's `FOR UPDATE SKIP LOCKED` for lock-free concurrent relay processing.
* **Defense-in-Depth Rate Limiting:**
    * *L7 Global Flood Protection:* Redis-backed Sliding Window Log algorithm to prevent DDoS attacks.
    * *Business Quota Management:* Token Bucket algorithm accurately handles large payload weights (batch processing).
    * *Channel Rate Limiting:* Per-channel (SMS/Email/Push) sliding window at 100 msg/sec.
* **Distributed Circuit Breaker (State Machine):** A custom, Redis-backed circuit breaker utilizing atomic Lua scripts to manage `Closed`, `Open`, and `Half-Open` states, preventing the "Thundering Herd" problem on downstream webhook failures.
* **Watermark-based Kafka Commit Tracker:** Custom implementation ensuring safe offset commits in parallel worker pools, preventing message loss from out-of-order commits.
* **Poison Pill & Deadlock Protection:** Robust batch processing that detects and discards corrupted JSON payloads (poison pills) directly at the database level, preventing infinite rollback loops.
* **O(1) Keyset Pagination:** High-performance cursor-based pagination for listing notifications, preventing the exponential slowdown associated with traditional `OFFSET/LIMIT` queries on large datasets.
* **Graceful Shutdown:** Implemented zero-downtime shutdown choreographies across all API and Worker nodes, allowing in-flight database transactions and HTTP requests to complete safely before termination.
* **Zombie Recovery & Exponential Backoff:** Background CTE sweepers automatically recover stuck processing jobs and implement dynamic retry intervals based on failure counts.
* **Comprehensive Prometheus Metrics:** 18 metrics covering API requests, rate limiting, webhook performance, Kafka consumption, circuit breaker state, and queue depth.

## Tech Stack

* **Language:** Go (Golang) 1.22+
* **Database:** PostgreSQL (with `pgx/v5` for high-performance connection pooling)
* **Message Broker:** Apache Kafka (KRaft mode, no Zookeeper)
* **In-Memory Store:** Redis (for Rate Limiting, Circuit Breaking, and Fast-Fail flags)
* **Telemetry:** Prometheus (18 metrics) & structured `slog` with Context Propagation

## Architecture Overview

```
┌────────────┐    ┌─────────────────────────────────────────────────┐
│   Client    │    │              Docker Compose Network             │
│  (curl/UI)  │    │                                                 │
└─────┬──────┘    │  ┌──────────┐   ┌──────────┐   ┌────────────┐  │
      │           │  │ PostgreSQL│   │  Redis   │   │   Kafka    │  │
      │ HTTP      │  │  (ACID)  │   │ (Cache)  │   │  (Broker)  │  │
      │           │  └────┬─────┘   └────┬─────┘   └──┬────┬────┘  │
      ▼           │       │              │             │    │       │
┌──────────┐      │       │              │             │    │       │
│  API GW  │──────┼───────┤              │             │    │       │
│ cmd/api  │      │  Write│(Outbox)      │Rate Limit   │    │       │
│ :8080    │      │       │              │             │    │       │
└──────────┘      │       ▼              │             │    │       │
                  │  ┌──────────┐        │             │    │       │
                  │  │  Relay   │────────┼─────────────┘    │       │
                  │  │ cmd/relay│  Poll  │  Publish          │       │
                  │  │ :9094   │  Outbox │  to Kafka         │       │
                  │  └──────────┘        │                   │       │
                  │                      │                   │       │
                  │  ┌──────────┐        │                   │       │
                  │  │ Worker   │────────┤───────────────────┘       │
                  │  │cmd/worker│  Lock  │CB  Consume                │
                  │  │ :9091   │        │                           │
                  │  └────┬─────┘        │    ┌──────────────┐      │
                  │       │              │    │ Mock Webhook  │      │
                  │       └──────────────┼───▶│ (nginx:alpine)│      │
                  │          Webhook     │    └──────────────┘      │
                  └─────────────────────────────────────────────────┘
```

1.  **API Gateway (`cmd/api`):** Receives single or batch notification requests (up to 1000 items). Validates payloads, applies rate limits, and safely writes to the `notifications` and `outbox_events` tables within a single ACID transaction.
2.  **Relay Worker (`cmd/relay`):** Continuously polls the `outbox_events` table using `SKIP LOCKED`. It publishes valid events to Kafka topics based on priority and immediately deletes the published records, maintaining an empty queue.
3.  **Notification Processors (`cmd/worker`):** A channel-based worker pool that consumes messages from Kafka partitions. Evaluates distributed locks, respects the circuit breaker state, makes external Webhook calls, and commits offsets back to Kafka only upon successful database status updates.

## Getting Started

### Prerequisites
* Docker & Docker Compose
* Go 1.22+ (for local development and testing)

### One-Command Setup

```bash
docker-compose up -d --build
```

This single command will:
1. Start PostgreSQL, Redis, Kafka, and a mock webhook service
2. Run database migrations automatically (`db-migrate` init service)
3. Create Kafka topics automatically (`kafka-init` init service)
4. Start API, Relay, and Worker services

The system is fully functional out of the box with the built-in mock webhook. No `.env` file required.

### Using a Real Webhook (Optional)

To use a real webhook endpoint (e.g., [webhook.site](https://webhook.site)):

```bash
cp .env.example .env
# Edit .env and set: WEBHOOK_URL=https://webhook.site/your-unique-uuid
docker-compose up -d --build
```

---

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/notifications` | Create a single notification |
| `POST` | `/notifications/batch` | Create up to 1000 notifications |
| `GET` | `/notifications` | List notifications (filter + pagination) |
| `GET` | `/notifications/{id}` | Get notification details by ID |
| `GET` | `/notifications/batch/{id}` | Get batch status by Batch ID |
| `DELETE` | `/notifications/{id}` | Cancel a pending/retrying notification |
| `GET` | `/health` | System health check |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/swagger` | Swagger UI |

---

## API Examples

### Create a Single Notification

```bash
curl -X POST http://localhost:8080/notifications \
  -H "Content-Type: application/json" \
  -d '{
    "idempotency_key": "unique-key-001",
    "recipient": "+905551234567",
    "channel": "sms",
    "content": "Siparisiz kargoya verildi!",
    "priority": "high"
  }'
```

**Response (202 Accepted):**
```json
{
  "message": "Bildirim alindi, isleniyor",
  "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
}
```

### Create Batch Notifications

```bash
curl -X POST http://localhost:8080/notifications/batch \
  -H "Content-Type: application/json" \
  -d '[
    {
      "idempotency_key": "batch-001",
      "recipient": "user@example.com",
      "channel": "email",
      "content": "Hosgeldiniz!",
      "priority": "normal"
    },
    {
      "idempotency_key": "batch-002",
      "recipient": "+905559876543",
      "channel": "sms",
      "content": "Dogrulama kodunuz: 1234",
      "priority": "high"
    }
  ]'
```

**Response (202 Accepted):**
```json
{
  "message": "Toplu bildirimler alindi, isleniyor",
  "count": 2,
  "batch_id": "f1e2d3c4-b5a6-7890-dcba-fedcba987654"
}
```

### Get Notification Status

```bash
curl http://localhost:8080/notifications/a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

### List Notifications with Filters

```bash
# Status filter + pagination
curl "http://localhost:8080/notifications?status=delivered&channel=sms&limit=20"

# Date range filter
curl "http://localhost:8080/notifications?start_date=2026-03-01&end_date=2026-03-27&limit=50"
```

### Cancel a Notification

```bash
curl -X DELETE http://localhost:8080/notifications/a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

### Health Check

```bash
curl http://localhost:8080/health
```

**Response:**
```json
{
  "api": "up",
  "database": "up",
  "redis": "up"
}
```

---

## Prometheus Metrics

The system exposes 18 Prometheus metrics across 6 categories:

| Category | Metric | Type | Description |
|----------|--------|------|-------------|
| **API** | `api_http_requests_total` | Counter | Total HTTP requests (method, route, status_code) |
| **API** | `api_request_duration_seconds` | Histogram | API endpoint latency |
| **API** | `notification_created_total` | Counter | Notifications created (channel) |
| **API** | `notification_batch_size` | Histogram | Batch request size distribution |
| **API** | `notification_canceled_total` | Counter | Canceled notifications |
| **Rate Limit** | `rate_limit_hits_total` | Counter | Rate limit rejections (limiter_type) |
| **Worker** | `notification_events_processed_total` | Counter | Processed notifications (status, channel) |
| **Worker** | `notification_processing_duration_seconds` | Histogram | End-to-end processing latency |
| **Worker** | `webhook_call_duration_seconds` | Histogram | Webhook call latency |
| **Worker** | `webhook_calls_total` | Counter | Webhook call results (channel, result) |
| **Worker** | `notification_retry_total` | Counter | Retry attempts (channel) |
| **Worker** | `notification_active_workers` | Gauge | Currently active workers (topic) |
| **Kafka** | `kafka_messages_consumed_total` | Counter | Messages consumed from Kafka (topic) |
| **Kafka** | `kafka_dlq_messages_total` | Counter | Dead Letter Queue messages |
| **Kafka** | `notification_inflight_messages` | Gauge | In-flight messages in worker pipeline |
| **Outbox** | `notification_outbox_queue_depth` | Gauge | Pending outbox messages |
| **Outbox** | `outbox_relayed_total` | Counter | Messages relayed to Kafka |
| **Circuit Breaker** | `circuit_breaker_state` | Gauge | CB state (0=closed, 1=open) |

Access metrics at: `http://localhost:8080/metrics` (API) or `http://localhost:9091/metrics` (Worker) or `http://localhost:9094/metrics` (Relay).

---

## Testing with Webhook.site

By default, the worker uses a built-in mock webhook service. To see live requests hitting a real webhook:

1.  Go to [webhook.site](https://webhook.site/).
2.  Copy your unique UUID URL.
3.  Create `.env` file: `WEBHOOK_URL=https://webhook.site/your-unique-uuid`
4.  Restart: `docker-compose up -d --build`

## Running Tests

```bash
# All tests (single command)
make test

# Unit tests only (no external dependencies)
go test ./internal/models/... ./internal/config/... ./internal/logger/...

# Integration tests (requires PostgreSQL and Redis running)
go test ./internal/... -count=1 -timeout=120s
```

## Stress Testing

```bash
chmod +x stress_test.sh
./stress_test.sh
```
