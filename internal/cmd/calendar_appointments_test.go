package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCalendarAppointmentsUsesAppointmentScheduleEventType(t *testing.T) {
	svc, closeSvc := newCalendarServiceForTest(t, withPrimaryCalendar(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query()["eventTypes"]; len(got) != 1 || got[0] != "appointmentSchedule" {
			t.Fatalf("eventTypes = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id":        "appt1",
				"summary":   "Office hours",
				"eventType": "appointmentSchedule",
				"start":     map[string]any{"dateTime": "2026-01-01T10:00:00Z"},
				"end":       map[string]any{"dateTime": "2026-01-01T11:00:00Z"},
			}},
		})
	})))
	defer closeSvc()
	stubCalendarServiceForTest(t, svc)

	out := captureStdout(t, func() {
		cmd := &CalendarAppointmentsCmd{From: "2026-01-01", To: "2026-01-02", Max: 10}
		if err := cmd.Run(newCalendarJSONContext(t), &RootFlags{Account: "a@example.com"}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
	if !strings.Contains(out, `"appointmentSchedule"`) || !strings.Contains(out, `"appt1"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}
