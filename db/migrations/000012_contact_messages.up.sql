-- Messages sent through the contact form on the public site.
--
-- The site used to offer links only — email, Telegram, LinkedIn — which asks
-- the visitor to leave the page and decide which of four channels is the right
-- one. A form removes that step, and the row here is what makes it safe to
-- offer: the message is stored before anything tries to deliver it, so a
-- Telegram outage delays the notification without losing the message.
--
-- read_at rather than a boolean, for the usual reason: "when" answers "whether"
-- and the reverse is not true.

CREATE TABLE contact_messages (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL,
    -- The language the visitor was reading in, so a reply can match it.
    locale     TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_contact_messages_created_at ON contact_messages (created_at DESC);
