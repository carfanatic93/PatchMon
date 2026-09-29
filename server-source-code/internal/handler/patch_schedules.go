package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/PatchMon/PatchMon/server-source-code/internal/db"
	"github.com/PatchMon/PatchMon/server-source-code/internal/middleware"
	"github.com/PatchMon/PatchMon/server-source-code/internal/notifications"
	"github.com/PatchMon/PatchMon/server-source-code/internal/pgtime"
	"github.com/PatchMon/PatchMon/server-source-code/internal/queue"
	"github.com/PatchMon/PatchMon/server-source-code/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxPatchScheduleTargets = 500

// SetScheduleStore wires the recurring patch schedules store. Kept as a setter so
// the existing NewPatchingHandler call sites don't change.
func (h *PatchingHandler) SetScheduleStore(s *store.PatchSchedulesStore) {
	h.schedules = s
}

// patchScheduleRequest is the create/update body.
type patchScheduleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	CronExpr    string   `json:"cron_expr"`
	Timezone    string   `json:"timezone"`
	Enabled     *bool    `json:"enabled"`
	TargetType  string   `json:"target_type"`
	TargetIDs   []string `json:"target_ids"`
}

// validatePatchSchedule normalises the request and returns a user-facing error message ("" if valid).
func (h *PatchingHandler) validatePatchSchedule(r *http.Request, req *patchScheduleRequest) string {
	req.Name = strings.TrimSpace(req.Name)
	req.CronExpr = strings.Join(strings.Fields(req.CronExpr), " ")
	if req.Name == "" {
		return "name is required"
	}
	if len(req.Name) > 100 {
		return "name must be at most 100 characters"
	}
	// Exactly five fields: this rejects descriptors such as "@every 1s" that
	// would otherwise turn a schedule into a patch-storm.
	if len(strings.Fields(req.CronExpr)) != 5 {
		return "cron_expr must be a standard 5-field cron expression (minute hour day-of-month month day-of-week)"
	}
	if strings.TrimSpace(req.Timezone) == "" {
		req.Timezone = h.resolveOrgTimezone(r.Context())
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		return "timezone must be a valid IANA timezone (e.g. Europe/Berlin)"
	}
	if _, err := notifications.NextCronRun(req.CronExpr, req.Timezone, time.Now()); err != nil {
		return "cron_expr is not a valid cron expression"
	}
	switch req.TargetType {
	case "all":
		req.TargetIDs = []string{}
	case "host", "host_group":
		if len(req.TargetIDs) == 0 {
			return "target_ids is required for target_type " + req.TargetType
		}
		if len(req.TargetIDs) > maxPatchScheduleTargets {
			return "too many targets"
		}
		for _, id := range req.TargetIDs {
			if !isValidPatchUUID(id) {
				return "target_ids must contain valid IDs"
			}
		}
	default:
		return "target_type must be all, host_group or host"
	}
	return ""
}

func patchScheduleToMap(s db.PatchSchedule) map[string]interface{} {
	var lastResult interface{}
	if len(s.LastRunResult) > 0 {
		_ = json.Unmarshal(s.LastRunResult, &lastResult)
	}
	ids := s.TargetIds
	if ids == nil {
		ids = []string{}
	}
	return map[string]interface{}{
		"id":              s.ID,
		"name":            s.Name,
		"description":     s.Description,
		"cron_expr":       s.CronExpr,
		"timezone":        s.Timezone,
		"enabled":         s.Enabled,
		"target_type":     s.TargetType,
		"target_ids":      ids,
		"next_run_at":     pgTimeToISO(s.NextRunAt),
		"last_run_at":     pgTimeToISO(s.LastRunAt),
		"last_run_result": lastResult,
		"created_at":      pgTimeToISO(s.CreatedAt),
		"updated_at":      pgTimeToISO(s.UpdatedAt),
	}
}

func (h *PatchingHandler) scheduleStoreReady(w http.ResponseWriter) bool {
	if h.schedules == nil {
		Error(w, http.StatusServiceUnavailable, "Patch schedules are not available")
		return false
	}
	return true
}

// ListSchedules handles GET /patching/schedules.
func (h *PatchingHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	if !h.scheduleStoreReady(w) {
		return
	}
	rows, err := h.schedules.List(r.Context())
	if err != nil {
		h.log.Error("patching: list schedules", "error", err)
		Error(w, http.StatusInternalServerError, "Failed to list schedules")
		return
	}
	out := make([]map[string]interface{}, len(rows))
	for i := range rows {
		out[i] = patchScheduleToMap(rows[i])
	}
	JSON(w, http.StatusOK, out)
}

// PreviewScheduleRuns handles GET /patching/schedules/preview?cron_expr=&timezone=
// and returns the next five fire times so the UI can confirm what a cron means.
func (h *PatchingHandler) PreviewScheduleRuns(w http.ResponseWriter, r *http.Request) {
	req := patchScheduleRequest{
		Name:       "preview",
		CronExpr:   r.URL.Query().Get("cron_expr"),
		Timezone:   r.URL.Query().Get("timezone"),
		TargetType: "all",
	}
	if msg := h.validatePatchSchedule(r, &req); msg != "" {
		Error(w, http.StatusBadRequest, msg)
		return
	}
	runs := make([]string, 0, 5)
	from := time.Now()
	for i := 0; i < 5; i++ {
		next, err := notifications.NextCronRun(req.CronExpr, req.Timezone, from)
		if err != nil || next.IsZero() {
			break
		}
		runs = append(runs, next.UTC().Format(time.RFC3339))
		from = next
	}
	JSON(w, http.StatusOK, map[string]interface{}{"timezone": req.Timezone, "next_runs": runs})
}

// CreateSchedule handles POST /patching/schedules.
func (h *PatchingHandler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	if !h.scheduleStoreReady(w) {
		return
	}
	var req patchScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		Error(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if msg := h.validatePatchSchedule(r, &req); msg != "" {
		Error(w, http.StatusBadRequest, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	next, err := notifications.NextCronRun(req.CronExpr, req.Timezone, time.Now())
	if err != nil {
		Error(w, http.StatusBadRequest, "cron_expr is not a valid cron expression")
		return
	}
	var createdBy *string
	if userID, _ := r.Context().Value(middleware.UserIDKey).(string); userID != "" {
		createdBy = &userID
	}
	row, err := h.schedules.Create(r.Context(), db.CreatePatchScheduleParams{
		ID:              uuid.New().String(),
		Name:            req.Name,
		Description:     strings.TrimSpace(req.Description),
		CronExpr:        req.CronExpr,
		Timezone:        req.Timezone,
		Enabled:         enabled,
		TargetType:      req.TargetType,
		TargetIds:       req.TargetIDs,
		NextRunAt:       pgtime.From(next),
		CreatedByUserID: createdBy,
	})
	if err != nil {
		h.log.Error("patching: create schedule", "error", err)
		Error(w, http.StatusInternalServerError, "Failed to create schedule")
		return
	}
	JSON(w, http.StatusCreated, patchScheduleToMap(*row))
}

// UpdateSchedule handles PUT /patching/schedules/{id}.
func (h *PatchingHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if !h.scheduleStoreReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !isValidPatchUUID(id) {
		Error(w, http.StatusBadRequest, "Invalid schedule ID")
		return
	}
	existing, err := h.schedules.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			Error(w, http.StatusNotFound, "Schedule not found")
			return
		}
		Error(w, http.StatusInternalServerError, "Failed to load schedule")
		return
	}
	var req patchScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		Error(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if msg := h.validatePatchSchedule(r, &req); msg != "" {
		Error(w, http.StatusBadRequest, msg)
		return
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// Any edit re-anchors the next run to the (possibly new) cron/timezone.
	next, err := notifications.NextCronRun(req.CronExpr, req.Timezone, time.Now())
	if err != nil {
		Error(w, http.StatusBadRequest, "cron_expr is not a valid cron expression")
		return
	}
	row, err := h.schedules.Update(r.Context(), db.UpdatePatchScheduleParams{
		ID:          id,
		Name:        req.Name,
		Description: strings.TrimSpace(req.Description),
		CronExpr:    req.CronExpr,
		Timezone:    req.Timezone,
		Enabled:     enabled,
		TargetType:  req.TargetType,
		TargetIds:   req.TargetIDs,
		NextRunAt:   pgtime.From(next),
	})
	if err != nil {
		h.log.Error("patching: update schedule", "error", err)
		Error(w, http.StatusInternalServerError, "Failed to update schedule")
		return
	}
	JSON(w, http.StatusOK, patchScheduleToMap(*row))
}

// DeleteSchedule handles DELETE /patching/schedules/{id}.
func (h *PatchingHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !h.scheduleStoreReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !isValidPatchUUID(id) {
		Error(w, http.StatusBadRequest, "Invalid schedule ID")
		return
	}
	if err := h.schedules.Delete(r.Context(), id); err != nil {
		h.log.Error("patching: delete schedule", "error", err)
		Error(w, http.StatusInternalServerError, "Failed to delete schedule")
		return
	}
	JSON(w, http.StatusOK, map[string]string{"message": "Schedule deleted"})
}

// RunScheduleNow handles POST /patching/schedules/{id}/run-now. It executes the
// schedule immediately without touching its regular next_run_at.
func (h *PatchingHandler) RunScheduleNow(w http.ResponseWriter, r *http.Request) {
	if !h.scheduleStoreReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !isValidPatchUUID(id) {
		Error(w, http.StatusBadRequest, "Invalid schedule ID")
		return
	}
	sched, err := h.schedules.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			Error(w, http.StatusNotFound, "Schedule not found")
			return
		}
		Error(w, http.StatusInternalServerError, "Failed to load schedule")
		return
	}
	var userID *string
	if uid, _ := r.Context().Value(middleware.UserIDKey).(string); uid != "" {
		userID = &uid
	}
	d := h.schedules.DB(r.Context())
	res, err := queue.ExecutePatchSchedule(r.Context(), d, h.queueClient, *sched, hostFromRequest(r), "manual", userID, h.log)
	if err != nil {
		h.log.Error("patching: run schedule now", "schedule_id", id, "error", err)
		Error(w, http.StatusInternalServerError, "Failed to run schedule")
		return
	}
	queue.PersistPatchScheduleResult(r.Context(), d, id, res, h.log)
	JSON(w, http.StatusOK, res)
}
