-- name: InsertJob :one
-- A job whose dedupe_key the household already used returns no row.
-- run_after NULL means now, by the database's clock.
INSERT INTO jobs (household_id, kind, dedupe_key, plan_id, meal_id, member_id, payload, run_after)
VALUES ($1, $2, sqlc.narg(dedupe_key), sqlc.narg(plan_id), sqlc.narg(meal_id), sqlc.narg(member_id), $3,
        coalesce(sqlc.narg(run_after)::timestamptz, now()))
ON CONFLICT (household_id, dedupe_key) DO NOTHING
RETURNING *;

-- name: ClaimJob :one
-- Takes the oldest due job; concurrent workers skip each other's rows.
UPDATE jobs SET status = 'running', started_at = now(), attempts = attempts + 1
WHERE id = (
    SELECT id FROM jobs WHERE status = 'queued' AND run_after <= now()
    ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1
)
RETURNING *;

-- name: FinishJob :exec
UPDATE jobs SET status = 'done', result = $2, finished_at = now() WHERE id = $1;

-- name: FailJob :exec
UPDATE jobs SET status = 'failed', error = $2, finished_at = now() WHERE id = $1;

-- name: RecoverStaleJobs :execrows
-- Jobs left running by a crash: run again, or give up after three tries.
UPDATE jobs SET
    status = CASE WHEN attempts >= 3 THEN 'failed' ELSE 'queued' END,
    error  = CASE WHEN attempts >= 3 THEN 'stopped while running' ELSE error END,
    finished_at = CASE WHEN attempts >= 3 THEN now() END
WHERE status = 'running' AND started_at < sqlc.arg(before)::timestamptz;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1 AND household_id = $2;

-- name: LatestJob :one
SELECT * FROM jobs WHERE household_id = $1 AND kind = $2
ORDER BY created_at DESC LIMIT 1;

-- name: LatestMealJob :one
SELECT * FROM jobs WHERE household_id = $1 AND meal_id = $2
ORDER BY created_at DESC LIMIT 1;

-- name: ListPendingJobs :many
SELECT * FROM jobs WHERE household_id = $1 AND status IN ('queued', 'running')
ORDER BY created_at;
