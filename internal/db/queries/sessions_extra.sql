-- name: DeleteOtherSessions :execrows
-- Signs a member out everywhere except the session with this token hash
-- (used after a password change).
DELETE FROM sessions WHERE member_id = $1 AND token_hash <> sqlc.arg(keep_token_hash);
