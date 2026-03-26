// handlers_plan.go — Crop cycle planning handlers.
//
// These handle the /plan page where the grower schedules a new crop cycle.
// It supports forward and backward planning with fixed trays or yield-driven
// calculations, previews the schedule before committing, and saves the cycle.
package gui

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

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
	source, err := crop.GetSource()
	if err != nil {
		renderPage(w, "plan.html", map[string]any{
			"HasCrops": false,
			"HasEnvs":  false,
		})
		return
	}
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
	source, err := crop.GetSource()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not find crops file."})
		return
	}
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

	// Resolve "any" to the first lit env for conflict checking.
	envForCycle := litEnv
	if envForCycle == "" {
		envForCycle = "any"
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
	trayWord := task.TrayWord(trays)
	anchorLabel := "harvest"
	if !fromHarvest {
		anchorLabel = "sow"
	}
	header := fmt.Sprintf("%s — %d %s — %s %s",
		task.Capitalize(found.Name), trays, trayWord, anchorLabel, dateStr)

	// ── Build swimlane preview ────────────────────────────────────────
	// Show the new cycle(s) in context alongside existing crops on a mini
	// swimlane calendar, just like the adjust preview does. Each temporary
	// cycle gets a throwaway ID so the template can highlight it.
	highlightIDs := map[string]bool{}
	for i := range tempCycles {
		id := fmt.Sprintf("plan-preview-%d", i)
		tempCycles[i].CycleID = id
		highlightIDs[id] = true
	}

	// Combine existing cycles with the planned ones for the swimlane.
	existingForSwim, _ := farm.LoadCycles()
	swimAll := append(existingForSwim, tempCycles...)

	// Date range: earliest sow to latest harvest across all planned cycles.
	rangeStart := baseSow
	rangeEnd := baseHarvest
	for _, tc := range tempCycles {
		if s, err := time.Parse(task.DateFormat, tc.SowDate); err == nil && s.Before(rangeStart) {
			rangeStart = s
		}
		if h, err := time.Parse(task.DateFormat, tc.HarvestDate); err == nil && h.After(rangeEnd) {
			rangeEnd = h
		}
	}

	swimWeeks, swimDayLabels := buildAdjustSwimlane(swimAll, "", rangeStart, rangeEnd, task.Today())

	renderFragment(w, "plan_preview.html", map[string]any{
		"Header":       header,
		"Days":         days,
		"Conflicts":    conflicts,
		"HasConflicts": len(conflicts) > 0,
		"TotalCycles":  totalCycles,
		// Swimlane preview data.
		"SwimWeeks":    swimWeeks,
		"SwimDayLabels": swimDayLabels,
		"HighlightIDs": highlightIDs,
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
	source, err := crop.GetSource()
	if err != nil {
		renderFragment(w, "plan_preview.html", map[string]any{"Error": "Could not find crops file."})
		return
	}
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

	// Resolve the lit environment. Empty or missing defaults to "any".
	envForCycle := strings.TrimSpace(litEnv)
	if envForCycle == "" {
		envForCycle = "any"
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
