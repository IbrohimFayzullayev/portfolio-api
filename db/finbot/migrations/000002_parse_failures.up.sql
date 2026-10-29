-- Messages the bot could not read ("Summani topolmadim"). Kept so the admin
-- can see whether people manage to use the bot - and which phrasings the
-- parser should learn next. Pruned after 60 days.
CREATE TABLE parse_failures (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    text       TEXT NOT NULL,
    -- 'no_amount' or 'no_person'
    reason     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX parse_failures_user ON parse_failures (user_id, created_at DESC);
