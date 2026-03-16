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
	"net/http"

	"github.com/littleguygreens/greenies/internal/calendar"
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
