-- Income, and the balance it feeds.
--
-- Income is one more kind of row, not a new table: it is still "an amount
-- moved, on a day", and every report that sums rows by kind picks it up.
--
-- The balance itself is not stored anywhere. A checkpoint records what was
-- actually in hand at one moment ("/balans 25mln"); the balance now is the
-- latest checkpoint plus the rows after it (internal/finance/balance.go).
-- Re-counting adds a checkpoint rather than editing rows, and computed_minor
-- keeps what the rows said at that moment, so the difference — money spent
-- but never written down — stays visible afterwards.

ALTER TABLE finance_entries DROP CONSTRAINT finance_entries_kind_check;
ALTER TABLE finance_entries ADD CONSTRAINT finance_entries_kind_check CHECK (kind IN
    ('expense', 'gift', 'lent', 'repaid_in', 'borrowed', 'repaid_out', 'income'));

CREATE TABLE finance_checkpoints (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    currency       TEXT NOT NULL CHECK (currency IN ('UZS', 'USD')),
    -- What was counted. May be negative: an overdrawn card is still a fact.
    amount_minor   BIGINT NOT NULL,
    -- The moment it was true. "Now" from the bot; the start of a chosen day
    -- from the dashboard, for an opening balance entered after the fact.
    taken_at       TIMESTAMPTZ NOT NULL,
    -- The balance the rows gave just before this checkpoint; NULL for the
    -- first one in a currency.
    computed_minor BIGINT,
    note           TEXT NOT NULL DEFAULT '',
    source         TEXT NOT NULL DEFAULT 'bot',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_finance_checkpoints_taken ON finance_checkpoints (currency, taken_at DESC);
