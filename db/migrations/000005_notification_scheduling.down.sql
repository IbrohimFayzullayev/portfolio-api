DROP INDEX IF EXISTS idx_notifications_dedupe;
DROP INDEX IF EXISTS idx_notifications_pending;

ALTER TABLE notifications
    DROP COLUMN IF EXISTS deliver_after,
    DROP COLUMN IF EXISTS dedupe_key,
    DROP COLUMN IF EXISTS severity;

CREATE INDEX idx_notifications_pending
    ON notifications (created_at)
    WHERE sent_at IS NULL;
