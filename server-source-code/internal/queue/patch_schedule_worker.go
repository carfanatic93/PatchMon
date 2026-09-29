package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	hostctx "github.com/PatchMon/PatchMon/server-source-code/internal/context"
	"github.com/PatchMon/PatchMon/server-source-code/internal/database"
	"github.com/PatchMon/PatchMon/server-source-code/internal/db"
	"github.com/PatchMon/PatchMon/server-source-code/internal/notifications"
	"github.com/PatchMon/PatchMon/server-source-code/internal/pgtime"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// patchScheduleMisfireGrace is how late a dispatch may be before the run is
// skipped. After a long outage (or a paused scheduler) we must not suddenly
// patch every host at an unexpected hour; the schedule just advances to its
// next regular slot instead.
const patchScheduleMisfireGrace = time.Hour

// patchScheduleDispatchUniqueWindow bounds the per-minute dispatcher so a slow
// tick can't pile up duplicates behind it.
const patchScheduleDispatchUniqueWindow = 55 * time.Second

// PatchScheduleResult summarises one execution of a patch schedule. It is
// persisted as patch_schedules.last_run_result and returned by "run now".
type PatchScheduleResult struct {
	RanAt          string `json:"ran_at"`
	Trigger        string `json:"trigger"` // "schedule" | "manual"
	Status         string `json:"status"`  // "ok" | "skipped_missed" | "error"
	HostsTargeted  int    `json:"hosts_targeted"`
	HostsQueued    int    `json:"hosts_queued"`
	HostsUpToDate  int    `json:"hosts_up_to_date"`
	HostsBusy      int    `json:"hosts_busy"`
	HostsFailed    int    `json:"hosts_failed"`
	ErrorMessage   string `json:"error_message,omitempty"`
	TriggeredBy    string `json:"triggered_by,omitempty"`
	ScheduledForAt string `json:"scheduled_for_at,omitempty"`
}

// ExecutePatchSchedule creates a patch_all run for every host targeted by the
// schedule that has pending updates and no run already in flight, and enqueues
// it for immediate execution. tenantHost is the multi-host context domain ("" in
// single-host mode). userID is the manual trigger's user, nil for the scheduler.
func ExecutePatchSchedule(ctx context.Context, d *database.DB, qc *asynq.Client, s db.PatchSchedule, tenantHost, trigger string, userID *string, log *slog.Logger) (PatchScheduleResult, error) {
	res := PatchScheduleResult{
		RanAt:   time.Now().UTC().Format(time.RFC3339),
		Trigger: trigger,
		Status:  "ok",
	}
	if userID != nil {
		res.TriggeredBy = *userID
	}

	hosts, err := d.Queries.ListHostsForPatchSchedule(ctx, db.ListHostsForPatchScheduleParams{
		Column1: s.TargetType,
		Column2: s.TargetIds,
	})
	if err != nil {
		res.Status = "error"
		res.ErrorMessage = "list hosts: " + err.Error()
		return res, err
	}
	res.HostsTargeted = len(hosts)

	candidates := make([]db.ListHostsForPatchScheduleRow, 0, len(hosts))
	candidateIDs := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h.PendingUpdates == 0 {
			res.HostsUpToDate++
			continue
		}
		candidates = append(candidates, h)
		candidateIDs = append(candidateIDs, h.ID)
	}

	busy := map[string]bool{}
	if len(candidateIDs) > 0 {
		ids, err := d.Queries.ListHostIDsWithActivePatchRun(ctx, candidateIDs)
		if err != nil {
			res.Status = "error"
			res.ErrorMessage = "list active runs: " + err.Error()
			return res, err
		}
		for _, id := range ids {
			busy[id] = true
		}
	}

	snapshot, _ := json.Marshal(map[string]interface{}{
		"patch_delay_type":  "immediate",
		"schedule_id":       s.ID,
		"schedule_name":     s.Name,
		"schedule_cron":     s.CronExpr,
		"schedule_timezone": s.Timezone,
		"schedule_trigger":  trigger,
	})
	scheduleName := "Schedule: " + s.Name

	for _, h := range candidates {
		if busy[h.ID] {
			res.HostsBusy++
			continue
		}
		runID := uuid.New().String()
		jobID := "patch-run-" + runID
		err := d.Queries.CreatePatchRun(ctx, db.CreatePatchRunParams{
			ID:                runID,
			HostID:            h.ID,
			JobID:             jobID,
			PatchType:         "patch_all",
			Status:            "queued",
			TriggeredByUserID: userID,
			PolicyName:        &scheduleName,
			PolicySnapshot:    snapshot,
		})
		if err != nil {
			res.HostsFailed++
			if log != nil {
				log.Error("patch schedule: create run failed", "schedule_id", s.ID, "host_id", h.ID, "error", err)
			}
			continue
		}
		task, err := NewRunPatchTask(RunPatchPayload{
			HostID:     h.ID,
			Host:       tenantHost,
			ApiID:      h.ApiID,
			PatchRunID: runID,
			PatchType:  "patch_all",
		})
		if err == nil {
			_, err = qc.Enqueue(task)
		}
		if err != nil {
			// Don't leave an orphaned "queued" run that nothing will ever pick up.
			_ = d.Queries.DeletePatchRun(ctx, runID)
			res.HostsFailed++
			if log != nil {
				log.Error("patch schedule: enqueue failed", "schedule_id", s.ID, "host_id", h.ID, "error", err)
			}
			continue
		}
		res.HostsQueued++
	}

	if res.HostsFailed > 0 && res.HostsQueued == 0 && res.HostsTargeted > 0 {
		res.Status = "error"
		res.ErrorMessage = fmt.Sprintf("failed to queue patch runs for %d host(s)", res.HostsFailed)
	}
	return res, nil
}

// PersistPatchScheduleResult stores the outcome on the schedule row.
func PersistPatchScheduleResult(ctx context.Context, d *database.DB, scheduleID string, res PatchScheduleResult, log *slog.Logger) {
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	ranAt, perr := time.Parse(time.RFC3339, res.RanAt)
	if perr != nil {
		ranAt = time.Now()
	}
	if err := d.Queries.SetPatchScheduleRunResult(ctx, db.SetPatchScheduleRunResultParams{
		ID:            scheduleID,
		LastRunAt:     pgtime.From(ranAt),
		LastRunResult: b,
	}); err != nil && log != nil {
		log.Error("patch schedule: persist result failed", "schedule_id", scheduleID, "error", err)
	}
}

// PatchSchedulesDispatchHandler runs every minute and executes due schedules.
type PatchSchedulesDispatchHandler struct {
	defaultDB *database.DB
	poolCache *hostctx.PoolCache
	qc        *asynq.Client
	log       *slog.Logger
}

// NewPatchSchedulesDispatchHandler creates the handler.
func NewPatchSchedulesDispatchHandler(defaultDB *database.DB, poolCache *hostctx.PoolCache, qc *asynq.Client, log *slog.Logger) *PatchSchedulesDispatchHandler {
	return &PatchSchedulesDispatchHandler{defaultDB: defaultDB, poolCache: poolCache, qc: qc, log: log}
}

func (h *PatchSchedulesDispatchHandler) processDB(ctx context.Context, d *database.DB, tenantHost string) {
	now := time.Now()
	due, err := d.Queries.ListPatchSchedulesDue(ctx, pgtime.From(now))
	if err != nil {
		if h.log != nil {
			h.log.Error("patch_schedules_dispatch: list due", "error", err)
		}
		return
	}
	for _, s := range due {
		next, err := notifications.NextCronRun(s.CronExpr, s.Timezone, now)
		if err != nil {
			if h.log != nil {
				h.log.Error("patch_schedules_dispatch: invalid cron, skipping", "schedule_id", s.ID, "cron", s.CronExpr, "error", err)
			}
			continue
		}
		missed := now.Sub(s.NextRunAt.Time) > patchScheduleMisfireGrace
		lastRun := pgtime.From(now)
		if missed {
			lastRun = s.LastRunAt // keep the real last execution time
		}
		// Claim first: only one dispatcher wins the UPDATE for this due time.
		n, err := d.Queries.ClaimPatchScheduleRun(ctx, db.ClaimPatchScheduleRunParams{
			ID:          s.ID,
			NextRunAt:   s.NextRunAt,
			LastRunAt:   lastRun,
			NextRunAt_2: pgtime.From(next),
		})
		if err != nil {
			if h.log != nil {
				h.log.Error("patch_schedules_dispatch: claim failed", "schedule_id", s.ID, "error", err)
			}
			continue
		}
		if n == 0 {
			continue
		}

		scheduledFor := s.NextRunAt.Time.UTC().Format(time.RFC3339)
		if missed {
			res := PatchScheduleResult{
				RanAt:          now.UTC().Format(time.RFC3339),
				Trigger:        "schedule",
				Status:         "skipped_missed",
				ErrorMessage:   "Run was more than 1h overdue (server or scheduler was down); skipped until the next slot.",
				ScheduledForAt: scheduledFor,
			}
			b, _ := json.Marshal(res)
			_ = d.Queries.SetPatchScheduleRunResult(ctx, db.SetPatchScheduleRunResultParams{
				ID: s.ID, LastRunAt: s.LastRunAt, LastRunResult: b,
			})
			if h.log != nil {
				h.log.Warn("patch_schedules_dispatch: run skipped (misfire)", "schedule_id", s.ID, "scheduled_for", scheduledFor)
			}
			continue
		}

		res, err := ExecutePatchSchedule(ctx, d, h.qc, s, tenantHost, "schedule", nil, h.log)
		res.ScheduledForAt = scheduledFor
		PersistPatchScheduleResult(ctx, d, s.ID, res, h.log)
		if h.log != nil {
			if err != nil {
				h.log.Error("patch_schedules_dispatch: execution failed", "schedule_id", s.ID, "error", err)
			} else {
				h.log.Info("patch_schedules_dispatch: executed",
					"schedule_id", s.ID, "name", s.Name,
					"targeted", res.HostsTargeted, "queued", res.HostsQueued,
					"up_to_date", res.HostsUpToDate, "busy", res.HostsBusy, "failed", res.HostsFailed)
			}
		}
	}
}

// ProcessTask implements asynq.Handler.
func (h *PatchSchedulesDispatchHandler) ProcessTask(ctx context.Context, _ *asynq.Task) error {
	forEachDB(ctx, h.defaultDB, h.poolCache, h.processDB)
	return nil
}
