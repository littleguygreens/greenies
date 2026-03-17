package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"context" // for context.Background(), used when pushing to Google Sheets
	"fmt"
	"os"      // for os.Exit
	"sort"    // for sorting observations and days
	"strconv" // for converting text like "2" into the number 2
	"strings" // for string utilities used throughout
	"time"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/trial"
)

// ─────────────────────────────────────────────────────────────────────────────
// greenies trial — Phase 6 crop trialling
// ─────────────────────────────────────────────────────────────────────────────

// runTrial is the entry point for the "greenies trial" command.
//
// When you run "greenies trial", the program checks whether any trials are
// currently active (growing right now). If there are, it shows them and asks
// whether you want to manage an existing trial or start a new one. If there
// are no active trials, it goes straight to starting a new one.
//
// This is a deliberately separate command from "greenies plan" — trials are
// experiments that live outside the confirmed crop library, and the two
// workflows should never be mixed up.
func runTrial() {
	reader := bufio.NewReader(os.Stdin)

	// ask is a local helper that prints a prompt, reads a line of input, and
	// handles the "cancel" escape hatch — the same pattern used in runPlan.
	ask := func(prompt string) string {
		fmt.Print(prompt)
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)
		if strings.ToLower(text) == "cancel" {
			fmt.Println("Cancelled — nothing was saved.")
			os.Exit(0)
		}
		return text
	}

	// Load all trial records from disk.
	trials, err := trial.LoadTrials()
	if err != nil {
		fmt.Printf("Error loading trials: %v\n", err)
		os.Exit(1)
	}

	// Filter to just the active (currently growing) ones.
	active := trial.ActiveTrials(trials)

	// Show active trials so the grower knows what is currently running.
	if len(active) > 0 {
		fmt.Println("Active trials:")
		today := time.Now()
		for i, tr := range active {
			dayNum := tr.DayNumber(today)
			sow, _ := time.Parse(task.DateFormat, tr.SowDate)
			fmt.Printf("  %d. %s %dx — Day %d (sown %s)\n",
				i+1, tr.DisplayName(), tr.Trays, dayNum, sow.Format("Mon Jan 02"))
		}
		fmt.Println()
	}

	// Work out which menu options are available based on what data exists.
	// Some options only make sense when certain trials are present:
	//   (m)anage — only when there are active trials to manage
	//   (v)iew   — only when at least one trial exists (any status)
	//   (c)ompare — only when at least two past trials of the same crop exist
	hasTwoPlus := false
	pastByCropCount := map[string]int{}
	for _, tr := range trials {
		if tr.Status != "active" {
			pastByCropCount[strings.ToLower(tr.CropName)]++
		}
	}
	for _, count := range pastByCropCount {
		if count >= 2 {
			hasTwoPlus = true
			break
		}
	}

	// Build the menu string from whichever options are currently available.
	var menuParts []string
	if len(active) > 0 {
		menuParts = append(menuParts, "(m)anage")
	}
	menuParts = append(menuParts, "(n)ew trial")
	if len(trials) > 0 {
		menuParts = append(menuParts, "(v)iew")
	}
	if hasTwoPlus {
		menuParts = append(menuParts, "(c)ompare")
	}

	// The default action depends on whether there is something to manage.
	defaultChoice := "n"
	if len(active) > 0 {
		defaultChoice = "m"
	}

	// Route to the right flow. If no trials exist at all, skip the menu.
	var choice string
	if len(trials) == 0 {
		choice = "n"
	} else {
		prompt := strings.Join(menuParts, "  ") + fmt.Sprintf("  [%s]: ", defaultChoice)
		choice = strings.ToLower(strings.TrimSpace(ask(prompt)))
		if choice == "" {
			choice = defaultChoice
		}
	}

	switch choice {
	case "n", "new":
		startNewTrial(ask, trials)
	case "m", "manage":
		// Explicit manage case — routes directly to managing active trials.
		if len(active) > 0 {
			manageTrial(ask, trials, active)
		} else {
			startNewTrial(ask, trials)
		}
	case "v", "view":
		viewTrial(reader, trials)
	case "c", "compare":
		compareTrial(reader, trials)
	default:
		// Any unrecognised input: manage if active trials exist, otherwise new.
		if len(active) > 0 {
			manageTrial(ask, trials, active)
		} else {
			startNewTrial(ask, trials)
		}
	}
}

// startNewTrial handles the "new trial" flow.
//
// It asks for the minimum required information (crop name and sow date),
// shows a summary of any past trials of the same crop so the grower can
// learn from them, then asks for optional upfront parameters — soak details,
// seed weight, dirt volume, and tentative milestone days.
//
// Nothing is written to disk until the very end. If the grower types "cancel"
// at any prompt, the whole thing exits cleanly.
func startNewTrial(ask func(string) string, trials []trial.TrialRecord) {
	// ── Step 1: crop name ─────────────────────────────────────────────────────

	cropName := strings.ToLower(strings.TrimSpace(ask("Crop name: ")))
	if cropName == "" {
		fmt.Println("No name entered — nothing saved.")
		return
	}

	// ── Step 2: show history for this crop ───────────────────────────────────
	//
	// If the grower has trialled this crop before, show a summary of past runs
	// — what they were testing, how it ended, and any observations they left.
	// This is the key reason why trial history is kept by crop name: every new
	// mustard trial can benefit from notes left by the last mustard trial.

	past := trial.PastTrialsByName(trials, cropName)
	if len(past) > 0 {
		fmt.Printf("\nPast trials of %s:\n", task.Capitalize(cropName))
		for _, pt := range past {
			sow, _ := time.Parse(task.DateFormat, pt.SowDate)

			// Build a short status line: what the outcome was, plus key numbers.
			var statusLine string
			switch pt.Status {
			case trial.StatusHarvested:
				statusLine = "harvested"
				if pt.ActualYieldGrams > 0 {
					statusLine += fmt.Sprintf(" (%dg yield)", pt.ActualYieldGrams)
				}
			case trial.StatusPromoted:
				statusLine = "promoted to crops.csv"
			case trial.StatusFailed:
				statusLine = "failed"
				if pt.FailureNote != "" {
					statusLine += " — " + pt.FailureNote
				}
			default:
				statusLine = pt.Status
			}

			fmt.Printf("  %s — sown %s — %s\n",
				pt.DisplayName(), sow.Format("Mon Jan 02 2006"), statusLine)

			// Print any observation notes, skipping days where the grower left
			// a blank entry. Indented under the trial header for readability.
			for _, obs := range pt.Observations {
				if obs.Notes != "" {
					d, _ := time.Parse(task.DateFormat, obs.Date)
					fmt.Printf("    Day %d (%s): %s\n", obs.Day, d.Format("Mon Jan 02"), obs.Notes)
				}
			}
			fmt.Println()
		}
	}

	// ── Step 3: trial variable ────────────────────────────────────────────────

	trialVar := strings.TrimSpace(ask("What are you testing? (e.g. \"seed lot xyz\") or Enter to skip: "))

	// ── Step 4: sow date ──────────────────────────────────────────────────────

	sowInput := ask("Sow date (MM-DD or YYYY-MM-DD): ")
	sowDate, err := parseDate(sowInput)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// ── Step 5: tray count ────────────────────────────────────────────────────

	traysStr := strings.TrimSpace(ask("How many trays? "))
	trays, err := strconv.Atoi(traysStr)
	if err != nil || trays < 1 {
		fmt.Println("Please enter a whole number greater than zero.")
		os.Exit(1)
	}

	// ── Step 6: optional parameters ──────────────────────────────────────────
	//
	// None of these are required. The grower can press Enter at every prompt
	// and start the trial with zero parameters — just a name, date, and count.
	// Parameters are filled in during the manage flow as the trial progresses.

	fmt.Println("\nOptional parameters — press Enter to skip any.")

	// Soak: ask yes/no first, then hours or "overnight" if yes.
	var overnightSoak bool
	var soakHours float64
	soakAnswer := strings.ToLower(strings.TrimSpace(ask("Soak required? (y/n): ")))
	if soakAnswer == "y" || soakAnswer == "yes" {
		soakInput := strings.ToLower(strings.TrimSpace(ask("  Hours (or \"overnight\"): ")))
		if soakInput == "overnight" {
			// Overnight soak means this is a Day 0 crop — the sow date we just
			// recorded is the day the soak begins; trays aren't prepared until
			// the following morning (Day 1).
			overnightSoak = true
		} else if h, err := strconv.ParseFloat(soakInput, 64); err == nil && h > 0 {
			soakHours = h
		}
	}

	// Seed grams per tray.
	var seedGrams float64
	seedStr := strings.TrimSpace(ask("Seed grams per tray: "))
	if seedStr != "" {
		if g, err := strconv.ParseFloat(seedStr, 64); err == nil && g > 0 {
			seedGrams = g
		}
	}

	// Dirt litres per tray — blank defaults to 1 (the standard for most crops).
	var dirtLitres float64
	dirtStr := strings.TrimSpace(ask("Dirt litres per tray [1]: "))
	if dirtStr == "" {
		dirtLitres = 1
	} else if d, err := strconv.ParseFloat(dirtStr, 64); err == nil && d > 0 {
		dirtLitres = d
	} else {
		dirtLitres = 1
	}

	// Move-to-light day — used to place a tentative calendar marker.
	var moveToLightDay int
	mtlStr := strings.TrimSpace(ask("Move to light on day (e.g. 5): "))
	if mtlStr != "" {
		if d, err := strconv.Atoi(mtlStr); err == nil && d > 0 {
			moveToLightDay = d
		}
	}

	// Harvest day — used to place a tentative calendar marker.
	var harvestDay int
	harvStr := strings.TrimSpace(ask("Expected harvest on day (e.g. 9): "))
	if harvStr != "" {
		if d, err := strconv.Atoi(harvStr); err == nil && d > 0 {
			harvestDay = d
		}
	}

	// ── Step 7: create the trial record ──────────────────────────────────────
	//
	// parseDate returns a YYYY-MM-DD string; we parse it into a time.Time here
	// so we can do date arithmetic (AddDate) for the summary and for setting
	// LastManaged to the day before the sow date.
	sowTime, _ := time.Parse(task.DateFormat, sowDate)

	// LastManaged is set to the day before the sow date so the very first
	// manage session always starts at Day 1 — the sow date itself.
	lastManaged := sowTime.AddDate(0, 0, -1).Format(task.DateFormat)

	trialID, err := task.GenerateID()
	if err != nil {
		fmt.Printf("Error generating trial ID: %v\n", err)
		os.Exit(1)
	}

	tr := trial.TrialRecord{
		ID:             trialID,
		CropName:       cropName,
		TrialVariable:  trialVar,
		SowDate:        sowDate, // already a YYYY-MM-DD string from parseDate
		Trays:          trays,
		Status:         trial.StatusActive,
		LastManaged:    lastManaged,
		OvernightSoak:  overnightSoak,
		SoakHours:      soakHours,
		SeedGrams:      seedGrams,
		DirtLitres:     dirtLitres,
		MoveToLightDay: moveToLightDay,
		HarvestDay:     harvestDay,
	}

	// ── Step 8: create tentative calendar tasks ───────────────────────────────
	//
	// If the grower specified a move-to-light day or harvest day, we place a
	// "(unconfirmed)" marker on the calendar right now. The task IDs are stored
	// in the trial record so the manage flow can find and update these tasks
	// automatically — no manual calendar tidying required.
	//
	// We create the tasks BEFORE saving the trial record so the IDs are
	// embedded in a single save rather than requiring two separate writes.

	if moveToLightDay > 0 {
		mtlDateStr := sowTime.AddDate(0, 0, moveToLightDay-1).Format(task.DateFormat)
		id, taskErr := createTentativeTask(tr.DisplayName(), "move to light", mtlDateStr)
		if taskErr != nil {
			// A failed task create is not fatal — the trial still starts, it
			// just won't have an automatic calendar marker for this milestone.
			fmt.Printf("Warning: could not create move-to-light calendar task: %v\n", taskErr)
		} else {
			tr.TentativeMTLTaskID = id
		}
	}

	if harvestDay > 0 {
		harvDateStr := sowTime.AddDate(0, 0, harvestDay-1).Format(task.DateFormat)
		id, taskErr := createTentativeTask(tr.DisplayName(), "harvest", harvDateStr)
		if taskErr != nil {
			fmt.Printf("Warning: could not create harvest calendar task: %v\n", taskErr)
		} else {
			tr.TentativeHarvestTaskID = id
		}
	}

	// ── Step 9: save the trial record (now includes task IDs if created) ──────

	updated := trial.ReplaceByID(trials, tr)
	if err := trial.SaveTrials(updated); err != nil {
		fmt.Printf("Error saving trial: %v\n", err)
		os.Exit(1)
	}

	// ── Step 10: confirmation summary ────────────────────────────────────────

	fmt.Printf("\nTrial started: %s %dx, sown %s\n",
		tr.DisplayName(), trays, sowTime.Format("Mon Jan 02"))

	if overnightSoak {
		fmt.Println("Day 0 soak noted — trays prepared on Day 1 (the day after).")
	} else if soakHours > 0 {
		fmt.Printf("%.0f-hour soak on sow day.\n", soakHours)
	}

	if moveToLightDay > 0 {
		mtlDate := sowTime.AddDate(0, 0, moveToLightDay-1)
		fmt.Printf("Tentative move to light: Day %d (%s) — unconfirmed\n",
			moveToLightDay, mtlDate.Format("Mon Jan 02"))
	}
	if harvestDay > 0 {
		harvDate := sowTime.AddDate(0, 0, harvestDay-1)
		fmt.Printf("Tentative harvest: Day %d (%s) — unconfirmed\n",
			harvestDay, harvDate.Format("Mon Jan 02"))
	}

	fmt.Println("\nRun \"greenies trial\" to manage this trial day by day.")
}

// manageTrial handles the "manage a trial" flow.
//
// It steps through every day since the last manage session, asking for
// observation notes and optionally confirmed parameters (stage + tasks) for
// each one. After the catch-up, it asks whether to end the trial (harvest
// or failure) or leave it active until the next session.
//
// This is the core of the trialling system: each manage session adds to the
// lab notebook, and confirmed days build up the parameter CSV row by row.
func manageTrial(ask func(string) string, trials []trial.TrialRecord, active []trial.TrialRecord) {
	// ── Pick which trial to manage ────────────────────────────────────────────

	var tr trial.TrialRecord

	if len(active) == 1 {
		// Only one active trial — no need to ask.
		tr = active[0]
	} else {
		input := strings.TrimSpace(ask("Which trial? (enter number): "))
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(active) {
			fmt.Printf("Please enter a number from 1 to %d.\n", len(active))
			os.Exit(1)
		}
		tr = active[n-1]
	}

	// ── Show trial header ─────────────────────────────────────────────────────

	sow, _ := time.Parse(task.DateFormat, tr.SowDate)
	fmt.Printf("\nManaging: %s %dx (sown %s)\n",
		tr.DisplayName(), tr.Trays, sow.Format("Mon Jan 02"))

	// ── Catch-up: step through missed days ────────────────────────────────────
	//
	// "Today" normalised to midnight UTC so it compares cleanly against the
	// midnight-UTC date strings stored on disk.

	today := time.Now()
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	// Parse the last-managed date to know where to resume from.
	// If for some reason it can't be parsed, start from the day before sow.
	lastManaged, err := time.Parse(task.DateFormat, tr.LastManaged)
	if err != nil {
		lastManaged = sow.AddDate(0, 0, -1)
	}

	catchupStart := lastManaged.AddDate(0, 0, 1)

	if !catchupStart.After(t) {
		fmt.Println()
		// Iterate day by day from the first uncovered day through to today.
		for d := catchupStart; !d.After(t); d = d.AddDate(0, 0, 1) {
			dayNum := tr.DayNumber(d)
			if dayNum < 1 {
				// This day falls before the sow date (can happen if lastManaged
				// was set before the sow date). Nothing to record yet.
				continue
			}

			fmt.Printf("── Day %d (%s) ──\n", dayNum, d.Format("Mon Jan 02"))

			// Observation note — free text, entirely optional.
			notes := strings.TrimSpace(ask("Observation notes (or Enter to skip): "))
			if notes != "" {
				tr.Observations = append(tr.Observations, trial.TrialObservation{
					Day:   dayNum,
					Date:  d.Format(task.DateFormat),
					Notes: notes,
				})
			}

			// Confirmed parameters — optional. If the grower says yes, we ask
			// for the stage and tasks for this day and write them to the record.
			confirmPrompt := fmt.Sprintf("Record confirmed tasks for Day %d? (y/n): ", dayNum)
			confirm := strings.ToLower(strings.TrimSpace(ask(confirmPrompt)))
			if confirm == "y" || confirm == "yes" {
				stage := strings.ToLower(strings.TrimSpace(ask("  Stage (sow/dark/light/harvest): ")))
				tasks := strings.TrimSpace(ask("  Tasks (comma-separated, or Enter for none): "))

				// If a confirmed entry for this day already exists (e.g. the
				// grower manages the same day twice in the same session), replace
				// it rather than creating a duplicate.
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
				fmt.Printf("  ✓ Day %d confirmed (%s)\n", dayNum, stage)
			}

			fmt.Println()
		}
	} else {
		fmt.Println("(all up to date — no missed days to catch up)")
		fmt.Println()
	}

	// Update the last-managed date to today so the next session knows where
	// to pick up from.
	tr.LastManaged = t.Format(task.DateFormat)

	// Refresh tentative calendar tasks based on everything confirmed so far.
	//
	// Two things are checked automatically:
	//   - If the grower confirmed a "light" stage day: the move-to-light task
	//     title changes from "(unconfirmed)" to "moved to light".
	//   - If the expected MTL or harvest date has already passed without a
	//     matching confirmed day: the title changes to "(overdue)" so the
	//     grower sees it on their calendar and knows to investigate.
	if err := refreshTentativeTasks(&tr, t); err != nil {
		fmt.Printf("Warning: could not update trial calendar tasks: %v\n", err)
	}

	// ── End-of-session prompt ─────────────────────────────────────────────────
	//
	// Default is to stay active — just pressing Enter saves the session and
	// exits. The grower only types h or f when the trial is actually done.

	outcomeInput := strings.ToLower(strings.TrimSpace(
		ask("End trial? Enter to continue, (h)arvest, (f)ailure: ")))

	switch outcomeInput {
	case "h", "harvest":
		trialHarvest(ask, &tr, trials)
	case "f", "failure", "fail":
		trialFailure(ask, &tr, trials)
	default:
		// Stay active — save the updated observations and confirmed days.
		updated := trial.ReplaceByID(trials, tr)
		if err := trial.SaveTrials(updated); err != nil {
			fmt.Printf("Error saving trial: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Trial updated. Run \"greenies trial\" to manage again.")
	}
}

// trialHarvest handles the harvest outcome for a trial.
//
// It asks for the actual yield in grams, marks the trial as harvested,
// and then offers to promote the confirmed parameters to crops.csv.
// If the grower declines to promote, the trial is kept as a completed
// record that future trials of the same crop can learn from.
func trialHarvest(ask func(string) string, tr *trial.TrialRecord, trials []trial.TrialRecord) {
	// Record actual yield — optional, since the grower may not have weighed it.
	yieldStr := strings.TrimSpace(ask("Actual yield in grams (or Enter to skip): "))
	if g, err := strconv.Atoi(yieldStr); err == nil && g > 0 {
		tr.ActualYieldGrams = g
	}

	tr.Status = trial.StatusHarvested

	// Refresh the harvest tentative task now that the harvest is confirmed.
	// The task title will change from "(unconfirmed)" to "harvested".
	if err := refreshTentativeTasks(tr, time.Now()); err != nil {
		fmt.Printf("Warning: could not update trial calendar tasks: %v\n", err)
	}

	// Offer to promote the confirmed parameters to crops.csv.
	promoteInput := strings.ToLower(strings.TrimSpace(
		ask("(p)romote to crops.csv, or Enter to leave as completed: ")))

	if promoteInput == "p" || promoteInput == "promote" {
		trialPromote(ask, tr, trials)
		return
	}

	// Leave as harvested — save and confirm.
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		fmt.Printf("Error saving trial: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Trial marked as harvested.")
	if tr.ActualYieldGrams > 0 {
		fmt.Printf("Yield recorded: %dg\n", tr.ActualYieldGrams)
	}
	fmt.Println("Run \"greenies trial\" to view past trial history.")
}

// trialFailure handles the failure outcome for a trial.
//
// It asks for a failure note, marks the trial as failed, and then offers
// to discard all data. The grower must type the word "discard" exactly to
// confirm deletion — this prevents accidental data loss.
//
// If the grower does not discard, the failed trial is kept in the log
// as a cautionary entry that future trials of the same crop can review.
func trialFailure(ask func(string) string, tr *trial.TrialRecord, trials []trial.TrialRecord) {
	failNote := strings.TrimSpace(ask("What went wrong? (optional — or Enter to skip): "))
	tr.FailureNote = failNote
	tr.Status = trial.StatusFailed

	fmt.Println("Trial marked as failed.")
	if failNote != "" {
		fmt.Printf("Note recorded: %s\n", failNote)
	}

	// Offer to discard. The grower must type "discard" exactly — a single
	// accidental keypress cannot wipe the log.
	discardInput := strings.TrimSpace(
		ask("Discard all trial data? Type \"discard\" to permanently delete, or Enter to keep: "))

	if discardInput == "discard" {
		// Delete tentative calendar tasks entirely — the trial is being fully
		// erased, so there is no reason to leave any trace on the calendar.
		if err := removeTentativeTasks(tr); err != nil {
			fmt.Printf("Warning: could not remove trial calendar tasks: %v\n", err)
		}
		// Remove the trial record from the list entirely and save.
		remaining := trial.RemoveByID(trials, tr.ID)
		if err := trial.SaveTrials(remaining); err != nil {
			fmt.Printf("Error saving trials: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Trial data permanently deleted.")
		return
	}

	// Keep the failed log. Mark tentative calendar tasks as "(cancelled)" so
	// the grower sees at a glance on their calendar that those milestones were
	// planned but the trial did not reach them.
	if err := cancelTentativeTasks(tr); err != nil {
		fmt.Printf("Warning: could not update trial calendar tasks: %v\n", err)
	}
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		fmt.Printf("Error saving trial: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Trial log kept for future reference.")
}

// trialPromote copies the confirmed parameters from a trial into crops.csv,
// making them official. This is the final step for a successful trial: the
// grower tested something, it worked, and now those parameters become part
// of the permanent crop library.
//
// If the crop already exists in crops.csv, the grower is warned and must
// confirm before the new rows are appended — both old and new entries will
// then coexist in the file, which is intentional (one might be the old seed
// lot, the other the new one).
func trialPromote(ask func(string) string, tr *trial.TrialRecord, trials []trial.TrialRecord) {
	if len(tr.ConfirmedDays) == 0 {
		fmt.Println("Cannot promote — no confirmed day parameters have been recorded yet.")
		fmt.Println("Manage the trial and confirm tasks for each day first.")
		// Revert status to harvested (it was set before we got here).
		tr.Status = trial.StatusHarvested
		updated := trial.ReplaceByID(trials, *tr)
		_ = trial.SaveTrials(updated)
		return
	}

	// Get the path to the user's crops.csv.
	cropsPath, err := crop.CropsFilePath()
	if err != nil {
		fmt.Printf("Error finding crops.csv: %v\n", err)
		os.Exit(1)
	}

	// Warn if this crop name already appears in crops.csv — the grower should
	// know they are adding a second entry, not replacing the existing one.
	existingSource, _ := crop.GetSource()
	var existingCrops []crop.Crop
	if existingSource != nil {
		existingCrops, _ = existingSource.LoadCrops()
	}
	for _, c := range existingCrops {
		if strings.EqualFold(c.Name, tr.CropName) {
			fmt.Printf("Note: %s already exists in crops.csv.\n",
				task.Capitalize(tr.CropName))
			fmt.Println("Promoting will add a new entry alongside the existing one.")
			proceed := strings.ToLower(strings.TrimSpace(ask("Continue? (y/n): ")))
			if proceed != "y" && proceed != "yes" {
				fmt.Println("Promotion cancelled.")
				tr.Status = trial.StatusHarvested
				updated := trial.ReplaceByID(trials, *tr)
				_ = trial.SaveTrials(updated)
				return
			}
			break
		}
	}

	// Append the confirmed rows to crops.csv.
	if err := trial.AppendToCropsCSV(cropsPath, *tr); err != nil {
		fmt.Printf("Error writing to crops.csv: %v\n", err)
		os.Exit(1)
	}

	// Mirror the change to Google Sheets (if linked).
	gcal.SyncLocalToSheet(context.Background())

	// Mark the trial as promoted and save.
	tr.Status = trial.StatusPromoted
	updated := trial.ReplaceByID(trials, *tr)
	if err := trial.SaveTrials(updated); err != nil {
		fmt.Printf("Error saving trial: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s promoted — %d day parameters added to crops.csv.\n",
		tr.DisplayName(), len(tr.ConfirmedDays))
	fmt.Println("Run \"greenies crops\" to see the updated crop library.")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tentative calendar task helpers
// ─────────────────────────────────────────────────────────────────────────────

// createTentativeTask creates a single calendar task with an "(unconfirmed)"
// marker and saves it to tasks.json. It returns the new task's ID so the
// caller can store it in the TrialRecord for later updates.
//
// The task title is formatted as: "DisplayName — eventLabel? (unconfirmed)"
// For example: "Mustard (seed lot xyz) — move to light? (unconfirmed)"
//
// These tasks appear on "greenies list" just like any other task, but the
// "(unconfirmed)" tag tells the grower they are based on an estimate — not a
// confirmed date from the main crop schedule. They update automatically the
// next time the trial is managed.
func createTentativeTask(displayName, eventLabel, dateStr string) (string, error) {
	title := displayName + " — " + eventLabel + "? (unconfirmed)"
	// task.New generates a unique ID and timestamps the task automatically.
	t, err := task.New(title, dateStr, "trial tentative marker")
	if err != nil {
		return "", err
	}
	existing, err := store.Load()
	if err != nil {
		return "", err
	}
	if err := store.Save(append(existing, t)); err != nil {
		return "", err
	}
	return t.ID, nil
}

// refreshTentativeTasks inspects the current state of a trial and updates the
// titles of its tentative calendar tasks to reflect what has happened.
//
// There are three possible outcomes for each tentative task:
//
//   - The event was confirmed (a "light" or "harvest" stage day was logged,
//     or the harvest outcome was recorded): the title updates to a clean
//     confirmation, e.g. "Mustard — moved to light".
//
//   - The expected date passed without confirmation: the title changes from
//     "(unconfirmed)" to "(overdue)" so the grower sees the slip on their
//     calendar and knows to investigate.
//
//   - Neither condition applies: the task is left as-is.
//
// Only writes to disk when a title actually needs to change — safe to call
// on every manage session without unnecessary disk writes.
func refreshTentativeTasks(tr *trial.TrialRecord, today time.Time) error {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		// This trial has no tentative tasks — nothing to do.
		return nil
	}

	tasks, err := store.Load()
	if err != nil {
		return err
	}

	changed := false

	// ── Move-to-light task ────────────────────────────────────────────────────
	//
	// The move-to-light event is confirmed when the grower logs any day with
	// stage="light" in the manage flow — that is the moment trays physically
	// moved off the blackout shelf onto a lit rack.

	if tr.TentativeMTLTaskID != "" {
		mtlConfirmed := false
		for _, cd := range tr.ConfirmedDays {
			if cd.Stage == "light" {
				mtlConfirmed = true
				break
			}
		}

		// Decide what the title should say now.
		var newTitle string
		if mtlConfirmed {
			newTitle = tr.DisplayName() + " — moved to light"
		} else {
			// Not confirmed yet. Has the expected date already passed?
			mtlDateStr := tr.TentativeMoveToLightDate()
			if mtlDateStr != "" {
				mtlDate, parseErr := time.Parse(task.DateFormat, mtlDateStr)
				// today.After(mtlDate) means today is strictly past the expected day.
				if parseErr == nil && today.After(mtlDate) {
					newTitle = tr.DisplayName() + " — move to light? (overdue)"
				}
			}
		}

		// If the title needs updating, find the task by ID and change it.
		if newTitle != "" {
			for i, t := range tasks {
				if t.ID == tr.TentativeMTLTaskID {
					if tasks[i].Title != newTitle {
						tasks[i].Title = newTitle
						tasks[i].UpdatedAt = time.Now()
						changed = true
					}
					break
				}
			}
		}
	}

	// ── Harvest task ──────────────────────────────────────────────────────────
	//
	// The harvest event is confirmed when the trial status is set to harvested
	// or promoted (via the harvest outcome flow), OR when a day with
	// stage="harvest" is confirmed in the manage flow. Any of these means the
	// crop was actually cut.

	if tr.TentativeHarvestTaskID != "" {
		harvestConfirmed := tr.Status == trial.StatusHarvested || tr.Status == trial.StatusPromoted
		if !harvestConfirmed {
			for _, cd := range tr.ConfirmedDays {
				if cd.Stage == "harvest" {
					harvestConfirmed = true
					break
				}
			}
		}

		var newTitle string
		if harvestConfirmed {
			newTitle = tr.DisplayName() + " — harvested"
		} else {
			harvDateStr := tr.TentativeHarvestDate()
			if harvDateStr != "" {
				harvDate, parseErr := time.Parse(task.DateFormat, harvDateStr)
				if parseErr == nil && today.After(harvDate) {
					newTitle = tr.DisplayName() + " — harvest? (overdue)"
				}
			}
		}

		if newTitle != "" {
			for i, t := range tasks {
				if t.ID == tr.TentativeHarvestTaskID {
					if tasks[i].Title != newTitle {
						tasks[i].Title = newTitle
						tasks[i].UpdatedAt = time.Now()
						changed = true
					}
					break
				}
			}
		}
	}

	if changed {
		return store.Save(tasks)
	}
	return nil
}

// cancelTentativeTasks updates the tentative calendar tasks for a failed trial
// to show "(cancelled)" instead of "(unconfirmed)" or "(overdue)".
//
// The tasks are intentionally left on the calendar rather than deleted — they
// serve as a record that those milestones were planned but the trial did not
// reach them. The grower can delete them manually when they are ready.
func cancelTentativeTasks(tr *trial.TrialRecord) error {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return nil
	}

	tasks, err := store.Load()
	if err != nil {
		return err
	}

	changed := false
	for i, t := range tasks {
		switch t.ID {
		case tr.TentativeMTLTaskID:
			newTitle := tr.DisplayName() + " — move to light? (cancelled)"
			if tasks[i].Title != newTitle {
				tasks[i].Title = newTitle
				tasks[i].UpdatedAt = time.Now()
				changed = true
			}
		case tr.TentativeHarvestTaskID:
			newTitle := tr.DisplayName() + " — harvest? (cancelled)"
			if tasks[i].Title != newTitle {
				tasks[i].Title = newTitle
				tasks[i].UpdatedAt = time.Now()
				changed = true
			}
		}
	}

	if changed {
		return store.Save(tasks)
	}
	return nil
}

// ── Trial view and comparison ──────────────────────────────────────────────

// viewTrial shows the full detail record of any single trial.
//
// The grower picks from a numbered list of all trials (any status) and gets
// a clean single-column report: setup parameters, confirmed day-by-day data,
// and any observations logged during the trial.
func viewTrial(reader *bufio.Reader, trials []trial.TrialRecord) {
	if len(trials) == 0 {
		fmt.Println("No trials found.")
		return
	}

	// List all trials so the grower can pick one by number.
	fmt.Println("\nTrials:")
	for i, tr := range trials {
		sow, _ := time.Parse(task.DateFormat, tr.SowDate)
		fmt.Printf("  %d. %-30s %-12s sown %s\n",
			i+1, tr.DisplayName(), tr.Status, sow.Format("Mon Jan 02 2006"))
	}
	fmt.Println()
	fmt.Printf("Pick a trial to view (1-%d): ", len(trials))

	input, _ := reader.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || n < 1 || n > len(trials) {
		fmt.Println("Invalid choice — nothing shown.")
		return
	}

	// Render the single-trial detail view.
	printTrialDetail(trials[n-1])
}

// compareTrial walks the grower through picking two past trials of the same
// crop and renders them side by side so they can spot what was different.
//
// Only past (non-active) trials are eligible — you compare completed
// experiments, not one that is still running.
func compareTrial(reader *bufio.Reader, trials []trial.TrialRecord) {
	// Group past trials by crop name.
	pastByCrop := map[string][]trial.TrialRecord{}
	for _, tr := range trials {
		if tr.Status != "active" {
			key := strings.ToLower(tr.CropName)
			pastByCrop[key] = append(pastByCrop[key], tr)
		}
	}

	// We can only compare crops that have at least two past trials.
	var eligibleCrops []string
	for name, list := range pastByCrop {
		if len(list) >= 2 {
			eligibleCrops = append(eligibleCrops, name)
		}
	}
	sort.Strings(eligibleCrops) // alphabetical order for a predictable list

	if len(eligibleCrops) == 0 {
		fmt.Println("No crops have two or more completed trials yet.")
		return
	}

	// Pick which crop to compare. Auto-select if there is only one choice.
	var cropName string
	if len(eligibleCrops) == 1 {
		cropName = eligibleCrops[0]
		fmt.Printf("Comparing trials of %s:\n\n", task.Capitalize(cropName))
	} else {
		fmt.Println("Compare trials of which crop?")
		fmt.Println("  Crops with multiple past trials:")
		for i, name := range eligibleCrops {
			fmt.Printf("    %d. %s (%d trials)\n", i+1, name, len(pastByCrop[name]))
		}
		fmt.Println()
		fmt.Printf("Crop number (or type a name): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		// Try parsing as a number first.
		n, err := strconv.Atoi(input)
		if err == nil && n >= 1 && n <= len(eligibleCrops) {
			cropName = eligibleCrops[n-1]
		} else {
			// Fall back to name matching (case-insensitive).
			inputLower := strings.ToLower(input)
			for _, name := range eligibleCrops {
				if name == inputLower {
					cropName = name
					break
				}
			}
			if cropName == "" {
				fmt.Println("No matching crop found — nothing shown.")
				return
			}
		}
	}

	// List the past trials of that crop so the grower can pick two.
	past := pastByCrop[cropName]
	fmt.Printf("Past trials of %s:\n", cropName)
	for i, tr := range past {
		sow, _ := time.Parse(task.DateFormat, tr.SowDate)
		label := tr.TrialVariable
		if label == "" {
			// If no trial variable was set, fall back to a generic label.
			label = fmt.Sprintf("trial %d", i+1)
		}
		fmt.Printf("  %d. %-24s %-12s sown %s\n",
			i+1, "("+label+")", tr.Status, sow.Format("Mon Jan 02 2006"))
	}
	fmt.Println()

	// Pick trial A.
	fmt.Printf("Pick first trial (1-%d): ", len(past))
	inputA, _ := reader.ReadString('\n')
	nA, err := strconv.Atoi(strings.TrimSpace(inputA))
	if err != nil || nA < 1 || nA > len(past) {
		fmt.Println("Invalid choice — nothing shown.")
		return
	}

	// Pick trial B — must be different from A.
	fmt.Printf("Pick second trial (1-%d): ", len(past))
	inputB, _ := reader.ReadString('\n')
	nB, err := strconv.Atoi(strings.TrimSpace(inputB))
	if err != nil || nB < 1 || nB > len(past) {
		fmt.Println("Invalid choice — nothing shown.")
		return
	}
	if nA == nB {
		fmt.Println("You picked the same trial twice — nothing shown.")
		return
	}

	// Render the two-column comparison.
	printTrialDetail(past[nA-1], past[nB-1])
}

// printTrialDetail renders a full detail view of one or two trials.
//
// Called with one trial  → single-column report titled "Trial — Name"
// Called with two trials → side-by-side comparison titled "Trial comparison — Crop"
//
// The same function handles both cases so the layout stays consistent.
// Sections: summary of setup params, day-by-day confirmed data, observations.
func printTrialDetail(trials ...trial.TrialRecord) {
	// The thin separator line used throughout this project (21 dashes).
	sep := strings.Repeat("─", 21)

	// ── Header ───────────────────────────────────────────────────────────────

	if len(trials) == 1 {
		fmt.Printf("\nTrial — %s\n", trials[0].DisplayName())
	} else {
		fmt.Printf("\nTrial comparison — %s\n", task.Capitalize(trials[0].CropName))
	}
	fmt.Println(sep)
	fmt.Println()

	// ── Formatting helpers ────────────────────────────────────────────────────
	// These small functions convert raw field values into tidy display strings.
	// Zero or empty values show "—" so the grower knows the field was not set.

	fmtIntDay := func(n int) string {
		if n == 0 {
			return "—"
		}
		return fmt.Sprintf("Day %d", n)
	}
	fmtYieldGrams := func(n int) string {
		if n == 0 {
			return "not recorded"
		}
		return fmt.Sprintf("%dg", n)
	}
	fmtSeedGrams := func(f float64) string {
		if f == 0 {
			return "—"
		}
		return fmt.Sprintf("%.0fg", f)
	}
	fmtDirt := func(f float64) string {
		if f == 0 {
			return "—"
		}
		return fmt.Sprintf("%.1fL", f)
	}
	// fmtSoak describes the soak setting for a trial: overnight, a number of
	// hours, or "—" if no soak was recorded.
	fmtSoak := func(tr trial.TrialRecord) string {
		if tr.OvernightSoak {
			return "overnight"
		}
		if tr.SoakHours > 0 {
			return fmt.Sprintf("%.0f hours", tr.SoakHours)
		}
		return "—"
	}
	fmtDate := func(dateStr string) string {
		t, err := time.Parse(task.DateFormat, dateStr)
		if err != nil {
			return dateStr
		}
		return t.Format("Mon Jan 02 2006")
	}

	// ── Summary block ─────────────────────────────────────────────────────────

	// labelW is how wide the left column (the field name) is.
	// colW is how wide each value column is in compare mode.
	// These keep both modes visually aligned.
	const labelW = 24
	const colW = 24

	// printRow prints one labelled row of the summary table.
	// In single-trial mode it prints: "  Label:    value"
	// In compare mode it prints:      "  Label:    valueA          valueB"
	// Empty values are replaced with "—" so the row never looks blank.
	printRow := func(label string, values ...string) {
		fmt.Printf("  %-*s", labelW, label+":")
		if len(trials) == 1 {
			v := values[0]
			if v == "" {
				v = "—"
			}
			fmt.Println(v)
		} else {
			a, b := values[0], values[1]
			if a == "" {
				a = "—"
			}
			if b == "" {
				b = "—"
			}
			fmt.Printf("%-*s%s\n", colW, a, b)
		}
	}

	// In compare mode, print a column header row so the grower knows which
	// column belongs to which trial (e.g. "[A] seed lot xyz  [B] humid soil").
	if len(trials) == 2 {
		labelA := trials[0].TrialVariable
		if labelA == "" {
			labelA = "trial A"
		}
		labelB := trials[1].TrialVariable
		if labelB == "" {
			labelB = "trial B"
		}
		// The header sits in the value columns, not the label column.
		fmt.Printf("  %-*s%-*s%s\n", labelW, "", colW, "[A] "+labelA, "[B] "+labelB)
		fmt.Println()
	}

	// Print every summary field for one or two trials.
	if len(trials) == 1 {
		tr := trials[0]
		printRow("Status", tr.Status)
		printRow("Sown", fmtDate(tr.SowDate))
		printRow("Trays", strconv.Itoa(tr.Trays))
		printRow("Overnight soak", fmtSoak(tr))
		printRow("Seed g/tray", fmtSeedGrams(tr.SeedGrams))
		printRow("Dirt l/tray", fmtDirt(tr.DirtLitres))
		printRow("Move-to-light day", fmtIntDay(tr.MoveToLightDay))
		printRow("Expected harvest", fmtIntDay(tr.HarvestDay))
		printRow("Actual yield", fmtYieldGrams(tr.ActualYieldGrams))
		printRow("Failure note", tr.FailureNote)
	} else {
		a, b := trials[0], trials[1]
		printRow("Status", a.Status, b.Status)
		printRow("Sown", fmtDate(a.SowDate), fmtDate(b.SowDate))
		printRow("Trays", strconv.Itoa(a.Trays), strconv.Itoa(b.Trays))
		printRow("Overnight soak", fmtSoak(a), fmtSoak(b))
		printRow("Seed g/tray", fmtSeedGrams(a.SeedGrams), fmtSeedGrams(b.SeedGrams))
		printRow("Dirt l/tray", fmtDirt(a.DirtLitres), fmtDirt(b.DirtLitres))
		printRow("Move-to-light day", fmtIntDay(a.MoveToLightDay), fmtIntDay(b.MoveToLightDay))
		printRow("Expected harvest", fmtIntDay(a.HarvestDay), fmtIntDay(b.HarvestDay))
		printRow("Actual yield", fmtYieldGrams(a.ActualYieldGrams), fmtYieldGrams(b.ActualYieldGrams))
		printRow("Failure note", a.FailureNote, b.FailureNote)
	}

	// ── Day-by-day block ──────────────────────────────────────────────────────

	fmt.Println()
	fmt.Println("  Day-by-day (confirmed parameters)")
	fmt.Println("  " + sep)

	// Collect every unique day number from all passed trials so we can print
	// a row for each day that appears in any of them.
	daySet := map[int]bool{}
	for _, tr := range trials {
		for _, d := range tr.ConfirmedDays {
			daySet[d.Day] = true
		}
	}

	if len(daySet) == 0 {
		fmt.Println("  (no confirmed day data recorded)")
	} else {
		// Sort numerically so Day 1 comes before Day 2, etc.
		var days []int
		for day := range daySet {
			days = append(days, day)
		}
		sort.Ints(days)

		if len(trials) == 1 {
			// Single-trial mode: one clean line per day.
			for _, day := range days {
				for _, d := range trials[0].ConfirmedDays {
					if d.Day == day {
						tasks := d.Tasks
						if tasks == "" {
							tasks = "(no tasks)"
						}
						fmt.Printf("  Day %-3d %-8s %s\n", day, d.Stage, tasks)
						break
					}
				}
			}
		} else {
			// Compare mode: one block per day with a line for each trial.
			// The stage label comes from whichever trial has data for that day.
			for _, day := range days {
				stage := ""
				for _, tr := range trials {
					for _, d := range tr.ConfirmedDays {
						if d.Day == day {
							stage = d.Stage
							break
						}
					}
					if stage != "" {
						break
					}
				}

				// Print "Day N  stage  [A] tasks" on the first line,
				// then "                  [B] tasks" indented to match.
				// The indent is: "  Day " (6) + "%-3d" (3) + " " (1) + "%-8s" (8) = 18 chars.
				labels := []string{"[A]", "[B]"}
				for i, tr := range trials {
					tasks := "(no confirmed data)"
					for _, d := range tr.ConfirmedDays {
						if d.Day == day {
							if d.Tasks == "" {
								tasks = "(no tasks)"
							} else {
								tasks = d.Tasks
							}
							break
						}
					}
					if i == 0 {
						fmt.Printf("  Day %-3d %-8s%s %s\n", day, stage, labels[i], tasks)
					} else {
						fmt.Printf("  %-18s%s %s\n", "", labels[i], tasks)
					}
				}
			}
		}
	}

	// ── Observations block ────────────────────────────────────────────────────

	// Collect all non-empty observations across all trials, tagged by which
	// trial they came from so the grower knows whose note is whose.
	type taggedObs struct {
		day   int
		trial int // 0 = first trial (A), 1 = second trial (B)
		notes string
	}
	var obs []taggedObs
	for i, tr := range trials {
		for _, o := range tr.Observations {
			if o.Notes != "" {
				obs = append(obs, taggedObs{day: o.Day, trial: i, notes: o.Notes})
			}
		}
	}

	if len(obs) > 0 {
		fmt.Println()
		fmt.Println("  Observations")
		fmt.Println("  " + sep)

		// Sort by day first, then by trial index (A before B on the same day).
		sort.Slice(obs, func(i, j int) bool {
			if obs[i].day != obs[j].day {
				return obs[i].day < obs[j].day
			}
			return obs[i].trial < obs[j].trial
		})

		labels := []string{"[A] ", "[B] "}
		for _, o := range obs {
			label := ""
			if len(trials) > 1 {
				label = labels[o.trial]
			}
			fmt.Printf("  Day %-3d  %s%s\n", o.day, label, o.notes)
		}
	}

	fmt.Println()
}

// removeTentativeTasks deletes the tentative calendar tasks for a discarded
// trial from tasks.json entirely. When a trial is fully discarded, all traces
// of it are erased — including any calendar markers placed at the start.
func removeTentativeTasks(tr *trial.TrialRecord) error {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return nil
	}

	existing, err := store.Load()
	if err != nil {
		return err
	}

	// Keep every task whose ID does not match either tentative marker.
	var remaining []task.Task
	for _, t := range existing {
		if t.ID == tr.TentativeMTLTaskID || t.ID == tr.TentativeHarvestTaskID {
			continue // this is a tentative task — skip it (i.e. delete it)
		}
		remaining = append(remaining, t)
	}
	return store.Save(remaining)
}
