DROP INDEX IF EXISTS idx_projects_translation_key_locale;
DROP INDEX IF EXISTS idx_posts_translation_key_locale;
DROP INDEX IF EXISTS idx_projects_translation_key;
DROP INDEX IF EXISTS idx_posts_translation_key;

ALTER TABLE projects DROP COLUMN IF EXISTS translation_key;
ALTER TABLE posts    DROP COLUMN IF EXISTS translation_key;
