// handlers_crops.go — Crop library, add crop, and edit crop handlers.
//
// These handle the /crops, /crops/new, and /crops/edit pages. The crop
// library shows every variety in a table; the add and edit pages let the
// grower manage crop parameters through browser forms.
package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/supply"
)

// ─────────────────────────────────────────────────────────────────────────────
// Crops
// ─────────────────────────────────────────────────────────────────────────────

// handleCrops renders the crop library page at "/crops".
//
// It works like "greenies crops" — loads the crop CSV and displays every
// variety in an HTML table. The table shows the key numbers a grower cares
// about: cycle length, seed per tray, and expected yield per tray.
// CropRow holds one row of the crop library table. It wraps the Crop struct
// with pre-calculated profitability numbers so the HTML template can display
// them without needing to call methods with arguments (Go templates cannot
// pass arguments to methods).
type CropRow struct {
	crop.Crop

	// UnitsPerTray is how many sellable units one tray produces (exact decimal).
	UnitsPerTray float64

	// CostPerTray is the all-in cost to grow and package one tray, in dollars.
	CostPerTray float64

	// RevenuePerTray is total revenue from selling one tray's harvest.
	RevenuePerTray float64

	// ProfitPerTray is revenue minus cost, in dollars.
	ProfitPerTray float64

	// ProfitMargin is profit as a percentage of revenue (e.g. 62.5).
	ProfitMargin float64

	// HasProfit is true if we have enough data to show profitability numbers.
	HasProfit bool
}

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

	// Load farm-wide supply costs so we can calculate profitability.
	// If supplies can't be loaded, the returned struct has all zeroes.
	sc := supply.LoadSupplyCosts()

	// Build a CropRow for each crop with pre-calculated profit numbers.
	rows := make([]CropRow, len(crops))
	for i, c := range crops {
		rows[i] = CropRow{
			Crop:           c,
			UnitsPerTray:   c.UnitsPerTray(),
			CostPerTray:    crop.RoundCents(c.TotalCostPerTray(sc)),
			RevenuePerTray: crop.RoundCents(c.RevenuePerTray()),
			ProfitPerTray:  crop.RoundCents(c.ProfitPerTray(sc)),
			ProfitMargin:   crop.RoundCents(c.ProfitMargin(sc)),
			HasProfit:      c.HasCostingData(),
		}
	}

	renderPage(w, "crops.html", map[string]any{
		"Crops":    rows,
		"HasCrops": len(rows) > 0,
		"Count":    len(rows),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared crop form parser
// ─────────────────────────────────────────────────────────────────────────────

// parseCropForm reads all crop parameters and day-by-day rows from an HTTP
// form submission. Both "Add New Crop" and "Edit Crop" use the same form
// fields, so this helper avoids duplicating ~130 lines of parsing logic.
//
// Returns the built Crop, the day list, and an error string (empty on success).
// The caller is responsible for displaying the error in the appropriate template.
func parseCropForm(r *http.Request) (crop.Crop, []crop.CropDay, string) {
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

	mediumLitres := 1.0
	if v, err := strconv.ParseFloat(r.FormValue("medium_litres"), 64); err == nil && v > 0 {
		mediumLitres = v
	}

	yieldGrams := 0
	if v, err := strconv.Atoi(r.FormValue("yield_grams")); err == nil {
		yieldGrams = v
	}

	// ── Read costing parameters (all optional) ──────────────────────────
	seedCost := 0.0
	if v, err := strconv.ParseFloat(r.FormValue("seed_cost"), 64); err == nil {
		seedCost = v
	}

	seedPurchaseWeight := 0.0
	if v, err := strconv.ParseFloat(r.FormValue("seed_purchase_weight"), 64); err == nil {
		seedPurchaseWeight = v
	}
	// If the grower chose the large unit (kg or lb) in the dropdown,
	// convert to the small unit (g or oz) for storage.
	if r.FormValue("seed_weight_unit") == "kg" {
		cfg, _ := config.Load()
		seedPurchaseWeight *= float64(cfg.LargeWeightMultiplier())
	}

	unitWeight := 100.0 // default: 100 g per sellable unit
	if v, err := strconv.ParseFloat(r.FormValue("unit_weight"), 64); err == nil && v > 0 {
		unitWeight = v
	}

	unitSellPrice := 0.0
	if v, err := strconv.ParseFloat(r.FormValue("unit_sell_price"), 64); err == nil {
		unitSellPrice = v
	}

	// ── Read the day rows ───────────────────────────────────────────────
	// The form sends parallel arrays: day_num[], day_stage[], day_tasks[].
	dayNums := r.Form["day_num[]"]
	dayStages := r.Form["day_stage[]"]
	dayTasks := r.Form["day_tasks[]"]

	if len(dayNums) == 0 {
		return crop.Crop{}, nil, "No cycle days defined — set the total cycle days first."
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
		return crop.Crop{}, nil, "A crop needs at least 2 days (sow and harvest)."
	}

	// ── Build the Crop value ────────────────────────────────────────────
	// CycleDays is derived from the last day's number — same as the CSV
	// loader does, so it can never get out of sync.
	c := crop.Crop{
		CycleDays:          days[len(days)-1].Day,
		OvernightSoak:      overnightSoak,
		SoakHours:          soakHours,
		SeedGrams:          seedGrams,
		MediumLitres:         mediumLitres,
		DarkDays:           darkDays,
		LightDays:          lightDays,
		YieldGrams:         yieldGrams,
		SeedCost:           seedCost,
		SeedPurchaseWeight: seedPurchaseWeight,
		UnitWeight:         unitWeight,
		UnitSellPrice:      unitSellPrice,
		Days:               days,
	}

	return c, days, ""
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

	// ── Parse all crop parameters from the form ─────────────────────────
	newCrop, _, errMsg := parseCropForm(r)
	if errMsg != "" {
		renderFragment(w, "crop_new_result.html", map[string]any{
			"Error": errMsg,
		})
		return
	}
	newCrop.Name = cropName

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

	// ── Parse all crop parameters from the form ─────────────────────────
	updatedCrop, _, errMsg := parseCropForm(r)
	if errMsg != "" {
		renderFragment(w, "crop_edit_result.html", map[string]any{
			"Error": errMsg,
		})
		return
	}
	updatedCrop.Name = cropName

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
