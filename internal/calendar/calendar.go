// Package calendar handles displaying tasks as a readable day-view calendar
// in the terminal.
//
// This package does not store or modify tasks — it only knows how to display
// them. Think of it as the "printer" that takes a list of tasks and formats
// them neatly on screen.
package calendar

import (
	"fmt"
	"sort"
	"time"

	"github.com/littleguygreens/greenies/internal/task"
)

// dateFormat is the layout string Go uses to parse and format dates.
// "2006-01-02" is Go's reference date — in Go, you always use this exact
// date (Jan 2, 2006) as a template. It looks odd but it is how Go works.
const dateFormat = "2006-01-02"

// PrintDay displays all tasks for a single date in the day-view format.
//
// Example output:
//
//	══════════════════════════════════════
//	  Thursday, 5 March 2026
//	══════════════════════════════════════
//	  [ ] Sow sunflowers
//	      Notes: 2 trays, main tent
//
// If there are no tasks on that date, it prints "(no tasks scheduled)" so
// the user knows the day was checked, not skipped.
func PrintDay(date string, tasks []task.Task) {
	// Filter the full task list down to only tasks on this specific date.
	daily := tasksForDate(tasks, date)

	// Parse the date string into a time.Time value so we can extract the
	// day name (e.g. "Thursday") and format it nicely.
	t, err := time.Parse(dateFormat, date)
	if err != nil {
		// If the date string is malformed, fall back to displaying it as-is
		// rather than crashing. This should not happen in normal use.
		fmt.Printf("\n  %s\n", date)
	} else {
		// Format the date as "Thursday, 5 March 2026"
		fmt.Printf("\n══════════════════════════════════════\n")
		fmt.Printf("  %s\n", t.Format("Monday, 2 January 2006"))
		fmt.Printf("══════════════════════════════════════\n")
	}

	if len(daily) == 0 {
		fmt.Println("  (no tasks scheduled)")
		return
	}

	for _, task := range daily {
		// Print the task title with a checkbox — [ ] means "not yet done".
		// Phase 3+ will add the ability to mark tasks as complete.
		fmt.Printf("  [ ] %s\n", task.Title)

		// Only print the Notes line if the task actually has notes,
		// to keep the display clean for simple tasks.
		if task.Notes != "" {
			fmt.Printf("      Notes: %s\n", task.Notes)
		}

		// Print the task's short ID so the user can reference it in
		// edit and delete commands.
		fmt.Printf("      ID: %s\n", task.ID)

		// A blank line between tasks makes the list easier to scan.
		fmt.Println()
	}
}

// PrintRange displays tasks for a consecutive range of dates, one day at a time.
//
// startDate and endDate are inclusive — both dates will be shown.
// This is used by the "list --week" command to show 7 days at a glance.
func PrintRange(startDate, endDate string, tasks []task.Task) error {
	start, err := time.Parse(dateFormat, startDate)
	if err != nil {
		return fmt.Errorf("invalid start date %q — use the format YYYY-MM-DD (e.g. 2026-03-05): %w", startDate, err)
	}

	end, err := time.Parse(dateFormat, endDate)
	if err != nil {
		return fmt.Errorf("invalid end date %q — use the format YYYY-MM-DD (e.g. 2026-03-05): %w", endDate, err)
	}

	// Walk forward one day at a time from start to end, printing each day.
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		PrintDay(d.Format(dateFormat), tasks)
	}

	return nil
}

// tasksForDate returns only the tasks from the list that fall on the given date.
// The result is sorted by the time the task was created, so tasks appear in
// the order they were added.
func tasksForDate(tasks []task.Task, date string) []task.Task {
	var result []task.Task
	for _, t := range tasks {
		if t.Date == date {
			result = append(result, t)
		}
	}

	// Sort by creation time so the display order is stable and predictable.
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result
}
