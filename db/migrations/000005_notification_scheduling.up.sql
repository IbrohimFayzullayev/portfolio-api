-- Scheduling and de-duplication for the outbox.
--
-- Three columns, because most of what the bot still has to learn is the same
-- question in different clothes: when should this message go out, and how many
-- times? A reminder two hours before an event, an error that repeats a hundred
-- times a minute, a routine notice arriving at 03:00 — all of them are answered
-- here, rather than by growing a second delivery path beside the outbox.
--
--   deliver_after  NULL means "as soon as possible". Anything else holds the
--                  row back until that moment.
--   dedupe_key     At most one PENDING row per key. Two jobs at once:
--                  collapsing a repeated error into one message, and making
--                  the scheduler idempotent — re-running the same hour must
--                  not queue the same reminder twice.
--   severity       0 info, 1 warning, 2 critical. Only critical is allowed to
--                  wake someone at night; the rest wait for the morning.

ALTER TABLE notifications
    ADD COLUMN deliver_after TIMESTAMPTZ,
    ADD COLUMN dedupe_key    TEXT,
    ADD COLUMN severity      SMALLINT NOT NULL DEFAULT 0;

-- The hot query is now "due", not merely "pending": order by the moment a row
-- is allowed to go out, which for an unscheduled row is when it was written.
DROP INDEX IF EXISTS idx_notifications_pending;
CREATE INDEX idx_notifications_pending
    ON notifications (COALESCE(deliver_after, created_at))
    WHERE sent_at IS NULL;

-- Partial on purpose. A full unique index would mean a key could only ever be
-- used once in the table's lifetime: the 24-hour reminder for one invitation
-- could never be queued again after delivery, and neither could the next
-- window of a recurring error. Uniqueness is only wanted among the rows still
-- waiting to go out.
CREATE UNIQUE INDEX idx_notifications_dedupe
    ON notifications (dedupe_key)
    WHERE sent_at IS NULL AND dedupe_key IS NOT NULL;
