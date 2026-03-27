# Distributed Event-Driven Notification System

A highly scalable, fault-tolerant, and high-throughput event-driven notification relay system built with **Go**, **Kafka**, **PostgreSQL**, and **Redis**. 

Designed to handle millions of notifications (SMS, Email, Push) with strict idempotency, multi-layered rate limiting, and robust retry mechanisms, ensuring zero data loss and maximum resilience in production environments.

## Key Engineering Features

* **Transactional Outbox Pattern:** Guarantees 100% reliable event publishing to Kafka without the "dual-write" problem, utilizing PostgreSQL's `FOR UPDATE SKIP LOCKED` for lock-free concurrent relay processing.
* **Defense-in-Depth Rate Limiting:** * *L7 Global Flood Protection:* Redis-backed Sliding Window Log algorithm to prevent DDoS attacks.
    * *Business Quota Management:* Token Bucket algorithm accurately handles large payload weights (batch processing).
* **Distributed Circuit Breaker (State Machine):** A custom, Redis-backed circuit breaker utilizing atomic Lua scripts to manage `Closed`, `Open`, and `Half-Open` states, preventing the "Thundering Herd" problem on downstream webhook failures.
* **Poison Pill & Deadlock Protection:** Robust batch processing that detects and discards corrupted JSON payloads (poison pills) directly at the database level, preventing infinite rollback loops.
* **O(1) Keyset Pagination:** High-performance cursor-based pagination for listing notifications, preventing the exponential slowdown associated with traditional `OFFSET/LIMIT` queries on large datasets.
* **Graceful Shutdown:** Implemented zero-downtime shutdown choreographies across all API and Worker nodes, allowing in-flight database transactions and HTTP requests to complete safely before termination.
* **Zombie Recovery & Exponential Backoff:** Background CTE sweepers automatically recover stuck processing jobs and implement dynamic retry intervals based on failure counts.

## Tech Stack

* **Language:** Go (Golang) 1.22+
* **Database:** PostgreSQL (with `pgx/v5` for high-performance connection pooling)
* **Message Broker:** Apache Kafka (Bitnami KRaft mode)
* **In-Memory Store:** Redis (for Rate Limiting, Circuit Breaking, and Fast-Fail flags)
* **Telemetry:** Prometheus (Metrics) & structured `slog` with Context Propagation

## Architecture Overview

1.  **API Gateway (`cmd/api`):** Receives single or batch notification requests (up to 1000 items). Validates payloads, applies rate limits, and safely writes to the `notifications` and `outbox_events` tables within a single ACID transaction.
2.  **Relay Worker (`cmd/relay`):** Continuously polls the `outbox_events` table using `SKIP LOCKED`. It publishes valid events to Kafka topics based on priority and immediately deletes the published records, maintaining an empty queue.
3.  **Notification Processors (`cmd/worker`):** A channel-based worker pool that consumes messages from Kafka partitions. Evaluates distributed locks, respects the circuit breaker state, makes external Webhook calls, and commits offsets back to Kafka only upon successful database status updates.

## Getting Started

### Prerequisites
* Docker & Docker Compose
* Go 1.22+

### 1. Bootstrapping the Infrastructure
First, copy the environment template and configure your Webhook URL:

```bash
cp .env.example .env
# Edit .env and set your WEBHOOK_URL (get one from https://webhook.site)
```

Then spin up PostgreSQL, Redis, and Kafka using Docker Compose:

```bash
make up
# or: docker-compose up -d --build
```

### 2. Automatic Bootstrap (Migrations + Kafka Topics)
`docker compose up` now runs two one-shot init services automatically:
- `db-migrate`: applies SQL migrations
- `kafka-init`: creates required topics (`notifications-high`, `notifications-normal`, `notifications-low`, `notifications-dlq`)

App services start only after these init steps complete successfully.

### 3. System Load & Stress Testing
To demonstrate the system's capabilities (Rate Limiting, Batch Processing, and concurrency), a built-in stress test script is provided. It sends a combination of single and batch requests to the API.

While the services are running, execute the following script in a new terminal:
```bash
chmod +x stress_test.sh
./stress_test.sh
```
*Observe the worker logs to see the Circuit Breaker and Rate Limiter in action!*

---

## API Endpoints

1.  **`POST /notifications`** - Create a single notification (Requires Idempotency Key).
2.  **`POST /notifications/batch`** - Create up to 1000 notifications in a single transaction.
3.  **`GET /notifications`** - List notifications (Supports Keyset Pagination cursor, limit, status, channel, date range filters).
4.  **`GET /notifications/{id}`** - Get notification details and current status by ID.
5.  **`GET /notifications/batch/{id}`** - Get the status of a specific batch.
6.  **`DELETE /notifications/{id}`** - Cancel a pending/retrying notification (Fast-fail via Redis flag).
7.  **`GET /health`** - System health check (Postgres & Redis ping).
8.  **`GET /metrics`** - Prometheus metrics (queue depth, success/failure rates, latency).
9.  **`GET /swagger`** - Swagger UI (serves `/swagger/swagger.json` and `/swagger/swagger.yaml`).

---

## Testing with Webhook.site

By default, the worker attempts to forward processed notifications to `https://webhook.site/default`. To see live requests hitting the webhook:

1.  Go to [webhook.site](https://webhook.site/).
2.  Copy your unique UUID URL.
3.  Update the `.env` file in the project root: 
    `WEBHOOK_URL=https://webhook.site/your-unique-uuid`
4.  Restart the worker (`make run-worker`) and send a `POST /notifications` request to watch the messages arrive in real-time!

## Running Tests

```bash
# Unit tests (no external dependencies required)
go test ./internal/models/...

# All tests (requires PostgreSQL and Redis running)
make test
```
