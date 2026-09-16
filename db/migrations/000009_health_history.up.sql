-- Health history and the bot's own pulse.
--
-- Two tables that answer two different questions.
--
-- health_checks is the record behind "how has it been?", which nothing could
-- answer before: the watcher kept the last verdict in memory and announced
-- changes, so an outage that healed before anyone looked left no trace at all.
-- One row per check, pruned after a month — this is a monitoring table, not an
-- archive.
--
-- bot_heartbeat exists because the bot is the single exit for every alert in
-- this stack. When it dies, the silence looks exactly like everything being
-- fine. So it writes a pulse here on every cycle, and something OUTSIDE the
-- bot — a cron job on the host, see deploy/cron/bot-watchdog.sh — notices when
-- the pulse stops and messages Telegram directly, without going through the
-- bot or its outbox.
--
-- On one server that still leaves the case where the whole machine is gone.
-- That gap is accepted deliberately; closing it needs a pinger somewhere else.

CREATE TABLE health_checks (
    id         BIGSERIAL PRIMARY KEY,
    target     TEXT NOT NULL,
    ok         BOOLEAN NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    detail     TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_health_checks_target_time
    ON health_checks (target, checked_at DESC);

-- A single row, forced. The heartbeat is a value, not a log: the watchdog only
-- ever asks "how old is it?", and a growing table would need pruning for no
-- reason.
CREATE TABLE bot_heartbeat (
    id      SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    beat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    note    TEXT NOT NULL DEFAULT ''
);

INSERT INTO bot_heartbeat (id) VALUES (1) ON CONFLICT DO NOTHING;
