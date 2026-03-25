// handlers_tasks.go — Delete and Clear task handlers.
//
// These handle the /delete page (remove individual tasks or whole cycles)
// and the /clear page (wipe all tasks). Both have confirmation steps to
// prevent accidental data loss.
package gui

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

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

	// Determine the displayed month — defaults to the current month.
	// Supports ?year=2026&month=4 query params for navigation.
	now := task.Today()
	year, month := now.Year(), now.Month()
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

	firstOfMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	lastOfMonth := firstOfMonth.AddDate(0, 1, -1)
	startStr := firstOfMonth.Format(task.DateFormat)
	endStr := lastOfMonth.Format(task.DateFormat)

	// Filter tasks to just the selected month, then sort by date.
	var monthTasks []task.Task
	for _, t := range allTasks {
		if t.Date >= startStr && t.Date <= endStr {
			monthTasks = append(monthTasks, t)
		}
	}
	sort.Slice(monthTasks, func(i, j int) bool {
		return monthTasks[i].Date < monthTasks[j].Date
	})

	// Group tasks by date — one deleteDay per unique date, in order.
	dayMap := map[string]*deleteDay{}
	var dayOrder []string
	for _, t := range monthTasks {
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

	// Navigation: previous and next month.
	prevMonth := firstOfMonth.AddDate(0, -1, 0)
	nextMonth := firstOfMonth.AddDate(0, 1, 0)

	renderPage(w, "delete.html", map[string]any{
		"MonthLabel": firstOfMonth.Format("January 2006"),
		"Days":       days,
		"HasTasks":   len(monthTasks) > 0,
		"Count":      len(monthTasks),
		"PrevYear":   prevMonth.Year(),
		"PrevMonth":  int(prevMonth.Month()),
		"NextYear":   nextMonth.Year(),
		"NextMonth":  int(nextMonth.Month()),
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
