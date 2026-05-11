package cmd

import (
	"context"
	"os"
	"strings"

	"google.golang.org/api/calendar/v3"

	"github.com/steipete/gogcli/internal/outfmt"
)

type CalendarAppointmentsCmd struct {
	CalendarID []string `arg:"" name:"calendarId" optional:"" help:"Calendar ID (default: primary)"`
	Cal        []string `name:"cal" help:"Calendar ID or name (can be repeated)"`
	Calendars  string   `name:"calendars" help:"Comma-separated calendar IDs, names, or indices from 'calendar calendars'"`
	From       string   `name:"from" help:"Start time (RFC3339 with timezone, date, or relative)"`
	To         string   `name:"to" help:"End time (RFC3339 with timezone, date, or relative)"`
	Today      bool     `name:"today" help:"Today only (timezone-aware)"`
	Tomorrow   bool     `name:"tomorrow" help:"Tomorrow only (timezone-aware)"`
	Week       bool     `name:"week" help:"This week (uses --week-start, default Mon)"`
	Days       int      `name:"days" help:"Next N days (timezone-aware)" default:"0"`
	WeekStart  string   `name:"week-start" help:"Week start day for --week (sun, mon, ...)" default:""`
	Max        int64    `name:"max" aliases:"limit" help:"Max results" default:"10"`
	Page       string   `name:"page" aliases:"cursor" help:"Page token"`
	AllPages   bool     `name:"all-pages" aliases:"allpages" help:"Fetch all pages"`
	All        bool     `name:"all" help:"Fetch appointment schedules from all calendars"`
	FailEmpty  bool     `name:"fail-empty" aliases:"non-empty,require-results" help:"Exit with code 3 if no results"`
	Fields     string   `name:"fields" help:"Comma-separated fields to return"`
}

func (c *CalendarAppointmentsCmd) Run(ctx context.Context, flags *RootFlags) error {
	account, err := requireAccount(flags)
	if err != nil {
		return err
	}
	calendarID, err := normalizeCalendarEventsArgs(c.CalendarID)
	if err != nil {
		return err
	}
	calInputs := append([]string{}, c.Cal...)
	if strings.TrimSpace(c.Calendars) != "" {
		calInputs = append(calInputs, splitCSV(c.Calendars)...)
	}
	if c.All && (calendarID != "" || len(calInputs) > 0) {
		return usage("calendarId or --cal/--calendars not allowed with --all flag")
	}
	if calendarID != "" && len(calInputs) > 0 {
		return usage("calendarId not allowed with --cal/--calendars")
	}

	svc, err := newCalendarService(ctx, account)
	if err != nil {
		return err
	}
	if !c.All && len(calInputs) == 0 {
		calendarID, err = resolveCalendarSelector(ctx, svc, calendarID, true)
		if err != nil {
			return err
		}
	}

	timeRange, err := ResolveTimeRange(ctx, svc, TimeRangeFlags{
		From:      c.From,
		To:        c.To,
		Today:     c.Today,
		Tomorrow:  c.Tomorrow,
		Week:      c.Week,
		Days:      c.Days,
		WeekStart: c.WeekStart,
	})
	if err != nil {
		return err
	}
	from, to := timeRange.FormatRFC3339()

	switch {
	case c.All:
		calendars, err := listCalendarList(ctx, svc)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(calendars))
		for _, cal := range calendars {
			if cal != nil && strings.TrimSpace(cal.Id) != "" {
				ids = append(ids, cal.Id)
			}
		}
		return listCalendarAppointmentIDs(ctx, svc, ids, from, to, c.Max, c.Page, c.AllPages, c.FailEmpty, c.Fields, calendarTimezoneHints(calendars))
	case len(calInputs) > 0:
		ids, err := resolveCalendarIDs(ctx, svc, calInputs)
		if err != nil {
			return err
		}
		return listCalendarAppointmentIDs(ctx, svc, ids, from, to, c.Max, c.Page, c.AllPages, c.FailEmpty, c.Fields, nil)
	default:
		return listCalendarAppointmentID(ctx, svc, calendarID, from, to, c.Max, c.Page, c.AllPages, c.FailEmpty, c.Fields)
	}
}

func listCalendarAppointmentID(ctx context.Context, svc *calendar.Service, calendarID, from, to string, maxResults int64, page string, allPages bool, failEmpty bool, fields string) error {
	calendarTimezone, loc := calendarDisplayTimezone(ctx, svc, calendarID, nil)
	fetch := func(pageToken string) ([]*calendar.Event, string, error) {
		resp, err := calendarAppointmentListCall(ctx, svc, calendarID, from, to, maxResults, fields, pageToken).Do()
		if err != nil {
			return nil, "", err
		}
		return resp.Items, resp.NextPageToken, nil
	}
	items, nextPageToken, err := loadPagedItems(page, allPages, fetch)
	if err != nil {
		return err
	}
	if outfmt.IsJSON(ctx) {
		if err := outfmt.WriteJSON(ctx, os.Stdout, map[string]any{"events": wrapEventsWithTimezone(items, calendarTimezone, loc), "nextPageToken": nextPageToken}); err != nil {
			return err
		}
		if len(items) == 0 {
			return failEmptyExit(failEmpty)
		}
		return nil
	}
	wrapped := make([]*eventWithCalendar, 0, len(items))
	for _, item := range items {
		wrapped = append(wrapped, wrapEventWithCalendar(item, "", calendarTimezone, loc))
	}
	return renderCalendarEventsTable(ctx, wrapped, nextPageToken, false, false, failEmpty, true)
}

func listCalendarAppointmentIDs(ctx context.Context, svc *calendar.Service, calendarIDs []string, from, to string, maxResults int64, page string, allPages bool, failEmpty bool, fields string, timezoneHints map[string]calendarTimezoneHint) error {
	all := []*eventWithCalendar{}
	for _, calID := range calendarIDs {
		calID = strings.TrimSpace(calID)
		if calID == "" {
			continue
		}
		calendarTimezone, loc := calendarDisplayTimezone(ctx, svc, calID, timezoneHints)
		fetch := func(pageToken string) ([]*calendar.Event, string, error) {
			resp, err := calendarAppointmentListCall(ctx, svc, calID, from, to, maxResults, fields, pageToken).Do()
			if err != nil {
				return nil, "", err
			}
			return resp.Items, resp.NextPageToken, nil
		}
		items, _, err := loadPagedItems(page, allPages, fetch)
		if err != nil {
			return err
		}
		for _, item := range items {
			all = append(all, wrapEventWithCalendar(item, calID, calendarTimezone, loc))
		}
	}
	if outfmt.IsJSON(ctx) {
		if err := outfmt.WriteJSON(ctx, os.Stdout, map[string]any{"events": all}); err != nil {
			return err
		}
		if len(all) == 0 {
			return failEmptyExit(failEmpty)
		}
		return nil
	}
	return renderCalendarEventsTable(ctx, all, "", true, false, failEmpty, false)
}

func calendarAppointmentListCall(ctx context.Context, svc *calendar.Service, calendarID, from, to string, maxResults int64, fields, pageToken string) *calendar.EventsListCall {
	return calendarEventsListCall(ctx, svc, calendarID, from, to, maxResults, "", "", "", fields, pageToken).
		EventTypes("appointmentSchedule")
}
