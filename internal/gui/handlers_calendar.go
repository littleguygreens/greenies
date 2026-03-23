// handlers_calendar.go — Week and month calendar view handler.
//
// This renders the full calendar page at "/list" — the same information as
// "greenies list" in the terminal, but laid out as a visual calendar grid.
package gui

import (
	"net/http"
	"strconv"
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// List (Calendar view)
// ─────────────────────────────────────────────────────────────────────────────

// dayCard holds the data for one day on the calendar page. Each day gets its
// own card showing the date heading and all the tasks scheduled for that day.
type dayCard struct {
	// DateHeading is the human-readable date, e.g. "Thursday, 5 March 2026".
	DateHeading string
	// Tasks is the list of tasks on this specific day.
	Tasks []task.Task
	// HasTasks is true if there is at least one task on this day.
	HasTasks bool
}

// handleCalendar renders the combined calendar page at "/list".
//
// This is the main scheduling view — two sections stacked on one page:
//  1. Swim-lane month grid (coloured bars showing crop stages at a glance)
//  2. Daily task tiles (every task for each day, grouped by date)
//
// Both sections show the same month. The grower navigates with prev/next
// buttons — one set of controls for the whole page.
func handleCalendar(w http.ResponseWriter, r *http.Request) {
	// ── Load data ────────────────────────────────────────────────────────
	// Tasks (for the daily tile list).
	allTasks, err := store.Load()
	if err != nil {
		allTasks = []task.Task{}
	}

	// Cycles + crop library (for the swim-lane grid).
	cycles, err := farm.LoadCycles()
	if err != nil {
		cycles = []farm.Cycle{}
	}

	cropMap := map[string]crop.Crop{}
	if cropsSource, err := crop.GetSource(); err == nil {
		if allCrops, err := cropsSource.LoadCrops(); err == nil {
			for _, cr := range allCrops {
				cropMap[cr.Name] = cr
			}
		}
	}

	// ── Determine the displayed month ────────────────────────────────────
	now := task.Today()
	year, month := now.Year(), now.Month()

	// Allow ?year=2026&month=4 to navigate to a different month.
	if y := r.URL.Query().Get("year"); y != "" {
		if parsed, err := strconv.Atoi(y); err == nil {
			year = parsed
		}
	}
	if m := r.URL.Query().Get("month"); m != "" {
		if parsed, err := strconv.Atoi(m); err == nil && parsed >= 1 && parsed <= 12 {
			month = time.Month(parsed)
		}
	}

	// ── Week start preference ───────────────────────────────────────────
	// Read from config.json (set on the Settings page). Defaults to Sunday.
	cfg, _ := config.Load()
	weekStartDay := cfg.WeekStart
	if weekStartDay != "mon" {
		weekStartDay = "sun"
	}

	var firstWeekday, lastWeekday time.Weekday
	if weekStartDay == "sun" {
		firstWeekday = time.Sunday
		lastWeekday = time.Saturday
	} else {
		firstWeekday = time.Monday
		lastWeekday = time.Sunday
	}

	dayLabels := [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	if weekStartDay == "sun" {
		dayLabels = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	}

	// First and last day of the displayed month.
	firstOfMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	lastOfMonth := firstOfMonth.AddDate(0, 1, -1)

	// The grid starts on the chosen weekday at or before the 1st, and
	// ends on the corresponding last weekday at or after the last day
	// of the month. This fills out complete weeks so the grid is
	// rectangular.
	gridStart := firstOfMonth
	for gridStart.Weekday() != firstWeekday {
		gridStart = gridStart.AddDate(0, 0, -1)
	}
	gridEnd := lastOfMonth
	for gridEnd.Weekday() != lastWeekday {
		gridEnd = gridEnd.AddDate(0, 0, 1)
	}

	// ── Pre-compute each cycle's daily stage map ─────────────────────────
	// buildCycleStages (in handlers_swimlane.go) sorts the cycles and builds
	// a date → stage lookup map for each one. Used by both the full-month
	// calendar here and the snapshot-week mini calendar.
	allCycleInfo := buildCycleStages(cycles, cropMap)

	// ── Build the swim-lane week sections ────────────────────────────────
	var weeks []monthWeek

	for weekStart := gridStart; !weekStart.After(gridEnd); weekStart = weekStart.AddDate(0, 0, 7) {
		week := monthWeek{}

		// Fill in the 7 date headers for this week row.
		for i := 0; i < 7; i++ {
			d := weekStart.AddDate(0, 0, i)
			week.Headers[i] = monthDayHeader{
				DayNum:        d.Day(),
				InMonth:       d.Month() == month,
				IsHighlighted: d.Equal(now),
				DateStr:       d.Format(task.DateFormat),
			}
		}

		// Check each cycle — does it have any activity this week?
		for _, ci := range allCycleInfo {
			row := monthCycleRow{
				CropName: ci.CropName,
				Trays:    ci.Trays,
			}
			hasActivity := false

			for i := 0; i < 7; i++ {
				d := weekStart.AddDate(0, 0, i)
				ds := d.Format(task.DateFormat)
				stage := ci.DayStage[ds]

				row.Cells[i] = monthCell{
					DayNum:        d.Day(),
					Stage:         stage,
					Trays:         ci.Trays,
					InMonth:       d.Month() == month,
					IsHighlighted: d.Equal(now),
					DateStr:       ds,
				}

				if stage != "" {
					hasActivity = true
				}
			}

			// Place one centered label per row across all active cells.
			placeRowLabel(&row.Cells, ci.CropName, ci.Trays, ci.SowDate, ci.HarvestDate)

			if hasActivity {
				week.Rows = append(week.Rows, row)
			}
		}

		// Only include weeks that have at least one active cycle.
		if len(week.Rows) > 0 {
			weeks = append(weeks, week)
		}
	}

	// ── Build the daily task tiles ───────────────────────────────────────
	// Show every day of the displayed month with that day's tasks.
	var days []dayCard
	for d := firstOfMonth; !d.After(lastOfMonth); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format(task.DateFormat)
		dayTasks := calendar.TasksForDate(allTasks, dateStr)
		days = append(days, dayCard{
			DateHeading: d.Format("Monday, 2 January 2006"),
			Tasks:       dayTasks,
			HasTasks:    len(dayTasks) > 0,
		})
	}

	// ── Navigation: previous and next month ──────────────────────────────
	prevMonth := firstOfMonth.AddDate(0, -1, 0)
	nextMonth := firstOfMonth.AddDate(0, 1, 0)

	renderPage(w, "list.html", map[string]any{
		// Swim-lane data
		"MonthLabel": firstOfMonth.Format("January 2006"),
		"Weeks":      weeks,
		"DayLabels":  dayLabels,
		"Year":       year,
		"Month":      int(month),
		"PrevYear":   prevMonth.Year(),
		"PrevMonth":  int(prevMonth.Month()),
		"NextYear":   nextMonth.Year(),
		"NextMonth":  int(nextMonth.Month()),
		// Task tile data
		"Days": days,
	})
}
