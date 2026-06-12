package cmd

import (
	"strings"
	"testing"
)

func TestBuildCalendarUpdatePlan(t *testing.T) {
	cmd := &CalendarUpdateCmd{
		CalendarID:  " cal@example.com ",
		EventID:     " event-1 ",
		Summary:     " Updated ",
		Attachments: []string{" https://drive.google.com/file/d/one "},
		SendUpdates: "all",
	}
	fields := calendarUpdateFields{
		Summary:     true,
		Attachments: true,
		WithZoom:    true,
	}

	plan, err := buildCalendarUpdatePlan(cmd, fields)
	if err != nil {
		t.Fatalf("buildCalendarUpdatePlan: %v", err)
	}
	if plan.CalendarID != "cal@example.com" || plan.EventID != "event-1" {
		t.Fatalf("unexpected normalized IDs: %#v", plan)
	}
	if plan.Scope != scopeAll || plan.SendUpdates != "all" {
		t.Fatalf("unexpected request options: %#v", plan)
	}
	if !plan.Changed || plan.Patch.Summary != "Updated" || len(plan.Patch.Attachments) != 1 {
		t.Fatalf("unexpected patch: %#v", plan.Patch)
	}

	request := plan.dryRunRequest()
	if request["supports_attachments"] != true {
		t.Fatalf("expected attachment support: %#v", request)
	}
	zoomPayload, ok := request["zoom"].(map[string]any)
	if !ok || zoomPayload["action"] != "create" {
		t.Fatalf("unexpected Zoom payload: %#v", request["zoom"])
	}
}

func TestBuildCalendarUpdatePlanValidatesSelectedFields(t *testing.T) {
	tests := []struct {
		name   string
		cmd    CalendarUpdateCmd
		fields calendarUpdateFields
		want   string
	}{
		{
			name:   "all day requires both times",
			cmd:    CalendarUpdateCmd{CalendarID: "primary", EventID: "event-1", From: "2025-01-01"},
			fields: calendarUpdateFields{AllDay: true, From: true},
			want:   "when changing --all-day",
		},
		{
			name:   "attendee modes conflict",
			cmd:    CalendarUpdateCmd{CalendarID: "primary", EventID: "event-1"},
			fields: calendarUpdateFields{Attendees: true, AddAttendee: true},
			want:   "cannot use both --attendees and --add-attendee",
		},
		{
			name:   "empty add attendee",
			cmd:    CalendarUpdateCmd{CalendarID: "primary", EventID: "event-1"},
			fields: calendarUpdateFields{AddAttendee: true},
			want:   "empty --add-attendee",
		},
		{
			name:   "no updates",
			cmd:    CalendarUpdateCmd{CalendarID: "primary", EventID: "event-1"},
			fields: calendarUpdateFields{},
			want:   "no updates provided",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildCalendarUpdatePlan(&tc.cmd, tc.fields)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestBuildCalendarUpdatePlanDefersPlaceResolution(t *testing.T) {
	cmd := &CalendarUpdateCmd{
		CalendarID:     "primary",
		EventID:        "event-1",
		LocationSearch: " Cafe ",
		PlaceLanguage:  "en",
	}
	fields := calendarUpdateFields{LocationSearch: true}

	plan, err := buildCalendarUpdatePlan(cmd, fields)
	if err != nil {
		t.Fatalf("buildCalendarUpdatePlan: %v", err)
	}
	if plan.PlaceLookup == nil || plan.PlaceLookup.Mode != "text_search" || plan.PlaceLookup.Query != "Cafe" {
		t.Fatalf("unexpected place lookup: %#v", plan.PlaceLookup)
	}
	if plan.Changed {
		t.Fatalf("provider lookup should remain deferred: %#v", plan.Patch)
	}
	payload, ok := plan.dryRunRequest()["place_lookup"].(map[string]string)
	if !ok || payload["query"] != "Cafe" || payload["language_code"] != "en" {
		t.Fatalf("unexpected place dry-run payload: %#v", payload)
	}
}
