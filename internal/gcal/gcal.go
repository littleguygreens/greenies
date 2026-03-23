// This file contains the Google Calendar exporter, which implements the
// Exporter interface.
//
// There are two ways to send data to Google:
//
//   Export (the interface method) — pushes each task as an individual all-day
//   event to the user's primary Google Calendar. Available for quick use but
//   not the primary sync path.
//
//   Sync (the "greenies sync" command) — sends the schedule to Google in two ways:
//     1. Google Tasks: one checkable to-do entry per day, listing that day's work.
//     2. Google Calendar: one all-day event per day, written to a dedicated
//        calendar called "Farm". The description contains the full farm snapshot
//        so the grower can see the live farm state when they tap on any day in
//        their phone calendar.
//
// Why a dedicated "Farm" calendar?
//   Writing to a separate calendar (rather than the grower's main calendar)
//   keeps farm events visually grouped and makes cleanup simple — when syncing,
//   we delete everything from the Farm calendar going forward, with no need to
//   tag events or filter by any special property.
//
// How sync works:
//   When "greenies sync" runs, it:
//     1. Finds or creates the "Greenies" task list, then deletes all future
//        uncompleted tasks from it (clean slate — no stale entries)
//     2. Creates fresh daily task entries for every upcoming day with work
//     3. Finds or creates the "Farm" calendar, then deletes all events from it
//        from today forward (clean slate)
//     4. Creates one all-day calendar event per upcoming day with tasks
//   Running Sync multiple times is safe — it always produces the same result.
//   This is called "idempotent" (eye-dem-PO-tent), meaning you can repeat it
//   without side effects like duplicate entries.
package gcal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
	gtasks "google.golang.org/api/tasks/v1"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/visualizer"
)

// apiPause is the delay we insert between individual Google API write calls
// (creates and deletes). Google's free-tier rate limit is 100 requests per
// 100 seconds per user — 500ms between calls means at most 2 per second,
// which is well within that limit and prevents quota errors on large syncs.
// Sync is not time-sensitive, so waiting a few extra seconds is no problem.
const apiPause = 500 * time.Millisecond

// GoogleCalendarExporter sends tasks to Google Calendar and Google Tasks.
// It satisfies the export.Exporter interface (via Export) and also provides
// a Sync method for the dedicated "greenies sync" command.
type GoogleCalendarExporter struct {
	// service is the authorised Google Calendar API client.
	// Used by both Export and Sync.
	service *calendar.Service

	// taskService is the authorised Google Tasks API client.
	// It uses the same HTTP client (and therefore the same saved token)
	// as the Calendar service — one login grants both permissions.
	taskService *gtasks.Service

	// calendarID is the identifier used by the Export interface method,
	// which writes to the user's main "primary" calendar as a quick push.
	calendarID string
}

// NewExporter creates a GoogleCalendarExporter that is ready to send events
// and tasks. It handles authorisation internally — loading the saved token,
// or running the browser flow if this is the first time.
func NewExporter(ctx context.Context) (*GoogleCalendarExporter, error) {
	// AuthorizeClient handles all token loading, refreshing, and first-time
	// browser setup. After this call we have an HTTP client that automatically
	// includes the user's Google Calendar AND Tasks permission on every request.
	client, err := AuthorizeClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("Google authorisation failed: %w", err)
	}

	// Build the Calendar API service using our authorised client.
	svc, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("could not create Google Calendar service: %w", err)
	}

	// Build the Tasks API service using the same authorised client.
	taskSvc, err := gtasks.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("could not create Google Tasks service: %w", err)
	}

	return &GoogleCalendarExporter{
		service:     svc,
		taskService: taskSvc,
		calendarID:  "primary",
	}, nil
}

// Export sends every task in the list to the user's primary Google Calendar
// as an individual all-day event. This satisfies the export.Exporter interface.
// For day-to-day use, prefer Sync — it creates the cleaner to-do entries in
// the dedicated Greenies task list and per-day calendar events in the Farm
// calendar with the full snapshot in the description.
func (g *GoogleCalendarExporter) Export(tasks []task.Task) error {
	created := 0
	for _, t := range tasks {
		if err := g.createEvent(t); err != nil {
			fmt.Printf("  Warning: could not create event for %q (%s): %v\n", t.Title, t.Date, err)
			continue
		}
		created++
	}

	if created > 0 {
		fmt.Printf("Google Calendar: %d events created.\n", created)
	}

	return nil
}

// Sync is the engine behind "greenies sync". It:
//  1. Filters the task list to today and later (past tasks aren't useful to add)
//  2. Finds or creates the shared "Greenies" task list, clears all future tasks,
//     then creates fresh daily task entries for every upcoming day with work
//  3. Finds or creates the dedicated "Farm" calendar, clears all events from it
//     from today forward, then creates one all-day event per upcoming day —
//     with the full farm snapshot for THAT DAY embedded in the description
//
// envs and cycles are the raw farm configuration data. They are passed through
// to syncCalendarEvents, which computes a fresh snapshot for each specific day
// so that each calendar event shows the farm state on that date — not today's
// state frozen at the moment "greenies sync" was run.
//
// Running Sync multiple times is safe — it always produces the same result.
func (g *GoogleCalendarExporter) Sync(tasks []task.Task, envs []farm.Environment, cycles []farm.Cycle) error {
	// "Today" at UTC midnight — the same convention used everywhere in Greenies
	// to avoid timezone mismatches between stored date strings and time.Now().
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Filter the full task list down to today and future dates only.
	var upcoming []task.Task
	for _, t := range tasks {
		d, err := time.Parse("2006-01-02", t.Date)
		if err != nil {
			continue
		}
		if !d.Before(today) {
			upcoming = append(upcoming, t)
		}
	}

	if len(upcoming) == 0 {
		fmt.Println("No upcoming tasks to sync.")
		return nil
	}

	// ── Google Tasks sync ─────────────────────────────────────────────────────

	// Find or create the shared "Labor" task list,
	// then clear all future uncompleted tasks from it.
	// Each step label ends with a newline so the live timer (running in the
	// background) ticks on the empty line that follows — never overwriting
	// the label text. The result count appears on the timer's line.
	fmt.Println("Finding Labor task list...")
	taskListID, err := g.ensureGreeniesTaskList()
	if err != nil {
		return fmt.Errorf("could not find or create Labor task list: %w", err)
	}
	fmt.Println("Clearing old tasks...")
	deletedTasks, err := g.deleteGreeniesToDos(taskListID)
	if err != nil {
		return fmt.Errorf("could not clear existing tasks: %w", err)
	}
	fmt.Printf("%d removed.\n", deletedTasks)

	// Create individual daily task entries in the shared Greenies task list.
	// Only days with actual instructions get an entry — "no tasks today" days
	// are skipped because there is nothing to tick off.
	fmt.Println("Creating daily tasks...")
	createdTasks, err := g.syncGreeniesToDos(upcoming, taskListID)
	if err != nil {
		return fmt.Errorf("could not create Google Tasks entries: %w", err)
	}
	fmt.Printf("%d tasks created.\n", createdTasks)

	// ── Google Calendar event sync ────────────────────────────────────────────

	// Find or create the dedicated "Farm" calendar. Because we own this entire
	// calendar, cleaning up is simple — we delete everything from today forward
	// and recreate it fresh, with no need to tag or filter individual events.
	fmt.Println("Finding Farm calendar...")
	farmCalID, err := g.ensureFarmCalendar()
	if err != nil {
		return fmt.Errorf("could not find or create Farm calendar: %w", err)
	}
	fmt.Println("Clearing old events...")
	deletedEvents, err := g.deleteAllEvents(farmCalID)
	if err != nil {
		return fmt.Errorf("could not clear Farm calendar: %w", err)
	}
	fmt.Printf("%d removed.\n", deletedEvents)

	// Create one all-day calendar event for each upcoming day that has tasks.
	// The description of every event contains the farm snapshot for THAT DAY,
	// so the grower sees what the farm will look like on the day they open.
	fmt.Println("Creating calendar events...")
	createdEvents, err := g.syncCalendarEvents(upcoming, farmCalID, envs, cycles)
	if err != nil {
		return fmt.Errorf("could not create calendar events: %w", err)
	}
	fmt.Printf("%d events created.\n", createdEvents)

	return nil
}

// ensureFarmCalendar finds the "Farm" calendar in the user's Google account
// and returns its ID. If the calendar does not exist yet, it creates it first.
//
// Using a dedicated calendar means farm events are grouped away from the
// grower's personal calendar, and cleanup on the next sync is trivial —
// we just delete all events in this calendar going forward.
func (g *GoogleCalendarExporter) ensureFarmCalendar() (string, error) {
	// Fetch all calendars belonging to this Google account.
	list, err := g.service.CalendarList.List().Do()
	if err != nil {
		return "", fmt.Errorf("could not list Google calendars: %w", err)
	}

	// Look for an existing "Farm" calendar.
	for _, cal := range list.Items {
		if cal.Summary == "Farm" {
			return cal.Id, nil
		}
	}

	// Not found — create it. This only runs once, on the very first sync.
	fmt.Print("(creating Farm calendar for the first time) ")
	newCal, err := g.service.Calendars.Insert(&calendar.Calendar{
		Summary: "Farm",
	}).Do()
	if err != nil {
		return "", fmt.Errorf("could not create Farm calendar: %w", err)
	}

	return newCal.Id, nil
}

// deleteAllEvents removes every event from the given calendar. Because Greenies
// owns the Farm calendar entirely, we can safely wipe it completely — there
// are no personal events to worry about.
func (g *GoogleCalendarExporter) deleteAllEvents(calendarID string) (int, error) {
	deleted := 0
	pageToken := ""

	for {
		// List all events in the calendar with no date filter.
		// SingleEvents(true) flattens recurring events so each occurrence gets
		// its own entry and can be deleted individually.
		call := g.service.Events.List(calendarID).
			SingleEvents(true)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return deleted, fmt.Errorf("could not list Farm calendar events: %w", err)
		}

		for _, event := range resp.Items {
			if err := g.service.Events.Delete(calendarID, event.Id).Do(); err != nil {
				// Non-fatal — log and keep going.
				fmt.Printf("\n  Warning: could not delete event %q: %v\n", event.Summary, err)
				continue
			}
			deleted++
			time.Sleep(apiPause) // stay within Google's rate limit
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return deleted, nil
}

// syncCalendarEvents creates one all-day Google Calendar event for each
// upcoming day that has real crop tasks. Days with no tasks are skipped.
//
// For each day, it computes a fresh snapshot using envs and cycles — this
// means the title and description reflect what the farm will look like on
// that specific day, not what it looks like right now at sync time.
//
// Each event's description contains:
//   - The specific tasks due on that day (e.g. "Sunnies 4x: top water, rotate stack")
//   - The full farm snapshot for that date, so the grower can see the farm
//     state when they open any day in their phone calendar
//
// Returns the number of events successfully created.
func (g *GoogleCalendarExporter) syncCalendarEvents(upcoming []task.Task, calendarID string, envs []farm.Environment, cycles []farm.Cycle) (int, error) {
	// Group tasks by date, keeping dates in the order we first encounter them
	// so the events are created in chronological order.
	//
	// dateOrder preserves insertion order (Go maps don't have a stable order).
	// dateMap holds the tasks for each date.
	dateOrder := []string{}
	dateMap := map[string][]task.Task{}

	for _, t := range upcoming {
		// Skip manually added reminders (no CycleID) — those are personal
		// notes and don't belong in the Farm calendar.
		if t.CycleID == "" {
			continue
		}

		// Add ALL cycle tasks to the map (including do-nothing days).
		// We'll filter the do-nothing tasks out when building the task line list
		// below, then skip the event entirely if no lines remain.
		if _, exists := dateMap[t.Date]; !exists {
			dateOrder = append(dateOrder, t.Date)
			dateMap[t.Date] = []task.Task{}
		}
		dateMap[t.Date] = append(dateMap[t.Date], t)
	}

	created := 0

	for _, date := range dateOrder {
		dayTasks := dateMap[date]

		// Parse this event's date so we can compute what the farm will look
		// like on that specific day — not what it looks like right now.
		eventDate, err := time.Parse("2006-01-02", date)
		if err != nil {
			fmt.Printf("\n  Warning: could not parse date %s: %v\n", date, err)
			continue
		}

		// Compute the calendar title and snapshot for THIS day specifically.
		// Each event now shows live data for its own date rather than today's.
		title := visualizer.CalendarTitle(envs, cycles, eventDate)
		snapshotText := visualizer.SnapshotText(envs, cycles, eventDate)

		// Build the task section of the description: one line per task.
		// e.g. "Sunnies 4x: top water, rotate stack"
		//      "Pea 2x: bottom water"
		// Tasks where the grower has nothing to do (notes == NoTasksNote) are
		// skipped here — the trays are tracked in the snapshot below, but
		// there is no instruction to display in the task list.
		var taskLines []string
		for _, t := range dayTasks {
			cropPrefix := extractCropPrefix(t.Title)
			taskDetail := extractTaskDetail(t.Notes)
			if taskDetail != "" && taskDetail != task.NoTasksNote {
				taskLines = append(taskLines, cropPrefix+": "+taskDetail)
			}
		}

		// If every task on this day was a do-nothing day, there is nothing
		// useful to put in a calendar event — skip it entirely.
		if len(taskLines) == 0 {
			continue
		}

		// Combine the task list with the farm snapshot in the description.
		// The separator makes it clear where the day's tasks end and the
		// farm-wide picture begins.
		description := strings.Join(taskLines, "\n") +
			"\n" + strings.Repeat("─", 21) + "\n" +
			snapshotText

		// Create the all-day event in the dedicated Farm calendar.
		endDate, err := dayAfter(date)
		if err != nil {
			fmt.Printf("\n  Warning: could not compute end date for %s: %v\n", date, err)
			continue
		}

		event := &calendar.Event{
			Summary:     title,
			Description: description,
			Start:       &calendar.EventDateTime{Date: date},
			End:         &calendar.EventDateTime{Date: endDate},
		}

		if _, err := g.service.Events.Insert(calendarID, event).Do(); err != nil {
			fmt.Printf("\n  Warning: could not create calendar event for %s: %v\n", date, err)
			continue
		}
		created++
		time.Sleep(apiPause) // stay within Google's rate limit
	}

	return created, nil
}

// createEvent sends a single task to the user's primary Google Calendar as one
// all-day event. Used by Export (the interface method) for a quick push.
func (g *GoogleCalendarExporter) createEvent(t task.Task) error {
	endDate, err := dayAfter(t.Date)
	if err != nil {
		return err
	}

	event := &calendar.Event{
		Summary:     t.Title,
		Description: buildDescription(t),
		Start:       &calendar.EventDateTime{Date: t.Date},
		End:         &calendar.EventDateTime{Date: endDate},
	}

	_, err = g.service.Events.Insert(g.calendarID, event).Do()
	return err
}

// buildDescription constructs the body text shown when you click on an event.
// Used by the Export method's per-day events.
func buildDescription(t task.Task) string {
	var parts []string

	if t.Notes != "" {
		parts = append(parts, t.Notes)
	}

	if t.CycleID != "" {
		parts = append(parts, "Cycle ID: "+t.CycleID)
	}

	parts = append(parts, "Scheduled by Greenies")

	return strings.Join(parts, "\n")
}

// dayAfter takes a YYYY-MM-DD date string and returns the next day's date
// in the same format. Required because Google Calendar's all-day event end
// dates are exclusive — the event ends at the start of the end date, not
// at the end of it.
func dayAfter(dateStr string) (string, error) {
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return "", fmt.Errorf("invalid date %q: %w", dateStr, err)
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02"), nil
}
