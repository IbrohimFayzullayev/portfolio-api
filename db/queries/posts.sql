-- name: ListPosts :many
SELECT * FROM posts
WHERE (sqlc.narg('locale')::text IS NULL OR locale = sqlc.narg('locale'))
  AND (sqlc.narg('draft')::boolean IS NULL OR draft = sqlc.narg('draft'))
ORDER BY content_date DESC, created_at DESC;

-- name: GetPostByID :one
SELECT * FROM posts WHERE id = $1;

-- name: CreatePost :one
INSERT INTO posts (
    locale, slug, title, description, body, tags, cover,
    featured, draft, content_date, published_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING *;

-- name: UpdatePost :one
UPDATE posts
SET locale = $2,
    slug = $3,
    title = $4,
    description = $5,
    body = $6,
    tags = $7,
    cover = $8,
    featured = $9,
    draft = $10,
    content_date = $11,
    published_at = $12,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetPostPublished :one
UPDATE posts
SET draft = $2,
    published_at = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeletePost :exec
DELETE FROM posts WHERE id = $1;

-- name: ListPublishedPosts :many
SELECT * FROM posts
WHERE draft = false
  AND (sqlc.narg('locale')::text IS NULL OR locale = sqlc.narg('locale'))
ORDER BY content_date DESC, created_at DESC;

-- name: GetPublishedPostBySlug :one
SELECT * FROM posts
WHERE draft = false AND locale = $1 AND slug = $2;

-- name: UpsertPost :one
INSERT INTO posts (
    locale, slug, title, description, body, tags, cover,
    featured, draft, content_date, published_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (locale, slug) DO UPDATE SET
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    body = EXCLUDED.body,
    tags = EXCLUDED.tags,
    cover = EXCLUDED.cover,
    featured = EXCLUDED.featured,
    draft = EXCLUDED.draft,
    content_date = EXCLUDED.content_date,
    published_at = EXCLUDED.published_at,
    updated_at = now()
RETURNING *;
