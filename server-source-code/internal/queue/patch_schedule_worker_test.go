package queue

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/PatchMon/PatchMon/server-source-code/internal/config"
	"github.com/PatchMon/PatchMon/server-source-code/internal/database"
	"github.com/PatchMon/PatchMon/server-source-code/internal/db"
	"github.com/PatchMon/PatchMon/server-source-code/internal/migrate"
	"github.com/PatchMon/PatchMon/server-source-code/internal/pgtime"
	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
)

// Integration test: needs a disposable Postgres, e.g.
//
//	PATCHMON_TEST_DATABASE_URL=postgres://user:pass@localhost:5432/test?sslmode=disable go test ./internal/queue -run PatchSchedule
func TestPatchScheduleDispatch(t *testing.T) {
	url := os.Getenv("PATCHMON_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PATCHMON_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := migrate.Run(url, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	d, err := database.NewFromURL(ctx, url, 2, 1, &config.Config{})
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer d.Close()
	q := d.Queries

	mr := miniredis.RunT(t)
	rc := asynq.RedisClientOpt{Addr: mr.Addr()}
	qc := asynq.NewClient(rc)
	defer func() { _ = qc.Close() }()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	_ = q
	exec(`DELETE FROM patch_runs; DELETE FROM patch_schedules; DELETE FROM host_group_memberships; DELETE FROM host_packages; DELETE FROM hosts; DELETE FROM host_groups; DELETE FROM packages`)
	host := func(id, os string) {
		exec(`INSERT INTO hosts (id, friendly_name, os_type, os_version, api_id, api_key, updated_at) VALUES ($1,$1,$2,'1',$1||'-api',$1||'-key',NOW())`, id, os)
	}
	host("h-pending", "ubuntu")
	host("h-uptodate", "ubuntu")
	host("h-windows", "Windows Server 2022")
	host("h-busy", "debian")
	host("h-grouped", "debian")
	exec(`INSERT INTO packages (id, name, updated_at) VALUES ('p1','curl',NOW())`)
	for _, h := range []string{"h-pending", "h-windows", "h-busy", "h-grouped"} {
		exec(`INSERT INTO host_packages (id, host_id, package_id, current_version, needs_update) VALUES ($1||'-hp',$1,'p1','1',true)`, h)
	}
	exec(`INSERT INTO host_packages (id, host_id, package_id, current_version, needs_update) VALUES ('h-uptodate-hp','h-uptodate','p1','1',false)`)
	exec(`INSERT INTO patch_runs (id, host_id, job_id, patch_type, status, updated_at) VALUES ('busy-run','h-busy','patch-run-busy-run','patch_all','running',NOW())`)
	exec(`INSERT INTO host_groups (id, name, updated_at) VALUES ('g1','grp',NOW())`)
	exec(`INSERT INTO host_group_memberships (id, host_id, host_group_id) VALUES ('m1','h-grouped','g1')`)

	mk := func(id, target string, ids []string, next time.Time) db.PatchSchedule {
		s, err := q.CreatePatchSchedule(ctx, db.CreatePatchScheduleParams{
			ID: id, Name: id, CronExpr: "0 12 * * 0", Timezone: "Europe/Berlin", Enabled: true,
			TargetType: target, TargetIds: ids, NextRunAt: pgtime.From(next),
		})
		if err != nil {
			t.Fatalf("create schedule: %v", err)
		}
		return s
	}

	t.Run("all hosts: skips up-to-date, windows and busy", func(t *testing.T) {
		s := mk("s-all", "all", []string{}, time.Now().Add(time.Hour))
		res, err := ExecutePatchSchedule(ctx, d, qc, s, "", "manual", nil, log)
		if err != nil {
			t.Fatal(err)
		}
		// targeted excludes windows in SQL: pending, uptodate, busy, grouped
		if res.HostsTargeted != 4 || res.HostsQueued != 2 || res.HostsUpToDate != 1 || res.HostsBusy != 1 {
			t.Fatalf("unexpected result: %+v", res)
		}
		var n int
		if err := d.RawQueryRow(ctx, `SELECT COUNT(*) FROM patch_runs WHERE policy_name = 'Schedule: s-all' AND status = 'queued' AND patch_type = 'patch_all'`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("queued runs = %d, err %v", n, err)
		}
	})

	t.Run("host group target", func(t *testing.T) {
		s := mk("s-grp", "host_group", []string{"g1"}, time.Now().Add(time.Hour))
		res, err := ExecutePatchSchedule(ctx, d, qc, s, "", "manual", nil, log)
		if err != nil {
			t.Fatal(err)
		}
		// h-grouped already got a queued run in the previous subtest -> busy
		if res.HostsTargeted != 1 || res.HostsQueued != 0 || res.HostsBusy != 1 {
			t.Fatalf("unexpected result: %+v", res)
		}
	})

	t.Run("dispatcher runs due schedule once and advances next_run_at", func(t *testing.T) {
		exec(`DELETE FROM patch_runs WHERE policy_name LIKE 'Schedule:%'`)
		exec(`DELETE FROM patch_schedules`)
		due := time.Now().Add(-30 * time.Second)
		s := mk("s-due", "host", []string{"h-pending"}, due)
		h := NewPatchSchedulesDispatchHandler(d, nil, qc, log)
		for i := 0; i < 2; i++ { // second tick must be a no-op
			if err := h.ProcessTask(ctx, asynq.NewTask(TypePatchSchedulesDispatch, nil)); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		_ = d.RawQueryRow(ctx, `SELECT COUNT(*) FROM patch_runs WHERE host_id = 'h-pending' AND policy_name = 'Schedule: s-due'`).Scan(&n)
		if n != 1 {
			t.Fatalf("expected exactly 1 run, got %d", n)
		}
		got, err := q.GetPatchScheduleByID(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.NextRunAt.Time.After(time.Now()) || !got.LastRunAt.Valid || len(got.LastRunResult) == 0 {
			t.Fatalf("schedule not advanced: %+v", got)
		}
	})

	t.Run("misfire: overdue by more than grace is skipped", func(t *testing.T) {
		exec(`DELETE FROM patch_runs WHERE policy_name LIKE 'Schedule:%'`)
		exec(`DELETE FROM patch_schedules`)
		s := mk("s-late", "host", []string{"h-pending"}, time.Now().Add(-3*time.Hour))
		h := NewPatchSchedulesDispatchHandler(d, nil, qc, log)
		if err := h.ProcessTask(ctx, asynq.NewTask(TypePatchSchedulesDispatch, nil)); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = d.RawQueryRow(ctx, `SELECT COUNT(*) FROM patch_runs WHERE policy_name = 'Schedule: s-late'`).Scan(&n)
		got, _ := q.GetPatchScheduleByID(ctx, s.ID)
		if n != 0 || !got.NextRunAt.Time.After(time.Now()) || got.LastRunAt.Valid {
			t.Fatalf("misfire handling wrong: runs=%d sched=%+v", n, got)
		}
	})
}
