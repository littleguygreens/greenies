// handlers_harvest.go — Harvest log and harvest logging handlers.
//
// These handle the /harvestlog (view history) and /harvest (log a new
// harvest) pages. The harvest log shows past harvests with expected vs
// actual yields. The harvest page lets the grower record what they cut.
package gui

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

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
			eligible = append(eligible, eligibleCycle{
				CycleID:         c.CycleID,
				CropName:        task.Capitalize(c.CropName),
				Trays:           c.Trays,
				TrayWord:        task.TrayWord(c.Trays),
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
