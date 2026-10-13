-- Personal money log, written from Telegram one line at a time.
--
-- One table for everything rather than one per kind. Spending, a gift, a loan
-- and its repayment are all "an amount moved, on a day, maybe to someone", and
-- the questions asked of them cut across kinds: "what went out this month"
-- sums expenses and gifts, "who owes me" nets loans against repayments. Keeping
-- them in one place also means the admin page later edits one shape of row.
--
-- A loan is not a separate object with a status. A person's balance is the net
-- of their rows (lent - repaid to me - borrowed + repaid by me), so a partial
-- repayment is simply another row, and correcting a typo in any of them fixes
-- the balance without anything else to keep in sync.

CREATE TABLE finance_entries (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- expense    — spent on myself
    -- gift       — given away, not expected back
    -- lent       — loan I gave          (they owe me more)
    -- repaid_in  — they paid me back    (they owe me less)
    -- borrowed   — loan I took          (I owe more)
    -- repaid_out — I paid back          (I owe less)
    kind         TEXT NOT NULL CHECK (kind IN
                     ('expense', 'gift', 'lent', 'repaid_in', 'borrowed', 'repaid_out')),

    -- Hundredths of the currency unit (tiyin, cents), so sums are exact
    -- integers and "$12.50" needs no float.
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency     TEXT NOT NULL DEFAULT 'UZS' CHECK (currency IN ('UZS', 'USD')),

    category     TEXT NOT NULL DEFAULT 'boshqa',
    -- As typed, first letter capitalised. Matched case-insensitively.
    person       TEXT NOT NULL DEFAULT '',
    -- The message with the amount and date taken out: "tushlik", "to'yga".
    note         TEXT NOT NULL DEFAULT '',
    -- Exactly what was sent, kept so a wrong guess can be checked against it.
    raw_text     TEXT NOT NULL DEFAULT '',

    -- The day the money moved, which is not always the day it was written
    -- down ("kecha taksi 20k").
    spent_on     DATE NOT NULL,

    -- 'bot' or 'admin'.
    source       TEXT NOT NULL DEFAULT 'bot',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_finance_entries_spent_on ON finance_entries (spent_on DESC, created_at DESC);
CREATE INDEX idx_finance_entries_person   ON finance_entries (lower(person)) WHERE person <> '';
