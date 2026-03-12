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

	// Generate one shared CycleID for this entire batch of tasks.
	// Every task created in this call gets the same CycleID stamped on it,
	// so the delete command can find and remove the whole cycle at once.
	cycleID, err := task.GenerateID()
	if err != nil {
		return nil, nil, fmt.Errorf("could not generate cycle ID: %w", err)
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

		// Build the task title: crop name + tray count + stage.
		// Example: "Sunnies 12x dark"
		// The "12x" shows at a glance how many trays are in this batch.
		// No day number in the title — the date on the calendar already tells
		// the grower exactly where they are in the cycle.
		title := fmt.Sprintf("%s %dx %s", task.Capitalize(c.Name), trays, day.Stage)

		// Put tray count and task instructions in the notes field.
		// Even days with no tasks are saved to the calendar — the trays are
		// still occupying physical space on the rack and should be visible.
		trayWord := "tray"
		if trays != 1 {
			trayWord = "trays"
		}
		var notes string
		if strings.TrimSpace(day.Tasks) == "" {
			notes = fmt.Sprintf("%d %s · %s", trays, trayWord, task.NoTasksNote)
		} else {
			notes = fmt.Sprintf("%d %s · %s", trays, trayWord, day.Tasks)
		}

		t, err := task.New(title, dateStr, notes)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating task for day %d: %w", day.Day, err)
		}

		// Stamp the shared batch ID so this task can be found and deleted
		// together with all other tasks from this same planning session.
		t.CycleID = cycleID

		tasks = append(tasks, t)
	}

	return preview, tasks, nil
}

// ScheduleForward takes a crop, a sow date, and a number of trays, and returns
// the same two things as Schedule — a preview slice and a tasks slice — but
// this time calculated forward from the sow date instead of backward from
// the harvest date.
//
// "Sow date" always means Day 1 for every crop (the day seed is actually sown).
// For crops that have an overnight soak (Day 0), the soak task is placed on
// the day before the sow date automatically. So if you enter Monday as the
// sow date for peas, the program will put the soak reminder on Sunday.
//
// Example: peas is an 8-day cycle. Sow date = Monday 9 March:
//
//	day 0 = Sunday 8 March   (soak overnight — placed 1 day before sow date)
//	day 1 = Monday 9 March   (drain, sow)
//	day 8 = Monday 16 March  (harvest)
func ScheduleForward(c crop.Crop, sowDate string, trays int) ([]ScheduledDay, []task.Task, error) {
	// Day 1 maps to the sow date. Every other day is offset from there.
	// Day 0 is 1 day before sow date (-1 offset).
	// Day 2 is 1 day after sow date (+1 offset), and so on.
	sow, err := time.Parse(task.DateFormat, sowDate)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid sow date %q — use YYYY-MM-DD format", sowDate)
	}

	// Same as Schedule — one shared CycleID for all tasks in this batch.
	cycleID, err := task.GenerateID()
	if err != nil {
		return nil, nil, fmt.Errorf("could not generate cycle ID: %w", err)
	}

	var preview []ScheduledDay
	var tasks []task.Task

	for _, day := range c.Days {
		// Offset from sow date: Day 1 → 0 days, Day 0 → -1 day, Day 2 → +1 day.
		daysFromSow := day.Day - 1
		date := sow.AddDate(0, 0, daysFromSow)
		dateStr := date.Format(task.DateFormat)

		preview = append(preview, ScheduledDay{
			Date:    dateStr,
			CropDay: day,
		})

		// Same title format as Schedule: "Sunnies 12x dark"
		title := fmt.Sprintf("%s %dx %s", task.Capitalize(c.Name), trays, day.Stage)

		trayWord := "tray"
		if trays != 1 {
			trayWord = "trays"
		}
		var notes string
		if strings.TrimSpace(day.Tasks) == "" {
			notes = fmt.Sprintf("%d %s · %s", trays, trayWord, task.NoTasksNote)
		} else {
			notes = fmt.Sprintf("%d %s · %s", trays, trayWord, day.Tasks)
		}

		t, err := task.New(title, dateStr, notes)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating task for day %d: %w", day.Day, err)
		}

		t.CycleID = cycleID

		tasks = append(tasks, t)
	}

	return preview, tasks, nil
}

