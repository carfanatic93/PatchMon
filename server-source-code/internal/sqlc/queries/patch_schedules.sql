-- name: ListPatchSchedules :many
SELECT * FROM patch_schedules ORDER BY name;

-- name: GetPatchScheduleByID :one
SELECT * FROM patch_schedules WHERE id = $1;

-- name: ListPatchSchedulesDue :many
SELECT * FROM patch_schedules
WHERE enabled = true AND next_run_at IS NOT NULL AND next_run_at <= $1
ORDER BY next_run_at;

-- name: CreatePatchSchedule :one
INSERT INTO patch_schedules (id, name, description, cron_expr, timezone, enabled, target_type, target_ids, next_run_at, created_by_user_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
RETURNING *;

-- name: UpdatePatchSchedule :one
UPDATE patch_schedules
SET name = $2, description = $3, cron_expr = $4, timezone = $5, enabled = $6, target_type = $7, target_ids = $8, next_run_at = $9, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ClaimPatchScheduleRun :execrows
-- The next_run_at equality guard is the concurrency control: if two dispatchers
-- (or a dispatcher and an edit) race, only one UPDATE matches the old value, so
-- a schedule is executed at most once per due time.
UPDATE patch_schedules
SET last_run_at = $3, next_run_at = $4, updated_at = NOW()
WHERE id = $1 AND enabled = true AND next_run_at = $2;

-- name: SetPatchScheduleRunResult :exec
UPDATE patch_schedules SET last_run_at = $2, last_run_result = $3, updated_at = NOW() WHERE id = $1;

-- name: DeletePatchSchedule :exec
DELETE FROM patch_schedules WHERE id = $1;

-- name: ListHostsForPatchSchedule :many
-- Active, non-Windows hosts in the schedule's target with their pending-update count.
SELECT h.id, h.api_id, h.os_type,
       (SELECT COUNT(*) FROM host_packages hp WHERE hp.host_id = h.id AND hp.needs_update) AS pending_updates
FROM hosts h
WHERE h.status = 'active'
  AND LOWER(h.os_type) NOT LIKE '%windows%'
  AND (
    $1::text = 'all'
    OR ($1::text = 'host' AND h.id = ANY($2::text[]))
    OR ($1::text = 'host_group' AND EXISTS (
        SELECT 1 FROM host_group_memberships m
        WHERE m.host_id = h.id AND m.host_group_id = ANY($2::text[])
    ))
  )
ORDER BY h.id;

-- name: ListHostIDsWithActivePatchRun :many
SELECT DISTINCT host_id FROM patch_runs
WHERE host_id = ANY($1::text[])
  AND dry_run = false
  AND status IN ('queued', 'running', 'pending_approval');
