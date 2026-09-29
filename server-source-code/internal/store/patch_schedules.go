package store

import (
	"context"

	"github.com/PatchMon/PatchMon/server-source-code/internal/database"
	"github.com/PatchMon/PatchMon/server-source-code/internal/db"
)

// PatchSchedulesStore provides access to recurring patch schedules.
type PatchSchedulesStore struct {
	db database.DBProvider
}

// NewPatchSchedulesStore creates a new patch schedules store.
func NewPatchSchedulesStore(db database.DBProvider) *PatchSchedulesStore {
	return &PatchSchedulesStore{db: db}
}

// DB returns the database for the request context (per-tenant in multi-host mode).
func (s *PatchSchedulesStore) DB(ctx context.Context) *database.DB {
	return s.db.DB(ctx)
}

// List returns all schedules ordered by name.
func (s *PatchSchedulesStore) List(ctx context.Context) ([]db.PatchSchedule, error) {
	return s.db.DB(ctx).Queries.ListPatchSchedules(ctx)
}

// GetByID returns a schedule by ID.
func (s *PatchSchedulesStore) GetByID(ctx context.Context, id string) (*db.PatchSchedule, error) {
	row, err := s.db.DB(ctx).Queries.GetPatchScheduleByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Create inserts a schedule.
func (s *PatchSchedulesStore) Create(ctx context.Context, p db.CreatePatchScheduleParams) (*db.PatchSchedule, error) {
	row, err := s.db.DB(ctx).Queries.CreatePatchSchedule(ctx, p)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Update replaces a schedule's editable fields.
func (s *PatchSchedulesStore) Update(ctx context.Context, p db.UpdatePatchScheduleParams) (*db.PatchSchedule, error) {
	row, err := s.db.DB(ctx).Queries.UpdatePatchSchedule(ctx, p)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Delete removes a schedule. Patch runs it created are kept.
func (s *PatchSchedulesStore) Delete(ctx context.Context, id string) error {
	return s.db.DB(ctx).Queries.DeletePatchSchedule(ctx, id)
}
