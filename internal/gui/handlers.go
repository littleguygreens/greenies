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
