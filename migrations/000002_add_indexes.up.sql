-- batch_id ile sorgulama (GetNotificationsByBatchID) için index
CREATE INDEX IF NOT EXISTS idx_notifications_batch_id ON notifications(batch_id);

-- channel ile filtreleme (ListNotifications) için index
CREATE INDEX IF NOT EXISTS idx_notifications_channel ON notifications(channel);

-- status tek başına filtreleme için index (ListNotifications status filtresi)
CREATE INDEX IF NOT EXISTS idx_notifications_status ON notifications(status);
