-- The finance bot: a money log for several people, one Telegram account each.
--
-- This database belongs to the finance bot alone. It is not the portfolio's
-- database and shares no table with it, so nothing here can be reached from
-- the portfolio API, the ops bot or the admin dashboard, and a dump of it is
-- the finance data and nothing else.
--
-- Every row of money belongs to exactly one user (user_id). Every query the
-- bot and its web API run filters on it; that filter is the whole privacy
-- model, so it is written out in each query rather than hidden in a view.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    -- In a private chat the chat id is the user id, so this is also where
    -- the bot writes to.
    telegram_id   BIGINT NOT NULL UNIQUE,
    first_name    TEXT NOT NULL DEFAULT '',
    username      TEXT NOT NULL DEFAULT '',
    -- pending: asked for access, waiting for the admin.
    -- active:  may use the bot and the web app.
    -- blocked: refused or removed; gets no answer at all.
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'active', 'blocked')),
    -- The 09:00 message. On by default, /xulosa off turns it off.
    daily_summary BOOLEAN NOT NULL DEFAULT true,
    -- How access was given: 'admin', 'approved', or 'invite:<code>'.
    joined_via    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at   TIMESTAMPTZ,
    last_seen_at  TIMESTAMPTZ
);

-- One-time links: t.me/<bot>?start=<code>. Opening one makes the account
-- active at once, so a new person needs no approval round-trip.
CREATE TABLE invites (
    code       TEXT PRIMARY KEY,
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_by    BIGINT REFERENCES users (id) ON DELETE SET NULL,
    used_at    TIMESTAMPTZ
);

-- The same shape as the portfolio's finance_entries (migrations 000013 and
-- 000014), plus the owner. See internal/finance for what the kinds mean.
CREATE TABLE entries (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN
                     ('expense', 'gift', 'lent', 'repaid_in', 'borrowed', 'repaid_out', 'income')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency     TEXT NOT NULL DEFAULT 'UZS' CHECK (currency IN ('UZS', 'USD')),
    category     TEXT NOT NULL DEFAULT 'boshqa',
    person       TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '',
    raw_text     TEXT NOT NULL DEFAULT '',
    spent_on     DATE NOT NULL,
    -- 'bot' or 'web'.
    source       TEXT NOT NULL DEFAULT 'bot',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX entries_user_day    ON entries (user_id, spent_on DESC, created_at DESC);
CREATE INDEX entries_user_person ON entries (user_id, lower(person)) WHERE person <> '';

-- Balance checkpoints: "this is what is really in hand now".
CREATE TABLE checkpoints (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    currency       TEXT NOT NULL CHECK (currency IN ('UZS', 'USD')),
    amount_minor   BIGINT NOT NULL,
    taken_at       TIMESTAMPTZ NOT NULL,
    computed_minor BIGINT,
    note           TEXT NOT NULL DEFAULT '',
    source         TEXT NOT NULL DEFAULT 'bot',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX checkpoints_user ON checkpoints (user_id, currency, taken_at DESC);

-- A question the bot asked with force_reply ("send the new amount"). The
-- answer finds its question by the message it replies to, so a restart in
-- the middle of an edit loses nothing.
CREATE TABLE prompts (
    chat_id    BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    intent     TEXT NOT NULL,
    target_id  TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chat_id, message_id)
);

-- One morning summary per user per day, claimed before it is sent.
CREATE TABLE summaries_sent (
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    day     DATE NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day)
);

-- Small counters that must survive a restart. 'offset' is the next Telegram
-- update to read: unlike the ops bot, this one never skips what arrived
-- while it was down, because every one of those messages is money.
CREATE TABLE bot_state (
    key        TEXT PRIMARY KEY,
    value      BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
