// handlers_cycle.go — Cycle info page handler.
//
// This renders the /cycle page — a detailed information view for a single
// crop cycle. It shows all key dates, current stage, financials (if costing
// data exists), and every task in the cycle grouped by day. At the bottom
// there are buttons for "Adjust" and "Delete".
//
// Think of this as the cycle's "baseball card" — everything you'd want to
// know about one batch of trays, all in one place.
package gui

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/supply"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Cycle info
// ─────────────────────────────────────────────────────────────────────────────

// cycleInfoTask is one task displayed on the cycle info page. We only need the
// title and notes — the date grouping is handled by cycleInfoDay.
type cycleInfoTask struct {
	Title string // e.g. "Sow sunnies — 4 trays"
	Notes string // extra detail (e.g. "main tent")
}

// cycleInfoDay groups all tasks for one day of the cycle. Each day gets a
// heading like "Day 3 — Mon Mar 11 (dark)" and a list of tasks underneath.
type cycleInfoDay struct {
	DayNum   int              // the day number in the cycle (1, 2, 3, …)
	Date     string           // formatted date like "Mon Mar 11"
	DateRaw  string           // YYYY-MM-DD for sorting
	Stage    string           // "sow", "dark", "light", or "harvest"
	Tasks    []cycleInfoTask  // the tasks on this day
	HasTasks bool             // true if there are tasks (some days are empty)
	IsToday  bool             // true if this day is today
	IsPast   bool             // true if this day is in the past
}

// handleCycleInfoPage renders GET /cycle?id=XXX — the cycle information page.
//
// It loads a single cycle by its ID, looks up the crop definition for task
// details and financials, and passes everything to the template.
func handleCycleInfoPage(w http.ResponseWriter, r *http.Request) {
	cycleID := r.URL.Query().Get("id")
	if cycleID == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Load all cycle records and find the one we want.
	cycles, err := farm.LoadCycles()
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
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
		// Cycle not found — maybe it was already deleted.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Parse the key dates from the cycle record.
	sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
	mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	today := task.Today()

	// Figure out what day of the cycle we are on and what stage.
	dayNum := 0
	stage := "upcoming"
	if !today.Before(sow) && !today.After(harv) {
		// We are somewhere within the cycle.
		dayNum = int(today.Sub(sow).Hours()/24) + 1
		stage = "dark"
		if today.Equal(harv) {
			stage = "harvest day"
		} else if !today.Before(mtl) {
			stage = "light"
		}
	} else if today.After(harv) {
		stage = "completed"
	}

	// Total number of days in the cycle (sow through harvest, inclusive).
	totalDays := int(harv.Sub(sow).Hours()/24) + 1

	// ── Load all tasks belonging to this cycle ───────────────────────────
	allTasks, _ := store.Load()

	// Build a map of date → list of tasks for this cycle.
	tasksByDate := make(map[string][]cycleInfoTask)
	for _, t := range allTasks {
		if t.CycleID != cycleID {
			continue
		}
		// Skip "no tasks today" placeholder tasks — they exist so the
		// visualizer knows trays are on the rack, but the grower doesn't
		// need to see them on the info page.
		if t.Notes == task.NoTasksNote {
			continue
		}
		tasksByDate[t.Date] = append(tasksByDate[t.Date], cycleInfoTask{
			Title: t.Title,
			Notes: t.Notes,
		})
	}

	// Build the day-by-day timeline for the cycle.
	var days []cycleInfoDay
	for d := sow; !d.After(harv); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format(task.DateFormat)
		dn := int(d.Sub(sow).Hours()/24) + 1

		// Determine which stage this day falls in.
		dayStage := "dark"
		if dn == 1 {
			dayStage = "sow"
		} else if d.Equal(harv) {
			dayStage = "harvest"
		} else if !d.Before(mtl) {
			dayStage = "light"
		}

		tasksForDay := tasksByDate[dateStr]

		days = append(days, cycleInfoDay{
			DayNum:   dn,
			Date:     d.Format("Mon Jan 02"),
			DateRaw:  dateStr,
			Stage:    dayStage,
			Tasks:    tasksForDay,
			HasTasks: len(tasksForDay) > 0,
			IsToday:  d.Equal(today),
			IsPast:   d.Before(today),
		})
	}

	// Sort days by date (they should already be in order, but let's be safe).
	sort.Slice(days, func(i, j int) bool {
		return days[i].DateRaw < days[j].DateRaw
	})

	// ── Financials ───────────────────────────────────────────────────────
	// Look up the crop definition so we can calculate cost/revenue/profit.
	//
	// The info page shows batch-level numbers (total for all trays in this
	// cycle) rather than per-tray numbers, because that's how the grower
	// actually thinks about costs:
	//   - Seed cost   = (seed cost per bag ÷ bag weight) × grams per tray × trays
	//   - Medium cost = litres per tray × trays × cost per litre
	//   - Packaging   = (container cost + label cost) × sellable units
	var hasFinancials bool
	var batchSeedCost, batchMediumCost, batchPackagingCost float64
	var batchCost, batchRevenue, batchProfit float64
	var profitPerTray, profitMargin float64
	var sellableUnits int

	source, err := crop.GetSource()
	if err == nil {
		crops, err := source.LoadCrops()
		if err == nil {
			// Find the crop by name (case-insensitive match).
			for _, c := range crops {
				if strings.EqualFold(c.Name, chosen.CropName) {
					if c.HasCostingData() {
						sc := supply.LoadSupplyCosts()
						hasFinancials = true
						trays := float64(chosen.Trays)

						// Seed: per-tray cost × number of trays.
						batchSeedCost = crop.RoundCents(c.SeedCostPerTray() * trays)

						// Medium: litres per tray × trays × cost per litre.
						batchMediumCost = crop.RoundCents(c.MediumLitres * trays * sc.MediumPerLitre)

						// Packaging: cost per unit × total sellable units.
						sellableUnits = c.SellableUnits(chosen.Trays)
						batchPackagingCost = crop.RoundCents(
							float64(sellableUnits) * (sc.ContainerEach + sc.LabelEach))

						batchCost = crop.RoundCents(batchSeedCost + batchMediumCost + batchPackagingCost)
						batchRevenue = crop.RoundCents(float64(sellableUnits) * c.UnitSellPrice)
						batchProfit = crop.RoundCents(batchRevenue - batchCost)
						profitPerTray = crop.RoundCents(c.ProfitPerTray(sc))
						profitMargin = crop.RoundCents(c.ProfitMargin(sc))
					}
					break
				}
			}
		}
	}

	// ── Render the page ──────────────────────────────────────────────────
	renderPage(w, "cycle_info.html", map[string]any{
		// Cycle identity
		"CycleID":  cycleID,
		"CropName": task.Capitalize(chosen.CropName),
		"Trays":    chosen.Trays,
		"LitEnv":   formatEnvName(chosen.LitEnvironment),

		// Key dates
		"SowDisplay":  sow.Format("Mon Jan 02"),
		"MTLDisplay":  mtl.Format("Mon Jan 02"),
		"HarvDisplay": harv.Format("Mon Jan 02"),

		// Current status
		"DayNum":    dayNum,
		"TotalDays": totalDays,
		"Stage":     stage,

		// Expected yield
		"ExpectedGrams": chosen.ExpectedGrams,
		"HasYield":      chosen.ExpectedGrams > 0,

		// Task timeline
		"Days":    days,
		"HasDays": len(days) > 0,

		// Financials (batch-level)
		"HasFinancials":     hasFinancials,
		"BatchSeedCost":     batchSeedCost,
		"BatchMediumCost":   batchMediumCost,
		"BatchPackagingCost": batchPackagingCost,
		"SellableUnits":     sellableUnits,
		"BatchCost":         batchCost,
		"BatchRevenue":      batchRevenue,
		"BatchProfit":       batchProfit,
		"ProfitPerTray":     profitPerTray,
		"ProfitMargin":      profitMargin,
	})
}

// formatEnvName turns a config-style environment name like "main_tent" into
// a human-friendly label like "Main Tent". The special value "any" becomes
// "Any (auto-assigned)".
func formatEnvName(name string) string {
	if name == "any" {
		return "Any (auto-assigned)"
	}
	// Replace underscores with spaces and capitalise each word.
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// handleCycleDelete handles POST /cycle/delete — deletes a cycle and all its
// tasks, then redirects back to the dashboard.
//
// This is the same logic that the /delete page uses for "whole cycle" mode,
// but triggered from the cycle info page's delete button instead.
func handleCycleDelete(w http.ResponseWriter, r *http.Request) {
	cycleID := r.FormValue("cycle_id")
	if cycleID == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Delete all tasks that belong to this cycle.
	allTasks, err := store.Load()
	if err != nil {
		http.Error(w, "Could not load tasks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var kept []task.Task
	for _, t := range allTasks {
		if t.CycleID != cycleID {
			kept = append(kept, t)
		}
	}

	if err := store.Save(kept); err != nil {
		http.Error(w, "Could not save tasks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove the cycle record from cycles.json so the snapshot stays in sync.
	cycles, err := farm.LoadCycles()
	if err == nil {
		var keptCycles []farm.Cycle
		for _, c := range cycles {
			if c.CycleID != cycleID {
				keptCycles = append(keptCycles, c)
			}
		}
		_ = farm.SaveCycles(keptCycles)
	}

	// Redirect to the dashboard — the cycle is gone.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
