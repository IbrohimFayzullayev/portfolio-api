-- name: CreateContactMessage :one
INSERT INTO contact_messages (name, email, message, locale, user_agent)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListContactMessages :many
SELECT * FROM contact_messages
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountContactMessages :one
SELECT count(*) FROM contact_messages;

-- name: CountUnreadContactMessages :one
SELECT count(*) FROM contact_messages WHERE read_at IS NULL;

-- name: MarkContactMessageRead :execrows
UPDATE contact_messages
SET read_at = COALESCE(read_at, now())
WHERE id = $1;

-- name: DeleteContactMessage :exec
DELETE FROM contact_messages WHERE id = $1;
