-- Grouped server-side errors.
--
-- This table is not a log — the container already has one. It is the bot's
-- memory of what has gone wrong, and it exists for one reason: to answer
-- "have I already said this?" A repeated 500 at request rate is a hundred
-- Telegram messages a minute without it, and a muted bot by morning. With it,
-- the hundredth occurrence is the same row with a larger count.
--
-- The fingerprint is a hash of service, route and the error text with numbers
-- and UUIDs replaced, so "post 8f3c… not found" and "post a91b… not found"
-- are one group rather than two. Without that normalisation the grouping is
-- decorative: every occurrence would be unique.

CREATE TABLE error_groups (
    fingerprint    TEXT PRIMARY KEY,
    service        TEXT NOT NULL,
    route          TEXT NOT NULL DEFAULT '',
    -- Mirrors notifications.severity: 0 info, 1 warning, 2 critical. A panic
    -- is critical; a failed query is a warning that can wait for the morning.
    severity       SMALLINT NOT NULL DEFAULT 1,
    message        TEXT NOT NULL,
    sample_stack   TEXT NOT NULL DEFAULT '',

    count          BIGINT NOT NULL DEFAULT 0,
    -- What count was when the last message went out, so the next one can say
    -- how many happened in between rather than the total since forever.
    notified_count BIGINT NOT NULL DEFAULT 0,

    first_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    notified_at    TIMESTAMPTZ,
    -- Set by the "Jimlatish" button in Telegram. A known, already-being-fixed
    -- error should be silenceable without turning off the whole channel.
    muted_until    TIMESTAMPTZ
);

CREATE INDEX idx_error_groups_recent ON error_groups (last_seen DESC);
