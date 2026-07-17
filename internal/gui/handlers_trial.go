// handlers_trial.go — Trial management handlers.
//
// These handle all the /trial pages: starting a new trial, managing daily
// observations, confirming days, viewing trial details, comparing trials,
// and promoting or discarding trials. Trials let the grower test new crop
// varieties with temporary parameters before committing to production.
package gui

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/trial"
)

// ─────────────────────────────────────────────────────────────────────────────
// Trial
// ─────────────────────────────────────────────────────────────────────────────

// trialCard is the template-friendly version of an active trial for the trial
// dashboard. It pre-computes display strings so the HTML template stays simple.
type trialCard struct {
	ID             string // unique trial ID
	DisplayName    string // e.g. "Mustard (seed lot xyz)"
	Trays          int
	TrayWord       string // "tray" or "trays"
	DayNum         int    // what cycle day the trial is on today
	SowDateFmt     string // human-readable sow date, e.g. "Mon Mar 09"
	HarvestDateFmt string // tentative harvest date, or "" if unknown
	Status         string // "active", "harvested", etc.
}

// trialListRow is the template-friendly version of any trial (any status) for
// the "all trials" table at the bottom of the trial page.
type trialListRow struct {
	ID          string
	DisplayName string
	Status      string
	SowDateFmt  string
	YieldGrams  int // actual yield (0 if not recorded)
	HasYield    bool
	FailureNote string
	CropName    string // lowercase, used for compare grouping
}

// handleTrialPage renders the trial dashboard at GET /trial.
//
// It shows three sections:
//  1. Active trials — cards with "Manage" buttons
//  2. "Start New Trial" form (always visible)
//  3. All trials table — every trial ever run, with "View" links
//
// This is the GUI version of the "greenies trial" command's main menu.
func handleTrialPage(w http.ResponseWriter, r *http.Request) {
	trials, err := trial.LoadTrials()
	if err != nil {
		trials = []trial.TrialRecord{}
	}

	today := task.Today()

	// Build active trial cards.
	var activeCards []trialCard
	for _, tr := range trials {
		if tr.Status != trial.StatusActive {
			continue
		}
		trayWord := task.TrayWord(tr.Trays)
		sow, _ := time.Parse(task.DateFormat, tr.SowDate)
		harvestFmt := ""
		if hd := tr.TentativeHarvestDate(); hd != "" {
			if ht, err := time.Parse(task.DateFormat, hd); err == nil {
				harvestFmt = ht.Format("Mon Jan 02")
			}
		}
		activeCards = append(activeCards, trialCard{
			ID:             tr.ID,
			DisplayName:    tr.DisplayName(),
			Trays:          tr.Trays,
			TrayWord:       trayWord,
			DayNum:         tr.DayNumber(today),
			SowDateFmt:     sow.Format("Mon Jan 02"),
			HarvestDateFmt: harvestFmt,
			Status:         tr.Status,
		})
	}

	// Build the "all trials" table rows.
	var allRows []trialListRow
	for _, tr := range trials {
		sow, _ := time.Parse(task.DateFormat, tr.SowDate)
		allRows = append(allRows, trialListRow{
			ID:          tr.ID,
			DisplayName: tr.DisplayName(),
			Status:      tr.Status,
			SowDateFmt:  sow.Format("Jan 02 2006"),
			YieldGrams:  tr.ActualYieldGrams,
			HasYield:    tr.ActualYieldGrams > 0,
			FailureNote: tr.FailureNote,
			CropName:    strings.ToLower(tr.CropName),
		})
	}

	// Check if comparison is possible (2+ past trials of the same crop).
	canCompare := false
	pastByCrop := map[string]int{}
	for _, tr := range trials {
		if tr.Status != trial.StatusActive {
			pastByCrop[strings.ToLower(tr.CropName)]++
		}
	}
	for _, count := range pastByCrop {
		if count >= 2 {
			canCompare = true
			break
		}
	}

	renderPage(w, "trial.html", map[string]any{
		"ActiveTrials": activeCards,
		"HasActive":    len(activeCards) > 0,
		"AllTrials":    allRows,
		"HasTrials":    len(allRows) > 0,
		"CanCompare":   canCompare,
		"Today":        today.Format(task.DateFormat),
	})
}

// handleTrialNew handles POST /trial/new — creates a new trial.
//
// This is the GUI version of startNewTrial() in cmd_trial.go. All the fields
// are submitted at once from a single form, instead of being asked one by one.
func handleTrialNew(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	// Read form values.
	cropName := strings.ToLower(strings.TrimSpace(r.FormValue("crop_name")))
	trialVar := strings.TrimSpace(r.FormValue("trial_variable"))
	sowDateStr := r.FormValue("sow_date")
	traysStr := r.FormValue("trays")
	soakType := r.FormValue("soak_type") // "none", "hours", "overnight"
	soakHoursStr := r.FormValue("soak_hours")
	seedGramsStr := r.FormValue("seed_grams")
	mediumLitresStr := r.FormValue("medium_litres")
	mtlDayStr := r.FormValue("mtl_day")
	harvestDayStr := r.FormValue("harvest_day")

	// Validate required fields.
	if cropName == "" {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Please enter a crop name.",
		})
		return
	}
	if sowDateStr == "" {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Please pick a sow date.",
		})
		return
	}
	sowTime, err := time.Parse(task.DateFormat, sowDateStr)
	if err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Invalid date format.",
		})
		return
	}
	trays, err := strconv.Atoi(traysStr)
	if err != nil || trays < 1 {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Please enter a tray count of 1 or more.",
		})
		return
	}

	// Parse optional parameters.
	var overnightSoak bool
	var soakHours float64
	if soakType == "overnight" {
		overnightSoak = true
	} else if soakType == "hours" {
		if h, err := strconv.ParseFloat(soakHoursStr, 64); err == nil && h > 0 {
			soakHours = h
		}
	}

	var seedGrams float64
	if seedGramsStr != "" {
		if g, err := strconv.ParseFloat(seedGramsStr, 64); err == nil && g > 0 {
			seedGrams = g
		}
	}

	var mediumLitres float64
	if mediumLitresStr == "" {
		mediumLitres = 1
	} else if d, err := strconv.ParseFloat(mediumLitresStr, 64); err == nil && d > 0 {
		mediumLitres = d
	} else {
		mediumLitres = 1
	}

	var moveToLightDay int
	if mtlDayStr != "" {
		if d, err := strconv.Atoi(mtlDayStr); err == nil && d > 0 {
			moveToLightDay = d
		}
	}

	var harvestDay int
	if harvestDayStr != "" {
		if d, err := strconv.Atoi(harvestDayStr); err == nil && d > 0 {
			harvestDay = d
		}
	}

	// Generate ID and build the record.
	trialID, err := task.GenerateID()
	if err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Could not generate trial ID: " + err.Error(),
		})
		return
	}

	lastManaged := sowTime.AddDate(0, 0, -1).Format(task.DateFormat)

	tr := trial.TrialRecord{
		ID:             trialID,
		CropName:       cropName,
		TrialVariable:  trialVar,
		SowDate:        sowDateStr,
		Trays:          trays,
		Status:         trial.StatusActive,
		LastManaged:    lastManaged,
		OvernightSoak:  overnightSoak,
		SoakHours:      soakHours,
		SeedGrams:      seedGrams,
		MediumLitres:   mediumLitres,
		MoveToLightDay: moveToLightDay,
		HarvestDay:     harvestDay,
	}

	// Create tentative calendar tasks (same logic as the CLI).
	if moveToLightDay > 0 {
		mtlDateStr := sowTime.AddDate(0, 0, moveToLightDay-1).Format(task.DateFormat)
		id, taskErr := trial.CreateTentativeTask(tr.DisplayName(), "move to light", mtlDateStr)
		if taskErr == nil {
			tr.TentativeMTLTaskID = id
		}
	}
	if harvestDay > 0 {
		harvDateStr := sowTime.AddDate(0, 0, harvestDay-1).Format(task.DateFormat)
		id, taskErr := trial.CreateTentativeTask(tr.DisplayName(), "harvest", harvDateStr)
		if taskErr == nil {
			tr.TentativeHarvestTaskID = id
		}
	}

	// Save.
	trials, loadErr := trial.LoadTrials()
	if loadErr != nil {
		trials = []trial.TrialRecord{}
	}
	updated := trial.ReplaceByID(trials, tr)
	if err := trial.SaveTrials(updated); err != nil {
		renderFragment(w, "trial_result.html", map[string]any{
			"Error": "Could not save trial: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_result.html", map[string]any{
		"Success":     true,
		"DisplayName": tr.DisplayName(),
		"Trays":       trays,
		"SowDateFmt":  sowTime.Format("Mon Jan 02"),
		"TrialID":     tr.ID,
	})
}

// manageDayRow holds the data for one missed day in the manage form.
// The template renders one row per missed day, each with optional fields.
type manageDayRow struct {
	DayNum  int    // cycle day number
	DateStr string // YYYY-MM-DD (used as form field name suffix)
	DateFmt string // human-readable date, e.g. "Mon Mar 09"
}

// handleTrialManage renders the manage page for a specific trial at
// GET /trial/manage?id=xxx.
//
// It shows:
//   - The trial header (name, trays, sow date)
//   - All missed days since the last manage session, each with fields for
//     observation notes and optional confirmed parameters
//   - An "all caught up" message if there are no missed days
//   - Outcome buttons (continue / harvest / failure) at the bottom
func handleTrialManage(w http.ResponseWriter, r *http.Request) {
	trialID := r.URL.Query().Get("id")
	if trialID == "" {
		http.Error(w, "Missing trial ID", http.StatusBadRequest)
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		http.Error(w, "Could not load trials", http.StatusInternalServerError)
		return
	}

	// Find the trial.
	tr := trial.FindByID(trials, trialID)
	if tr == nil {
		http.Error(w, "Trial not found", http.StatusNotFound)
		return
	}

	// Calculate missed days.
	today := task.Today()
	sow, _ := time.Parse(task.DateFormat, tr.SowDate)
	lastManaged, err := time.Parse(task.DateFormat, tr.LastManaged)
	if err != nil {
		lastManaged = sow.AddDate(0, 0, -1)
	}

	catchupStart := lastManaged.AddDate(0, 0, 1)

	var missedDays []manageDayRow
	for d := catchupStart; !d.After(today); d = d.AddDate(0, 0, 1) {
		dayNum := tr.DayNumber(d)
		if dayNum < 1 {
			continue
		}
		missedDays = append(missedDays, manageDayRow{
			DayNum:  dayNum,
			DateStr: d.Format(task.DateFormat),
			DateFmt: d.Format("Mon Jan 02"),
		})
	}

	renderPage(w, "trial_manage.html", map[string]any{
		"TrialID":     tr.ID,
		"DisplayName": tr.DisplayName(),
		"Trays":       tr.Trays,
		"SowDateFmt":  sow.Format("Mon Jan 02"),
		"DayNum":      tr.DayNumber(today),
		"MissedDays":  missedDays,
		"HasMissed":   len(missedDays) > 0,
		"IsActive":    tr.Status == trial.StatusActive,
	})
}

// handleTrialManageAction handles POST /trial/manage — processes the manage
// form submission.
//
// For each missed day, it reads:
//   - obs_YYYY-MM-DD: observation notes (free text)
//   - confirm_YYYY-MM-DD: "on" if the grower checked the "confirm tasks" box
//   - stage_YYYY-MM-DD: stage (sow/dark/light/harvest) — only if confirmed
//   - tasks_YYYY-MM-DD: tasks comma list — only if confirmed
//
// After saving, it returns a success fragment.
func handleTrialManageAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	if trialID == "" {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Missing trial ID.",
		})
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Could not load trials: " + err.Error(),
		})
		return
	}

	tr := trial.FindByID(trials, trialID)
	if tr == nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Trial not found.",
		})
		return
	}

	// Parse the day-by-day form data. Day dates come as hidden fields
	// named "day_dates" (one per missed day).
	dayDates := r.Form["day_dates"]
	observationsAdded := 0
	daysConfirmed := 0

	for _, dateStr := range dayDates {
		d, err := time.Parse(task.DateFormat, dateStr)
		if err != nil {
			continue
		}
		dayNum := tr.DayNumber(d)
		if dayNum < 1 {
			continue
		}

		// Observation notes.
		obsKey := "obs_" + dateStr
		notes := strings.TrimSpace(r.FormValue(obsKey))
		if notes != "" {
			tr.Observations = append(tr.Observations, trial.TrialObservation{
				Day:   dayNum,
				Date:  dateStr,
				Notes: notes,
			})
			observationsAdded++
		}

		// Confirmed parameters.
		confirmKey := "confirm_" + dateStr
		if r.FormValue(confirmKey) == "on" {
			stageKey := "stage_" + dateStr
			tasksKey := "tasks_" + dateStr
			stage := strings.ToLower(strings.TrimSpace(r.FormValue(stageKey)))
			tasks := strings.TrimSpace(r.FormValue(tasksKey))

			if stage != "" {
				// Replace if this day was already confirmed.
				replaced := false
				for i, cd := range tr.ConfirmedDays {
					if cd.Day == dayNum {
						tr.ConfirmedDays[i] = trial.TrialDayParams{
							Day:   dayNum,
							Stage: stage,
							Tasks: tasks,
						}
						replaced = true
						break
					}
				}
				if !replaced {
					tr.ConfirmedDays = append(tr.ConfirmedDays, trial.TrialDayParams{
						Day:   dayNum,
						Stage: stage,
						Tasks: tasks,
					})
				}
				daysConfirmed++
			}
		}
	}

	// Update last-managed date to today.
	tr.LastManaged = task.Today().Format(task.DateFormat)

	// Refresh tentative calendar tasks.
	_ = trial.RefreshTentativeTasks(tr, task.Today())

	// Save.
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		renderFragment(w, "trial_manage_result.html", map[string]any{
			"Error": "Could not save: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_manage_result.html", map[string]any{
		"Success":           true,
		"ObservationsAdded": observationsAdded,
		"DaysConfirmed":     daysConfirmed,
		"TrialID":           tr.ID,
		"IsActive":          tr.Status == trial.StatusActive,
	})
}

// handleTrialOutcome handles POST /trial/outcome — marks a trial as harvested,
// failed, or continues it as active.
//
// Form values:
//   - trial_id: the trial to update
//   - outcome: "harvest", "failure", or "continue"
//   - yield_grams: actual yield (harvest only)
//   - failure_note: what went wrong (failure only)
func handleTrialOutcome(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	outcome := r.FormValue("outcome")

	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not load trials.",
		})
		return
	}

	tr := trial.FindByID(trials, trialID)
	if tr == nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Trial not found.",
		})
		return
	}

	switch outcome {
	case "harvest":
		// Record yield if provided.
		if yieldStr := r.FormValue("yield_grams"); yieldStr != "" {
			if g, err := strconv.Atoi(yieldStr); err == nil && g > 0 {
				tr.ActualYieldGrams = g
			}
		}
		tr.Status = trial.StatusHarvested
		_ = trial.RefreshTentativeTasks(tr, task.Today())

		updated := trial.ReplaceByID(trials, *tr)
		if err := trial.SaveTrials(updated); err != nil {
			renderFragment(w, "trial_outcome_result.html", map[string]any{
				"Error": "Could not save: " + err.Error(),
			})
			return
		}
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Success":    true,
			"Message":    fmt.Sprintf("%s marked as harvested.", tr.DisplayName()),
			"CanPromote": len(tr.ConfirmedDays) > 0,
			"TrialID":    tr.ID,
		})

	case "failure":
		tr.FailureNote = strings.TrimSpace(r.FormValue("failure_note"))
		tr.Status = trial.StatusFailed

		// Cancel tentative tasks (mark as "(cancelled)").
		_ = trial.CancelTentativeTasks(tr)

		updated := trial.ReplaceByID(trials, *tr)
		if err := trial.SaveTrials(updated); err != nil {
			renderFragment(w, "trial_outcome_result.html", map[string]any{
				"Error": "Could not save: " + err.Error(),
			})
			return
		}
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Success":    true,
			"Message":    fmt.Sprintf("%s marked as failed.", tr.DisplayName()),
			"CanDiscard": true,
			"TrialID":    tr.ID,
		})

	default:
		// "continue" — trial stays active, just redirect back.
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Success": true,
			"Message": "Trial updated — still active.",
		})
	}
}

// handleTrialPromote handles POST /trial/promote — promotes a harvested trial
// to crops.csv.
func handleTrialPromote(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not load trials.",
		})
		return
	}

	tr := trial.FindByID(trials, trialID)
	if tr == nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Trial not found.",
		})
		return
	}

	if len(tr.ConfirmedDays) == 0 {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Cannot promote — no confirmed day parameters recorded yet.",
		})
		return
	}

	// Append to crops.csv.
	cropsPath, err := crop.CropsFilePath()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not find crops.csv: " + err.Error(),
		})
		return
	}

	if err := trial.AppendToCropsCSV(cropsPath, *tr); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not write to crops.csv: " + err.Error(),
		})
		return
	}

	// Mirror the change to Google Sheets (if linked).
	gcal.SyncLocalToSheet(context.Background())

	tr.Status = trial.StatusPromoted
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not save: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_outcome_result.html", map[string]any{
		"Success": true,
		"Message": fmt.Sprintf("%s promoted — %d day parameters added to crops.csv.",
			tr.DisplayName(), len(tr.ConfirmedDays)),
	})
}

// handleTrialDiscard handles POST /trial/discard — permanently deletes a trial.
func handleTrialDiscard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Bad request.",
		})
		return
	}

	trialID := r.FormValue("trial_id")
	trials, err := trial.LoadTrials()
	if err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not load trials.",
		})
		return
	}

	// Find the trial to remove its tentative tasks.
	tr := trial.FindByID(trials, trialID)
	if tr != nil {
		_ = trial.RemoveTentativeTasks(tr)
	}

	remaining := trial.RemoveByID(trials, trialID)
	if err := trial.SaveTrials(remaining); err != nil {
		renderFragment(w, "trial_outcome_result.html", map[string]any{
			"Error": "Could not save: " + err.Error(),
		})
		return
	}

	renderFragment(w, "trial_outcome_result.html", map[string]any{
		"Success": true,
		"Message": "Trial data permanently deleted.",
	})
}

// handleTrialView renders a full detail view of a single trial at
// GET /trial/view?id=xxx.
func handleTrialView(w http.ResponseWriter, r *http.Request) {
	trialID := r.URL.Query().Get("id")
	if trialID == "" {
		http.Error(w, "Missing trial ID", http.StatusBadRequest)
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		http.Error(w, "Could not load trials", http.StatusInternalServerError)
		return
	}

	tr := trial.FindByID(trials, trialID)
	if tr == nil {
		http.Error(w, "Trial not found", http.StatusNotFound)
		return
	}

	renderPage(w, "trial_view.html", buildTrialViewData(*tr))
}

// handleTrialCompare renders a side-by-side comparison of two trials at
// GET /trial/compare?a=xxx&b=yyy.
func handleTrialCompare(w http.ResponseWriter, r *http.Request) {
	idA := r.URL.Query().Get("a")
	idB := r.URL.Query().Get("b")
	if idA == "" || idB == "" {
		http.Error(w, "Missing trial IDs", http.StatusBadRequest)
		return
	}

	trials, err := trial.LoadTrials()
	if err != nil {
		http.Error(w, "Could not load trials", http.StatusInternalServerError)
		return
	}

	trA := trial.FindByID(trials, idA)
	trB := trial.FindByID(trials, idB)
	if trA == nil || trB == nil {
		http.Error(w, "Trial not found", http.StatusNotFound)
		return
	}

	dataA := buildTrialViewData(*trA)
	dataB := buildTrialViewData(*trB)

	renderPage(w, "trial_compare.html", map[string]any{
		"CropName": task.Capitalize(trA.CropName),
		"A":        dataA,
		"B":        dataB,
	})
}

// handleTrialComparePicker renders the compare picker page at
// GET /trial/compare-pick — lets the user choose a crop and two trials.
func handleTrialComparePicker(w http.ResponseWriter, r *http.Request) {
	trials, err := trial.LoadTrials()
	if err != nil {
		trials = []trial.TrialRecord{}
	}

	// Group past trials by crop name.
	type cropGroup struct {
		CropName string
		Trials   []trialListRow
	}

	pastByCrop := map[string][]trial.TrialRecord{}
	for _, tr := range trials {
		if tr.Status != trial.StatusActive {
			key := strings.ToLower(tr.CropName)
			pastByCrop[key] = append(pastByCrop[key], tr)
		}
	}

	var groups []cropGroup
	for name, list := range pastByCrop {
		if len(list) < 2 {
			continue
		}
		var rows []trialListRow
		for _, tr := range list {
			sow, _ := time.Parse(task.DateFormat, tr.SowDate)
			rows = append(rows, trialListRow{
				ID:          tr.ID,
				DisplayName: tr.DisplayName(),
				Status:      tr.Status,
				SowDateFmt:  sow.Format("Jan 02 2006"),
			})
		}
		groups = append(groups, cropGroup{
			CropName: task.Capitalize(name),
			Trials:   rows,
		})
	}

	// Sort groups alphabetically.
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].CropName < groups[j].CropName
	})

	renderPage(w, "trial_compare_pick.html", map[string]any{
		"Groups":    groups,
		"HasGroups": len(groups) > 0,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Trial view data helpers
// ─────────────────────────────────────────────────────────────────────────────

// trialViewData holds all the display-ready data for one trial's detail view.
type trialViewData struct {
	DisplayName   string
	Status        string
	SowDateFmt    string
	Trays         int
	Soak          string // "overnight", "4 hours", or "—"
	SeedGrams     string // "50g" or "—"
	MediumLitres  string // "1.0L" or "—"
	MTLDay        string // "Day 5" or "—"
	HarvestDay    string // "Day 9" or "—"
	ActualYield   string // "1400g" or "not recorded"
	FailureNote   string
	ConfirmedDays []trialDayView
	HasConfirmed  bool
	Observations  []trialObsView
	HasObs        bool
	TrialVariable string
}

// trialDayView is a single confirmed-day row for the view template.
type trialDayView struct {
	DayNum int
	Stage  string
	Tasks  string // "(no tasks)" if empty
}

// trialObsView is a single observation row for the view template.
type trialObsView struct {
	DayNum  int
	DateFmt string
	Notes   string
}

// buildTrialViewData converts a TrialRecord into template-ready data.
func buildTrialViewData(tr trial.TrialRecord) trialViewData {
	sow, _ := time.Parse(task.DateFormat, tr.SowDate)

	// Format soak.
	soak := "—"
	if tr.OvernightSoak {
		soak = "overnight"
	} else if tr.SoakHours > 0 {
		soak = fmt.Sprintf("%.0f hours", tr.SoakHours)
	}

	// Format seed weight and medium volume using the grower's unit system.
	cfg, _ := config.Load()
	seedGrams := "—"
	if tr.SeedGrams > 0 {
		seedGrams = fmt.Sprintf("%.0f%s", tr.SeedGrams, cfg.WeightLabel())
	}

	// Format medium.
	mediumLitres := "—"
	if tr.MediumLitres > 0 {
		mediumLitres = fmt.Sprintf("%.1f%s", tr.MediumLitres, cfg.VolumeLabel())
	}

	// Format milestone days.
	mtlDay := "—"
	if tr.MoveToLightDay > 0 {
		mtlDay = fmt.Sprintf("Day %d", tr.MoveToLightDay)
	}
	harvestDay := "—"
	if tr.HarvestDay > 0 {
		harvestDay = fmt.Sprintf("Day %d", tr.HarvestDay)
	}

	// Format yield.
	actualYield := "not recorded"
	if tr.ActualYieldGrams > 0 {
		actualYield = fmt.Sprintf("%d%s", tr.ActualYieldGrams, cfg.WeightLabel())
	}

	// Confirmed days.
	var confirmedDays []trialDayView
	// Sort by day number.
	sorted := make([]trial.TrialDayParams, len(tr.ConfirmedDays))
	copy(sorted, tr.ConfirmedDays)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Day < sorted[j].Day })
	for _, d := range sorted {
		tasks := d.Tasks
		if tasks == "" {
			tasks = "(no tasks)"
		}
		confirmedDays = append(confirmedDays, trialDayView{
			DayNum: d.Day,
			Stage:  d.Stage,
			Tasks:  tasks,
		})
	}

	// Observations.
	var observations []trialObsView
	for _, o := range tr.Observations {
		if o.Notes == "" {
			continue
		}
		dateFmt := o.Date
		if t, err := time.Parse(task.DateFormat, o.Date); err == nil {
			dateFmt = t.Format("Mon Jan 02")
		}
		observations = append(observations, trialObsView{
			DayNum:  o.Day,
			DateFmt: dateFmt,
			Notes:   o.Notes,
		})
	}

	return trialViewData{
		DisplayName:   tr.DisplayName(),
		Status:        tr.Status,
		SowDateFmt:    sow.Format("Mon Jan 02 2006"),
		Trays:         tr.Trays,
		Soak:          soak,
		SeedGrams:     seedGrams,
		MediumLitres:  mediumLitres,
		MTLDay:        mtlDay,
		HarvestDay:    harvestDay,
		ActualYield:   actualYield,
		FailureNote:   tr.FailureNote,
		ConfirmedDays: confirmedDays,
		HasConfirmed:  len(confirmedDays) > 0,
		Observations:  observations,
		HasObs:        len(observations) > 0,
		TrialVariable: tr.TrialVariable,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tentative calendar task helpers are in internal/trial/tentative.go.
// Both the CLI and the GUI call the same shared functions:
//   trial.CreateTentativeTask()
//   trial.RefreshTentativeTasks()
//   trial.CancelTentativeTasks()
//   trial.RemoveTentativeTasks()
