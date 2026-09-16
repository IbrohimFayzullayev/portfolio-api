-- Scheduled publishing.
--
-- The bot's scheduler flips the row when the moment arrives; the site's ISR
-- window is 60 seconds, so "tomorrow at 10:00" means the post is live within a
-- minute of ten. Nothing else changes: the same draft → published edge fires
-- the same post.published notification, whether a human or the clock caused it.
--
-- publish_at is cleared when it fires, so the column always means "waiting",
-- never "was published at some point" — published_at already answers that.

ALTER TABLE posts ADD COLUMN publish_at TIMESTAMPTZ;

-- Partial: the scheduler only ever asks for drafts with a date on them, and
-- that is a handful of rows out of the table.
CREATE INDEX idx_posts_publish_at
    ON posts (publish_at)
    WHERE publish_at IS NOT NULL AND draft;
