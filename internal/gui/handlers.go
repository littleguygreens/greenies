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
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
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

	// Load the farm snapshot — the same function used by "greenies sync"
	// to build the Google Calendar description.
	snapshotText := ""
	envs, err := farm.LoadConfig()
	if err == nil {
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			snapshotText = visualizer.SnapshotText(envs, cycles, today)
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
		"Today":        today.Format("Monday, 2 January 2006"),
		"TodayDate":    todayStr,
		"Snapshot":     snapshotText,
		"Tasks":        todayTasks,
		"HasTasks":     len(todayTasks) > 0,
		"HasSnapshot":  snapshotText != "",
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

	// Load the farm config and cycle records — same as the CLI does.
	snapshotText := ""
	envs, err := farm.LoadConfig()
	if err == nil {
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			snapshotText = visualizer.SnapshotText(envs, cycles, snapshotTime)
		}
	}

	renderPage(w, "snapshot.html", map[string]any{
		"Date":        snapshotTime.Format("Monday, 2 January 2006"),
		"DateValue":   snapshotTime.Format(task.DateFormat),
		"Snapshot":    snapshotText,
		"HasSnapshot": snapshotText != "",
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

// handleList renders the calendar view at "/list".
//
// It works like "greenies list" but without the interactive prompt — instead,
// the grower clicks buttons to switch between week and month views. The URL
// parameter ?mode=week or ?mode=month controls which view is shown.
//
// Each day appears as a card with all its tasks listed inside.
func handleList(w http.ResponseWriter, r *http.Request) {
	// Load all saved tasks from disk.
	allTasks, err := store.Load()
	if err != nil {
		allTasks = []task.Task{}
	}

	// Decide the view mode. Default is "week" if nothing is specified.
	mode := r.URL.Query().Get("mode")
	if mode != "month" {
		mode = "week"
	}

	// Work out the date range to display based on the mode.
	now := task.Today()
	var start, end time.Time

	if mode == "month" {
		// Show the full current calendar month: 1st to last day.
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, -1) // last day of this month
	} else {
		// Show 7 days starting from today.
		start = now
		end = now.AddDate(0, 0, 6)
	}

	// Build a card for each day in the range.
	var days []dayCard
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format(task.DateFormat)
		dayTasks := calendar.TasksForDate(allTasks, dateStr)
		days = append(days, dayCard{
			DateHeading: d.Format("Monday, 2 January 2006"),
			Tasks:       dayTasks,
			HasTasks:    len(dayTasks) > 0,
		})
	}

	renderPage(w, "list.html", map[string]any{
		"Mode": mode,
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
	// Find and load the crops CSV file.
	path, err := crop.CropsFilePath()
	if err != nil {
		renderPage(w, "crops.html", map[string]any{
			"Error":    "Could not find crops file: " + err.Error(),
			"HasCrops": false,
		})
		return
	}

	source := crop.CSVSource{Path: path}
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
	path, err := crop.CropsFilePath()
	if err != nil {
		renderPage(w, "plan.html", map[string]any{
			"HasCrops": false,
			"HasEnvs":  false,
		})
		return
	}
	source := crop.CSVSource{Path: path}
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
	path, err := crop.CropsFilePath()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not find crops file."})
		return
	}
	source := crop.CSVSource{Path: path}
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
	path, err := crop.CropsFilePath()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not find crops file."})
		return
	}
	source := crop.CSVSource{Path: path}
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
	CycleID       string // the unique ID for this crop cycle
	CropName      string // capitalised crop name, e.g. "Sunnies"
	Trays         int    // how many trays were planned
	TrayWord      string // "tray" or "trays" — English is weird about plurals
	HarvestDate   string // human-readable date, e.g. "Mar 15"
	ExpectedGrams int    // planned yield in grams (may be 0 for older cycles)
	HasExpected   bool   // true if ExpectedGrams > 0
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
				CycleID:       c.CycleID,
				CropName:      task.Capitalize(c.CropName),
				Trays:         c.Trays,
				TrayWord:      trayWord,
				HarvestDate:   harv.Format("Jan 02"),
				ExpectedGrams: c.ExpectedGrams,
				HasExpected:   c.ExpectedGrams > 0,
			})
		}
	}

	// Sort most recent harvest first — same order as the CLI.
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].HarvestDate > eligible[j].HarvestDate
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
