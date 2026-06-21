-- name: ListProjects :many
SELECT * FROM projects
WHERE (sqlc.narg('locale')::text IS NULL OR locale = sqlc.narg('locale'))
  AND (sqlc.narg('draft')::boolean IS NULL OR draft = sqlc.narg('draft'))
ORDER BY sort_order DESC, content_date DESC;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: CreateProject :one
INSERT INTO projects (
    locale, slug, title, description, body, tags, stack, url, repo,
    sort_order, featured, draft, content_date
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
RETURNING *;

-- name: UpdateProject :one
UPDATE projects
SET locale = $2,
    slug = $3,
    title = $4,
    description = $5,
    body = $6,
    tags = $7,
    stack = $8,
    url = $9,
    repo = $10,
    sort_order = $11,
    featured = $12,
    draft = $13,
    content_date = $14,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1;

-- name: ListPublishedProjects :many
SELECT * FROM projects
WHERE draft = false
  AND (sqlc.narg('locale')::text IS NULL OR locale = sqlc.narg('locale'))
ORDER BY sort_order DESC, content_date DESC;

-- name: GetPublishedProjectBySlug :one
SELECT * FROM projects
WHERE draft = false AND locale = $1 AND slug = $2;

-- name: UpsertProject :one
INSERT INTO projects (
    locale, slug, title, description, body, tags, stack, url, repo,
    sort_order, featured, draft, content_date
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
ON CONFLICT (locale, slug) DO UPDATE SET
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    body = EXCLUDED.body,
    tags = EXCLUDED.tags,
    stack = EXCLUDED.stack,
    url = EXCLUDED.url,
    repo = EXCLUDED.repo,
    sort_order = EXCLUDED.sort_order,
    featured = EXCLUDED.featured,
    draft = EXCLUDED.draft,
    content_date = EXCLUDED.content_date,
    updated_at = now()
RETURNING *;
