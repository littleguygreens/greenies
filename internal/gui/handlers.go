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
	"net/http"
	"sort"
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
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
