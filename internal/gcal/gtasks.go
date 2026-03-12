// This file contains the Google Tasks integration — the half of the Phase 5
// sync that puts daily task instructions into the grower's Google Tasks list.
//
// Why Google Tasks alongside Calendar events?
//   The multi-day stage blocks in Google Calendar give an at-a-glance view of
//   which crops are in which phase. But they don't show what to actually DO
//   each day. Google Tasks fills that gap: each day with real work gets a
//   checkable to-do item (e.g. "Sunnies: top water, rotate stack") that the
//   grower can tick off as they move through the farm.
//
// The "Labor" task list:
//   All task entries are created in a dedicated task list called "Labor".
//   This keeps farm tasks separate from the grower's personal to-do list.
//   The list is created automatically on the first sync — no manual setup needed.
package gcal

import (
	"fmt"
	"strings"
	"time"

	gtasks "google.golang.org/api/tasks/v1"

	"github.com/littleguygreens/greenies/internal/task"
)

// ensureGreeniesTaskList finds the "Labor" task list in Google Tasks and
// returns its ID. If the list does not exist yet, it creates it first.
//
// A task list ID is a unique code Google assigns — something like
// "MDEyMzQ1Njc4OTA". We use this ID in all subsequent API calls to make
// sure we're reading and writing the right list.
func (g *GoogleCalendarExporter) ensureGreeniesTaskList() (string, error) {
	// Fetch all task lists belonging to this Google account.
	lists, err := g.taskService.Tasklists.List().Do()
	if err != nil {
		return "", fmt.Errorf("could not list Google Task lists: %w", err)
	}

	// "Labor" is the name shown in Google Tasks. Hardcoded for now — a future
	// version could make this configurable in a settings file.
	const listName = "Labor"

	// Look for an existing "Labor" list.
	for _, list := range lists.Items {
		if list.Title == listName {
			return list.Id, nil
		}
	}

	// Not found — create it. This only runs once, on the very first sync.
	fmt.Print("(creating Labor task list for the first time) ")
	newList, err := g.taskService.Tasklists.Insert(&gtasks.TaskList{
		Title: listName,
	}).Do()
	if err != nil {
		return "", fmt.Errorf("could not create Labor task list: %w", err)
	}

	return newList.Id, nil
}

// deleteGreeniesToDos removes all uncompleted tasks in the Greenies task list.
// This gives us a completely clean slate to recreate from the current schedule
// without any stale entries — past or future — left behind.
//
// Completed tasks (ones the grower has already ticked off) are intentionally
// left alone — they are the grower's record of work done.
func (g *GoogleCalendarExporter) deleteGreeniesToDos(taskListID string) (int, error) {
	deleted := 0
	pageToken := ""

	for {
		// List all tasks in the Greenies list.
		// ShowCompleted(false) means completed tasks are excluded from results,
		// so we never accidentally delete tasks the grower has already ticked off.
		// ShowHidden(false) excludes tasks the grower has manually hidden.
		call := g.taskService.Tasks.List(taskListID).
			ShowCompleted(false).
			ShowHidden(false)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return deleted, fmt.Errorf("could not list Google Tasks: %w", err)
		}

		for _, t := range resp.Items {
			if err := g.taskService.Tasks.Delete(taskListID, t.Id).Do(); err != nil {
				// Non-fatal — log and keep going.
				fmt.Printf("\n  Warning: could not delete task %q: %v\n", t.Title, err)
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

// syncGreeniesToDos creates one Google Task entry for each upcoming day
// that has real work to do. Days where no tasks are required are skipped —
// there is nothing to tick off.
//
// Returns the number of task entries successfully created.
func (g *GoogleCalendarExporter) syncGreeniesToDos(upcoming []task.Task, taskListID string) (int, error) {
	created := 0

	for _, t := range upcoming {
		// Only tasks from a planned crop cycle get a Google Tasks entry.
		// Manually added calendar reminders (no CycleID) are skipped.
		if t.CycleID == "" {
			continue
		}

		// Extract the specific task detail from the notes field and skip
		// days where there is genuinely nothing to do. This is safer than
		// searching for the literal string "no tasks today" — if the format
		// of the notes field ever changes, the constant will catch it too.
		taskDetail := extractTaskDetail(t.Notes)
		if taskDetail == "" || taskDetail == task.NoTasksNote {
			continue
		}

		if err := g.createTaskEntry(t, taskListID); err != nil {
			fmt.Printf("\n  Warning: could not create task for %q (%s): %v\n", t.Title, t.Date, err)
			continue
		}
		created++
	}

	return created, nil
}

// createTaskEntry sends one day's task instructions to Google Tasks as a
// single checkable to-do item due on that day.
//
// The entry title is built from the crop name and the day's task list:
// e.g. "Sunnies: top water, rotate stack"
//
// The task notes record the cycle ID so the grower can always trace a
// Google Tasks entry back to the specific plan it came from.
func (g *GoogleCalendarExporter) createTaskEntry(t task.Task, taskListID string) error {
	// Build a readable title for the Google Tasks entry.
	// We extract the crop name from the task title ("Sunnies 4x dark" → "Sunnies")
	// and the specific instructions from the notes ("4 trays · top water, rotate stack"
	// → "top water, rotate stack"), then combine them.
	cropPrefix := extractCropPrefix(t.Title)
	taskDetail := extractTaskDetail(t.Notes)
	title := cropPrefix + ": " + taskDetail

	// Google Tasks requires due dates as RFC 3339 timestamps.
	// The time portion (T00:00:00Z) is ignored — only the date is shown.
	dueTime, err := time.Parse("2006-01-02", t.Date)
	if err != nil {
		return fmt.Errorf("invalid date %q: %w", t.Date, err)
	}

	entry := &gtasks.Task{
		Title: title,
		Due:   dueTime.UTC().Format(time.RFC3339),
		// Notes appears in the detail view when the grower taps on the task.
		// Including the Cycle ID lets them trace this task back to the plan.
		Notes: "Cycle: " + t.CycleID,
	}

	_, err = g.taskService.Tasks.Insert(taskListID, entry).Do()
	if err == nil {
		time.Sleep(apiPause) // stay within Google's rate limit
	}
	return err
}

// extractCropPrefix pulls the crop name and tray count out of a task title.
// Task titles follow the format "CropName Nx stage" (e.g. "Sunnies 4x dark").
// We return the first two words — "Sunnies 4x" — so that Google Tasks entries
// distinguish between multiple batches of the same crop running at once.
// For example, two sunnies cycles become "Sunnies 4x: ..." and "Sunnies 8x: ..."
// rather than both showing as "Sunnies: ...".
func extractCropPrefix(title string) string {
	parts := strings.Fields(title)
	if len(parts) == 0 {
		return "Unknown"
	}
	if len(parts) < 2 {
		return parts[0]
	}
	// "Sunnies" + " " + "4x" → "Sunnies 4x"
	return parts[0] + " " + parts[1]
}

// extractTaskDetail pulls the specific task instructions out of the notes field.
// Notes follow the format "N trays · task1, task2" (e.g. "4 trays · top water, rotate stack").
// The instructions are everything after the middle-dot separator " · ".
// If the separator isn't found, the whole notes string is returned as a fallback.
func extractTaskDetail(notes string) string {
	// Split on " · " (space, middle dot, space) — the separator used by the scheduler.
	parts := strings.SplitN(notes, " · ", 2)
	if len(parts) < 2 {
		return notes // fallback: use the whole string if the format is unexpected
	}
	return parts[1]
}
