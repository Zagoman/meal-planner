-- Households, members, memberships, invites, sessions and API tokens.
-- Lookups by token hash are the only queries without a household_id filter:
-- they are how the caller's household is discovered in the first place.

-- name: CreateHousehold :one
INSERT INTO households (name) VALUES ($1) RETURNING *;

-- name: GetHousehold :one
SELECT * FROM households WHERE id = $1;

-- name: ListHouseholds :many
SELECT * FROM households ORDER BY created_at;

-- name: CreateMember :one
INSERT INTO members (email, name, password_hash, is_server_admin)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetMember :one
SELECT * FROM members WHERE id = $1;

-- name: GetMemberByEmail :one
SELECT * FROM members WHERE lower(email) = lower(sqlc.arg(email));

-- name: UpdateMemberPassword :exec
UPDATE members SET password_hash = $2 WHERE id = $1;

-- name: SetServerAdmin :exec
UPDATE members SET is_server_admin = $2 WHERE id = $1;

-- name: AddHouseholdMember :exec
INSERT INTO household_members (household_id, member_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (household_id, member_id) DO NOTHING;

-- name: GetMembership :one
SELECT * FROM household_members WHERE household_id = $1 AND member_id = $2;

-- name: ListHouseholdMembers :many
SELECT m.id, m.email, m.name, hm.role, hm.created_at AS joined_at
FROM household_members hm
JOIN members m ON m.id = hm.member_id
WHERE hm.household_id = $1
ORDER BY hm.created_at;

-- name: ListMemberHouseholds :many
SELECT h.id, h.name, hm.role
FROM household_members hm
JOIN households h ON h.id = hm.household_id
WHERE hm.member_id = $1
ORDER BY h.name;

-- name: RemoveHouseholdMember :exec
DELETE FROM household_members WHERE household_id = $1 AND member_id = $2;

-- name: CreateInvite :one
INSERT INTO invites (household_id, token_hash, role, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetInviteByTokenHash :one
SELECT * FROM invites WHERE token_hash = $1;

-- name: ListInvites :many
SELECT * FROM invites WHERE household_id = $1 ORDER BY created_at DESC;

-- name: AcceptInvite :execrows
UPDATE invites SET accepted_at = now(), accepted_by = sqlc.arg(member_id)
WHERE id = sqlc.arg(id) AND household_id = sqlc.arg(household_id)
  AND accepted_at IS NULL AND expires_at > now();

-- name: DeleteInvite :exec
DELETE FROM invites WHERE id = $1 AND household_id = $2;

-- name: CreateSession :one
INSERT INTO sessions (household_id, member_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = $1 AND expires_at > now();

-- name: SwitchSessionHousehold :exec
UPDATE sessions SET household_id = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= now();

-- name: CreateAPIToken :one
INSERT INTO api_tokens (household_id, member_id, name, token_hash)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetAPITokenByHash :one
SELECT * FROM api_tokens WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;

-- name: ListAPITokens :many
SELECT * FROM api_tokens WHERE household_id = $1 AND member_id = $2 ORDER BY created_at DESC;

-- name: RevokeAPIToken :exec
UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND household_id = $2 AND member_id = $3;

-- name: CreateLoginToken :one
INSERT INTO login_tokens (household_id, member_id, invite_id, token_hash, expires_at)
VALUES ($1, $2, sqlc.narg(invite_id), $3, $4) RETURNING *;

-- name: UseLoginToken :one
-- Single use: only an unused, unexpired link matches, even under races.
UPDATE login_tokens SET used_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: RecentLoginTokens :one
-- Links sent to a member in the last minute (throttles "email me a link").
SELECT count(*) FROM login_tokens WHERE member_id = $1 AND created_at > now() - interval '1 minute';

-- name: GetInvite :one
SELECT * FROM invites WHERE id = $1 AND household_id = $2;

-- name: SetMemberName :exec
UPDATE members SET name = $2 WHERE id = $1 AND name = '';

-- name: LatestMembership :one
-- The household a member signs in to by default: the one joined last.
SELECT * FROM household_members WHERE member_id = $1 ORDER BY created_at DESC LIMIT 1;

-- name: ListMemberEmails :many
SELECT m.email, m.name FROM household_members hm JOIN members m ON m.id = hm.member_id
WHERE hm.household_id = $1 ORDER BY hm.created_at;

-- name: SetOwnMemberName :one
-- A member renames themself, only through a household they belong to (so
-- a scope for another household matches nothing).
UPDATE members m SET name = sqlc.arg(name)
FROM household_members hm
WHERE m.id = sqlc.arg(member_id) AND hm.member_id = m.id AND hm.household_id = sqlc.arg(household_id)
RETURNING m.id, m.email, m.name;
