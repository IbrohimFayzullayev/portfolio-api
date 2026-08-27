-- name: CreateInvitation :one
INSERT INTO invitations (
    source, session_id, event_date, event_time,
    food_id, food_label, food_emoji,
    place_id, place_label, place_emoji,
    invite_text, user_agent
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING *;

-- name: ListInvitations :many
SELECT * FROM invitations
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: GetInvitationByID :one
SELECT * FROM invitations WHERE id = $1;

-- name: CountInvitations :one
SELECT count(*) FROM invitations;

-- name: DeleteInvitation :exec
DELETE FROM invitations WHERE id = $1;
