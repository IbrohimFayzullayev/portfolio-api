-- Invitations submitted by the standalone "date planner" invitation site.
-- That site has no backend of its own: it posts the visitor's choices here
-- when they reach the final invitation card.

CREATE TABLE invitations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Which front-end produced this row; lets other sites reuse the endpoint.
    source      TEXT NOT NULL DEFAULT 'planner',
    -- Stable per-browser id. Rows are never merged, but this makes it obvious
    -- when several rows come from the same visitor changing their mind.
    session_id  TEXT NOT NULL DEFAULT '',

    event_date  DATE NOT NULL,
    event_time  TEXT NOT NULL DEFAULT '',

    food_id     TEXT NOT NULL DEFAULT '',
    food_label  TEXT NOT NULL DEFAULT '',
    food_emoji  TEXT NOT NULL DEFAULT '',

    place_id    TEXT NOT NULL DEFAULT '',
    place_label TEXT NOT NULL DEFAULT '',
    place_emoji TEXT NOT NULL DEFAULT '',

    -- The fully rendered letter, exactly as the visitor saw it.
    invite_text TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invitations_created_at ON invitations (created_at DESC);
CREATE INDEX idx_invitations_session_id ON invitations (session_id);
