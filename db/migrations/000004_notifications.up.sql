-- Outbox for messages the operations bot should deliver.
--
-- The API and the bot are separate containers on purpose (a monitor that lives
-- inside the thing it monitors goes quiet exactly when you need it), so the API
-- cannot simply call a function in the bot. Of the three ways to bridge that —
-- the API talking to Telegram itself, an internal HTTP endpoint on the bot, or
-- a row in the database — only this one survives the bot being down: the row
-- waits, and the bot drains the backlog when it comes back.
--
-- It also means the API never blocks on Telegram. Writing a row is a local
-- INSERT; whether Telegram is slow or unreachable is the bot's problem.

CREATE TABLE notifications (
    id         BIGSERIAL PRIMARY KEY,
    -- e.g. 'invitation.created', 'post.published', 'deploy.finished',
    -- 'health.down'. Free-form on purpose: adding a message type should be an
    -- INSERT, not a migration.
    kind       TEXT NOT NULL,
    payload    JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at    TIMESTAMPTZ,
    -- Delivery attempts. A row that keeps failing is parked rather than
    -- retried forever, so one bad message cannot block the queue behind it.
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT ''
);

-- The only hot query: undelivered rows, oldest first. Partial, because
-- delivered rows are never scanned again.
CREATE INDEX idx_notifications_pending
    ON notifications (created_at)
    WHERE sent_at IS NULL;
