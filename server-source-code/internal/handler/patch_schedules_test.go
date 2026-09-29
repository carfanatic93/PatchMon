package handler

import (
	"net/http/httptest"
	"testing"
)

func TestValidatePatchSchedule(t *testing.T) {
	h := &PatchingHandler{}
	const hostID = "0b3f8c1e-6a52-4c53-8f5b-1f2d9a7e4c10"
	tests := []struct {
		name    string
		req     patchScheduleRequest
		wantErr bool
	}{
		{"sunday noon all hosts", patchScheduleRequest{Name: "Weekly", CronExpr: "0 12 * * 0", Timezone: "Europe/Berlin", TargetType: "all"}, false},
		{"extra whitespace is normalised", patchScheduleRequest{Name: "Weekly", CronExpr: "  0  12 * * SUN ", Timezone: "UTC", TargetType: "all"}, false},
		{"host group needs ids", patchScheduleRequest{Name: "G", CronExpr: "0 3 * * *", Timezone: "UTC", TargetType: "host_group"}, true},
		{"host group with id", patchScheduleRequest{Name: "G", CronExpr: "0 3 * * *", Timezone: "UTC", TargetType: "host_group", TargetIDs: []string{hostID}}, false},
		{"bad target id", patchScheduleRequest{Name: "H", CronExpr: "0 3 * * *", Timezone: "UTC", TargetType: "host", TargetIDs: []string{"x'; DROP"}}, true},
		{"missing name", patchScheduleRequest{CronExpr: "0 3 * * *", Timezone: "UTC", TargetType: "all"}, true},
		{"descriptor rejected", patchScheduleRequest{Name: "Storm", CronExpr: "@every 1s", Timezone: "UTC", TargetType: "all"}, true},
		{"six fields rejected", patchScheduleRequest{Name: "Sec", CronExpr: "* * * * * *", Timezone: "UTC", TargetType: "all"}, true},
		{"invalid cron", patchScheduleRequest{Name: "Bad", CronExpr: "99 99 * * *", Timezone: "UTC", TargetType: "all"}, true},
		{"invalid timezone", patchScheduleRequest{Name: "TZ", CronExpr: "0 3 * * *", Timezone: "Mars/Base", TargetType: "all"}, true},
		{"invalid target type", patchScheduleRequest{Name: "T", CronExpr: "0 3 * * *", Timezone: "UTC", TargetType: "everything"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			msg := h.validatePatchSchedule(httptest.NewRequest("POST", "/", nil), &req)
			if (msg != "") != tt.wantErr {
				t.Fatalf("validate() = %q, wantErr %v", msg, tt.wantErr)
			}
		})
	}
}
