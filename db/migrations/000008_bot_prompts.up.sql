-- What the bot is waiting for.
--
-- Editing a post over Telegram is a conversation: "which field?", then the new
-- value in the next message. The usual way to hold that state is a map in the
-- process, and it is wrong here for a boring reason — the bot container is
-- restarted on every deploy, and a half-written post that disappears because
-- something unrelated shipped is worse than no editing at all.
--
-- So the state lives in the message itself. The bot asks with force_reply and
-- remembers which question that message was; the answer arrives carrying
-- reply_to_message_id, and this table turns that id back into an intent. A
-- restart in between costs nothing: the question is still on screen, and the
-- answer still finds its way home.

CREATE TABLE bot_prompts (
    chat_id    BIGINT NOT NULL,
    -- The id of the QUESTION the bot sent. The reply points back at it.
    message_id BIGINT NOT NULL,

    -- What to do with the answer, e.g. 'post.title', 'post.body'.
    intent     TEXT NOT NULL,
    -- Usually a post id. Text rather than uuid: not every intent will be
    -- about a post, and a prompt that outlives its target must not become a
    -- foreign-key problem.
    target_id  TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- An unanswered question stops being an open question eventually. Without
    -- this, a reply to something from last week would silently rewrite a post.
    expires_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (chat_id, message_id)
);

CREATE INDEX idx_bot_prompts_expires ON bot_prompts (expires_at);
