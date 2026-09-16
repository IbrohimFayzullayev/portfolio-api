DROP INDEX IF EXISTS idx_posts_publish_at;
ALTER TABLE posts DROP COLUMN IF EXISTS publish_at;
