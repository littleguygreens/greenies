// handlers_settings.go — Farm settings page handler.
//
// These handle the /settings page where the grower configures their farm
// layout (blackout/lit environments, tray inventory), supply costs, and
// program preferences (theme, week start day, lowercase mode).
package gui

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/supply"
)

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
			"Supplies":  []supply.Supply{},
		})
		return
	}

	// Load preferences from config.json.
	cfg, _ := config.Load()

	// Load farm-wide supplies from supplies.csv. If the file is empty or
	// doesn't exist yet, start with the three default items that the
	// profitability calculator looks up by name. This way a new grower
	// sees the right rows already waiting to be filled in.
	supplies, _ := supply.Load()
	if len(supplies) == 0 {
		supplies = []supply.Supply{
			{Name: "grow medium", Category: "medium"},
			{Name: "containers", Category: "container"},
			{Name: "labels", Category: "label"},
		}
	}

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
		"Spaces":         spaces,
		"Inventory":      inventory,
		"Supplies":       supplies,
		"Lowercase":      cfg.Lowercase,
		"WeekStart":      cfg.WeekStart,
		"Theme":          cfg.Theme,
		"Units":          cfg.Units,
		"IrrigationMode": cfg.IrrigationMode,
		"FlashyGUI":      cfg.FlashyGUI,
		"GOOS":           runtime.GOOS,
		"Saved":          r.URL.Query().Get("saved") == "1",
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

	// Read the supply rows: supply_name[], supply_category[], supply_cost[],
	// supply_units[].
	supplyNames := r.Form["supply_name"]
	supplyCats := r.Form["supply_category"]
	supplyCosts := r.Form["supply_cost"]
	supplyUnits := r.Form["supply_units"]

	var supplies []supply.Supply
	for i := range supplyNames {
		name := strings.TrimSpace(supplyNames[i])
		if name == "" {
			continue // skip blank rows
		}

		cat := ""
		if i < len(supplyCats) {
			cat = strings.TrimSpace(supplyCats[i])
		}

		costVal := 0.0
		if i < len(supplyCosts) {
			costVal, _ = strconv.ParseFloat(strings.TrimSpace(supplyCosts[i]), 64)
		}

		unitsVal := 0.0
		if i < len(supplyUnits) {
			unitsVal, _ = strconv.ParseFloat(strings.TrimSpace(supplyUnits[i]), 64)
		}

		supplies = append(supplies, supply.Supply{
			Name:         name,
			Category:     cat,
			CostPerCase:  costVal,
			UnitsPerCase: unitsVal,
		})
	}

	// Save supplies to supplies.csv.
	if err := supply.Save(supplies); err != nil {
		renderPage(w, "settings.html", map[string]any{
			"Spaces":    []farm.Environment{},
			"Inventory": []farm.Environment{},
			"Supplies":  []supply.Supply{},
			"Error":     "Could not save supplies: " + err.Error(),
		})
		return
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
	un := r.FormValue("units")
	if un == "imperial" {
		cfg.Units = "imperial"
	} else {
		cfg.Units = "metric"
	}
	im := r.FormValue("irrigation_mode")
	if im == "tray_pairs" {
		cfg.IrrigationMode = "tray_pairs"
	} else {
		cfg.IrrigationMode = "flood"
	}
	cfg.FlashyGUI = r.FormValue("flashy_gui") == "1"
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
		"Spaces":         spaces,
		"Inventory":      inventory,
		"Supplies":       supplies,
		"Lowercase":      cfg.Lowercase,
		"WeekStart":      cfg.WeekStart,
		"Theme":          cfg.Theme,
		"Units":          cfg.Units,
		"IrrigationMode": cfg.IrrigationMode,
		"FlashyGUI":      cfg.FlashyGUI,
		"GOOS":           runtime.GOOS,
		"Saved":          true,
	})
}
