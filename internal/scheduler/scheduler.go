// Package scheduler turns a crop variety + harvest date + tray count into a
// list of calendar tasks ready to be saved.
//
// Think of this as the planning brain of the program. You tell it "I want to
// harvest sunnies on March 15th with 2 trays" and it works backwards through
// the crop's day-by-day schedule to tell you what needs doing on every day
// between now and then.
package scheduler

import (
	"fmt"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/task"
)

// ScheduledDay is one day in the preview shown to the user before they confirm.
// It holds the calculated calendar date alongside the crop day information,
// so the preview can display both "March 7th" and "Day 1 — sow" together.
type ScheduledDay struct {
	Date    string      // calendar date in YYYY-MM-DD format
	CropDay crop.CropDay // the day entry from the crop library
}

// Schedule takes a crop, a harvest date, and a number of trays, and returns
// two things:
//
//  1. A preview slice ([]ScheduledDay) — every day in the cycle with its
//     calculated date, including do-nothing days. Used to show the user a
//     full picture before they confirm.
//
//  2. A tasks slice ([]task.Task) — only the days that have tasks, ready to
//     be saved to the calendar if the user confirms.
//
// The harvest date is treated as the last day of the cycle (day = cycle_days).
// Every earlier day is calculated by counting backwards from that date.
// Example: sunnies is a 9-day cycle. If harvest is March 15, then:
//
//	day 9 = March 15 (harvest date − 0 days)
//	day 8 = March 14 (harvest date − 1 day)
//	day 1 = March 7  (harvest date − 8 days)
func Schedule(c crop.Crop, harvestDate string, trays int) ([]ScheduledDay, []task.Task, error) {
	harvest, err := time.Parse(task.DateFormat, harvestDate)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid harvest date %q — use YYYY-MM-DD format", harvestDate)
	}

	var preview []ScheduledDay
	var tasks []task.Task

	for _, day := range c.Days {
		// Calculate this day's actual calendar date by counting back from harvest.
		// If harvest is day 9 and this is day 3, that is 6 days before harvest.
		daysBeforeHarvest := c.CycleDays - day.Day
		date := harvest.AddDate(0, 0, -daysBeforeHarvest)
		dateStr := date.Format(task.DateFormat)

		// Always add to the preview so the user sees the full cycle.
		preview = append(preview, ScheduledDay{
			Date:    dateStr,
			CropDay: day,
		})

		// Build the task title: crop name + day number + stage.
		// Example: "Sunnies — Day 1 (sow)"
		title := fmt.Sprintf("%s — Day %d (%s)", capitalize(c.Name), day.Day, day.Stage)

		// Put tray count and task instructions in the notes field.
		// Even days with no tasks are saved to the calendar — the trays are
		// still occupying physical space on the rack and should be visible.
		trayWord := "tray"
		if trays != 1 {
			trayWord = "trays"
		}
		var notes string
		if strings.TrimSpace(day.Tasks) == "" {
			notes = fmt.Sprintf("%d %s · no tasks today", trays, trayWord)
		} else {
			notes = fmt.Sprintf("%d %s · %s", trays, trayWord, day.Tasks)
		}

		t, err := task.New(title, dateStr, notes)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating task for day %d: %w", day.Day, err)
		}

		tasks = append(tasks, t)
	}

	return preview, tasks, nil
}

// capitalize uppercases the first letter of a string and leaves the rest alone.
// Used to display crop names from the CSV (which are lowercase) with a capital
// at the start of task titles.
//
// We write this ourselves rather than using a library function because the
// standard library's strings.Title is deprecated and the replacement requires
// an external dependency — overkill for capitalising one word.
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
