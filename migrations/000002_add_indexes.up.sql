-- batch_id ile sorgulama (GetNotificationsByBatchID) için index
CREATE INDEX IF NOT EXISTS idx_notifications_batch_id ON notifications(batch_id);

-- channel ile filtreleme (ListNotifications) için index
CREATE INDEX IF NOT EXISTS idx_notifications_channel ON notifications(channel);

-- status tek başına filtreleme için index (ListNotifications status filtresi)
CREATE INDEX IF NOT EXISTS idx_notifications_status ON notifications(status);

-- migration dosyanın içine eklenebilecek TLA+ Invariant kuralı:
CREATE OR REPLACE FUNCTION enforce_terminal_state_immutability()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('delivered', 'failed_permanently') AND NEW.status != OLD.status THEN
        RAISE EXCEPTION 'TLA+ Invariant Violation: Cannot transition out of terminal state %', OLD.status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER check_terminal_states
BEFORE UPDATE ON notifications
FOR EACH ROW
EXECUTE FUNCTION enforce_terminal_state_immutability();
