/* migrate -path migrations -database "postgres://insider:secretpassword@localhost:5432/notifications_db?sslmode=disable" up */

CREATE TABLE IF NOT EXISTS notifications (
    id UUID PRIMARY KEY,
    batch_id UUID,
    idempotency_key VARCHAR(255) UNIQUE NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    channel VARCHAR(50) NOT NULL,
    content TEXT NOT NULL,
    priority VARCHAR(50) NOT NULL DEFAULT 'normal',
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    external_id VARCHAR(255),
    retry_count INT NOT NULL DEFAULT 0,
    correlation_id VARCHAR(255),
    next_retry_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_notifications_status_next_retry ON notifications(status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_notifications_pagination ON notifications(created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_outbox_events_status_created ON outbox_events(status, created_at);
