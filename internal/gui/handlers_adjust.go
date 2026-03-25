// handlers_adjust.go — Mid-cycle adjustment handlers.
//
// These handle the /adjust pages where the grower can change dates or tray
// counts for active crop cycles. Adjustments cascade to other overlapping
// cycles when requested, and the conflict checker re-runs automatically.
package gui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Adjust
// ─────────────────────────────────────────────────────────────────────────────

// adjustCycleRow holds the display data for one cycle in the adjust page list.
// We pre-compute everything the template needs so the template stays simple.
type adjustCycleRow struct {
	CycleID        string // hidden value for the radio button
	CropName       string // e.g. "Sunnies"
	Trays          int
	SowDisplay     string // e.g. "Mar 09"
	HarvestDisplay string
	DayNum         int    // which day of the cycle (for active cycles)
	Stage          string // "dark", "light", or "harvest day" (for active cycles)
	DaysUntil      int    // days until sow (for upcoming cycles)
}

// adjustPreviewRow is one row in the before/after side-by-side table.
type adjustPreviewRow struct {
	Date        time.Time // the actual calendar date (used for merging before/after)
	DateDisplay string    // e.g. "Mon Mar 09" (for the template)
	BeforeLabel string    // e.g. "Day 3 (dark)" — empty string means "---"
	AfterLabel  string
	IsToday     bool // true if this row is today's date
}

// handleAdjustPage renders GET /adjust — the cycle adjustment page.
//
// It loads all active and upcoming (within 7 days) cycles and passes them
// to the template as two separate lists. The grower picks one cycle, then
// fills in the adjustment form and clicks "Preview".
func handleAdjustPage(w http.ResponseWriter, r *http.Request) {
	cycles, err := farm.LoadCycles()
	if err != nil {
		renderPage(w, "adjust.html", map[string]any{"HasCycles": false})
		return
	}

	today := task.Today()
	oneWeekOut := today.AddDate(0, 0, 7)

	var active, upcoming []adjustCycleRow

	for _, c := range cycles {
		sow, err := time.Parse(task.DateFormat, c.SowDate)
		if err != nil {
			continue
		}
		harv, err := time.Parse(task.DateFormat, c.HarvestDate)
		if err != nil {
			continue
		}
		mtl, _ := time.Parse(task.DateFormat, c.MoveToLightDate)

		if !today.Before(sow) && !today.After(harv) {
			// Active cycle — currently growing.
			dayNum := int(today.Sub(sow).Hours()/24) + 1
			stage := "dark"
			if today.Equal(harv) {
				stage = "harvest day"
			} else if !today.Before(mtl) {
				stage = "light"
			}
			active = append(active, adjustCycleRow{
				CycleID:        c.CycleID,
				CropName:       task.Capitalize(c.CropName),
				Trays:          c.Trays,
				SowDisplay:     sow.Format("Mon Jan 02"),
				HarvestDisplay: harv.Format("Mon Jan 02"),
				DayNum:         dayNum,
				Stage:          stage,
			})
		} else if sow.After(today) && !sow.After(oneWeekOut) {
			// Upcoming cycle — starts within 7 days.
			daysUntil := int(sow.Sub(today).Hours() / 24)
			upcoming = append(upcoming, adjustCycleRow{
				CycleID:        c.CycleID,
				CropName:       task.Capitalize(c.CropName),
				Trays:          c.Trays,
				SowDisplay:     sow.Format("Mon Jan 02"),
				HarvestDisplay: harv.Format("Mon Jan 02"),
				DaysUntil:      daysUntil,
			})
		}
	}

	renderPage(w, "adjust.html", map[string]any{
		"ActiveCycles":   active,
		"UpcomingCycles": upcoming,
		"HasCycles":      len(active) > 0 || len(upcoming) > 0,
	})
}

// handleAdjustCyclePage renders GET /adjust/cycle?id=XXX — the per-cycle
// adjustment page. The grower clicked a specific cycle on the /adjust list
// and landed here. This page shows the cycle's current state and the
// adjustment form.
func handleAdjustCyclePage(w http.ResponseWriter, r *http.Request) {
	cycleID := r.URL.Query().Get("id")
	if cycleID == "" {
		http.Redirect(w, r, "/adjust", http.StatusSeeOther)
		return
	}

	cycles, err := farm.LoadCycles()
	if err != nil {
		http.Redirect(w, r, "/adjust", http.StatusSeeOther)
		return
	}

	// Find the requested cycle by its unique ID.
	var chosen *farm.Cycle
	for i := range cycles {
		if cycles[i].CycleID == cycleID {
			chosen = &cycles[i]
			break
		}
	}
	if chosen == nil {
		http.Redirect(w, r, "/adjust", http.StatusSeeOther)
		return
	}

	sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
	mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	today := task.Today()

	// Figure out what day of the cycle we are on and what stage.
	dayNum := 0
	stage := "upcoming"
	inBlackout := today.Before(mtl)
	if !today.Before(sow) && !today.After(harv) {
		dayNum = int(today.Sub(sow).Hours()/24) + 1
		stage = "dark"
		if today.Equal(harv) {
			stage = "harvest day"
		} else if !today.Before(mtl) {
			stage = "light"
		}
	}

	renderPage(w, "adjust_cycle.html", map[string]any{
		"CycleID":        cycleID,
		"CropName":       task.Capitalize(chosen.CropName),
		"Trays":          chosen.Trays,
		"SowDisplay":     sow.Format("Mon Jan 02"),
		"HarvestDisplay": harv.Format("Mon Jan 02"),
		"MTLDisplay":     mtl.Format("Mon Jan 02"),
		"DayNum":         dayNum,
		"Stage":          stage,
		"InBlackout":     inBlackout,
	})
}

// handleAdjustPreview handles POST /adjust/preview.
//
// It reads the form values (cycle ID, anchor, operation, parameters), computes
// the new dates, and renders a before/after preview as an htmx fragment. No
// data is saved — this is just a "what would happen" view.
func handleAdjustPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Bad request."})
		return
	}

	cycleID := r.FormValue("cycle_id")
	anchor := r.FormValue("anchor")
	operation := r.FormValue("operation")
	direction := r.FormValue("direction")
	daysStr := r.FormValue("days")
	newTraysStr := r.FormValue("new_trays")
	cascade := r.FormValue("cascade")
	updateCSV := r.FormValue("update_csv")

	if cycleID == "" {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Please select a cycle first."})
		return
	}

	// Load the cycle record.
	cycles, err := farm.LoadCycles()
	if err != nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Could not load cycles."})
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
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Cycle not found — it may have been deleted."})
		return
	}

	sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
	mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	today := task.Today()
	anchorSow := anchor == "sow"

	// Common hidden form fields that get passed to the confirm handler.
	formBase := map[string]any{
		"FormCycleID":   cycleID,
		"FormAnchor":    anchor,
		"FormOperation": operation,
		"FormDirection": direction,
		"FormDays":      daysStr,
		"FormNewTrays":  newTraysStr,
		"FormCascade":   cascade,
		"FormUpdateCSV": updateCSV,
	}

	// ── Cancel preview ─────────────────────────────────────────────────
	if operation == "cancel" {
		header := fmt.Sprintf("Cancel %s %dx (sown %s, harvest %s)",
			task.Capitalize(chosen.CropName), chosen.Trays,
			sow.Format("Mon Jan 02"), harv.Format("Mon Jan 02"))
		data := map[string]any{
			"Header":   header,
			"IsCancel": true,
		}
		for k, v := range formBase {
			data[k] = v
		}
		renderFragment(w, "adjust_preview.html", data)
		return
	}

	// ── Retray preview ─────────────────────────────────────────────────
	if operation == "retray" {
		newTrays, convErr := strconv.Atoi(newTraysStr)
		if convErr != nil || newTrays < 1 {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": "Please enter a tray count of 1 or more.",
			})
			return
		}
		header := fmt.Sprintf("%s — change tray count %d → %d",
			task.Capitalize(chosen.CropName), chosen.Trays, newTrays)
		data := map[string]any{
			"Header":   header,
			"IsRetray": true,
		}
		for k, v := range formBase {
			data[k] = v
		}
		renderFragment(w, "adjust_preview.html", data)
		return
	}

	// ── Blackout / light preview ───────────────────────────────────────

	// Parse the day count.
	days, convErr := strconv.Atoi(daysStr)
	if convErr != nil || days < 1 {
		renderFragment(w, "adjust_preview.html", map[string]any{
			"Error": "Please enter a positive number of days.",
		})
		return
	}

	// n is the signed shift: positive = add, negative = remove.
	n := days
	if direction == "remove" {
		n = -n
	}

	// Compute the new key dates using the same arithmetic as the CLI.
	var newSow, newMTL, newHarv time.Time
	inBlackout := today.Before(mtl)

	stage := operation // "blackout" or "light"

	// Check if the crop is already past the blackout stage before computing
	// new dates — gives a clearer error message than date validation alone.
	if stage == "blackout" && !inBlackout {
		renderFragment(w, "adjust_preview.html", map[string]any{
			"Error": "This crop has already moved to light — the blackout stage is done. Adjust light days instead.",
		})
		return
	}

	switch {
	case stage == "blackout" && anchorSow:
		newSow = sow
		newMTL = mtl.AddDate(0, 0, n)
		newHarv = harv.AddDate(0, 0, n)
		if !newMTL.After(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: move-to-light would land on %s (today or earlier).",
					newMTL.Format("Mon Jan 02")),
			})
			return
		}
		if newHarv.Before(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: harvest would land on %s (already passed).",
					newHarv.Format("Mon Jan 02")),
			})
			return
		}

	case stage == "blackout" && !anchorSow:
		newSow = sow.AddDate(0, 0, -n)
		newMTL = mtl
		newHarv = harv
		if n < 0 && !sow.After(today) && newSow.After(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": "Cannot do that: sow date would move to the future. You cannot un-sow a crop that is already growing.",
			})
			return
		}

	case stage == "light" && anchorSow:
		newSow = sow
		newMTL = mtl
		newHarv = harv.AddDate(0, 0, n)
		if newHarv.Before(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: harvest would land on %s (already passed).",
					newHarv.Format("Mon Jan 02")),
			})
			return
		}

	case stage == "light" && !anchorSow:
		shiftN := -n
		newSow = sow.AddDate(0, 0, shiftN)
		newMTL = mtl.AddDate(0, 0, shiftN)
		newHarv = harv
		if inBlackout && !newMTL.After(today) {
			renderFragment(w, "adjust_preview.html", map[string]any{
				"Error": fmt.Sprintf("Cannot do that: move-to-light would land on %s (today or earlier).",
					newMTL.Format("Mon Jan 02")),
			})
			return
		}
	}

	// Build the before/after side-by-side rows.
	before := buildGUICycleView(sow, mtl, harv)
	after := buildGUICycleView(newSow, newMTL, newHarv)
	rows := buildPreviewRows(before, after, today)

	// Build a human-readable header.
	dirWord := "add"
	if n < 0 {
		dirWord = "remove"
	}
	absN := n
	if absN < 0 {
		absN = -absN
	}
	dayWord := "day"
	if absN != 1 {
		dayWord = "days"
	}
	anchorLabel := "harvest"
	if anchorSow {
		anchorLabel = "sow"
	}
	header := fmt.Sprintf("%s %dx — %s %d %s %s (anchor: %s)",
		task.Capitalize(chosen.CropName), chosen.Trays,
		dirWord, absN, stage, dayWord, anchorLabel)

	// Run conflict check with the new dates.
	var conflicts []string
	farmEnvs, farmErr := farm.LoadConfig()
	if farmErr == nil {
		// Build a temporary cycle list with the adjusted cycle.
		tempCycles := make([]farm.Cycle, len(cycles))
		copy(tempCycles, cycles)
		for i := range tempCycles {
			if tempCycles[i].CycleID == cycleID {
				tempCycles[i].SowDate = newSow.Format(task.DateFormat)
				tempCycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				tempCycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		conflicts = checker.Check(farmEnvs, tempCycles)
	}

	data := map[string]any{
		"Header":       header,
		"Rows":         rows,
		"Conflicts":    conflicts,
		"HasConflicts": len(conflicts) > 0,
	}
	for k, v := range formBase {
		data[k] = v
	}
	renderFragment(w, "adjust_preview.html", data)
}

// handleAdjustConfirm handles POST /adjust/confirm.
//
// It reads the hidden form fields from the preview, replays the adjustment
// (same math — the scheduler is deterministic), saves to disk, and optionally
// cascades to other cycles and/or updates crops.csv.
func handleAdjustConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "adjust_preview.html", map[string]any{"Error": "Bad request."})
		return
	}

	cycleID := r.FormValue("cycle_id")
	anchor := r.FormValue("anchor")
	operation := r.FormValue("operation")
	direction := r.FormValue("direction")
	daysStr := r.FormValue("days")
	newTraysStr := r.FormValue("new_trays")
	doCascade := r.FormValue("cascade") == "yes"
	doUpdateCSV := r.FormValue("update_csv") == "yes"

	// Load current data from disk.
	cycles, err := farm.LoadCycles()
	if err != nil {
		renderFragment(w, "adjust_success.html", map[string]any{"Message": "Error loading cycles: " + err.Error()})
		return
	}
	tasks, err := store.Load()
	if err != nil {
		renderFragment(w, "adjust_success.html", map[string]any{"Message": "Error loading tasks: " + err.Error()})
		return
	}
	farmEnvs, _ := farm.LoadConfig()

	// Find the cycle.
	var chosen *farm.Cycle
	for i := range cycles {
		if cycles[i].CycleID == cycleID {
			chosen = &cycles[i]
			break
		}
	}
	if chosen == nil {
		renderFragment(w, "adjust_success.html", map[string]any{"Message": "Cycle not found — it may have been deleted."})
		return
	}

	sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
	mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	today := task.Today()
	anchorSow := anchor == "sow"

	var message string

	// ── Cancel ─────────────────────────────────────────────────────────
	if operation == "cancel" {
		todayStr := today.Format(task.DateFormat)
		var kept []task.Task
		removed := 0
		for _, t := range tasks {
			if t.CycleID == cycleID && t.Date > todayStr {
				removed++
			} else {
				kept = append(kept, t)
			}
		}
		tasks = kept

		// Remove the cycle record.
		var keptCycles []farm.Cycle
		for _, c := range cycles {
			if c.CycleID != cycleID {
				keptCycles = append(keptCycles, c)
			}
		}

		_ = store.Save(tasks)
		_ = farm.SaveCycles(keptCycles)

		message = fmt.Sprintf("%s %dx cycle cancelled. %d future task(s) deleted.",
			task.Capitalize(chosen.CropName), chosen.Trays, removed)
		renderFragment(w, "adjust_success.html", map[string]any{
			"Message": message,
		})
		return
	}

	// ── Retray ─────────────────────────────────────────────────────────
	if operation == "retray" {
		newTrays, _ := strconv.Atoi(newTraysStr)
		if newTrays < 1 {
			newTrays = 1
		}

		// Load the crop definition to regenerate tasks.
		cropDef, found := guiLoadCropByName(chosen.CropName)
		if !found {
			renderFragment(w, "adjust_success.html", map[string]any{
				"Message": fmt.Sprintf("Crop %q not found in crops.csv.", chosen.CropName),
			})
			return
		}

		fromDayNum := int(today.Sub(sow).Hours()/24) + 2
		todayStr := today.Format(task.DateFormat)

		// Remove future tasks.
		var kept []task.Task
		for _, t := range tasks {
			if t.CycleID != cycleID || t.Date <= todayStr {
				kept = append(kept, t)
			}
		}
		tasks = kept

		// Regenerate from tomorrow with new tray count.
		if fromDayNum <= cropDef.CycleDays {
			newTasks, schedErr := scheduler.ScheduleFromDay(cropDef, chosen.SowDate, fromDayNum, newTrays, cycleID)
			if schedErr == nil {
				tasks = append(tasks, newTasks...)
			}
		}

		// Update cycle record.
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].Trays = newTrays
				cycles[i].ExpectedGrams = cropDef.YieldGrams * newTrays
				break
			}
		}

		_ = store.Save(tasks)
		_ = farm.SaveCycles(cycles)

		warnings := checker.Check(farmEnvs, cycles)
		message = fmt.Sprintf("%s updated: %d → %d trays.",
			task.Capitalize(chosen.CropName), chosen.Trays, newTrays)
		renderFragment(w, "adjust_success.html", map[string]any{
			"Message":      message,
			"Conflicts":    warnings,
			"HasConflicts": len(warnings) > 0,
		})
		return
	}

	// ── Blackout / light adjustment ────────────────────────────────────

	days, _ := strconv.Atoi(daysStr)
	n := days
	if direction == "remove" {
		n = -n
	}

	stage := operation // "blackout" or "light"
	inBlackout := today.Before(mtl)

	var newSow, newMTL, newHarv time.Time

	switch {
	case stage == "blackout" && anchorSow:
		newSow = sow
		newMTL = mtl.AddDate(0, 0, n)
		newHarv = harv.AddDate(0, 0, n)
	case stage == "blackout" && !anchorSow:
		newSow = sow.AddDate(0, 0, -n)
		newMTL = mtl
		newHarv = harv
	case stage == "light" && anchorSow:
		newSow = sow
		newMTL = mtl
		newHarv = harv.AddDate(0, 0, n)
	case stage == "light" && !anchorSow:
		shiftN := -n
		newSow = sow.AddDate(0, 0, shiftN)
		newMTL = mtl.AddDate(0, 0, shiftN)
		newHarv = harv
	}

	// Apply the adjustment — same logic as cmd_adjust.go.
	switch {
	case stage == "blackout" && anchorSow:
		// Re-generate future tasks using a phantom sow date shifted by n.
		cropDef, found := guiLoadCropByName(chosen.CropName)
		if !found {
			renderFragment(w, "adjust_success.html", map[string]any{
				"Message": fmt.Sprintf("Crop %q not found in crops.csv.", chosen.CropName),
			})
			return
		}
		fromDayNum := int(today.Sub(sow).Hours()/24) + 2
		tasks = guiRemoveFutureTasks(tasks, cycleID, today)
		if fromDayNum <= cropDef.CycleDays {
			shiftedSow := sow.AddDate(0, 0, n)
			newTasks, schedErr := scheduler.ScheduleFromDay(cropDef, shiftedSow.Format(task.DateFormat), fromDayNum, chosen.Trays, cycleID)
			if schedErr == nil {
				newTasks = guiTagAdjusted(newTasks)
				tasks = append(tasks, newTasks...)
			}
		}
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				cycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — move to light: %s → %s  ·  harvest: %s → %s",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"),
			harv.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))

	case stage == "blackout" && !anchorSow:
		// Metadata only — update sow date.
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].SowDate = newSow.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — sow date: %s → %s (no task dates changed)",
			sow.Format("Mon Jan 02"), newSow.Format("Mon Jan 02"))

	case stage == "light" && anchorSow:
		// Date-shift existing light-stage future tasks only.
		mtlStr := mtl.Format(task.DateFormat)
		todayStr := today.Format(task.DateFormat)
		for i := range tasks {
			t := &tasks[i]
			if t.CycleID != cycleID || t.Date <= todayStr || t.Date < mtlStr {
				continue
			}
			d, parseErr := time.Parse(task.DateFormat, t.Date)
			if parseErr != nil {
				continue
			}
			newDate := d.AddDate(0, 0, n).Format(task.DateFormat)
			if newDate != t.Date {
				t.Date = newDate
				if !strings.Contains(t.Notes, "adjusted - be mindful") {
					t.Notes += "\nadjusted - be mindful"
				}
			}
		}
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — harvest: %s → %s",
			harv.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))

	case stage == "light" && !anchorSow && inBlackout:
		// Re-generate future tasks with shifted phantom sow.
		cropDef, found := guiLoadCropByName(chosen.CropName)
		if !found {
			renderFragment(w, "adjust_success.html", map[string]any{
				"Message": fmt.Sprintf("Crop %q not found in crops.csv.", chosen.CropName),
			})
			return
		}
		shiftN := -n
		shiftedSow := sow.AddDate(0, 0, shiftN)
		fromDayNum := int(today.Sub(sow).Hours()/24) + 2
		tasks = guiRemoveFutureTasks(tasks, cycleID, today)
		if fromDayNum <= cropDef.CycleDays {
			newTasks, schedErr := scheduler.ScheduleFromDay(cropDef, shiftedSow.Format(task.DateFormat), fromDayNum, chosen.Trays, cycleID)
			if schedErr == nil {
				newTasks = guiTagAdjusted(newTasks)
				tasks = append(tasks, newTasks...)
			}
		}
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].SowDate = newSow.Format(task.DateFormat)
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — move to light: %s → %s (tasks regenerated)",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"))

	case stage == "light" && !anchorSow && !inBlackout:
		// Metadata only — correct the recorded move-to-light date.
		for i := range cycles {
			if cycles[i].CycleID == cycleID {
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				break
			}
		}
		message = fmt.Sprintf("Updated — move-to-light corrected: %s → %s (no task dates changed)",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"))
	}

	// Save the primary adjustment.
	_ = store.Save(tasks)
	_ = farm.SaveCycles(cycles)

	// ── Cascade to other cycles of the same crop ───────────────────────
	// ModifyCropDays and the cascade function use "dark" (not "blackout")
	// because that is the stage name used in crops.csv and the CLI.
	csvStage := stage
	if csvStage == "blackout" {
		csvStage = "dark"
	}

	cascaded := 0
	if doCascade && (stage == "blackout" || stage == "light") {
		cascaded = guiCascade(chosen, stage, n, anchorSow, today, farmEnvs)
	}

	// ── Update crops.csv template ──────────────────────────────────────
	csvUpdated := false
	if doUpdateCSV && (stage == "blackout" || stage == "light") {
		csvPath, pathErr := crop.CropsFilePath()
		if pathErr == nil {
			modErr := crop.ModifyCropDays(csvPath, chosen.CropName, csvStage, n)
			if modErr == nil {
				csvUpdated = true
				// Mirror the change to Google Sheets (if linked).
				gcal.SyncLocalToSheet(context.Background())
			}
		}
	}

	// Re-run conflict checker with updated data.
	cycles, _ = farm.LoadCycles()
	warnings := checker.Check(farmEnvs, cycles)

	renderFragment(w, "adjust_success.html", map[string]any{
		"Message":      message,
		"Cascaded":     cascaded,
		"CSVUpdated":   csvUpdated,
		"Conflicts":    warnings,
		"HasConflicts": len(warnings) > 0,
	})
}

// ─── Adjust helper functions ─────────────────────────────────────────────────
//
// GUI-specific versions of the adjustment helpers. Prefixed with "gui" to
// distinguish them from the CLI equivalents in cmd_adjust.go.

// buildGUICycleView returns a day-by-day timeline from sow to harvest.
// Each entry pairs a date with a label describing what that day is.
func buildGUICycleView(sow, mtl, harvest time.Time) []adjustPreviewRow {
	var result []adjustPreviewRow
	for d := sow; !d.After(harvest); d = d.AddDate(0, 0, 1) {
		dayNum := int(d.Sub(sow).Hours()/24) + 1
		var label string
		switch {
		case d.Equal(harvest):
			label = "harvest"
		case d.Equal(mtl):
			label = "move to light"
		case d.Equal(sow):
			label = fmt.Sprintf("Day %d (sow)", dayNum)
		case d.Before(mtl):
			label = fmt.Sprintf("Day %d (dark)", dayNum)
		default:
			label = fmt.Sprintf("Day %d (light)", dayNum)
		}
		result = append(result, adjustPreviewRow{
			Date:        d,
			DateDisplay: d.Format("Mon Jan 02"),
			BeforeLabel: label,
		})
	}
	return result
}

// buildPreviewRows merges the "before" and "after" cycle views into a single
// list of rows, aligned by calendar date. Dates that exist in one view but
// not the other get an empty label (rendered as "---" in the template).
//
// Uses the real time.Time stored in each row's .Date field — no string
// parsing needed, which avoids the year-guessing bug that plagued the old
// version.
func buildPreviewRows(before, after []adjustPreviewRow, today time.Time) []adjustPreviewRow {
	// Build lookup maps keyed by "2006-01-02" so we can merge the two
	// timelines into one table.
	beforeMap := make(map[string]string)
	var startDate, endDate time.Time
	for i, r := range before {
		key := r.Date.Format(task.DateFormat)
		beforeMap[key] = r.BeforeLabel
		if i == 0 {
			startDate = r.Date
			endDate = r.Date
		}
		if r.Date.Before(startDate) {
			startDate = r.Date
		}
		if r.Date.After(endDate) {
			endDate = r.Date
		}
	}

	afterMap := make(map[string]string)
	for _, r := range after {
		key := r.Date.Format(task.DateFormat)
		// The "after" rows have the label in BeforeLabel from buildGUICycleView.
		afterMap[key] = r.BeforeLabel
		if r.Date.Before(startDate) {
			startDate = r.Date
		}
		if r.Date.After(endDate) {
			endDate = r.Date
		}
	}

	// Walk day-by-day from the earliest date to the latest, looking up
	// labels in each map.
	var rows []adjustPreviewRow
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		key := d.Format(task.DateFormat)
		rows = append(rows, adjustPreviewRow{
			Date:        d,
			DateDisplay: d.Format("Mon Jan 02"),
			BeforeLabel: beforeMap[key],
			AfterLabel:  afterMap[key],
			IsToday:     d.Equal(today),
		})
	}
	return rows
}

// guiLoadCropByName looks up a crop variety by name in crops.csv.
func guiLoadCropByName(name string) (crop.Crop, bool) {
	source, err := crop.GetSource()
	if err != nil {
		return crop.Crop{}, false
	}
	crops, err := source.LoadCrops()
	if err != nil {
		return crop.Crop{}, false
	}
	for _, c := range crops {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return crop.Crop{}, false
}

// guiRemoveFutureTasks returns the task list with all future tasks for the
// given cycleID removed (date > today).
func guiRemoveFutureTasks(tasks []task.Task, cycleID string, today time.Time) []task.Task {
	todayStr := today.Format(task.DateFormat)
	var kept []task.Task
	for _, t := range tasks {
		if t.CycleID != cycleID || t.Date <= todayStr {
			kept = append(kept, t)
		}
	}
	return kept
}

// guiTagAdjusted appends "adjusted - be mindful" to every task in the slice.
func guiTagAdjusted(tasks []task.Task) []task.Task {
	for i := range tasks {
		if !strings.Contains(tasks[i].Notes, "adjusted - be mindful") {
			tasks[i].Notes += "\nadjusted - be mindful"
		}
	}
	return tasks
}

// guiCascade applies the same adjustment to all other active/upcoming cycles
// of the same crop. Returns how many cycles were updated.
func guiCascade(chosenCycle *farm.Cycle, stage string, n int, anchorSow bool,
	today time.Time, envs []farm.Environment) int {

	// Reload fresh data — the primary adjustment was already saved.
	cycles, err := farm.LoadCycles()
	if err != nil {
		return 0
	}
	tasks, err := store.Load()
	if err != nil {
		return 0
	}

	// Find target cycles — same crop, not yet harvested, not the one just adjusted.
	var targets []farm.Cycle
	for _, c := range cycles {
		if c.CycleID == chosenCycle.CycleID {
			continue
		}
		if !strings.EqualFold(c.CropName, chosenCycle.CropName) {
			continue
		}
		harv, err := time.Parse(task.DateFormat, c.HarvestDate)
		if err != nil {
			continue
		}
		if harv.After(today) {
			targets = append(targets, c)
		}
	}

	if len(targets) == 0 {
		return 0
	}

	updated := 0
	todayStr := today.Format(task.DateFormat)

	for _, c := range targets {
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mtl, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		sowStr := sow.Format(task.DateFormat)
		mtlStr := mtl.Format(task.DateFormat)
		harvStr := harv.Format(task.DateFormat)

		// Compute new dates — same four-case table as cmd_adjust.go.
		var newSow, newMTL, newHarv time.Time
		switch {
		case anchorSow && stage == "blackout":
			newSow = sow
			newMTL = mtl.AddDate(0, 0, n)
			newHarv = harv.AddDate(0, 0, n)
		case anchorSow && stage == "light":
			newSow = sow
			newMTL = mtl
			newHarv = harv.AddDate(0, 0, n)
		case !anchorSow && stage == "blackout":
			newSow = sow.AddDate(0, 0, -n)
			newMTL = mtl
			newHarv = harv
		case !anchorSow && stage == "light":
			newSow = sow.AddDate(0, 0, -n)
			newMTL = mtl.AddDate(0, 0, -n)
			newHarv = harv
		}

		// Skip if harvest would land in the past.
		if !newHarv.After(today) {
			continue
		}

		// Shift relevant future tasks.
		for i := range tasks {
			t := &tasks[i]
			if t.CycleID != c.CycleID || t.Date <= todayStr {
				continue
			}

			var shouldShift bool
			shift := n

			switch {
			case anchorSow && stage == "blackout":
				shouldShift = t.Date > sowStr
				shift = n
			case anchorSow && stage == "light":
				shouldShift = t.Date > mtlStr
				shift = n
			case !anchorSow && stage == "blackout":
				shouldShift = t.Date < mtlStr
				shift = -n
			case !anchorSow && stage == "light":
				shouldShift = t.Date < harvStr
				shift = -n
			}

			if !shouldShift {
				continue
			}

			d, err := time.Parse(task.DateFormat, t.Date)
			if err != nil {
				continue
			}
			newDate := d.AddDate(0, 0, shift)
			if newDate.Before(today) {
				continue
			}
			t.Date = newDate.Format(task.DateFormat)
			if !strings.Contains(t.Notes, "adjusted - be mindful") {
				t.Notes += "\nadjusted - be mindful"
			}
		}

		// Update cycle record.
		for i := range cycles {
			if cycles[i].CycleID == c.CycleID {
				cycles[i].SowDate = newSow.Format(task.DateFormat)
				cycles[i].MoveToLightDate = newMTL.Format(task.DateFormat)
				cycles[i].HarvestDate = newHarv.Format(task.DateFormat)
				break
			}
		}
		updated++
	}

	// Save all cascaded changes.
	_ = store.Save(tasks)
	_ = farm.SaveCycles(cycles)

	return updated
}
