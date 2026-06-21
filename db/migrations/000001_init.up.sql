CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE posts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    locale       TEXT NOT NULL,
    slug         TEXT NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    tags         TEXT[] NOT NULL DEFAULT '{}',
    cover        TEXT NOT NULL DEFAULT '',
    featured     BOOLEAN NOT NULL DEFAULT false,
    draft        BOOLEAN NOT NULL DEFAULT true,
    content_date DATE NOT NULL DEFAULT CURRENT_DATE,
    published_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (locale, slug)
);

CREATE INDEX idx_posts_locale ON posts (locale);
CREATE INDEX idx_posts_draft ON posts (draft);
CREATE INDEX idx_posts_content_date ON posts (content_date DESC);

CREATE TABLE projects (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    locale       TEXT NOT NULL,
    slug         TEXT NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    tags         TEXT[] NOT NULL DEFAULT '{}',
    stack        TEXT[] NOT NULL DEFAULT '{}',
    url          TEXT NOT NULL DEFAULT '',
    repo         TEXT NOT NULL DEFAULT '',
    sort_order   INTEGER NOT NULL DEFAULT 0,
    featured     BOOLEAN NOT NULL DEFAULT false,
    draft        BOOLEAN NOT NULL DEFAULT true,
    content_date DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (locale, slug)
);

CREATE INDEX idx_projects_locale ON projects (locale);
CREATE INDEX idx_projects_sort_order ON projects (sort_order DESC);
