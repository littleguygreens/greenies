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

// buildTask creates a single calendar task from one day in a crop cycle.
// This is the shared core used by all three scheduling functions below.
//
// seedGrams is the crop's seed weight per tray (from crop.Crop.SeedGrams).
// When the day's task instructions mention "measure seed", the phrase is
// expanded to include the total seed weight — e.g. "measure 2000g seed" for
// 8 trays of a crop with 250g/tray. Pass 0 to skip this substitution.
func buildTask(cropName string, day crop.CropDay, dateStr string, trays int, cycleID string, seedGrams int, saturateMl int) (task.Task, error) {
	// Title format: "Sunnies 12x dark" — crop name, tray count, stage.
	title := fmt.Sprintf("%s %dx %s", task.Capitalize(cropName), trays, day.Stage)

	// Notes include tray count and that day's task instructions.
	trayWord := task.TrayWord(trays)
	taskText := day.Tasks

	// Expand "measure seed" → "measure Xg seed" so the grower knows exactly
	// how much seed to weigh out without needing to calculate it themselves.
	if seedGrams > 0 && strings.Contains(taskText, "measure seed") {
		totalGrams := trays * seedGrams
		taskText = strings.ReplaceAll(taskText, "measure seed", fmt.Sprintf("measure %dg seed", totalGrams))
	}

	// Expand "sow seed" → "sow seed - Xg/tray" so the grower can see the
	// per-tray seed rate at a glance without looking it up in the crop library.
	// Uses per-tray rate (not total) because sowing is done tray by tray.
	if seedGrams > 0 && strings.Contains(taskText, "sow seed") {
		taskText = strings.ReplaceAll(taskText, "sow seed", fmt.Sprintf("sow seed - %dg/tray", seedGrams))
	}

	// Expand "saturate dirt" → "saturate dirt - XmL" so the grower knows
	// exactly how much water to use without looking it up per variety.
	if saturateMl > 0 && strings.Contains(taskText, "saturate dirt") {
		taskText = strings.ReplaceAll(taskText, "saturate dirt", fmt.Sprintf("saturate dirt - %dmL", saturateMl))
	}

	var notes string
	if strings.TrimSpace(taskText) == "" {
		notes = fmt.Sprintf("%d %s · %s", trays, trayWord, task.NoTasksNote)
	} else {
		notes = fmt.Sprintf("%d %s · %s", trays, trayWord, taskText)
	}

	t, err := task.New(title, dateStr, notes)
	if err != nil {
		return task.Task{}, fmt.Errorf("error creating task for day %d: %w", day.Day, err)
	}
	t.CycleID = cycleID
	return t, nil
}

// ScheduledDay is one day in the preview shown to the user before they confirm.
// It holds the calculated calendar date alongside the crop day information,
// so the preview can display both "March 7th" and "Day 1 — sow" together.
type ScheduledDay struct {
	Date    string       // calendar date in YYYY-MM-DD format
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

		t, err := buildTask(c.Name, day, dateStr, trays, cycleID, c.SeedGrams, c.SaturateMl)
		if err != nil {
			return nil, nil, err
		}
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

		t, err := buildTask(c.Name, day, dateStr, trays, cycleID, c.SeedGrams, c.SaturateMl)
		if err != nil {
			return nil, nil, err
		}
		tasks = append(tasks, t)
	}

	return preview, tasks, nil
}

// ScheduleFromDay generates calendar tasks for a cycle that is already in
// progress, starting from a specific day number onwards. Past days are skipped.
//
// Use this when a batch needs its remaining schedule regenerated — for example
// when the tray count changes mid-cycle, or when the sow-date anchor shifts
// because blackout days were added or removed.
//
// Parameters:
//
//	c          — the crop template loaded from crops.csv
//	sowDate    — the Day 1 reference date in YYYY-MM-DD format.
//	             For a blackout-days adjustment, pass the *shifted* sow date
//	             so that every day-to-calendar-date mapping moves together.
//	fromDayNum — the first day number to include. Days before this are skipped
//	             because they are already in the past.
//	             Compute as: int(today.Sub(originalSow).Hours()/24) + 2
//	             (that gives you "tomorrow's day number" in the original cycle)
//	trays      — how many trays to use in the task titles and notes
//	cycleID    — the *existing* CycleID to stamp on every generated task.
//	             Reusing the same ID keeps the tasks linked to the existing
//	             Cycle record in cycles.json — the snapshot, conflict checker,
//	             and delete command all continue to work without special cases.
//
// Returns only a tasks slice (no preview) because the user has already
// confirmed the change before this function is called.
func ScheduleFromDay(c crop.Crop, sowDate string, fromDayNum int, trays int, cycleID string) ([]task.Task, error) {
	sow, err := time.Parse(task.DateFormat, sowDate)
	if err != nil {
		return nil, fmt.Errorf("invalid sow date %q — use YYYY-MM-DD format", sowDate)
	}

	var tasks []task.Task

	for _, day := range c.Days {
		// Skip every day that has already happened — we only want to regenerate
		// the portion of the schedule that lies in the future.
		if day.Day < fromDayNum {
			continue
		}

		// Same offset formula as ScheduleForward:
		// Day 1 maps to sowDate, Day 0 maps to sowDate-1, Day 2 to sowDate+1, etc.
		daysFromSow := day.Day - 1
		date := sow.AddDate(0, 0, daysFromSow)
		dateStr := date.Format(task.DateFormat)

		t, err := buildTask(c.Name, day, dateStr, trays, cycleID, c.SeedGrams, c.SaturateMl)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}

	return tasks, nil
}
