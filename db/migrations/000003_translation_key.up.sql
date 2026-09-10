-- Links the two language versions of the same piece of content.
--
-- Until now uz and en rows were only related by convention, so hreflang on the
-- public site pointed each locale at its OWN slug — a lie whenever the slugs
-- differ ("nextjs-ssg-isr-amaliyot" vs "nextjs-ssg-isr-in-practice").
--
-- Empty string means "no sibling", which is the correct default for a piece of
-- content that exists in one language only.

ALTER TABLE posts    ADD COLUMN translation_key TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN translation_key TEXT NOT NULL DEFAULT '';

-- Partial: rows without a key are never looked up by it.
CREATE INDEX idx_posts_translation_key
    ON posts (translation_key) WHERE translation_key <> '';
CREATE INDEX idx_projects_translation_key
    ON projects (translation_key) WHERE translation_key <> '';

-- A key may appear at most once per locale — two uz posts claiming to be the
-- same content is always a mistake.
CREATE UNIQUE INDEX idx_posts_translation_key_locale
    ON posts (translation_key, locale) WHERE translation_key <> '';
CREATE UNIQUE INDEX idx_projects_translation_key_locale
    ON projects (translation_key, locale) WHERE translation_key <> '';
