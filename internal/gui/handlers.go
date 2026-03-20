// handlers.go contains the HTTP handler functions for the GUI.
//
// Each handler is responsible for one page or action. It loads data from the
// same internal packages the CLI uses (store, farm, visualizer, etc.), then
// passes that data to an HTML template for display.
//
// Think of each handler as the GUI equivalent of a runX() function in the CLI.
// The difference is that instead of printing to the terminal with fmt.Println,
// it fills in an HTML template and sends it to the browser.
package gui

import (
	"context"       // for context.Background(), used when calling Google Calendar
	"encoding/json"  // for marshalling crop day data to JSON for the edit page
	"fmt"
	"html/template"  // for template.JS — marks a string as safe JavaScript
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/trial"
	"github.com/littleguygreens/greenies/internal/visualizer"
)

// ─────────────────────────────────────────────────────────────────────────────
// Dashboard
// ─────────────────────────────────────────────────────────────────────────────

// handleDashboard renders the home page at "/".
//
// It shows two things:
//  1. Today's farm snapshot (what's growing, what needs attention)
//  2. Today's scheduled tasks (what the grower needs to do today)
//
// This gives the grower a quick overview the moment they open the GUI —
// the same information they'd get from running "greenies snapshot" and
// "greenies list" in the terminal, but all on one page.
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	today := task.Today()
	todayStr := today.Format(task.DateFormat)

	// Load the farm snapshot as structured data — the GUI renders this with
	// styled cards and progress bars instead of a plain <pre> text block.
	var snap visualizer.SnapshotData
	var swimWeek monthWeek
	var dayLabels [7]string
	envs, err := farm.LoadConfig()
	if err == nil {
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			snap = visualizer.BuildSnapshot(envs, cycles, today)
			// Build one week of swim-lane calendar data for this week,
			// with today's column highlighted.
			cfg, _ := config.Load()
			swimWeek, dayLabels = buildSnapshotWeek(today, cycles, cfg.WeekStart)
		}
	}

	// Load today's tasks — the same data "greenies list" would show for today.
	todayTasks := []task.Task{}
	allTasks, err := store.Load()
	if err == nil {
		todayTasks = calendar.TasksForDate(allTasks, todayStr)
	}

	// Send the data to the dashboard template for rendering.
	renderPage(w, "dashboard.html", map[string]any{
		"Today":     today.Format("Monday, 2 January 2006"),
		"TodayDate": todayStr,
		"Snapshot":  snap,
		"SwimWeek":  swimWeek,
		"DayLabels": dayLabels,
		"HasSwim":   len(swimWeek.Rows) > 0,
		"Tasks":     todayTasks,
		"HasTasks":  len(todayTasks) > 0,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot
// ─────────────────────────────────────────────────────────────────────────────

// handleSnapshot renders the farm snapshot page at "/snapshot".
//
// It works exactly like "greenies snapshot" in the terminal — loads the farm
// layout and cycle records, then renders the text snapshot in a <pre> block.
//
// The page includes a date picker so the grower can look at past or future
// snapshots without typing a date in the terminal. If no date is provided
// in the URL (e.g. /snapshot?date=2026-03-20), it defaults to today.
func handleSnapshot(w http.ResponseWriter, r *http.Request) {
	// Check if the grower picked a specific date using the date picker.
	// The browser sends it as a query parameter like ?date=2026-03-20.
	// If no date is given, default to today.
	dateStr := r.URL.Query().Get("date")
	snapshotTime := task.Today()
	if dateStr != "" {
		parsed, err := time.Parse(task.DateFormat, dateStr)
		if err == nil {
			snapshotTime = parsed
		}
	}

	// Load the farm snapshot as structured data for the GUI to render
	// with styled cards, progress bars, and colour-coded stages.
	var snap visualizer.SnapshotData
	var swimWeek monthWeek
	var dayLabels [7]string
	envs, err := farm.LoadConfig()
	if err == nil {
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			snap = visualizer.BuildSnapshot(envs, cycles, snapshotTime)
			// Build one week of swim-lane calendar data centred on the
			// snapshot date, with that date's column highlighted.
			cfg, _ := config.Load()
			swimWeek, dayLabels = buildSnapshotWeek(snapshotTime, cycles, cfg.WeekStart)
		}
	}

	renderPage(w, "snapshot.html", map[string]any{
		"Date":      snapshotTime.Format("Monday, 2 January 2006"),
		"DateValue": snapshotTime.Format(task.DateFormat),
		"Snapshot":  snap,
		"SwimWeek":  swimWeek,
		"DayLabels": dayLabels,
		"HasSwim":   len(swimWeek.Rows) > 0,
	})
}

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
//   1. Swim-lane month grid (coloured bars showing crop stages at a glance)
//   2. Daily task tiles (every task for each day, grouped by date)
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

	// ── Sort cycles oldest-first ─────────────────────────────────────────
	sort.Slice(cycles, func(i, j int) bool {
		if cycles[i].SowDate != cycles[j].SowDate {
			return cycles[i].SowDate < cycles[j].SowDate
		}
		return cycles[i].CropName < cycles[j].CropName
	})

	// ── Pre-compute each cycle's daily stage map ─────────────────────────
	// For each cycle, build a map from date string → stage name. This makes
	// it fast to look up "what stage is this cycle in on March 12th?"
	type cycleInfo struct {
		CycleID  string
		CropName string
		Trays    int
		DayStage map[string]string // date → "soak"/"dark"/"light"/"harvest"
	}

	var allCycleInfo []cycleInfo

	for _, c := range cycles {
		sowDate, err1 := time.Parse(task.DateFormat, c.SowDate)
		harvestDate, err2 := time.Parse(task.DateFormat, c.HarvestDate)
		mtlDate, err3 := time.Parse(task.DateFormat, c.MoveToLightDate)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		stages := map[string]string{}
		cr, hasCrop := cropMap[c.CropName]

		// Soak day(s) — before the blackout bar starts.
		if hasCrop && cr.OvernightSoak {
			soakDay := sowDate.AddDate(0, 0, -1)
			stages[soakDay.Format(task.DateFormat)] = "soak"
		} else if hasCrop && cr.SoakHours > 0 {
			stages[sowDate.Format(task.DateFormat)] = "soak"
		}

		// Walk from sow date to harvest date, assigning stages.
		for d := sowDate; !d.After(harvestDate); d = d.AddDate(0, 0, 1) {
			ds := d.Format(task.DateFormat)
			if _, already := stages[ds]; already {
				continue // soak day already set
			}
			if d.Equal(harvestDate) {
				stages[ds] = "harvest"
			} else if d.Before(mtlDate) {
				stages[ds] = "dark"
			} else {
				stages[ds] = "light"
			}
		}

		allCycleInfo = append(allCycleInfo, cycleInfo{
			CycleID:  c.CycleID,
			CropName: c.CropName,
			Trays:    c.Trays,
			DayStage: stages,
		})
	}

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

			prevStage := ""
			for i := 0; i < 7; i++ {
				d := weekStart.AddDate(0, 0, i)
				ds := d.Format(task.DateFormat)
				stage := ci.DayStage[ds]

				// Put a label on the first cell of each new stage run.
				label := ""
				if stage != "" && stage != prevStage {
					label = fmt.Sprintf("%s %dx", ci.CropName, ci.Trays)
				}

				row.Cells[i] = monthCell{
					DayNum:        d.Day(),
					Stage:         stage,
					Label:         label,
					Trays:         ci.Trays,
					InMonth:       d.Month() == month,
					IsHighlighted: d.Equal(now),
					DateStr:       ds,
				}

				prevStage = stage
				if stage != "" {
					hasActivity = true
				}
			}

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

// ─────────────────────────────────────────────────────────────────────────────
// Crops
// ─────────────────────────────────────────────────────────────────────────────

// handleCrops renders the crop library page at "/crops".
//
// It works like "greenies crops" — loads the crop CSV and displays every
// variety in an HTML table. The table shows the key numbers a grower cares
// about: cycle length, seed per tray, and expected yield per tray.
func handleCrops(w http.ResponseWriter, r *http.Request) {
	// Load the crop library using the shared factory function.
	source, err := crop.GetSource()
	if err != nil {
		renderPage(w, "crops.html", map[string]any{
			"Error":    "Could not find crops file: " + err.Error(),
			"HasCrops": false,
		})
		return
	}

	crops, err := source.LoadCrops()
	if err != nil {
		renderPage(w, "crops.html", map[string]any{
			"Error":    "Could not load crop library: " + err.Error(),
			"HasCrops": false,
		})
		return
	}

	renderPage(w, "crops.html", map[string]any{
		"Crops":    crops,
		"HasCrops": len(crops) > 0,
		"Count":    len(crops),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Add New Crop
// ─────────────────────────────────────────────────────────────────────────────

// handleCropNewPage renders the "Add New Crop" form at GET /crops/new.
//
// This is a static form page — no data needs to be loaded. The form uses
// JavaScript to dynamically generate day rows based on the total cycle
// days the grower enters, and htmx to submit without a full page reload.
func handleCropNewPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "crop_new.html", nil)
}

// handleCropNewAction processes the POST /crops/new form submission.
//
// It reads the crop parameters and day-by-day tasks from the form, builds
// a crop.Crop value, appends it to crops.csv, and optionally syncs to
// Google Sheets. Returns an htmx fragment with a success or error message.
func handleCropNewAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "crop_new_result.html", map[string]any{
			"Error": "Could not read form data: " + err.Error(),
		})
		return
	}

	// ── Read crop name ──────────────────────────────────────────────────
	cropName := strings.TrimSpace(r.FormValue("crop_name"))
	if cropName == "" {
		renderFragment(w, "crop_new_result.html", map[string]any{
			"Error": "Crop name is required.",
		})
		return
	}
	// Lowercase the name — the scheduler expects lowercase crop names.
	cropName = strings.ToLower(cropName)

	// ── Read soak settings ──────────────────────────────────────────────
	soakType := r.FormValue("soak_type") // "none", "hours", or "overnight"
	overnightSoak := soakType == "overnight"
	soakHours := 0
	if soakType == "hours" {
		if v, err := strconv.Atoi(r.FormValue("soak_hours")); err == nil {
			soakHours = v
		}
	} else if overnightSoak {
		soakHours = 12 // sensible default for overnight soaks
	}

	// ── Read numeric parameters ─────────────────────────────────────────
	seedGrams := 0
	if v, err := strconv.Atoi(r.FormValue("seed_grams")); err == nil {
		seedGrams = v
	}

	dirtLitres := 1.0
	if v, err := strconv.ParseFloat(r.FormValue("dirt_litres"), 64); err == nil && v > 0 {
		dirtLitres = v
	}

	yieldGrams := 0
	if v, err := strconv.Atoi(r.FormValue("yield_grams")); err == nil {
		yieldGrams = v
	}

	// ── Read the day rows ───────────────────────────────────────────────
	// The form sends parallel arrays: day_num[], day_stage[], day_tasks[].
	dayNums := r.Form["day_num[]"]
	dayStages := r.Form["day_stage[]"]
	dayTasks := r.Form["day_tasks[]"]

	if len(dayNums) == 0 {
		renderFragment(w, "crop_new_result.html", map[string]any{
			"Error": "No cycle days defined — set the total cycle days first.",
		})
		return
	}

	// Build the CropDay list and count dark/light days along the way.
	var days []crop.CropDay
	darkDays := 0
	lightDays := 0

	for i := range dayNums {
		dayNum, err := strconv.Atoi(dayNums[i])
		if err != nil {
			continue
		}

		stage := "dark"
		if i < len(dayStages) {
			stage = strings.TrimSpace(dayStages[i])
		}

		tasks := ""
		if i < len(dayTasks) {
			tasks = strings.TrimSpace(dayTasks[i])
		}

		days = append(days, crop.CropDay{
			Day:   dayNum,
			Stage: stage,
			Tasks: tasks,
		})

		// Count stages for the dark_days and light_days fields.
		switch stage {
		case "dark":
			darkDays++
		case "light":
			lightDays++
		}
	}

	if len(days) < 2 {
		renderFragment(w, "crop_new_result.html", map[string]any{
			"Error": "A crop needs at least 2 days (sow and harvest).",
		})
		return
	}

	// ── Build the Crop value ────────────────────────────────────────────
	// CycleDays is derived from the last day's number — same as the CSV
	// loader does, so it can never get out of sync.
	newCrop := crop.Crop{
		Name:          cropName,
		CycleDays:     days[len(days)-1].Day,
		OvernightSoak: overnightSoak,
		SoakHours:     soakHours,
		SeedGrams:     seedGrams,
		DirtLitres:    dirtLitres,
		DarkDays:      darkDays,
		LightDays:     lightDays,
		YieldGrams:    yieldGrams,
		Days:          days,
	}

	// ── Save to crops.csv ───────────────────────────────────────────────
	if err := crop.AppendCrop(newCrop); err != nil {
		renderFragment(w, "crop_new_result.html", map[string]any{
			"Error": "Could not save crop: " + err.Error(),
		})
		return
	}

	// ── Sync to Google Sheets (fire-and-forget) ─────────────────────────
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		go gcal.SyncLocalToSheet(context.Background())
	}

	renderFragment(w, "crop_new_result.html", map[string]any{
		"Success":  true,
		"CropName": cropName,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Edit Crop
// ─────────────────────────────────────────────────────────────────────────────

// handleCropEditPage renders the "Edit Crop" form at GET /crops/edit?name=X.
//
// It loads the named crop from crops.csv and passes it to the template so
// every field starts pre-filled. The day-by-day data is also passed as a
// JSON array so JavaScript can build the pre-filled day rows.
func handleCropEditPage(w http.ResponseWriter, r *http.Request) {
	cropName := strings.TrimSpace(r.URL.Query().Get("name"))
	if cropName == "" {
		renderPage(w, "crops.html", map[string]any{
			"Error":    "No crop name specified.",
			"HasCrops": false,
		})
		return
	}

	// Load the crop library and find the one we want to edit.
	source, err := crop.GetSource()
	if err != nil {
		renderPage(w, "crops.html", map[string]any{
			"Error":    "Could not find crops file: " + err.Error(),
			"HasCrops": false,
		})
		return
	}

	crops, err := source.LoadCrops()
	if err != nil {
		renderPage(w, "crops.html", map[string]any{
			"Error":    "Could not load crop library: " + err.Error(),
			"HasCrops": false,
		})
		return
	}

	// Find the crop by name (case-insensitive).
	var found *crop.Crop
	for i := range crops {
		if strings.EqualFold(crops[i].Name, cropName) {
			found = &crops[i]
			break
		}
	}
	if found == nil {
		renderPage(w, "crops.html", map[string]any{
			"Error":    fmt.Sprintf("Crop %q not found in your library.", cropName),
			"HasCrops": false,
		})
		return
	}

	// Convert the day data to JSON so the JavaScript on the page can use it
	// to pre-fill the day-by-day table rows. We wrap it in template.JS so
	// that Go's html/template engine knows this is safe JavaScript and does
	// not HTML-escape the quotes (which would break the JSON parsing).
	daysJSON, err := json.Marshal(found.Days)
	if err != nil {
		daysJSON = []byte("[]")
	}

	renderPage(w, "crop_edit.html", map[string]any{
		"Crop":     *found,
		"DaysJSON": template.JS(daysJSON),
		"OrigName": found.Name,
	})
}

// handleCropEditAction processes the POST /crops/edit form submission.
//
// It reads the updated crop parameters and day-by-day tasks from the form,
// builds a new crop.Crop value, and replaces the old version in crops.csv.
// The original name is sent as a hidden field so renames work correctly.
func handleCropEditAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": "Could not read form data: " + err.Error(),
		})
		return
	}

	// ── Read the original name (hidden field) ─────────────────────────
	originalName := strings.TrimSpace(r.FormValue("original_name"))
	if originalName == "" {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": "Missing original crop name — cannot save.",
		})
		return
	}

	// ── Read crop name ────────────────────────────────────────────────
	cropName := strings.TrimSpace(r.FormValue("crop_name"))
	if cropName == "" {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": "Crop name is required.",
		})
		return
	}
	cropName = strings.ToLower(cropName)

	// ── Read soak settings ────────────────────────────────────────────
	soakType := r.FormValue("soak_type")
	overnightSoak := soakType == "overnight"
	soakHours := 0
	if soakType == "hours" {
		if v, err := strconv.Atoi(r.FormValue("soak_hours")); err == nil {
			soakHours = v
		}
	} else if overnightSoak {
		soakHours = 12
	}

	// ── Read numeric parameters ───────────────────────────────────────
	seedGrams := 0
	if v, err := strconv.Atoi(r.FormValue("seed_grams")); err == nil {
		seedGrams = v
	}

	dirtLitres := 1.0
	if v, err := strconv.ParseFloat(r.FormValue("dirt_litres"), 64); err == nil && v > 0 {
		dirtLitres = v
	}

	yieldGrams := 0
	if v, err := strconv.Atoi(r.FormValue("yield_grams")); err == nil {
		yieldGrams = v
	}

	// ── Read the day rows ─────────────────────────────────────────────
	dayNums := r.Form["day_num[]"]
	dayStages := r.Form["day_stage[]"]
	dayTasks := r.Form["day_tasks[]"]

	if len(dayNums) == 0 {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": "No cycle days defined — set the total cycle days first.",
		})
		return
	}

	var days []crop.CropDay
	darkDays := 0
	lightDays := 0

	for i := range dayNums {
		dayNum, err := strconv.Atoi(dayNums[i])
		if err != nil {
			continue
		}

		stage := "dark"
		if i < len(dayStages) {
			stage = strings.TrimSpace(dayStages[i])
		}

		tasks := ""
		if i < len(dayTasks) {
			tasks = strings.TrimSpace(dayTasks[i])
		}

		days = append(days, crop.CropDay{
			Day:   dayNum,
			Stage: stage,
			Tasks: tasks,
		})

		switch stage {
		case "dark":
			darkDays++
		case "light":
			lightDays++
		}
	}

	if len(days) < 2 {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": "A crop needs at least 2 days (sow and harvest).",
		})
		return
	}

	// ── Build the updated Crop value ──────────────────────────────────
	updatedCrop := crop.Crop{
		Name:          cropName,
		CycleDays:     days[len(days)-1].Day,
		OvernightSoak: overnightSoak,
		SoakHours:     soakHours,
		SeedGrams:     seedGrams,
		DirtLitres:    dirtLitres,
		DarkDays:      darkDays,
		LightDays:     lightDays,
		YieldGrams:    yieldGrams,
		Days:          days,
	}

	// ── Replace in crops.csv ──────────────────────────────────────────
	if err := crop.ReplaceCrop(originalName, updatedCrop); err != nil {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": "Could not save crop: " + err.Error(),
		})
		return
	}

	// ── Sync to Google Sheets (fire-and-forget) ───────────────────────
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		go gcal.SyncLocalToSheet(context.Background())
	}

	renderFragment(w, "crop_edit_result.html", map[string]any{
		"Success":  true,
		"CropName": cropName,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Harvest Log
// ─────────────────────────────────────────────────────────────────────────────

// handleHarvestLog renders the harvest history page at "/harvestlog".
//
// It works like "greenies harvestlog" — loads all saved harvest records and
// displays them in a table, most recent first. Each row shows the planned
// yield alongside what was actually cut, so the grower can spot trends.
func handleHarvestLog(w http.ResponseWriter, r *http.Request) {
	harvests, err := farm.LoadHarvests()
	if err != nil {
		harvests = []farm.HarvestRecord{}
	}

	// Sort most recent first — same order as the CLI command.
	// HarvestDate is YYYY-MM-DD, so string comparison works correctly.
	sort.Slice(harvests, func(i, j int) bool {
		return harvests[i].HarvestDate > harvests[j].HarvestDate
	})

	// Parse harvest dates into human-readable format for the template.
	// We build a parallel slice of display data because Go templates
	// can't call time.Parse directly.
	type harvestRow struct {
		DateDisplay   string // e.g. "Mar 15"
		CropName      string // capitalised crop name
		ExpectedTrays int
		ActualTrays   int
		ExpectedGrams int
		ActualGrams   int
		Notes         string
		HasExpected   bool // true if ExpectedGrams > 0
	}

	var rows []harvestRow
	for _, h := range harvests {
		dateDisplay := h.HarvestDate // fallback to raw string
		if t, err := time.Parse(task.DateFormat, h.HarvestDate); err == nil {
			dateDisplay = t.Format("Jan 02")
		}
		rows = append(rows, harvestRow{
			DateDisplay:   dateDisplay,
			CropName:      task.Capitalize(h.CropName),
			ExpectedTrays: h.ExpectedTrays,
			ActualTrays:   h.ActualTrays,
			ExpectedGrams: h.ExpectedGrams,
			ActualGrams:   h.ActualGrams,
			Notes:         h.Notes,
			HasExpected:   h.ExpectedGrams > 0,
		})
	}

	renderPage(w, "harvestlog.html", map[string]any{
		"Harvests":    rows,
		"HasHarvests": len(rows) > 0,
		"Count":       len(rows),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

// deleteTask is the template-friendly version of a task for the delete page.
// It adds a HasCycle flag so the template knows whether to show the
// task-vs-cycle choice or just a plain delete button.
type deleteTask struct {
	ID       string // the task's unique ID
	Title    string // what the task says (e.g. "Sow sunflowers — 4 trays")
	Notes    string // extra detail (e.g. "main tent")
	Date     string // the date string in YYYY-MM-DD format
	HasCycle bool   // true if this task belongs to a planned crop cycle
}

// deleteDay groups tasks by date for the delete page — same idea as dayCard
// on the calendar page, but using deleteTask instead of task.Task.
type deleteDay struct {
	DateHeading string       // e.g. "Thursday, 5 March 2026"
	Tasks       []deleteTask // the tasks on this day
}

// handleDeletePage renders the task deletion page at GET /delete.
//
// It loads all tasks, groups them by date (soonest first), and passes them
// to the delete template. Each task card includes a delete button that
// uses htmx to remove it without reloading the whole page.
func handleDeletePage(w http.ResponseWriter, r *http.Request) {
	allTasks, err := store.Load()
	if err != nil {
		allTasks = []task.Task{}
	}

	// Sort tasks by date so the page shows them in chronological order.
	sort.Slice(allTasks, func(i, j int) bool {
		return allTasks[i].Date < allTasks[j].Date
	})

	// Group tasks by date — one deleteDay per unique date, in order.
	dayMap := map[string]*deleteDay{}
	var dayOrder []string
	for _, t := range allTasks {
		if _, exists := dayMap[t.Date]; !exists {
			heading := t.Date // fallback
			if parsed, err := time.Parse(task.DateFormat, t.Date); err == nil {
				heading = parsed.Format("Monday, 2 January 2006")
			}
			dayMap[t.Date] = &deleteDay{DateHeading: heading}
			dayOrder = append(dayOrder, t.Date)
		}
		dayMap[t.Date].Tasks = append(dayMap[t.Date].Tasks, deleteTask{
			ID:       t.ID,
			Title:    t.Title,
			Notes:    t.Notes,
			Date:     t.Date,
			HasCycle: t.CycleID != "",
		})
	}

	var days []deleteDay
	for _, date := range dayOrder {
		days = append(days, *dayMap[date])
	}

	renderPage(w, "delete.html", map[string]any{
		"Days":     days,
		"HasTasks": len(allTasks) > 0,
		"Count":    len(allTasks),
	})
}

// handleDeleteConfirm handles GET /delete/confirm?id=xxx.
//
// This is called by htmx when the grower clicks "Delete…" on a cycle task.
// It returns a small HTML fragment with the "Just this task" and "Whole cycle"
// buttons. The fragment is swapped into the button area of that task card.
func handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing task ID", http.StatusBadRequest)
		return
	}

	// Load tasks to count how many share this task's cycle.
	allTasks, err := store.Load()
	if err != nil {
		http.Error(w, "Could not load tasks", http.StatusInternalServerError)
		return
	}

	// Find the target task and count its cycle siblings.
	var cycleID string
	for _, t := range allTasks {
		if t.ID == id {
			cycleID = t.CycleID
			break
		}
	}
	cycleCount := 0
	for _, t := range allTasks {
		if t.CycleID == cycleID {
			cycleCount++
		}
	}

	renderFragment(w, "delete_confirm.html", map[string]any{
		"ID":         id,
		"CycleCount": cycleCount,
	})
}

// handleDeleteAction handles POST /delete.
//
// This is the handler that actually removes tasks. It reads two form values:
//   - "id"   — the task ID that was clicked
//   - "mode" — either "task" (delete just this one) or "cycle" (delete all
//     tasks sharing this task's CycleID)
//
// After deletion, it returns a small HTML fragment. For a single-task delete,
// it returns empty HTML (the card just disappears). For a cycle delete, it
// returns a brief message saying how many tasks were removed.
func handleDeleteAction(w http.ResponseWriter, r *http.Request) {
	// Parse the form data sent by htmx.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	id := r.FormValue("id")
	mode := r.FormValue("mode")

	if id == "" {
		http.Error(w, "Missing task ID", http.StatusBadRequest)
		return
	}

	allTasks, err := store.Load()
	if err != nil {
		http.Error(w, "Could not load tasks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Find the target task.
	var target *task.Task
	for i := range allTasks {
		if allTasks[i].ID == id {
			target = &allTasks[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// Build the set of IDs to delete.
	deleteByID := map[string]bool{id: true}
	if mode == "cycle" && target.CycleID != "" {
		for _, t := range allTasks {
			if t.CycleID == target.CycleID {
				deleteByID[t.ID] = true
			}
		}
	}

	// Filter out the deleted tasks.
	var kept []task.Task
	for _, t := range allTasks {
		if !deleteByID[t.ID] {
			kept = append(kept, t)
		}
	}

	if err := store.Save(kept); err != nil {
		http.Error(w, "Could not save: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// If a whole cycle was deleted, also remove the cycle record from
	// cycles.json so "greenies snapshot" stays in sync.
	if mode == "cycle" && target.CycleID != "" {
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			var keptCycles []farm.Cycle
			for _, c := range cycles {
				if c.CycleID != target.CycleID {
					keptCycles = append(keptCycles, c)
				}
			}
			_ = farm.SaveCycles(keptCycles)
		}
	}

	// Return a fragment to replace the deleted task card.
	removed := len(allTasks) - len(kept)
	message := ""
	if removed > 1 {
		message = fmt.Sprintf("%d tasks deleted (entire cycle).", removed)
	}
	renderFragment(w, "delete_success.html", map[string]any{
		"Message": message,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Clear
// ─────────────────────────────────────────────────────────────────────────────

// handleClearPage renders the "clear all" confirmation page at GET /clear.
//
// It loads the current task and cycle counts to show the grower exactly
// what will be deleted, so there are no surprises.
func handleClearPage(w http.ResponseWriter, r *http.Request) {
	allTasks, _ := store.Load()
	cycles, _ := farm.LoadCycles()

	renderPage(w, "clear.html", map[string]any{
		"TaskCount":  len(allTasks),
		"CycleCount": len(cycles),
	})
}

// handleClearAction handles POST /clear.
//
// This wipes all tasks and cycle records — the same as "greenies clear" in
// the terminal. The harvest log is preserved (it's permanent history).
//
// Returns an HTML fragment that replaces the warning area with a success
// message, so the grower sees confirmation without a full page reload.
func handleClearAction(w http.ResponseWriter, r *http.Request) {
	// Clear all tasks.
	if err := store.Save([]task.Task{}); err != nil {
		http.Error(w, "Could not clear tasks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Clear all cycle records.
	if err := farm.SaveCycles([]farm.Cycle{}); err != nil {
		http.Error(w, "Could not clear cycles: "+err.Error(), http.StatusInternalServerError)
		return
	}

	renderFragment(w, "clear_success.html", nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Plan
// ─────────────────────────────────────────────────────────────────────────────

// handlePlanPage renders the crop planning form at GET /plan.
//
// This is the GUI equivalent of the interactive "greenies plan" command.
// Instead of answering questions one at a time in the terminal, the grower
// sees all the fields at once and fills them in any order.
//
// The form uses htmx to send a preview request without reloading the page.
func handlePlanPage(w http.ResponseWriter, r *http.Request) {
	// Load the crop library so we can populate the dropdown.
	source, err := crop.GetSource()
	if err != nil {
		renderPage(w, "plan.html", map[string]any{
			"HasCrops": false,
			"HasEnvs":  false,
		})
		return
	}
	crops, err := source.LoadCrops()
	if err != nil {
		renderPage(w, "plan.html", map[string]any{
			"HasCrops": false,
			"HasEnvs":  false,
		})
		return
	}

	// Load the farm layout to get the lit environment options.
	var litEnvs []farm.Environment
	farmEnvs, err := farm.LoadConfig()
	if err == nil {
		for _, e := range farmEnvs {
			if e.Type == "lit" {
				litEnvs = append(litEnvs, e)
			}
		}
	}

	renderPage(w, "plan.html", map[string]any{
		"Crops":    crops,
		"HasCrops": len(crops) > 0,
		"LitEnvs":  litEnvs,
		"HasEnvs":  len(litEnvs) > 0,
		"Today":    task.Today().Format(task.DateFormat),
	})
}

// previewDay holds the display data for one day in the schedule preview table.
// It is the template-friendly version of scheduler.ScheduledDay.
type previewDay struct {
	DateDisplay string // human-readable date, e.g. "Mon Mar 15"
	DayNum      int    // day number in the cycle (0, 1, 2, …)
	Stage       string // stage name (sow, dark, light, harvest)
	Tasks       string // task description, or empty for do-nothing days
}

// handlePlanPreview handles POST /plan/preview.
//
// This is called by htmx when the grower clicks "Preview Schedule". It reads
// the form fields, runs the scheduler to generate the full cycle, runs the
// conflict checker, and returns an HTML fragment with a preview table and
// any warnings. The grower can then click "Confirm" to save.
//
// Nothing is saved to disk during the preview — it is purely read-only.
func handlePlanPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Bad request."})
		return
	}

	// Read form values.
	cropName := r.FormValue("crop")
	planMode := r.FormValue("plan_mode")
	traysStr := r.FormValue("trays")
	yieldStr := r.FormValue("yield_grams")
	direction := r.FormValue("direction")
	dateStr := r.FormValue("date")
	litEnv := r.FormValue("lit_env")
	repeatsStr := r.FormValue("repeats")

	// ── Validate the crop ────────────────────────────────────────────────
	source, err := crop.GetSource()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not find crops file."})
		return
	}
	crops, err := source.LoadCrops()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not load crop library."})
		return
	}

	// Find the selected crop by name.
	var found *crop.Crop
	for i := range crops {
		if crops[i].Name == cropName {
			found = &crops[i]
			break
		}
	}
	if found == nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": fmt.Sprintf("Crop %q not found in the library.", cropName),
		})
		return
	}

	// ── Determine tray count ─────────────────────────────────────────────
	var trays int
	if planMode == "yield" {
		// Yield mode: calculate how many trays are needed to hit the target.
		if found.YieldGrams == 0 {
			renderFragment(w, "plan_preview.html", map[string]any{
				"Error": fmt.Sprintf("%s has no yield data in the crop library. Plan by tray count instead.", task.Capitalize(found.Name)),
			})
			return
		}
		desiredYield, convErr := strconv.Atoi(yieldStr)
		if convErr != nil || desiredYield < 1 {
			renderFragment(w, "plan_preview.html", map[string]any{
				"Error": "Please enter a yield target greater than zero.",
			})
			return
		}
		trays = int(math.Ceil(float64(desiredYield) / float64(found.YieldGrams)))
	} else {
		// Tray count mode (default).
		n, convErr := strconv.Atoi(traysStr)
		if convErr != nil || n < 1 {
			renderFragment(w, "plan_preview.html", map[string]any{
				"Error": "Please enter a tray count of 1 or more.",
			})
			return
		}
		trays = n
	}

	// ── Validate the date ────────────────────────────────────────────────
	if dateStr == "" {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Please pick a date."})
		return
	}
	// The HTML date input sends YYYY-MM-DD, which is task.DateFormat.
	if _, parseErr := time.Parse(task.DateFormat, dateStr); parseErr != nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": "Invalid date format. Please use the date picker.",
		})
		return
	}

	// ── Run the scheduler ────────────────────────────────────────────────
	// direction == "sow" → plan forward from sow date
	// direction == "harvest" → plan backward from harvest date
	fromHarvest := direction != "sow"
	var preview []scheduler.ScheduledDay
	if fromHarvest {
		preview, _, err = scheduler.Schedule(*found, dateStr, trays)
	} else {
		preview, _, err = scheduler.ScheduleForward(*found, dateStr, trays)
	}
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": "Scheduling error: " + err.Error(),
		})
		return
	}

	// ── Build the preview table data ─────────────────────────────────────
	var days []previewDay
	for _, d := range preview {
		dateDisplay := d.Date // fallback
		if t, parseErr := time.Parse(task.DateFormat, d.Date); parseErr == nil {
			dateDisplay = t.Format("Mon Jan 02")
		}
		days = append(days, previewDay{
			DateDisplay: dateDisplay,
			DayNum:      d.CropDay.Day,
			Stage:       d.CropDay.Stage,
			Tasks:       d.CropDay.Tasks,
		})
	}

	// ── Weekly repeats ───────────────────────────────────────────────────
	repeats := 0
	if repeatsStr != "" {
		if n, convErr := strconv.Atoi(repeatsStr); convErr == nil && n > 0 {
			repeats = n
		}
	}
	totalCycles := 1 + repeats

	// ── Conflict check ───────────────────────────────────────────────────
	// Build temporary cycle records (base + repeats) and check them against
	// existing cycles, just like the CLI does before saving.
	var tempCycles []farm.Cycle
	var sowDateStr string
	for _, d := range preview {
		if d.CropDay.Day == 1 {
			sowDateStr = d.Date
			break
		}
	}
	harvestDateStr := preview[len(preview)-1].Date
	baseSow, _ := time.Parse(task.DateFormat, sowDateStr)
	baseHarvest, _ := time.Parse(task.DateFormat, harvestDateStr)
	baseMoveToLight := baseSow.AddDate(0, 0, found.DarkDays+1)

	// Resolve "either" to the first lit env for conflict checking.
	envForCycle := litEnv
	if envForCycle == "" {
		envForCycle = "either"
	}

	// Base cycle.
	tempCycles = append(tempCycles, farm.Cycle{
		CropName:        found.Name,
		Trays:           trays,
		SowDate:         sowDateStr,
		HarvestDate:     harvestDateStr,
		MoveToLightDate: baseMoveToLight.Format(task.DateFormat),
		LitEnvironment:  envForCycle,
	})

	// Weekly repeat cycles.
	for week := 1; week <= repeats; week++ {
		shift := week * 7
		tempCycles = append(tempCycles, farm.Cycle{
			CropName:        found.Name,
			Trays:           trays,
			SowDate:         baseSow.AddDate(0, 0, shift).Format(task.DateFormat),
			HarvestDate:     baseHarvest.AddDate(0, 0, shift).Format(task.DateFormat),
			MoveToLightDate: baseMoveToLight.AddDate(0, 0, shift).Format(task.DateFormat),
			LitEnvironment:  envForCycle,
		})
	}

	// Run the checker against existing cycles + the new temporary ones.
	var conflicts []string
	farmEnvs, farmErr := farm.LoadConfig()
	if farmErr == nil {
		existingCycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			allCycles := append(existingCycles, tempCycles...)
			conflicts = checker.Check(farmEnvs, allCycles)
		}
	}

	// ── Build the header line ────────────────────────────────────────────
	trayWord := "tray"
	if trays != 1 {
		trayWord = "trays"
	}
	anchorLabel := "harvest"
	if !fromHarvest {
		anchorLabel = "sow"
	}
	header := fmt.Sprintf("%s — %d %s — %s %s",
		task.Capitalize(found.Name), trays, trayWord, anchorLabel, dateStr)

	renderFragment(w, "plan_preview.html", map[string]any{
		"Header":       header,
		"Days":         days,
		"Conflicts":    conflicts,
		"HasConflicts": len(conflicts) > 0,
		"TotalCycles":  totalCycles,
		// Hidden form fields passed through to the confirm handler.
		"FormCrop":      cropName,
		"FormTrays":     strconv.Itoa(trays),
		"FormDirection": direction,
		"FormDate":      dateStr,
		"FormLitEnv":    litEnv,
		"FormRepeats":   strconv.Itoa(repeats),
	})
}

// handlePlanConfirm handles POST /plan/confirm.
//
// This is called when the grower clicks "Confirm — add to calendar" in the
// preview. It reads the hidden form fields, regenerates the schedule (the
// scheduler is deterministic — same inputs = same output), and saves the
// tasks and cycle records to disk.
//
// This is intentionally a fresh generation rather than caching the preview
// results. The scheduler is fast (microseconds for a 9-day cycle), and
// regenerating avoids the complexity of server-side session state.
func handlePlanConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Bad request."})
		return
	}

	cropName := r.FormValue("crop")
	traysStr := r.FormValue("trays")
	direction := r.FormValue("direction")
	dateStr := r.FormValue("date")
	litEnv := r.FormValue("lit_env")
	repeatsStr := r.FormValue("repeats")

	// ── Load the crop ────────────────────────────────────────────────────
	source, err := crop.GetSource()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not find crops file."})
		return
	}
	crops, err := source.LoadCrops()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not load crop library."})
		return
	}
	var found *crop.Crop
	for i := range crops {
		if crops[i].Name == cropName {
			found = &crops[i]
			break
		}
	}
	if found == nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": fmt.Sprintf("Crop %q not found.", cropName),
		})
		return
	}

	trays, _ := strconv.Atoi(traysStr)
	if trays < 1 {
		trays = 1
	}
	repeats, _ := strconv.Atoi(repeatsStr)
	if repeats < 0 {
		repeats = 0
	}
	fromHarvest := direction != "sow"

	// ── Generate the base schedule ───────────────────────────────────────
	var preview []scheduler.ScheduledDay
	var newTasks []task.Task

	if fromHarvest {
		preview, newTasks, err = scheduler.Schedule(*found, dateStr, trays)
	} else {
		preview, newTasks, err = scheduler.ScheduleForward(*found, dateStr, trays)
	}
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": "Scheduling error: " + err.Error(),
		})
		return
	}

	// ── Extract key dates from the preview ───────────────────────────────
	var sowDateStr string
	for _, d := range preview {
		if d.CropDay.Day == 1 {
			sowDateStr = d.Date
			break
		}
	}
	harvestDateStr := preview[len(preview)-1].Date
	baseSow, _ := time.Parse(task.DateFormat, sowDateStr)
	baseHarvest, _ := time.Parse(task.DateFormat, harvestDateStr)
	baseMoveToLight := baseSow.AddDate(0, 0, found.DarkDays+1)

	// Resolve the lit environment. Empty or missing defaults to "either".
	envForCycle := strings.TrimSpace(litEnv)
	if envForCycle == "" {
		envForCycle = "either"
	}

	// ── Build cycle records and task lists ────────────────────────────────
	allNewTasks := newTasks
	var newCycleRecords []farm.Cycle

	// Base cycle record.
	newCycleRecords = append(newCycleRecords, farm.Cycle{
		CycleID:         newTasks[0].CycleID,
		CropName:        found.Name,
		Trays:           trays,
		SowDate:         sowDateStr,
		HarvestDate:     harvestDateStr,
		MoveToLightDate: baseMoveToLight.Format(task.DateFormat),
		LitEnvironment:  envForCycle,
		ExpectedGrams:   found.YieldGrams * trays,
	})

	// ── Weekly repeats ───────────────────────────────────────────────────
	// Same logic as the CLI: shift the anchor date by 7 days per repeat
	// and regenerate the schedule.
	if repeats > 0 {
		baseDate, _ := time.Parse(task.DateFormat, dateStr)

		for week := 1; week <= repeats; week++ {
			weeklyDate := baseDate.AddDate(0, 0, week*7).Format(task.DateFormat)
			var weekTasks []task.Task

			if fromHarvest {
				_, weekTasks, err = scheduler.Schedule(*found, weeklyDate, trays)
			} else {
				_, weekTasks, err = scheduler.ScheduleForward(*found, weeklyDate, trays)
			}
			if err != nil {
				renderFragment(w, "plan_preview.html", map[string]any{
					"Error": fmt.Sprintf("Error generating week %d: %v", week, err),
				})
				return
			}

			allNewTasks = append(allNewTasks, weekTasks...)

			weekSow := baseSow.AddDate(0, 0, week*7)
			weekHarvest := baseHarvest.AddDate(0, 0, week*7)
			weekMoveToLight := baseMoveToLight.AddDate(0, 0, week*7)

			newCycleRecords = append(newCycleRecords, farm.Cycle{
				CycleID:         weekTasks[0].CycleID,
				CropName:        found.Name,
				Trays:           trays,
				SowDate:         weekSow.Format(task.DateFormat),
				HarvestDate:     weekHarvest.Format(task.DateFormat),
				MoveToLightDate: weekMoveToLight.Format(task.DateFormat),
				LitEnvironment:  envForCycle,
				ExpectedGrams:   found.YieldGrams * trays,
			})
		}
	}

	// ── Save tasks ───────────────────────────────────────────────────────
	existing, err := store.Load()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": "Could not load existing tasks: " + err.Error(),
		})
		return
	}
	all := append(existing, allNewTasks...)
	if err := store.Save(all); err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{
			"Error": "Could not save tasks: " + err.Error(),
		})
		return
	}

	// ── Save cycle records ───────────────────────────────────────────────
	existingCycles, err := farm.LoadCycles()
	if err != nil {
		// Non-fatal — tasks are already saved.
		existingCycles = []farm.Cycle{}
	}
	allCycles := append(existingCycles, newCycleRecords...)
	_ = farm.SaveCycles(allCycles)

	renderFragment(w, "plan_success.html", map[string]any{
		"TaskCount":  len(allNewTasks),
		"CycleCount": len(newCycleRecords),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Harvest
// ─────────────────────────────────────────────────────────────────────────────

// eligibleCycle is the template-friendly version of a farm.Cycle for the
// harvest page. It adds human-readable date formatting and pre-computes
// labels so the HTML template doesn't need to do any date parsing.
type eligibleCycle struct {
	CycleID         string // the unique ID for this crop cycle
	CropName        string // capitalised crop name, e.g. "Sunnies"
	Trays           int    // how many trays were planned
	TrayWord        string // "tray" or "trays" — English is weird about plurals
	HarvestDate     string // human-readable date, e.g. "Mar 15"
	HarvestDateSort string // YYYY-MM-DD format, used for sorting (not displayed)
	ExpectedGrams   int    // planned yield in grams (may be 0 for older cycles)
	HasExpected     bool   // true if ExpectedGrams > 0
}

// handleHarvestPage renders the harvest logging page at GET /harvest.
//
// It works like "greenies harvest" — finds all cycles whose harvest date has
// passed (or is today) and is within the last 30 days, and that haven't been
// logged yet. The grower sees each one as a card with a "Log harvest" button.
//
// Clicking "Log harvest" expands an inline form (via htmx) where the grower
// enters actual trays, actual grams, and optional notes — then clicks "Save".
func handleHarvestPage(w http.ResponseWriter, r *http.Request) {
	// Load cycle records and existing harvest log.
	cycles, err := farm.LoadCycles()
	if err != nil {
		cycles = []farm.Cycle{}
	}
	harvests, err := farm.LoadHarvests()
	if err != nil {
		harvests = []farm.HarvestRecord{}
	}

	// Build a set of CycleIDs that have already been logged, so we don't
	// offer the same batch twice.
	logged := map[string]bool{}
	for _, h := range harvests {
		logged[h.CycleID] = true
	}

	// The log window: harvest date must be today or earlier, and within the
	// last 30 days. Same logic as the CLI command in cmd_harvest.go.
	today := task.Today()
	cutoff := today.AddDate(0, 0, -30)

	var eligible []eligibleCycle
	for _, c := range cycles {
		harv, parseErr := time.Parse(task.DateFormat, c.HarvestDate)
		if parseErr != nil {
			continue
		}
		// harv <= today  →  !today.Before(harv)
		// harv >= cutoff →  !harv.Before(cutoff)
		if !today.Before(harv) && !harv.Before(cutoff) && !logged[c.CycleID] {
			trayWord := "tray"
			if c.Trays != 1 {
				trayWord = "trays"
			}
			eligible = append(eligible, eligibleCycle{
				CycleID:         c.CycleID,
				CropName:        task.Capitalize(c.CropName),
				Trays:           c.Trays,
				TrayWord:        trayWord,
				HarvestDate:     harv.Format("Jan 02"),
				HarvestDateSort: c.HarvestDate, // YYYY-MM-DD — sorts chronologically
				ExpectedGrams:   c.ExpectedGrams,
				HasExpected:     c.ExpectedGrams > 0,
			})
		}
	}

	// Sort most recent harvest first — same order as the CLI.
	// HarvestDateSort is YYYY-MM-DD so string comparison gives correct
	// chronological order. The display field (HarvestDate = "Jan 02")
	// would sort alphabetically, not by date.
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].HarvestDateSort > eligible[j].HarvestDateSort
	})

	renderPage(w, "harvest.html", map[string]any{
		"Eligible":    eligible,
		"HasEligible": len(eligible) > 0,
	})
}

// handleHarvestAction handles POST /harvest.
//
// This is called when the grower fills in the inline form and clicks "Save".
// It reads the form values (cycle ID, actual trays, actual grams, notes),
// builds a HarvestRecord, and appends it to the harvest log on disk.
//
// After saving, it returns a small success fragment that replaces the cycle
// card, so the grower sees confirmation without a full page reload.
func handleHarvestAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	cycleID := r.FormValue("cycle_id")
	actualTraysStr := r.FormValue("actual_trays")
	actualGramsStr := r.FormValue("actual_grams")
	notes := r.FormValue("notes")

	if cycleID == "" {
		http.Error(w, "Missing cycle ID", http.StatusBadRequest)
		return
	}

	// Find the matching cycle record.
	cycles, err := farm.LoadCycles()
	if err != nil {
		http.Error(w, "Could not load cycles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var chosen *farm.Cycle
	for i := range cycles {
		if cycles[i].CycleID == cycleID {
			chosen = &cycles[i]
			break
		}
	}
	if chosen == nil {
		http.Error(w, "Cycle not found", http.StatusNotFound)
		return
	}

	// Parse actual trays — default to the planned count if left blank.
	actualTrays := chosen.Trays
	if actualTraysStr != "" {
		n, convErr := strconv.Atoi(actualTraysStr)
		if convErr != nil || n < 0 {
			renderFragment(w, "harvest_error.html", map[string]any{
				"Error":   "Please enter a whole number for trays (e.g. 3).",
				"CycleID": cycleID,
			})
			return
		}
		actualTrays = n
	}

	// Parse actual grams — default to expected grams if left blank.
	actualGrams := chosen.ExpectedGrams
	if actualGramsStr != "" {
		g, convErr := strconv.Atoi(actualGramsStr)
		if convErr != nil || g < 0 {
			renderFragment(w, "harvest_error.html", map[string]any{
				"Error":   "Please enter a whole number for grams (e.g. 1400).",
				"CycleID": cycleID,
			})
			return
		}
		actualGrams = g
	} else if chosen.ExpectedGrams == 0 {
		// No default and the grower left it blank — we need a number.
		renderFragment(w, "harvest_error.html", map[string]any{
			"Error":   "Please enter the actual grams harvested.",
			"CycleID": cycleID,
		})
		return
	}

	// Build the record and save.
	record := farm.HarvestRecord{
		CycleID:       chosen.CycleID,
		CropName:      chosen.CropName,
		HarvestDate:   chosen.HarvestDate,
		ExpectedTrays: chosen.Trays,
		ActualTrays:   actualTrays,
		ExpectedGrams: chosen.ExpectedGrams,
		ActualGrams:   actualGrams,
		Notes:         strings.TrimSpace(notes),
	}

	harvests, err := farm.LoadHarvests()
	if err != nil {
		harvests = []farm.HarvestRecord{}
	}
	harvests = append(harvests, record)

	if err := farm.SaveHarvests(harvests); err != nil {
		http.Error(w, "Could not save harvest: "+err.Error(), http.StatusInternalServerError)
		return
	}

	renderFragment(w, "harvest_success.html", map[string]any{
		"CropName":    task.Capitalize(chosen.CropName),
		"ActualTrays": actualTrays,
		"ActualGrams": actualGrams,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Trial
// ─────────────────────────────────────────────────────────────────────────────

// trialCard is the template-friendly version of an active trial for the trial
// dashboard. It pre-computes display strings so the HTML template stays simple.
type trialCard struct {
	ID            string // unique trial ID
	DisplayName   string // e.g. "Mustard (seed lot xyz)"
	Trays         int
	TrayWord      string // "tray" or "trays"
	DayNum        int    // what cycle day the trial is on today
	SowDateFmt    string // human-readable sow date, e.g. "Mon Mar 09"
	HarvestDateFmt string // tentative harvest date, or "" if unknown
	Status        string // "active", "harvested", etc.
}

// trialListRow is the template-friendly version of any trial (any status) for
// the "all trials" table at the bottom of the trial page.
type trialListRow struct {
	ID           string
	DisplayName  string
	Status       string
	SowDateFmt   string
	YieldGrams   int    // actual yield (0 if not recorded)
	HasYield     bool
	FailureNote  string
	CropName     string // lowercase, used for compare grouping
}

// handleTrialPage renders the trial dashboard at GET /trial.
//
// It shows three sections:
//   1. Active trials — cards with "Manage" buttons
//   2. "Start New Trial" form (always visible)
//   3. All trials table — every trial ever run, with "View" links
//
// This is the GUI version of the "greenies trial" command's main menu.
func handleTrialPage(w http.ResponseWriter, r *http.Request) {
	trials, err := trial.LoadTrials()
	if err != nil {
		trials = []trial.TrialRecord{}
	}

	today := task.Today()

	// Build active trial cards.
	var activeCards []trialCard
	for _, tr := range trials {
		if tr.Status != trial.StatusActive {
			continue
		}
		trayWord := "tray"
		if tr.Trays != 1 {
			trayWord = "trays"
		}
		sow, _ := time.Parse(task.DateFormat, tr.SowDate)
		harvestFmt := ""
		if hd := tr.TentativeHarvestDate(); hd != "" {
			if ht, err := time.Parse(task.DateFormat, hd); err == nil {
				harvestFmt = ht.Format("Mon Jan 02")
			}
		}
		activeCards = append(activeCards, trialCard{
			ID:             tr.ID,
			DisplayName:    tr.DisplayName(),
			Trays:          tr.Trays,
			TrayWord:       trayWord,
			DayNum:         tr.DayNumber(today),
			SowDateFmt:     sow.Format("Mon Jan 02"),
			HarvestDateFmt: harvestFmt,
			Status:         tr.Status,
		})
	}

	// Build the "all trials" table rows.
	var allRows []trialListRow
	for _, tr := range trials {
		sow, _ := time.Parse(task.DateFormat, tr.SowDate)
		allRows = append(allRows, trialListRow{
			ID:          tr.ID,
			DisplayName: tr.DisplayName(),
			Status:      tr.Status,
			SowDateFmt:  sow.Format("Jan 02 2006"),
			YieldGrams:  tr.ActualYieldGrams,
			HasYield:    tr.ActualYieldGrams > 0,
			FailureNote: tr.FailureNote,
			CropName:    strings.ToLower(tr.CropName),
		})
	}

	// Check if comparison is possible (2+ past trials of the same crop).
	canCompare := false
	pastByCrop := map[string]int{}
	for _, tr := range trials {
		if tr.Status != trial.StatusActive {
			pastByCrop[strings.ToLower(tr.CropName)]++
		}
	}
	for _, count := range pastByCrop {
		if count >= 2 {
			canCompare = true
			break
		}
	}

	renderPage(w, "trial.html", map[string]any{
		"ActiveTrials":  activeCards,
		"HasActive":     len(activeCards) > 0,
		"AllTrials":     allRows,
		"HasTrials":     len(allRows) > 0,
		"CanCompare":    canCompare,
		"Today":         today.Format(task.DateFormat),
	})
}

// handleTrialNew handles POST /trial/new — creates a new trial.
//
// This is the GUI version of startNewTrial() in cmd_trial.go. All the fields
// are submitted at once from a single form, instead of being asked one by one.
func handleTrialNew(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	// Read form values.
	cropName := strings.ToLower(strings.TrimSpace(r.FormValue("crop_name")))
	trialVar := strings.TrimSpace(r.FormValue("trial_variable"))
	sowDateStr := r.FormValue("sow_date")
	traysStr := r.FormValue("trays")
	soakType := r.FormValue("soak_type")      // "none", "hours", "overnight"
	soakHoursStr := r.FormValue("soak_hours")
	seedGramsStr := r.FormValue("seed_grams")
	dirtLitresStr := r.FormValue("dirt_litres")
	mtlDayStr := r.FormValue("mtl_day")
	harvestDayStr := r.FormValue("harvest_day")

	// Validate required fields.
	if cropName == "" {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Please enter a crop name.",
		})
		return
	}
	if sowDateStr == "" {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Please pick a sow date.",
		})
		return
	}
	sowTime, err := time.Parse(task.DateFormat, sowDateStr)
	if err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Invalid date format.",
		})
		return
	}
	trays, err := strconv.Atoi(traysStr)
	if err != nil || trays < 1 {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Please enter a tray count of 1 or more.",
		})
		return
	}

	// Parse optional parameters.
	var overnightSoak bool
	var soakHours float64
	if soakType == "overnight" {
		overnightSoak = true
	} else if soakType == "hours" {
		if h, err := strconv.ParseFloat(soakHoursStr, 64); err == nil && h > 0 {
			soakHours = h
		}
	}

	var seedGrams float64
	if seedGramsStr != "" {
		if g, err := strconv.ParseFloat(seedGramsStr, 64); err == nil && g > 0 {
			seedGrams = g
		}
	}

	var dirtLitres float64
	if dirtLitresStr == "" {
		dirtLitres = 1
	} else if d, err := strconv.ParseFloat(dirtLitresStr, 64); err == nil && d > 0 {
		dirtLitres = d
	} else {
		dirtLitres = 1
	}

	var moveToLightDay int
	if mtlDayStr != "" {
		if d, err := strconv.Atoi(mtlDayStr); err == nil && d > 0 {
			moveToLightDay = d
		}
	}

	var harvestDay int
	if harvestDayStr != "" {
		if d, err := strconv.Atoi(harvestDayStr); err == nil && d > 0 {
			harvestDay = d
		}
	}

	// Generate ID and build the record.
	trialID, err := task.GenerateID()
	if err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Could not generate trial ID: " + err.Error(),
		})
		return
	}

	lastManaged := sowTime.AddDate(0, 0, -1).Format(task.DateFormat)

	tr := trial.TrialRecord{
		ID:             trialID,
		CropName:       cropName,
		TrialVariable:  trialVar,
		SowDate:        sowDateStr,
		Trays:          trays,
		Status:         trial.StatusActive,
		LastManaged:    lastManaged,
		OvernightSoak:  overnightSoak,
		SoakHours:      soakHours,
		SeedGrams:      seedGrams,
		DirtLitres:     dirtLitres,
		MoveToLightDay: moveToLightDay,
		HarvestDay:     harvestDay,
	}

	// Create tentative calendar tasks (same logic as the CLI).
	if moveToLightDay > 0 {
		mtlDateStr := sowTime.AddDate(0, 0, moveToLightDay-1).Format(task.DateFormat)
		id, taskErr := createTrialTentativeTask(tr.DisplayName(), "move to light", mtlDateStr)
		if taskErr == nil {
			tr.TentativeMTLTaskID = id
		}
	}
	if harvestDay > 0 {
		harvDateStr := sowTime.AddDate(0, 0, harvestDay-1).Format(task.DateFormat)
		id, taskErr := createTrialTentativeTask(tr.DisplayName(), "harvest", harvDateStr)
		if taskErr == nil {
			tr.TentativeHarvestTaskID = id
		}
	}

	// Save.
	trials, loadErr := trial.LoadTrials()
	if loadErr != nil {
		trials = []trial.TrialRecord{}
	}
	updated := trial.ReplaceByID(trials, tr)
	if err := trial.SaveTrials(updated); err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Could not save trial: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_result.html", map[string]any{
		"Success":     true,
		"DisplayName": tr.DisplayName(),
		"Trays":       trays,
		"SowDateFmt":  sowTime.Format("Mon Jan 02"),
		"TrialID":     tr.ID,
	})
}

// createTrialTentativeTask is the GUI version of createTentativeTask from
// cmd_trial.go. It creates a tentative calendar marker and returns its ID.
func createTrialTentativeTask(displayName, eventLabel, dateStr string) (string, error) {
	title := displayName + " — " + eventLabel + "? (unconfirmed)"
	t, err := task.New(title, dateStr, "trial tentative marker")
	if err != nil {
		return "", err
	}
	existing, err := store.Load()
	if err != nil {
		return "", err
	}
	if err := store.Save(append(existing, t)); err != nil {
		return "", err
	}
	return t.ID, nil
}

// manageDayRow holds the data for one missed day in the manage form.
// The template renders one row per missed day, each with optional fields.
type manageDayRow struct {
	DayNum    int    // cycle day number
	DateStr   string // YYYY-MM-DD (used as form field name suffix)
	DateFmt   string // human-readable date, e.g. "Mon Mar 09"
}

// handleTrialManage renders the manage page for a specific trial at
// GET /trial/manage?id=xxx.
//
// It shows:
//   - The trial header (name, trays, sow date)
//   - All missed days since the last manage session, each with fields for
//     observation notes and optional confirmed parameters
//   - An "all caught up" message if there are no missed days
//   - Outcome buttons (continue / harvest / failure) at the bottom
func handleTrialManage(w http.ResponseWriter, r *http.Request) {
	trialID := r.URL.Query().Get("id")
	if trialID == "" {
		http.Error(w, "Missing trial ID", http.StatusBadRequest)
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		http.Error(w, "Could not load trials", http.StatusInternalServerError)
		return
	}

	// Find the trial.
	var tr *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == trialID {
			tr = &trials[i]
			break
		}
	}
	if tr == nil {
		http.Error(w, "Trial not found", http.StatusNotFound)
		return
	}

	// Calculate missed days.
	today := task.Today()
	sow, _ := time.Parse(task.DateFormat, tr.SowDate)
	lastManaged, err := time.Parse(task.DateFormat, tr.LastManaged)
	if err != nil {
		lastManaged = sow.AddDate(0, 0, -1)
	}

	catchupStart := lastManaged.AddDate(0, 0, 1)

	var missedDays []manageDayRow
	for d := catchupStart; !d.After(today); d = d.AddDate(0, 0, 1) {
		dayNum := tr.DayNumber(d)
		if dayNum < 1 {
			continue
		}
		missedDays = append(missedDays, manageDayRow{
			DayNum:  dayNum,
			DateStr: d.Format(task.DateFormat),
			DateFmt: d.Format("Mon Jan 02"),
		})
	}

	renderPage(w, "trial_manage.html", map[string]any{
		"TrialID":     tr.ID,
		"DisplayName": tr.DisplayName(),
		"Trays":       tr.Trays,
		"SowDateFmt":  sow.Format("Mon Jan 02"),
		"DayNum":      tr.DayNumber(today),
		"MissedDays":  missedDays,
		"HasMissed":   len(missedDays) > 0,
		"IsActive":    tr.Status == trial.StatusActive,
	})
}

// handleTrialManageAction handles POST /trial/manage — processes the manage
// form submission.
//
// For each missed day, it reads:
//   - obs_YYYY-MM-DD: observation notes (free text)
//   - confirm_YYYY-MM-DD: "on" if the grower checked the "confirm tasks" box
//   - stage_YYYY-MM-DD: stage (sow/dark/light/harvest) — only if confirmed
//   - tasks_YYYY-MM-DD: tasks comma list — only if confirmed
//
// After saving, it returns a success fragment.
func handleTrialManageAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	if trialID == "" {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Missing trial ID.",
		})
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Could not load trials: " + err.Error(),
		})
		return
	}

	var tr *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == trialID {
			tr = &trials[i]
			break
		}
	}
	if tr == nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Trial not found.",
		})
		return
	}

	// Parse the day-by-day form data. Day dates come as hidden fields
	// named "day_dates" (one per missed day).
	dayDates := r.Form["day_dates"]
	observationsAdded := 0
	daysConfirmed := 0

	for _, dateStr := range dayDates {
		d, err := time.Parse(task.DateFormat, dateStr)
		if err != nil {
			continue
		}
		dayNum := tr.DayNumber(d)
		if dayNum < 1 {
			continue
		}

		// Observation notes.
		obsKey := "obs_" + dateStr
		notes := strings.TrimSpace(r.FormValue(obsKey))
		if notes != "" {
			tr.Observations = append(tr.Observations, trial.TrialObservation{
				Day:   dayNum,
				Date:  dateStr,
				Notes: notes,
			})
			observationsAdded++
		}

		// Confirmed parameters.
		confirmKey := "confirm_" + dateStr
		if r.FormValue(confirmKey) == "on" {
			stageKey := "stage_" + dateStr
			tasksKey := "tasks_" + dateStr
			stage := strings.ToLower(strings.TrimSpace(r.FormValue(stageKey)))
			tasks := strings.TrimSpace(r.FormValue(tasksKey))

			if stage != "" {
				// Replace if this day was already confirmed.
				replaced := false
				for i, cd := range tr.ConfirmedDays {
					if cd.Day == dayNum {
						tr.ConfirmedDays[i] = trial.TrialDayParams{
							Day:   dayNum,
							Stage: stage,
							Tasks: tasks,
						}
						replaced = true
						break
					}
				}
				if !replaced {
					tr.ConfirmedDays = append(tr.ConfirmedDays, trial.TrialDayParams{
						Day:   dayNum,
						Stage: stage,
						Tasks: tasks,
					})
				}
				daysConfirmed++
			}
		}
	}

	// Update last-managed date to today.
	tr.LastManaged = task.Today().Format(task.DateFormat)

	// Refresh tentative calendar tasks.
	refreshTrialTentativeTasks(tr, task.Today())

	// Save.
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Could not save: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_manage_result.html", map[string]any{
		"Success":           true,
		"ObservationsAdded": observationsAdded,
		"DaysConfirmed":     daysConfirmed,
		"TrialID":           tr.ID,
		"IsActive":          tr.Status == trial.StatusActive,
	})
}

// handleTrialOutcome handles POST /trial/outcome — marks a trial as harvested,
// failed, or continues it as active.
//
// Form values:
//   - trial_id: the trial to update
//   - outcome: "harvest", "failure", or "continue"
//   - yield_grams: actual yield (harvest only)
//   - failure_note: what went wrong (failure only)
func handleTrialOutcome(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	outcome := r.FormValue("outcome")

	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not load trials.",
		})
		return
	}

	var tr *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == trialID {
			tr = &trials[i]
			break
		}
	}
	if tr == nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Trial not found.",
		})
		return
	}

	switch outcome {
	case "harvest":
		// Record yield if provided.
		if yieldStr := r.FormValue("yield_grams"); yieldStr != "" {
			if g, err := strconv.Atoi(yieldStr); err == nil && g > 0 {
				tr.ActualYieldGrams = g
			}
		}
		tr.Status = trial.StatusHarvested
		refreshTrialTentativeTasks(tr, task.Today())

		updated := trial.ReplaceByID(trials, *tr)
		if err := trial.SaveTrials(updated); err != nil {
			renderFragment(w, "trial_outcome_result.html", map[string]any{
				"Error": "Could not save: " + err.Error(),
			})
			return
		}
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Success":     true,
			"Message":     fmt.Sprintf("%s marked as harvested.", tr.DisplayName()),
			"CanPromote":  len(tr.ConfirmedDays) > 0,
			"TrialID":     tr.ID,
		})

	case "failure":
		tr.FailureNote = strings.TrimSpace(r.FormValue("failure_note"))
		tr.Status = trial.StatusFailed

		// Cancel tentative tasks (mark as "(cancelled)").
		cancelTrialTentativeTasks(tr)

		updated := trial.ReplaceByID(trials, *tr)
		if err := trial.SaveTrials(updated); err != nil {
			renderFragment(w, "trial_outcome_result.html", map[string]any{
				"Error": "Could not save: " + err.Error(),
			})
			return
		}
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Success":   true,
			"Message":   fmt.Sprintf("%s marked as failed.", tr.DisplayName()),
			"CanDiscard": true,
			"TrialID":   tr.ID,
		})

	default:
		// "continue" — trial stays active, just redirect back.
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Success": true,
			"Message": "Trial updated — still active.",
		})
	}
}

// handleTrialPromote handles POST /trial/promote — promotes a harvested trial
// to crops.csv.
func handleTrialPromote(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not load trials.",
		})
		return
	}

	var tr *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == trialID {
			tr = &trials[i]
			break
		}
	}
	if tr == nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Trial not found.",
		})
		return
	}

	if len(tr.ConfirmedDays) == 0 {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Cannot promote — no confirmed day parameters recorded yet.",
		})
		return
	}

	// Append to crops.csv.
	cropsPath, err := crop.CropsFilePath()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not find crops.csv: " + err.Error(),
		})
		return
	}

	if err := trial.AppendToCropsCSV(cropsPath, *tr); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not write to crops.csv: " + err.Error(),
		})
		return
	}

	// Mirror the change to Google Sheets (if linked).
	gcal.SyncLocalToSheet(context.Background())

	tr.Status = trial.StatusPromoted
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not save: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_outcome_result.html", map[string]any{
		"Success": true,
		"Message": fmt.Sprintf("%s promoted — %d day parameters added to crops.csv.",
			tr.DisplayName(), len(tr.ConfirmedDays)),
	})
}

// handleTrialDiscard handles POST /trial/discard — permanently deletes a trial.
func handleTrialDiscard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not load trials.",
		})
		return
	}

	// Find the trial to remove its tentative tasks.
	var tr *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == trialID {
			tr = &trials[i]
			break
		}
	}
	if tr != nil {
		removeTrialTentativeTasks(tr)
	}

	remaining := trial.RemoveByID(trials, trialID)
	if err := trial.SaveTrials(remaining); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not save: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_outcome_result.html", map[string]any{
		"Success": true,
		"Message": "Trial data permanently deleted.",
	})
}

// handleTrialView renders a full detail view of a single trial at
// GET /trial/view?id=xxx.
func handleTrialView(w http.ResponseWriter, r *http.Request) {
	trialID := r.URL.Query().Get("id")
	if trialID == "" {
		http.Error(w, "Missing trial ID", http.StatusBadRequest)
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		http.Error(w, "Could not load trials", http.StatusInternalServerError)
		return
	}

	var tr *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == trialID {
			tr = &trials[i]
			break
		}
	}
	if tr == nil {
		http.Error(w, "Trial not found", http.StatusNotFound)
		return
	}

	renderPage(w, "trial_view.html", buildTrialViewData(*tr))
}

// handleTrialCompare renders a side-by-side comparison of two trials at
// GET /trial/compare?a=xxx&b=yyy.
func handleTrialCompare(w http.ResponseWriter, r *http.Request) {
	idA := r.URL.Query().Get("a")
	idB := r.URL.Query().Get("b")
	if idA == "" || idB == "" {
		http.Error(w, "Missing trial IDs", http.StatusBadRequest)
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		http.Error(w, "Could not load trials", http.StatusInternalServerError)
		return
	}

	var trA, trB *trial.TrialRecord
	for i := range trials {
		if trials[i].ID == idA {
			trA = &trials[i]
		}
		if trials[i].ID == idB {
			trB = &trials[i]
		}
	}
	if trA == nil || trB == nil {
		http.Error(w, "Trial not found", http.StatusNotFound)
		return
	}

	dataA := buildTrialViewData(*trA)
	dataB := buildTrialViewData(*trB)

	renderPage(w, "trial_compare.html", map[string]any{
		"CropName": task.Capitalize(trA.CropName),
		"A":        dataA,
		"B":        dataB,
	})
}

// handleTrialComparePicker renders the compare picker page at
// GET /trial/compare-pick — lets the user choose a crop and two trials.
func handleTrialComparePicker(w http.ResponseWriter, r *http.Request) {
	trials, err := trial.LoadTrials()
	if err != nil {
		trials = []trial.TrialRecord{}
	}

	// Group past trials by crop name.
	type cropGroup struct {
		CropName string
		Trials   []trialListRow
	}

	pastByCrop := map[string][]trial.TrialRecord{}
	for _, tr := range trials {
		if tr.Status != trial.StatusActive {
			key := strings.ToLower(tr.CropName)
			pastByCrop[key] = append(pastByCrop[key], tr)
		}
	}

	var groups []cropGroup
	for name, list := range pastByCrop {
		if len(list) < 2 {
			continue
		}
		var rows []trialListRow
		for _, tr := range list {
			sow, _ := time.Parse(task.DateFormat, tr.SowDate)
			rows = append(rows, trialListRow{
				ID:          tr.ID,
				DisplayName: tr.DisplayName(),
				Status:      tr.Status,
				SowDateFmt:  sow.Format("Jan 02 2006"),
			})
		}
		groups = append(groups, cropGroup{
			CropName: task.Capitalize(name),
			Trials:   rows,
		})
	}

	// Sort groups alphabetically.
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].CropName < groups[j].CropName
	})

	renderPage(w, "trial_compare_pick.html", map[string]any{
		"Groups":    groups,
		"HasGroups": len(groups) > 0,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Trial view data helpers
// ─────────────────────────────────────────────────────────────────────────────

// trialViewData holds all the display-ready data for one trial's detail view.
type trialViewData struct {
	DisplayName    string
	Status         string
	SowDateFmt     string
	Trays          int
	Soak           string // "overnight", "4 hours", or "—"
	SeedGrams      string // "50g" or "—"
	DirtLitres     string // "1.0L" or "—"
	MTLDay         string // "Day 5" or "—"
	HarvestDay     string // "Day 9" or "—"
	ActualYield    string // "1400g" or "not recorded"
	FailureNote    string
	ConfirmedDays  []trialDayView
	HasConfirmed   bool
	Observations   []trialObsView
	HasObs         bool
	TrialVariable  string
}

// trialDayView is a single confirmed-day row for the view template.
type trialDayView struct {
	DayNum int
	Stage  string
	Tasks  string // "(no tasks)" if empty
}

// trialObsView is a single observation row for the view template.
type trialObsView struct {
	DayNum  int
	DateFmt string
	Notes   string
}

// buildTrialViewData converts a TrialRecord into template-ready data.
func buildTrialViewData(tr trial.TrialRecord) trialViewData {
	sow, _ := time.Parse(task.DateFormat, tr.SowDate)

	// Format soak.
	soak := "—"
	if tr.OvernightSoak {
		soak = "overnight"
	} else if tr.SoakHours > 0 {
		soak = fmt.Sprintf("%.0f hours", tr.SoakHours)
	}

	// Format seed grams.
	seedGrams := "—"
	if tr.SeedGrams > 0 {
		seedGrams = fmt.Sprintf("%.0fg", tr.SeedGrams)
	}

	// Format dirt.
	dirtLitres := "—"
	if tr.DirtLitres > 0 {
		dirtLitres = fmt.Sprintf("%.1fL", tr.DirtLitres)
	}

	// Format milestone days.
	mtlDay := "—"
	if tr.MoveToLightDay > 0 {
		mtlDay = fmt.Sprintf("Day %d", tr.MoveToLightDay)
	}
	harvestDay := "—"
	if tr.HarvestDay > 0 {
		harvestDay = fmt.Sprintf("Day %d", tr.HarvestDay)
	}

	// Format yield.
	actualYield := "not recorded"
	if tr.ActualYieldGrams > 0 {
		actualYield = fmt.Sprintf("%dg", tr.ActualYieldGrams)
	}

	// Confirmed days.
	var confirmedDays []trialDayView
	// Sort by day number.
	sorted := make([]trial.TrialDayParams, len(tr.ConfirmedDays))
	copy(sorted, tr.ConfirmedDays)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Day < sorted[j].Day })
	for _, d := range sorted {
		tasks := d.Tasks
		if tasks == "" {
			tasks = "(no tasks)"
		}
		confirmedDays = append(confirmedDays, trialDayView{
			DayNum: d.Day,
			Stage:  d.Stage,
			Tasks:  tasks,
		})
	}

	// Observations.
	var observations []trialObsView
	for _, o := range tr.Observations {
		if o.Notes == "" {
			continue
		}
		dateFmt := o.Date
		if t, err := time.Parse(task.DateFormat, o.Date); err == nil {
			dateFmt = t.Format("Mon Jan 02")
		}
		observations = append(observations, trialObsView{
			DayNum:  o.Day,
			DateFmt: dateFmt,
			Notes:   o.Notes,
		})
	}

	return trialViewData{
		DisplayName:   tr.DisplayName(),
		Status:        tr.Status,
		SowDateFmt:    sow.Format("Mon Jan 02 2006"),
		Trays:         tr.Trays,
		Soak:          soak,
		SeedGrams:     seedGrams,
		DirtLitres:    dirtLitres,
		MTLDay:        mtlDay,
		HarvestDay:    harvestDay,
		ActualYield:   actualYield,
		FailureNote:   tr.FailureNote,
		ConfirmedDays: confirmedDays,
		HasConfirmed:  len(confirmedDays) > 0,
		Observations:  observations,
		HasObs:        len(observations) > 0,
		TrialVariable: tr.TrialVariable,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Trial tentative task helpers (GUI versions)
// ─────────────────────────────────────────────────────────────────────────────
//
// These mirror the functions in cmd_trial.go but are callable from the GUI
// handlers. They share the same logic — inspect the trial state and update
// the tentative calendar tasks accordingly.

// refreshTrialTentativeTasks updates tentative task titles based on current
// trial state (same logic as refreshTentativeTasks in cmd_trial.go).
func refreshTrialTentativeTasks(tr *trial.TrialRecord, today time.Time) {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return
	}

	tasks, err := store.Load()
	if err != nil {
		return
	}

	changed := false

	// Move-to-light task.
	if tr.TentativeMTLTaskID != "" {
		mtlConfirmed := false
		for _, cd := range tr.ConfirmedDays {
			if cd.Stage == "light" {
				mtlConfirmed = true
				break
			}
		}

		var newTitle string
		if mtlConfirmed {
			newTitle = tr.DisplayName() + " — moved to light"
		} else {
			mtlDateStr := tr.TentativeMoveToLightDate()
			if mtlDateStr != "" {
				mtlDate, parseErr := time.Parse(task.DateFormat, mtlDateStr)
				if parseErr == nil && today.After(mtlDate) {
					newTitle = tr.DisplayName() + " — move to light? (overdue)"
				}
			}
		}

		if newTitle != "" {
			for i, t := range tasks {
				if t.ID == tr.TentativeMTLTaskID {
					if tasks[i].Title != newTitle {
						tasks[i].Title = newTitle
						tasks[i].UpdatedAt = time.Now()
						changed = true
					}
					break
				}
			}
		}
	}

	// Harvest task.
	if tr.TentativeHarvestTaskID != "" {
		harvestConfirmed := tr.Status == trial.StatusHarvested || tr.Status == trial.StatusPromoted
		if !harvestConfirmed {
			for _, cd := range tr.ConfirmedDays {
				if cd.Stage == "harvest" {
					harvestConfirmed = true
					break
				}
			}
		}

		var newTitle string
		if harvestConfirmed {
			newTitle = tr.DisplayName() + " — harvested"
		} else {
			harvDateStr := tr.TentativeHarvestDate()
			if harvDateStr != "" {
				harvDate, parseErr := time.Parse(task.DateFormat, harvDateStr)
				if parseErr == nil && today.After(harvDate) {
					newTitle = tr.DisplayName() + " — harvest? (overdue)"
				}
			}
		}

		if newTitle != "" {
			for i, t := range tasks {
				if t.ID == tr.TentativeHarvestTaskID {
					if tasks[i].Title != newTitle {
						tasks[i].Title = newTitle
						tasks[i].UpdatedAt = time.Now()
						changed = true
					}
					break
				}
			}
		}
	}

	if changed {
		_ = store.Save(tasks)
	}
}

// cancelTrialTentativeTasks marks tentative tasks as "(cancelled)".
func cancelTrialTentativeTasks(tr *trial.TrialRecord) {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return
	}

	tasks, err := store.Load()
	if err != nil {
		return
	}

	changed := false
	for i, t := range tasks {
		switch t.ID {
		case tr.TentativeMTLTaskID:
			newTitle := tr.DisplayName() + " — move to light? (cancelled)"
			if tasks[i].Title != newTitle {
				tasks[i].Title = newTitle
				tasks[i].UpdatedAt = time.Now()
				changed = true
			}
		case tr.TentativeHarvestTaskID:
			newTitle := tr.DisplayName() + " — harvest? (cancelled)"
			if tasks[i].Title != newTitle {
				tasks[i].Title = newTitle
				tasks[i].UpdatedAt = time.Now()
				changed = true
			}
		}
	}

	if changed {
		_ = store.Save(tasks)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Adjust
// ─────────────────────────────────────────────────────────────────────────────

// adjustCycleRow holds the display data for one cycle in the adjust page list.
// We pre-compute everything the template needs so the template stays simple.
type adjustCycleRow struct {
	CycleID        string // hidden value for the radio button
	CropName       string // e.g. "Sunnies"
	Trays          int
	SowDisplay     string // e.g. "Mar 09"
	HarvestDisplay string
	DayNum         int    // which day of the cycle (for active cycles)
	Stage          string // "dark", "light", or "harvest day" (for active cycles)
	DaysUntil      int    // days until sow (for upcoming cycles)
}

// adjustPreviewRow is one row in the before/after side-by-side table.
type adjustPreviewRow struct {
	DateDisplay string // e.g. "Mon Mar 09"
	BeforeLabel string // e.g. "Day 3 (dark)" — empty string means "---"
	AfterLabel  string
	IsToday     bool   // true if this row is today's date
}

// handleAdjustPage renders GET /adjust — the cycle adjustment page.
//
// It loads all active and upcoming (within 7 days) cycles and passes them
// to the template as two separate lists. The grower picks one cycle, then
// fills in the adjustment form and clicks "Preview".
func handleAdjustPage(w http.ResponseWriter, r *http.Request) {
	cycles, err := farm.LoadCycles()
	if err != nil {
		renderPage(w, "adjust.html", map[string]any{"HasCycles": false})
		return
	}

	today := task.Today()
	oneWeekOut := today.AddDate(0, 0, 7)

	var active, upcoming []adjustCycleRow

	for _, c := range cycles {
		sow, err := time.Parse(task.DateFormat, c.SowDate)
		if err != nil {
			continue
		}
		harv, err := time.Parse(task.DateFormat, c.HarvestDate)
		if err != nil {
			continue
		}
		mtl, _ := time.Parse(task.DateFormat, c.MoveToLightDate)

		if !today.Before(sow) && !today.After(harv) {
			// Active cycle — currently growing.
			dayNum := int(today.Sub(sow).Hours()/24) + 1
			stage := "dark"
			if today.Equal(harv) {
				stage = "harvest day"
			} else if !today.Before(mtl) {
				stage = "light"
			}
			active = append(active, adjustCycleRow{
				CycleID:        c.CycleID,
				CropName:       task.Capitalize(c.CropName),
				Trays:          c.Trays,
				SowDisplay:     sow.Format("Mon Jan 02"),
				HarvestDisplay: harv.Format("Mon Jan 02"),
				DayNum:         dayNum,
				Stage:          stage,
			})
		} else if sow.After(today) && !sow.After(oneWeekOut) {
			// Upcoming cycle — starts within 7 days.
			daysUntil := int(sow.Sub(today).Hours() / 24)
			upcoming = append(upcoming, adjustCycleRow{
				CycleID:        c.CycleID,
				CropName:       task.Capitalize(c.CropName),
				Trays:          c.Trays,
				SowDisplay:     sow.Format("Mon Jan 02"),
				HarvestDisplay: harv.Format("Mon Jan 02"),
				DaysUntil:      daysUntil,
			})
		}
	}

	renderPage(w, "adjust.html", map[string]any{
		"ActiveCycles":   active,
		"UpcomingCycles": upcoming,
		"HasCycles":      len(active) > 0 || len(upcoming) > 0,
	})
}

// handleAdjustPreview handles POST /adjust/preview.
//
// It reads the form values (cycle ID, anchor, operation, parameters), computes
// the new dates, and renders a before/after preview as an htmx fragment. No
// data is saved — this is just a "what would happen" view.
func handleAdjustPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Bad request."})
		return
	}

	cycleID := r.FormValue("cycle_id")
	anchor := r.FormValue("anchor")
	operation := r.FormValue("operation")
	direction := r.FormValue("direction")
	daysStr := r.FormValue("days")
	newTraysStr := r.FormValue("new_trays")
	cascade := r.FormValue("cascade")
	updateCSV := r.FormValue("update_csv")

	if cycleID == "" {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Please select a cycle first."})
		return
	}

	// Load the cycle record.
	cycles, err := farm.LoadCycles()
	if err != nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Could not load cycles."})
		return
	}

	var chosen *farm.Cycle
	for i := range cycles {
		if cycles[i].CycleID == cycleID {
			chosen = &cycles[i]
			break
		}
	}
	if chosen == nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Cycle not found — it may have been deleted."})
		return
	}

	sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
	mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	today := task.Today()
	anchorSow := anchor == "sow"

	// Common hidden form fields that get passed to the confirm handler.
	formBase := map[string]any{
		"FormCycleID":   cycleID,
		"FormAnchor":    anchor,
		"FormOperation": operation,
		"FormDirection": direction,
		"FormDays":      daysStr,
		"FormNewTrays":  newTraysStr,
		"FormCascade":   cascade,
		"FormUpdateCSV": updateCSV,
	}

	// ── Cancel preview ─────────────────────────────────────────────────
	if operation == "cancel" {
		header := fmt.Sprintf("Cancel %s %dx (sown %s, harvest %s)",
			task.Capitalize(chosen.CropName), chosen.Trays,
			sow.Format("Mon Jan 02"), harv.Format("Mon Jan 02"))
		data := map[string]any{
			"Header":   header,
			"IsCancel": true,
		}
		for k, v := range formBase {
			data[k] = v
		}
		renderFragment(w, "adjust_preview.html", data)
		return
	}

	// ── Retray preview ─────────────────────────────────────────────────
	if operation == "retray" {
		newTrays, convErr := strconv.Atoi(newTraysStr)
		if convErr != nil || newTrays < 1 {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": "Please enter a tray count of 1 or more.",
			})
			return
		}
		header := fmt.Sprintf("%s — change tray count %d → %d",
			task.Capitalize(chosen.CropName), chosen.Trays, newTrays)
		data := map[string]any{
			"Header":   header,
			"IsRetray": true,
		}
		for k, v := range formBase {
			data[k] = v
		}
		renderFragment(w, "adjust_preview.html", data)
		return
	}

	// ── Blackout / light preview ───────────────────────────────────────

	// Parse the day count.
	days, convErr := strconv.Atoi(daysStr)
	if convErr != nil || days < 1 {
		renderFragment(w, "adjust_preview.html", map[string]any{
			"Error": "Please enter a positive number of days.",
		})
		return
	}

	// n is the signed shift: positive = add, negative = remove.
	n := days
	if direction == "remove" {
		n = -n
	}

	// Compute the new key dates using the same arithmetic as the CLI.
	var newSow, newMTL, newHarv time.Time
	inBlackout := today.Before(mtl)

	stage := operation // "blackout" or "light"

	// Check if the crop is already past the blackout stage before computing
	// new dates — gives a clearer error message than date validation alone.
	if stage == "blackout" && !inBlackout {
		renderFragment(w, "adjust_preview.html", map[string]any{
			"Error": "This crop has already moved to light — the blackout stage is done. Adjust light days instead.",
		})
		return
	}

	switch {
	case stage == "blackout" && anchorSow:
		newSow = sow
		newMTL = mtl.AddDate(0, 0, n)
		newHarv = harv.AddDate(0, 0, n)
		if !newMTL.After(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: move-to-light would land on %s (today or earlier).",
					newMTL.Format("Mon Jan 02")),
			})
			return
		}
		if newHarv.Before(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: harvest would land on %s (already passed).",
					newHarv.Format("Mon Jan 02")),
			})
			return
		}

	case stage == "blackout" && !anchorSow:
		newSow = sow.AddDate(0, 0, -n)
		newMTL = mtl
		newHarv = harv
		if n < 0 && !sow.After(today) && newSow.After(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": "Cannot do that: sow date would move to the future. You cannot un-sow a crop that is already growing.",
			})
			return
		}

	case stage == "light" && anchorSow:
		newSow = sow
		newMTL = mtl
		newHarv = harv.AddDate(0, 0, n)
		if newHarv.Before(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: harvest would land on %s (already passed).",
					newHarv.Format("Mon Jan 02")),
			})
			return
		}

	case stage == "light" && !anchorSow:
		shiftN := -n
		newSow = sow.AddDate(0, 0, shiftN)
		newMTL = mtl.AddDate(0, 0, shiftN)
		newHarv = harv
		if inBlackout && !newMTL.After(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: move-to-light would land on %s (today or earlier).",
					newMTL.Format("Mon Jan 02")),
			})
			return
		}
	}

	// Build the before/after side-by-side rows.
	before := buildGUICycleView(sow, mtl, harv)
	after := buildGUICycleView(newSow, newMTL, newHarv)
	rows := buildPreviewRows(before, after, today)

	// Build a human-readable header.
	dirWord := "add"
	if n < 0 {
		dirWord = "remove"
	}
	absN := n
	if absN < 0 {
		absN = -absN
	}
	dayWord := "day"
	if absN != 1 {
		dayWord = "days"
	}
	anchorLabel := "harvest"
	if anchorSow {
		anchorLabel = "sow"
	}
	header := fmt.Sprintf("%s %dx — %s %d %s %s (anchor: %s)",
		task.Capitalize(chosen.CropName), chosen.Trays,
		dirWord, absN, stage, dayWord, anchorLabel)

	// Run conflict check with the new dates.
	var conflicts []string
	farmEnvs, farmErr := farm.LoadConfig()
	if farmErr == nil {
		// Build a temporary cycle list with the adjusted cycle.
		tempCycles := make([]farm.Cycle, len(cycles))
		copy(tempCycles, cycles)
		for i := range tempCycles {
			if tempCycles[i].CycleID == cycleID {
				tempCycles[i].SowDate = newSow.Format(task.DateFormat)
				tempCycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				tempCycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		conflicts = checker.Check(farmEnvs, tempCycles)
	}

	data := map[string]any{
		"Header":       header,
		"Rows":         rows,
		"Conflicts":    conflicts,
		"HasConflicts": len(conflicts) > 0,
	}
	for k, v := range formBase {
		data[k] = v
	}
	renderFragment(w, "adjust_preview.html", data)
}

// handleAdjustConfirm handles POST /adjust/confirm.
//
// It reads the hidden form fields from the preview, replays the adjustment
// (same math — the scheduler is deterministic), saves to disk, and optionally
// cascades to other cycles and/or updates crops.csv.
func handleAdjustConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Bad request."})
		return
	}

	cycleID := r.FormValue("cycle_id")
	anchor := r.FormValue("anchor")
	operation := r.FormValue("operation")
	direction := r.FormValue("direction")
	daysStr := r.FormValue("days")
	newTraysStr := r.FormValue("new_trays")
	doCascade := r.FormValue("cascade") == "yes"
	doUpdateCSV := r.FormValue("update_csv") == "yes"

	// Load current data from disk.
	cycles, err := farm.LoadCycles()
	if err != nil {
		renderFragment(w, "adjust_success.html", map[string]any{"Message": "Error loading cycles: " + err.Error()})
		return
	}
	tasks, err := store.Load()
	if err != nil {
		renderFragment(w, "adjust_success.html", map[string]any{"Message": "Error loading tasks: " + err.Error()})
		return
	}
	farmEnvs, _ := farm.LoadConfig()

	// Find the cycle.
	var chosen *farm.Cycle
	for i := range cycles {
		if cycles[i].CycleID == cycleID {
			chosen = &cycles[i]
			break
		}
	}
	if chosen == nil {
		renderFragment(w, "adjust_success.html", map[string]any{"Message": "Cycle not found — it may have been deleted."})
		return
	}

	sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
	mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	today := task.Today()
	anchorSow := anchor == "sow"

	var message string

	// ── Cancel ─────────────────────────────────────────────────────────
	if operation == "cancel" {
		todayStr := today.Format(task.DateFormat)
		var kept []task.Task
		removed := 0
		for _, t := range tasks {
			if t.CycleID == cycleID && t.Date > todayStr {
				removed++
			} else {
				kept = append(kept, t)
			}
		}
		tasks = kept

		// Remove the cycle record.
		var keptCycles []farm.Cycle
		for _, c := range cycles {
			if c.CycleID != cycleID {
				keptCycles = append(keptCycles, c)
			}
		}

		_ = store.Save(tasks)
		_ = farm.SaveCycles(keptCycles)

		message = fmt.Sprintf("%s %dx cycle cancelled. %d future task(s) deleted.",
			task.Capitalize(chosen.CropName), chosen.Trays, removed)
		renderFragment(w, "adjust_success.html", map[string]any{
			"Message": message,
		})
		return
	}

	// ── Retray ─────────────────────────────────────────────────────────
	if operation == "retray" {
		newTrays, _ := strconv.Atoi(newTraysStr)
		if newTrays < 1 {
			newTrays = 1
		}

		// Load the crop definition to regenerate tasks.
		cropDef, found := guiLoadCropByName(chosen.CropName)
		if !found {
			renderFragment(w, "adjust_success.html", map[string]any{
				"Message": fmt.Sprintf("Crop %q not found in crops.csv.", chosen.CropName),
			})
			return
		}

		fromDayNum := int(today.Sub(sow).Hours()/24) + 2
		todayStr := today.Format(task.DateFormat)

		// Remove future tasks.
		var kept []task.Task
		for _, t := range tasks {
			if t.CycleID != cycleID || t.Date <= todayStr {
				kept = append(kept, t)
			}
		}
		tasks = kept

		// Regenerate from tomorrow with new tray count.
		if fromDayNum <= cropDef.CycleDays {
			newTasks, schedErr := scheduler.ScheduleFromDay(cropDef, chosen.SowDate, fromDayNum, newTrays, cycleID)
			if schedErr == nil {
				tasks = append(tasks, newTasks...)
			}
		}

		// Update cycle record.
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].Trays = newTrays
				cycles[i].ExpectedGrams = cropDef.YieldGrams * newTrays
				break
			}
		}

		_ = store.Save(tasks)
		_ = farm.SaveCycles(cycles)

		warnings := checker.Check(farmEnvs, cycles)
		message = fmt.Sprintf("%s updated: %d → %d trays.",
			task.Capitalize(chosen.CropName), chosen.Trays, newTrays)
		renderFragment(w, "adjust_success.html", map[string]any{
			"Message":      message,
			"Conflicts":    warnings,
			"HasConflicts": len(warnings) > 0,
		})
		return
	}

	// ── Blackout / light adjustment ────────────────────────────────────

	days, _ := strconv.Atoi(daysStr)
	n := days
	if direction == "remove" {
		n = -n
	}

	stage := operation // "blackout" or "light"
	inBlackout := today.Before(mtl)

	var newSow, newMTL, newHarv time.Time

	switch {
	case stage == "blackout" && anchorSow:
		newSow = sow
		newMTL = mtl.AddDate(0, 0, n)
		newHarv = harv.AddDate(0, 0, n)
	case stage == "blackout" && !anchorSow:
		newSow = sow.AddDate(0, 0, -n)
		newMTL = mtl
		newHarv = harv
	case stage == "light" && anchorSow:
		newSow = sow
		newMTL = mtl
		newHarv = harv.AddDate(0, 0, n)
	case stage == "light" && !anchorSow:
		shiftN := -n
		newSow = sow.AddDate(0, 0, shiftN)
		newMTL = mtl.AddDate(0, 0, shiftN)
		newHarv = harv
	}

	// Apply the adjustment — same logic as cmd_adjust.go.
	switch {
	case stage == "blackout" && anchorSow:
		// Re-generate future tasks using a phantom sow date shifted by n.
		cropDef, found := guiLoadCropByName(chosen.CropName)
		if !found {
			renderFragment(w, "adjust_success.html", map[string]any{
				"Message": fmt.Sprintf("Crop %q not found in crops.csv.", chosen.CropName),
			})
			return
		}
		fromDayNum := int(today.Sub(sow).Hours()/24) + 2
		tasks = guiRemoveFutureTasks(tasks, cycleID, today)
		if fromDayNum <= cropDef.CycleDays {
			shiftedSow := sow.AddDate(0, 0, n)
			newTasks, schedErr := scheduler.ScheduleFromDay(cropDef, shiftedSow.Format(task.DateFormat), fromDayNum, chosen.Trays, cycleID)
			if schedErr == nil {
				newTasks = guiTagAdjusted(newTasks)
				tasks = append(tasks, newTasks...)
			}
		}
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				cycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — move to light: %s → %s  ·  harvest: %s → %s",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"),
			harv.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))

	case stage == "blackout" && !anchorSow:
		// Metadata only — update sow date.
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].SowDate = newSow.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — sow date: %s → %s (no task dates changed)",
			sow.Format("Mon Jan 02"), newSow.Format("Mon Jan 02"))

	case stage == "light" && anchorSow:
		// Date-shift existing light-stage future tasks only.
		mtlStr := mtl.Format(task.DateFormat)
		todayStr := today.Format(task.DateFormat)
		for i := range tasks {
			t := &tasks[i]
			if t.CycleID != cycleID || t.Date <= todayStr || t.Date < mtlStr {
				continue
			}
			d, parseErr := time.Parse(task.DateFormat, t.Date)
			if parseErr != nil {
				continue
			}
			newDate := d.AddDate(0, 0, n).Format(task.DateFormat)
			if newDate != t.Date {
				t.Date = newDate
				if !strings.Contains(t.Notes, "adjusted - be mindful") {
					t.Notes += "\nadjusted - be mindful"
				}
			}
		}
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — harvest: %s → %s",
			harv.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))

	case stage == "light" && !anchorSow && inBlackout:
		// Re-generate future tasks with shifted phantom sow.
		cropDef, found := guiLoadCropByName(chosen.CropName)
		if !found {
			renderFragment(w, "adjust_success.html", map[string]any{
				"Message": fmt.Sprintf("Crop %q not found in crops.csv.", chosen.CropName),
			})
			return
		}
		shiftN := -n
		shiftedSow := sow.AddDate(0, 0, shiftN)
		fromDayNum := int(today.Sub(sow).Hours()/24) + 2
		tasks = guiRemoveFutureTasks(tasks, cycleID, today)
		if fromDayNum <= cropDef.CycleDays {
			newTasks, schedErr := scheduler.ScheduleFromDay(cropDef, shiftedSow.Format(task.DateFormat), fromDayNum, chosen.Trays, cycleID)
			if schedErr == nil {
				newTasks = guiTagAdjusted(newTasks)
				tasks = append(tasks, newTasks...)
			}
		}
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].SowDate = newSow.Format(task.DateFormat)
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — move to light: %s → %s (tasks regenerated)",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"))

	case stage == "light" && !anchorSow && !inBlackout:
		// Metadata only — correct the recorded move-to-light date.
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — move-to-light corrected: %s → %s (no task dates changed)",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"))
	}

	// Save the primary adjustment.
	_ = store.Save(tasks)
	_ = farm.SaveCycles(cycles)

	// ── Cascade to other cycles of the same crop ───────────────────────
	// ModifyCropDays and the cascade function use "dark" (not "blackout")
	// because that is the stage name used in crops.csv and the CLI.
	csvStage := stage
	if csvStage == "blackout" {
		csvStage = "dark"
	}

	cascaded := 0
	if doCascade && (stage == "blackout" || stage == "light") {
		cascaded = guiCascade(chosen, stage, n, anchorSow, today, farmEnvs)
	}

	// ── Update crops.csv template ──────────────────────────────────────
	csvUpdated := false
	if doUpdateCSV && (stage == "blackout" || stage == "light") {
		csvPath, pathErr := crop.CropsFilePath()
		if pathErr == nil {
			modErr := crop.ModifyCropDays(csvPath, chosen.CropName, csvStage, n)
			if modErr == nil {
				csvUpdated = true
				// Mirror the change to Google Sheets (if linked).
				gcal.SyncLocalToSheet(context.Background())
			}
		}
	}

	// Re-run conflict checker with updated data.
	cycles, _ = farm.LoadCycles()
	warnings := checker.Check(farmEnvs, cycles)

	renderFragment(w, "adjust_success.html", map[string]any{
		"Message":      message,
		"Cascaded":     cascaded,
		"CSVUpdated":   csvUpdated,
		"Conflicts":    warnings,
		"HasConflicts": len(warnings) > 0,
	})
}

// ─── Adjust helper functions ─────────────────────────────────────────────────
//
// These mirror the helper functions in cmd_adjust.go but work without terminal
// I/O. They are prefixed with "gui" to avoid name collisions.

// buildGUICycleView returns a day-by-day timeline from sow to harvest.
// Each entry pairs a date with a label describing what that day is.
func buildGUICycleView(sow, mtl, harvest time.Time) []adjustPreviewRow {
	var result []adjustPreviewRow
	for d := sow; !d.After(harvest); d = d.AddDate(0, 0, 1) {
		dayNum := int(d.Sub(sow).Hours()/24) + 1
		var label string
		switch {
		case d.Equal(harvest):
			label = "harvest"
		case d.Equal(mtl):
			label = "move to light"
		case d.Equal(sow):
			label = fmt.Sprintf("Day %d (sow)", dayNum)
		case d.Before(mtl):
			label = fmt.Sprintf("Day %d (dark)", dayNum)
		default:
			label = fmt.Sprintf("Day %d (light)", dayNum)
		}
		result = append(result, adjustPreviewRow{
			DateDisplay: d.Format("Mon Jan 02"),
			BeforeLabel: label,
		})
	}
	return result
}

// buildPreviewRows merges the "before" and "after" cycle views into a single
// list of rows, aligned by calendar date. Dates that exist in one view but
// not the other get an empty label (rendered as "---" in the template).
func buildPreviewRows(before, after []adjustPreviewRow, today time.Time) []adjustPreviewRow {
	// Parse dates back so we can find the union range.
	type dateLabel struct {
		date  time.Time
		label string
	}

	parseDateDisplay := func(s string) time.Time {
		t, _ := time.Parse("Mon Jan 02", s)
		// time.Parse without a year defaults to year 0. Add the current year.
		return time.Date(today.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}

	beforeMap := make(map[string]string)
	var startDate, endDate time.Time
	for i, r := range before {
		d := parseDateDisplay(r.DateDisplay)
		beforeMap[d.Format(task.DateFormat)] = r.BeforeLabel
		if i == 0 {
			startDate = d
			endDate = d
		}
		if d.Before(startDate) {
			startDate = d
		}
		if d.After(endDate) {
			endDate = d
		}
	}

	afterMap := make(map[string]string)
	for _, r := range after {
		d := parseDateDisplay(r.DateDisplay)
		// The "after" rows have the label in BeforeLabel from buildGUICycleView.
		afterMap[d.Format(task.DateFormat)] = r.BeforeLabel
		if d.Before(startDate) {
			startDate = d
		}
		if d.After(endDate) {
			endDate = d
		}
	}

	var rows []adjustPreviewRow
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		key := d.Format(task.DateFormat)
		rows = append(rows, adjustPreviewRow{
			DateDisplay: d.Format("Mon Jan 02"),
			BeforeLabel: beforeMap[key],
			AfterLabel:  afterMap[key],
			IsToday:     d.Equal(today),
		})
	}
	return rows
}

// guiLoadCropByName looks up a crop variety by name in crops.csv.
func guiLoadCropByName(name string) (crop.Crop, bool) {
	source, err := crop.GetSource()
	if err != nil {
		return crop.Crop{}, false
	}
	crops, err := source.LoadCrops()
	if err != nil {
		return crop.Crop{}, false
	}
	for _, c := range crops {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return crop.Crop{}, false
}

// guiRemoveFutureTasks returns the task list with all future tasks for the
// given cycleID removed (date > today).
func guiRemoveFutureTasks(tasks []task.Task, cycleID string, today time.Time) []task.Task {
	todayStr := today.Format(task.DateFormat)
	var kept []task.Task
	for _, t := range tasks {
		if t.CycleID != cycleID || t.Date <= todayStr {
			kept = append(kept, t)
		}
	}
	return kept
}

// guiTagAdjusted appends "adjusted - be mindful" to every task in the slice.
func guiTagAdjusted(tasks []task.Task) []task.Task {
	for i := range tasks {
		if !strings.Contains(tasks[i].Notes, "adjusted - be mindful") {
			tasks[i].Notes += "\nadjusted - be mindful"
		}
	}
	return tasks
}

// guiCascade applies the same adjustment to all other active/upcoming cycles
// of the same crop. Returns how many cycles were updated.
func guiCascade(chosenCycle *farm.Cycle, stage string, n int, anchorSow bool,
	today time.Time, envs []farm.Environment) int {

	// Reload fresh data — the primary adjustment was already saved.
	cycles, err := farm.LoadCycles()
	if err != nil {
		return 0
	}
	tasks, err := store.Load()
	if err != nil {
		return 0
	}

	// Find target cycles — same crop, not yet harvested, not the one just adjusted.
	var targets []farm.Cycle
	for _, c := range cycles {
		if c.CycleID == chosenCycle.CycleID {
			continue
		}
		if !strings.EqualFold(c.CropName, chosenCycle.CropName) {
			continue
		}
		harv, err := time.Parse(task.DateFormat, c.HarvestDate)
		if err != nil {
			continue
		}
		if harv.After(today) {
			targets = append(targets, c)
		}
	}

	if len(targets) == 0 {
		return 0
	}

	updated := 0
	todayStr := today.Format(task.DateFormat)

	for _, c := range targets {
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mtl, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		sowStr := sow.Format(task.DateFormat)
		mtlStr := mtl.Format(task.DateFormat)
		harvStr := harv.Format(task.DateFormat)

		// Compute new dates — same four-case table as cmd_adjust.go.
		var newSow, newMTL, newHarv time.Time
		switch {
		case anchorSow && stage == "blackout":
			newSow = sow
			newMTL = mtl.AddDate(0, 0, n)
			newHarv = harv.AddDate(0, 0, n)
		case anchorSow && stage == "light":
			newSow = sow
			newMTL = mtl
			newHarv = harv.AddDate(0, 0, n)
		case !anchorSow && stage == "blackout":
			newSow = sow.AddDate(0, 0, -n)
			newMTL = mtl
			newHarv = harv
		case !anchorSow && stage == "light":
			newSow = sow.AddDate(0, 0, -n)
			newMTL = mtl.AddDate(0, 0, -n)
			newHarv = harv
		}

		// Skip if harvest would land in the past.
		if !newHarv.After(today) {
			continue
		}

		// Shift relevant future tasks.
		for i := range tasks {
			t := &tasks[i]
			if t.CycleID != c.CycleID || t.Date <= todayStr {
				continue
			}

			var shouldShift bool
			shift := n

			switch {
			case anchorSow && stage == "blackout":
				shouldShift = t.Date > sowStr
				shift = n
			case anchorSow && stage == "light":
				shouldShift = t.Date > mtlStr
				shift = n
			case !anchorSow && stage == "blackout":
				shouldShift = t.Date < mtlStr
				shift = -n
			case !anchorSow && stage == "light":
				shouldShift = t.Date < harvStr
				shift = -n
			}

			if !shouldShift {
				continue
			}

			d, err := time.Parse(task.DateFormat, t.Date)
			if err != nil {
				continue
			}
			newDate := d.AddDate(0, 0, shift)
			if newDate.Before(today) {
				continue
			}
			t.Date = newDate.Format(task.DateFormat)
			if !strings.Contains(t.Notes, "adjusted - be mindful") {
				t.Notes += "\nadjusted - be mindful"
			}
		}

		// Update cycle record.
		for i := range cycles {
			if cycles[i].CycleID == c.CycleID {
				cycles[i].SowDate = newSow.Format(task.DateFormat)
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				cycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		updated++
	}

	// Save all cascaded changes.
	_ = store.Save(tasks)
	_ = farm.SaveCycles(cycles)

	return updated
}

// removeTrialTentativeTasks deletes tentative tasks entirely (for discards).
func removeTrialTentativeTasks(tr *trial.TrialRecord) {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return
	}

	existing, err := store.Load()
	if err != nil {
		return
	}

	var remaining []task.Task
	for _, t := range existing {
		if t.ID == tr.TentativeMTLTaskID || t.ID == tr.TentativeHarvestTaskID {
			continue
		}
		remaining = append(remaining, t)
	}
	_ = store.Save(remaining)
}

// ─────────────────────────────────────────────────────────────────────────────
// Sync
// ─────────────────────────────────────────────────────────────────────────────

// handleSyncPage renders the sync page (GET /sync).
//
// It checks whether Google Calendar credentials are set up and whether
// Google Sheets has been linked. The template uses these flags to decide
// what to show:
//   - No credentials at all → setup instructions
//   - Credentials exist but Sheets not linked → "Link Google Sheets?" section
//   - Everything linked → "Sync Now" button
func handleSyncPage(w http.ResponseWriter, r *http.Request) {
	cfg, _ := config.Load()
	renderPage(w, "sync.html", map[string]any{
		"CredentialsExist": gcal.CredentialsExist(),
		"TokenExists":      gcal.TokenExists(),
		"SheetsEnabled":    cfg.SheetsEnabled,
		"SheetID":          cfg.SheetID,
	})
}

// handleSyncAction performs the full Google sync (POST /sync).
//
// This does the same thing as "greenies sync" in the terminal:
//   1. Google Sheets sync — pulls crops and farm layout from the Sheet,
//      pushes trials to the Sheet (same two-way + one-way pattern as CLI)
//   2. Google Calendar + Tasks sync — pushes the schedule to Google
//
// The response is an htmx fragment (just a piece of HTML, not a full page)
// that replaces the button area with a success or error message.
func handleSyncAction(w http.ResponseWriter, r *http.Request) {
	// context.Background() means "no deadline, no cancellation" — fine for
	// a user-initiated action they're waiting on.
	ctx := context.Background()
	syncStart := time.Now()

	// ── Google Sheets sync ──────────────────────────────────────────────
	//
	// Pull crops and farm layout from the Sheet (two-way), push trials
	// (one-way). This runs BEFORE Calendar/Tasks so that any changes
	// from the Sheet are reflected in the local data first.
	//
	// Sheets sync is best-effort — if it fails, we continue with the
	// Calendar/Tasks sync using whatever local data we have.
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		sc, err := gcal.NewSheetsClient(ctx, cfg.SheetID)
		if err == nil {
			// Pull crops (two-way).
			if pulledCrops, pullErr := sc.PullCrops(); pullErr == nil && len(pulledCrops) > 0 {
				if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
					_ = crop.WriteCrops(cropsPath, pulledCrops)
				}
			}

			// Pull farm layout (two-way).
			if pulledFarm, pullErr := sc.PullFarm(); pullErr == nil && len(pulledFarm) > 0 {
				if farmPath, pathErr := farm.FarmConfigPath(); pathErr == nil {
					_ = farm.WriteConfig(farmPath, pulledFarm)
				}
			}

			// Push trials (one-way to Sheet).
			if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
				_ = sc.PushTrials(records)
			}

			// Push schedule (one-way to Sheet).
			if allTasks, loadErr := store.Load(); loadErr == nil {
				_ = sc.PushSchedule(allTasks)
			}

			// Push batches (one-way to Sheet).
			if allCycles, loadErr := farm.LoadCycles(); loadErr == nil {
				_ = sc.PushBatches(allCycles)
			}

			// Push harvests (one-way to Sheet).
			if allHarvests, loadErr := farm.LoadHarvests(); loadErr == nil {
				_ = sc.PushHarvests(allHarvests)
			}
		}
	}

	// ── Load local data (freshly synced from Sheets if available) ───────
	tasks, err := store.Load()
	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not load tasks: %v", err),
		})
		return
	}

	envs, _ := farm.LoadConfig()
	cycles, _ := farm.LoadCycles()

	// ── Google Calendar + Tasks sync ────────────────────────────────────
	exporter, err := gcal.NewExporter(ctx)
	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not connect to Google: %v", err),
		})
		return
	}

	err = exporter.Sync(tasks, envs, cycles)
	elapsed := time.Since(syncStart).Round(time.Second)

	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("%v", err),
		})
		return
	}

	renderFragment(w, "sync_result.html", map[string]any{
		"Elapsed": elapsed.String(),
	})
}

// handleSheetsSetup creates the Google Sheet for the first time (POST /sheets-setup).
//
// This is the GUI equivalent of the "Link Google Sheets?" prompt that appears
// in the terminal during the first "greenies sync". When the user clicks the
// "Link Google Sheets" button on the sync page, this handler:
//
//  1. Creates a new Google Spreadsheet with all seven tabs
//     (Crops, Cycle, Farm, Trials, Schedule, Batches, Harvests)
//  2. Saves the spreadsheet ID to config.json
//  3. Pushes all existing local data to the new Sheet
//  4. Returns an htmx fragment showing the Sheet URL and a "Sync Now" button
//
// The response replaces the setup section with a success message, so the user
// can immediately start syncing without reloading the page.
func handleSheetsSetup(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Create the Google Sheet with all seven tabs and headers.
	sheetID, err := gcal.CreateSheet(ctx)
	if err != nil {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not create Google Sheet: %v", err),
		})
		return
	}

	// Save the sheet ID to config so we never have to ask again.
	cfg, _ := config.Load()
	cfg.SheetsEnabled = true
	cfg.SheetID = sheetID
	if saveErr := config.Save(cfg); saveErr != nil {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": fmt.Sprintf("Created Sheet but could not save config: %v", saveErr),
		})
		return
	}

	// Push existing local data to the new Sheet so the user doesn't have
	// to re-enter everything by hand.
	sc, clientErr := gcal.NewSheetsClient(ctx, sheetID)
	if clientErr == nil {
		// Push crops.
		if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
			localSource := crop.CSVSource{Path: cropsPath}
			if localCrops, loadErr := localSource.LoadCrops(); loadErr == nil && len(localCrops) > 0 {
				_ = sc.PushCrops(localCrops)
			}
		}

		// Push farm layout.
		if envs, loadErr := farm.LoadConfig(); loadErr == nil && len(envs) > 0 {
			_ = sc.PushFarm(envs)
		}

		// Push trials.
		if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
			_ = sc.PushTrials(records)
		}

		// Push schedule (tasks.json).
		if allTasks, loadErr := store.Load(); loadErr == nil {
			_ = sc.PushSchedule(allTasks)
		}

		// Push batches (cycles.json).
		if allCycles, loadErr := farm.LoadCycles(); loadErr == nil {
			_ = sc.PushBatches(allCycles)
		}

		// Push harvests (harvests.json).
		if allHarvests, loadErr := farm.LoadHarvests(); loadErr == nil {
			_ = sc.PushHarvests(allHarvests)
		}
	}

	renderFragment(w, "sheets_setup_result.html", map[string]any{
		"SheetID": sheetID,
	})
}

// handleGoogleSignIn runs just the Google OAuth browser sign-in flow
// (POST /google-signin). This is the GUI equivalent of the browser popup
// that normally triggers the first time you run "greenies sync" in the
// terminal.
//
// It does NOT create a Sheet or sync anything — just authenticates. Once
// signed in, the sync page reloads to show the next step (Link Google
// Sheets / Sync Now).
//
// How it works:
//  1. Calls AuthorizeClient, which opens the user's browser to Google's
//     sign-in page if no saved token exists
//  2. The user approves in their browser — the token is saved to token.json
//  3. Returns an htmx fragment confirming success (or showing the error)
func handleGoogleSignIn(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// AuthorizeClient handles everything: opens the browser, waits for
	// approval, saves the token. If a token already exists, it returns
	// immediately without opening the browser.
	_, err := gcal.AuthorizeClient(ctx)
	if err != nil {
		renderFragment(w, "google_signin_result.html", map[string]any{
			"Error": fmt.Sprintf("%v", err),
		})
		return
	}

	renderFragment(w, "google_signin_result.html", map[string]any{})
}

// ─────────────────────────────────────────────────────────────────────────────
// Month calendar with swim lanes
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot swim-lane helper
// ─────────────────────────────────────────────────────────────────────────────

// buildSnapshotWeek builds one week of swim-lane calendar data containing the
// given date. The highlighted date's column gets IsHighlighted = true so the
// template can visually mark it.
//
// This is used by the snapshot and dashboard pages to show a mini calendar
// view at the top — the same swim-lane style as the full month calendar,
// but just one week.
//
// The weekStartPref parameter controls which day the week starts on:
// "mon" for Monday–Sunday, anything else (including "") for Sunday–Saturday.
// This matches the setting from the Settings page.
func buildSnapshotWeek(focusDate time.Time, cycles []farm.Cycle, weekStartPref string) (monthWeek, [7]string) {
	// Pick the day labels and first-weekday based on the preference.
	var dayLabels [7]string
	var firstWeekday time.Weekday
	if weekStartPref == "mon" {
		dayLabels = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
		firstWeekday = time.Monday
	} else {
		dayLabels = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
		firstWeekday = time.Sunday
	}

	// Find the start of the calendar week containing the focus date.
	weekStart := focusDate
	for weekStart.Weekday() != firstWeekday {
		weekStart = weekStart.AddDate(0, 0, -1)
	}

	// Load the crop library so we can check soak settings.
	cropMap := map[string]crop.Crop{}
	if cropsSource, err := crop.GetSource(); err == nil {
		if allCrops, err := cropsSource.LoadCrops(); err == nil {
			for _, cr := range allCrops {
				cropMap[cr.Name] = cr
			}
		}
	}

	// Sort cycles oldest-first (same order as the full calendar).
	sort.Slice(cycles, func(i, j int) bool {
		if cycles[i].SowDate != cycles[j].SowDate {
			return cycles[i].SowDate < cycles[j].SowDate
		}
		return cycles[i].CropName < cycles[j].CropName
	})

	// Pre-compute each cycle's daily stage map — same logic as handleCalendar.
	type cycleInfo struct {
		CropName string
		Trays    int
		DayStage map[string]string
	}

	var allCycleInfo []cycleInfo
	for _, c := range cycles {
		sowDate, err1 := time.Parse(task.DateFormat, c.SowDate)
		harvestDate, err2 := time.Parse(task.DateFormat, c.HarvestDate)
		mtlDate, err3 := time.Parse(task.DateFormat, c.MoveToLightDate)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		stages := map[string]string{}
		cr, hasCrop := cropMap[c.CropName]

		// Soak day(s).
		if hasCrop && cr.OvernightSoak {
			soakDay := sowDate.AddDate(0, 0, -1)
			stages[soakDay.Format(task.DateFormat)] = "soak"
		} else if hasCrop && cr.SoakHours > 0 {
			stages[sowDate.Format(task.DateFormat)] = "soak"
		}

		// Walk sow → harvest assigning stages.
		for d := sowDate; !d.After(harvestDate); d = d.AddDate(0, 0, 1) {
			ds := d.Format(task.DateFormat)
			if _, already := stages[ds]; already {
				continue
			}
			if d.Equal(harvestDate) {
				stages[ds] = "harvest"
			} else if d.Before(mtlDate) {
				stages[ds] = "dark"
			} else {
				stages[ds] = "light"
			}
		}

		allCycleInfo = append(allCycleInfo, cycleInfo{
			CropName: c.CropName,
			Trays:    c.Trays,
			DayStage: stages,
		})
	}

	// Build the single week.
	week := monthWeek{}

	// Fill in the 7 column headers.
	for i := 0; i < 7; i++ {
		d := weekStart.AddDate(0, 0, i)
		week.Headers[i] = monthDayHeader{
			DayNum:        d.Day(),
			InMonth:       true, // always "in month" for snapshot view
			IsHighlighted: d.Equal(focusDate),
			DateStr:       d.Format(task.DateFormat),
		}
	}

	// Build a swim-lane row for each cycle that has activity this week.
	for _, ci := range allCycleInfo {
		row := monthCycleRow{
			CropName: ci.CropName,
			Trays:    ci.Trays,
		}
		hasActivity := false
		prevStage := ""

		for i := 0; i < 7; i++ {
			d := weekStart.AddDate(0, 0, i)
			ds := d.Format(task.DateFormat)
			stage := ci.DayStage[ds]

			label := ""
			if stage != "" && stage != prevStage {
				label = fmt.Sprintf("%s %dx", ci.CropName, ci.Trays)
			}

			row.Cells[i] = monthCell{
				DayNum:        d.Day(),
				Stage:         stage,
				Label:         label,
				Trays:         ci.Trays,
				InMonth:       true,
				IsHighlighted: d.Equal(focusDate),
				DateStr:       ds,
			}

			prevStage = stage
			if stage != "" {
				hasActivity = true
			}
		}

		if hasActivity {
			week.Rows = append(week.Rows, row)
		}
	}

	return week, dayLabels
}

// monthCell holds the display info for one day-cell in the month calendar grid.
// Each cell belongs to a specific cycle on a specific day. The template uses
// the Stage field to pick the background colour.
type monthCell struct {
	// DayNum is the calendar day number (1–31). Zero means this cell is
	// outside the current month (padding at the start or end of the grid).
	DayNum int

	// Stage is which phase of the crop cycle falls on this day:
	// "soak", "dark", "light", "harvest", or "" (no activity).
	Stage string

	// Label is the text shown on the first cell of each stage run — e.g.
	// "sunnies 16x — dark". Empty on continuation cells (2nd, 3rd day
	// of the same stage) so the text only appears once at the start.
	Label string

	// Trays is the batch size — shown as a tooltip or label so the grower
	// can tell overlapping batches apart.
	Trays int

	// InMonth is true if this cell falls within the displayed month.
	// False for padding cells at the start/end of the grid. The template
	// dims or hides these cells so they don't distract.
	InMonth bool

	// IsHighlighted is true if this cell is the "focus" day — used on the
	// snapshot page to highlight which day the grower is looking at.
	IsHighlighted bool

	// DateStr is the full date in YYYY-MM-DD format (e.g. "2026-03-19").
	// Used to build a clickable link so tapping a cell takes the grower
	// to the snapshot for that day.
	DateStr string
}

// monthCycleRow is one swim-lane row in a week section. It represents one
// crop cycle's activity across 7 days (Monday to Sunday).
type monthCycleRow struct {
	// CropName is the variety label shown in the left column.
	CropName string

	// Trays is the batch size — appended to the label so the grower can
	// distinguish "sunnies 16x" from "sunnies 12x" in the same week.
	Trays int

	// Cells holds exactly 7 entries, one per day (Monday index 0 through
	// Sunday index 6). Each cell is either coloured by stage or empty.
	Cells [7]monthCell
}

// monthDayHeader holds the info for one column header in a week row.
type monthDayHeader struct {
	// DayNum is the calendar day number (1–31).
	DayNum int

	// InMonth is true if this day falls within the displayed month.
	InMonth bool

	// IsHighlighted is true if this column is the "focus" day — used
	// on the snapshot page to highlight the snapshot date's column.
	IsHighlighted bool

	// DateStr is the full date in YYYY-MM-DD format (e.g. "2026-03-19").
	// Used to build a clickable link from the header to the snapshot page.
	DateStr string
}

// monthWeek groups all the cycle rows that have activity in one calendar week.
type monthWeek struct {
	// Headers holds 7 entries (Mon–Sun) with the date number and
	// whether that day is inside the displayed month.
	Headers [7]monthDayHeader

	// Rows holds one swim-lane row per cycle that has activity this week.
	// Sorted oldest cycle first (by sow date).
	Rows []monthCycleRow
}

// ─────────────────────────────────────────────────────────────────────────────
// Settings
// ─────────────────────────────────────────────────────────────────────────────

// handleSettingsPage renders the farm settings page at "/settings".
//
// It loads the farm layout from farm.csv and splits it into two groups:
//   - Spaces — environments with type "blackout" or "lit" (physical areas)
//   - Inventory — environments with type "inventory" (countable items)
//
// The template shows editable forms for both groups so the grower can
// change capacities, add new spaces, or remove items — all without
// touching a CSV file by hand.
func handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	envs, err := farm.LoadConfig()
	if err != nil {
		renderPage(w, "settings.html", map[string]any{
			"Spaces":    []farm.Environment{},
			"Inventory": []farm.Environment{},
		})
		return
	}

	// Load preferences from config.json.
	cfg, _ := config.Load()

	// Split environments into spaces (blackout/lit) and inventory items.
	var spaces, inventory []farm.Environment
	for _, env := range envs {
		if env.Type == "inventory" {
			inventory = append(inventory, env)
		} else {
			spaces = append(spaces, env)
		}
	}

	renderPage(w, "settings.html", map[string]any{
		"Spaces":    spaces,
		"Inventory": inventory,
		"Lowercase": cfg.Lowercase,
		"WeekStart": cfg.WeekStart,
		"Theme":     cfg.Theme,
		"Saved":     r.URL.Query().Get("saved") == "1",
	})
}

// handleSettingsUpdate processes the settings form submission (POST /settings).
//
// It reads the form data — space names, types, and capacities plus inventory
// names and counts — rebuilds the full environment list, and saves it to
// farm.csv. Then it pushes the updated layout to Google Sheets (if enabled).
//
// Blank-name rows are silently skipped — this is how "remove" works: the
// grower deletes the name or clicks the ✕ button, and the row disappears
// on the next save.
func handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderPage(w, "settings.html", map[string]any{
			"Spaces":    []farm.Environment{},
			"Inventory": []farm.Environment{},
			"Error":     "Could not read form data.",
		})
		return
	}

	// Read the space rows from the form. Each row has three parallel
	// arrays: space_name[], space_type[], space_capacity[].
	spaceNames := r.Form["space_name"]
	spaceTypes := r.Form["space_type"]
	spaceCaps := r.Form["space_capacity"]

	var envs []farm.Environment

	for i := range spaceNames {
		name := strings.TrimSpace(spaceNames[i])
		if name == "" {
			continue // skip blank rows (removed by the grower)
		}

		// Default to "lit" if type is missing somehow.
		envType := "lit"
		if i < len(spaceTypes) {
			envType = strings.TrimSpace(spaceTypes[i])
		}

		capacity := 0
		if i < len(spaceCaps) {
			capacity, _ = strconv.Atoi(strings.TrimSpace(spaceCaps[i]))
		}

		envs = append(envs, farm.Environment{
			Name:     name,
			Type:     envType,
			Capacity: capacity,
		})
	}

	// Read the inventory rows: inv_name[] and inv_capacity[].
	invNames := r.Form["inv_name"]
	invCaps := r.Form["inv_capacity"]

	for i := range invNames {
		name := strings.TrimSpace(invNames[i])
		if name == "" {
			continue
		}

		capacity := 0
		if i < len(invCaps) {
			capacity, _ = strconv.Atoi(strings.TrimSpace(invCaps[i]))
		}

		envs = append(envs, farm.Environment{
			Name:     name,
			Type:     "inventory",
			Capacity: capacity,
		})
	}

	// Save preferences to config.json.
	cfg, _ := config.Load()
	cfg.Lowercase = r.FormValue("lowercase") == "1"
	ws := r.FormValue("week_start")
	if ws == "mon" {
		cfg.WeekStart = "mon"
	} else {
		cfg.WeekStart = "sun"
	}
	th := r.FormValue("theme")
	if th == "light" {
		cfg.Theme = "light"
	} else {
		cfg.Theme = "dark"
	}
	_ = config.Save(cfg)

	// Save to farm.csv.
	path, err := farm.FarmConfigPath()
	if err != nil {
		renderPage(w, "settings.html", map[string]any{
			"Spaces":    []farm.Environment{},
			"Inventory": []farm.Environment{},
			"Error":     "Could not find farm config path: " + err.Error(),
		})
		return
	}

	if err := farm.WriteConfig(path, envs); err != nil {
		renderPage(w, "settings.html", map[string]any{
			"Spaces":    []farm.Environment{},
			"Inventory": []farm.Environment{},
			"Error":     "Could not save settings: " + err.Error(),
		})
		return
	}

	// Push the updated layout to Google Sheets (fire-and-forget).
	go gcal.SyncLocalToSheet(context.Background())

	// Re-split the saved data and re-render with a success banner.
	var spaces, inventory []farm.Environment
	for _, env := range envs {
		if env.Type == "inventory" {
			inventory = append(inventory, env)
		} else {
			spaces = append(spaces, env)
		}
	}

	renderPage(w, "settings.html", map[string]any{
		"Spaces":    spaces,
		"Inventory": inventory,
		"Lowercase": cfg.Lowercase,
		"WeekStart": cfg.WeekStart,
		"Saved":     true,
	})
}

