// handlers_dashboard.go — Dashboard and Snapshot page handlers.
//
// These are the two most-visited pages in the GUI. The dashboard is the home
// page that greets the grower when they open the browser. The snapshot page
// shows a detailed view of the farm on any given date.
package gui

import (
	"net/http"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/supply"
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

	// Add profitability info to the snapshot — cost, revenue, and profit
	// for each active cycle and an overall financial summary.
	enrichSnapshotFinancials(&snap)

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

	// Add profitability info to the snapshot.
	enrichSnapshotFinancials(&snap)

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
// Financial enrichment
// ─────────────────────────────────────────────────────────────────────────────

// enrichSnapshotFinancials loads the crop library and supply costs, then fills
// in the profitability fields on every CycleRow in the snapshot. It also
// computes a FinancialSummary — but only for cycles harvesting this calendar
// week (Monday through Sunday). This gives the grower a useful "what am I
// making this week?" number instead of a sum of everything on the farm.
//
// This runs after BuildSnapshot() because the visualizer package deliberately
// knows nothing about crops or supplies — it only deals with farm layout and
// cycle timing. The financial data is layered on top by the GUI handler.
func enrichSnapshotFinancials(snap *visualizer.SnapshotData) {
	if !snap.HasData {
		return
	}

	// Load the crop library — we need it to look up costing fields by name.
	source, err := crop.GetSource()
	if err != nil {
		return
	}
	crops, err := source.LoadCrops()
	if err != nil {
		return
	}

	// Build a quick lookup map: lowercase crop name → Crop struct.
	cropMap := make(map[string]crop.Crop, len(crops))
	for _, c := range crops {
		cropMap[strings.ToLower(c.Name)] = c
	}

	// Load supply costs (medium, containers, labels).
	sc := supply.LoadSupplyCosts()

	// Work out the boundaries of the current calendar week. The grower's
	// WeekStart setting controls whether weeks run Mon–Sun or Sun–Sat.
	// Any cycle whose harvest date falls in this window counts toward the
	// weekly financial summary.
	cfg, _ := config.Load()
	now := time.Now()
	weekday := int(now.Weekday()) // Sunday=0 … Saturday=6
	var daysBack int
	if cfg.WeekStart == "mon" {
		// ISO weeks: Monday is day 1, Sunday is day 7.
		if weekday == 0 {
			daysBack = 6 // today is Sunday → go back 6 days to Monday
		} else {
			daysBack = weekday - 1 // e.g. Wednesday (3) → back 2 to Monday
		}
	} else {
		// US weeks: Sunday is the first day of the week.
		daysBack = weekday // e.g. Wednesday (3) → back 3 to Sunday
	}
	weekStart := time.Date(now.Year(), now.Month(), now.Day()-daysBack, 0, 0, 0, 0, now.Location())
	weekEnd := weekStart.AddDate(0, 0, 7)

	// Helper that fills in financial fields on a single CycleRow.
	enrich := func(row *visualizer.CycleRow) {
		c, ok := cropMap[strings.ToLower(row.CropName)]
		if !ok || !c.HasCostingData() {
			return
		}
		row.CostPerTray = crop.RoundCents(c.TotalCostPerTray(sc))
		row.RevenuePerTray = crop.RoundCents(c.RevenuePerTray())
		row.ProfitPerTray = crop.RoundCents(c.ProfitPerTray(sc))
		row.ProfitBatch = crop.RoundCents(c.ProfitPerTray(sc) * float64(row.Trays))
		row.HasProfit = true
	}

	// Helper that checks whether a cycle's harvest falls in this week.
	inThisWeek := func(row *visualizer.CycleRow) bool {
		h := row.HarvestRaw
		return !h.Before(weekStart) && h.Before(weekEnd)
	}

	// Helper that adds a cycle's financials to the weekly totals.
	// Uses SellableUnits (floor of total units across all trays) so we
	// don't count fractional clamshells that can't actually be sold.
	addToTotals := func(fin *visualizer.FinancialSummary, row *visualizer.CycleRow) {
		if !row.HasProfit || !inThisWeek(row) {
			return
		}
		c := cropMap[strings.ToLower(row.CropName)]
		trays := float64(row.Trays)
		fin.TotalCost += row.CostPerTray * trays
		sellable := c.SellableUnits(row.Trays)
		fin.TotalRevenue += float64(sellable) * c.UnitSellPrice
		fin.HasData = true
	}

	// Walk through every cycle in every environment and enrich it.
	var fin visualizer.FinancialSummary
	for i := range snap.Environments {
		for j := range snap.Environments[i].Cycles {
			row := &snap.Environments[i].Cycles[j]
			enrich(row)
			addToTotals(&fin, row)
		}
	}

	// Also enrich harvest-today and upcoming rows (for per-cycle display).
	for i := range snap.HarvestToday {
		row := &snap.HarvestToday[i]
		enrich(row)
		addToTotals(&fin, row)
	}
	// Upcoming cycles aren't active yet — don't include them in totals,
	// but still enrich for per-cycle display.
	for i := range snap.Upcoming {
		enrich(&snap.Upcoming[i])
	}

	// Round the totals and compute profit.
	fin.TotalCost = crop.RoundCents(fin.TotalCost)
	fin.TotalRevenue = crop.RoundCents(fin.TotalRevenue)
	fin.TotalProfit = crop.RoundCents(fin.TotalRevenue - fin.TotalCost)

	snap.Financial = fin
}
