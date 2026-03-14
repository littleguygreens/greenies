package main

import (
	"bufio"   // for reading a full line of user input, including spaces
	"fmt"
	"os"      // for os.Exit and os.Stdin
	"strconv" // for converting text like "2" into the number 2
	"strings" // for TrimSpace, ToLower, Contains
	"time"

	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// scheduleView is one row in the side-by-side preview table shown to the
// grower before they confirm an adjustment.
//
// It pairs a calendar date with a plain-English label describing what that
// day is in the cycle — for example "Day 3 (dark)" or "move to light".
type scheduleView struct {
	Date  time.Time
	Label string // e.g. "Day 1 (sow)", "Day 4 (dark)", "move to light", "harvest"
}

// runAdjust handles the "greenies adjust" command.
//
// This lets the grower make corrections to a crop batch that is already
// growing on the farm — without touching crops.csv or wiping past calendar
// entries.
//
// The grower picks one "anchor" point up front — either the sow date or
// the harvest date — and that anchor stays fixed for the whole session.
//
//   Anchor sow     — "I already sowed; I'm working forward."
//                    Adding days to a stage pushes everything after it later.
//
//   Anchor harvest — "I need to hit this harvest date, no matter what."
//                    Adding days to a stage pulls the schedule earlier.
//
// Within one session the grower can make as many fine adjustments as they
// need. Each one shows a side-by-side before/after preview and asks for
// confirmation before making any changes.
func runAdjust() {
	reader := bufio.NewReader(os.Stdin)

	// Load farm config once — we need it to run the conflict checker after
	// each operation saves new data to disk.
	farmEnvs, err := farm.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading farm config: %v\n", err)
		os.Exit(1)
	}

	// Load cycles and tasks so we can display the active list to the grower.
	cycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Error loading cycle records: %v\n", err)
		os.Exit(1)
	}

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// task.Today() returns midnight UTC on the current local date — the single
	// correct way to get "today" throughout this program. See task/task.go.
	today := task.Today()

	// ─── Filter to active and upcoming cycles ────────────────────────────────
	//
	// Active: already started (sow ≤ today) and not yet harvested (harvest ≥ today).
	// Upcoming: sowing within the next 7 days (sow > today and sow ≤ today + 7).
	//
	// Both groups are combined into one numbered list so the grower can pick
	// from either. Adjusting an upcoming cycle before it starts is the ideal
	// time to fine-tune blackout or light days.

	var activeCycles []farm.Cycle
	var upcomingCycles []farm.Cycle
	oneWeekOut := today.AddDate(0, 0, 7)

	for _, c := range cycles {
		sow, err := time.Parse(task.DateFormat, c.SowDate)
		if err != nil {
			continue // skip records with bad dates rather than crashing
		}
		harv, err := time.Parse(task.DateFormat, c.HarvestDate)
		if err != nil {
			continue
		}
		if !today.Before(sow) && !today.After(harv) {
			activeCycles = append(activeCycles, c)
		} else if sow.After(today) && !sow.After(oneWeekOut) {
			upcomingCycles = append(upcomingCycles, c)
		}
	}

	if len(activeCycles) == 0 && len(upcomingCycles) == 0 {
		fmt.Println("No active or upcoming cycles to adjust.")
		fmt.Println("Active cycles appear here once sown and before their harvest date.")
		fmt.Println("Upcoming cycles appear here if they start within the next 7 days.")
		return
	}

	// ─── Display the list and pick a cycle ───────────────────────────────────
	//
	// Active and upcoming cycles are printed in two sections but share one
	// sequential numbering scheme so the grower types a single number to pick.

	// candidates holds the combined ordered list that the number input maps to.
	var candidates []farm.Cycle

	if len(activeCycles) > 0 {
		fmt.Println("Active cycles:")
		fmt.Println()
		for _, c := range activeCycles {
			candidates = append(candidates, c)
			i := len(candidates)

			sow, _ := time.Parse(task.DateFormat, c.SowDate)
			harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
			mtl, _ := time.Parse(task.DateFormat, c.MoveToLightDate)

			dayNum := int(today.Sub(sow).Hours()/24) + 1

			stage := "dark"
			if today.Equal(harv) {
				stage = "harvest day"
			} else if !today.Before(mtl) {
				stage = "light"
			}

			fmt.Printf("  %d.  %-12s %dx   sown %s   harvest %s   Day %d (%s)\n",
				i,
				task.Capitalize(c.CropName),
				c.Trays,
				sow.Format("Jan 02"),
				harv.Format("Jan 02"),
				dayNum,
				stage,
			)
		}
		fmt.Println()
	}

	if len(upcomingCycles) > 0 {
		fmt.Println("Upcoming cycles (starting within 7 days):")
		fmt.Println()
		for _, c := range upcomingCycles {
			candidates = append(candidates, c)
			i := len(candidates)

			sow, _ := time.Parse(task.DateFormat, c.SowDate)
			harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
			daysUntil := int(sow.Sub(today).Hours() / 24)

			fmt.Printf("  %d.  %-12s %dx   sowing %s   harvest %s   (in %d day(s))\n",
				i,
				task.Capitalize(c.CropName),
				c.Trays,
				sow.Format("Jan 02"),
				harv.Format("Jan 02"),
				daysUntil,
			)
		}
		fmt.Println()
	}

	fmt.Print("Which cycle? (number): ")
	numStr, _ := reader.ReadString('\n')
	numStr = strings.TrimSpace(numStr)

	num, err := strconv.Atoi(numStr)
	if err != nil || num < 1 || num > len(candidates) {
		fmt.Println("Invalid choice — cancelled.")
		return
	}

	// Remember the CycleID. After each operation saves data to disk, the
	// loop reloads fresh data and uses this ID to re-find the chosen cycle.
	chosenID := candidates[num-1].CycleID

	// ─── Pick an anchor ───────────────────────────────────────────────────────
	//
	// The anchor is chosen once and stays fixed for the whole session.
	// It determines which end of the schedule is treated as immovable.

	fmt.Println()
	fmt.Println("Anchor adjustments to:")
	fmt.Println("  (s)ow date     — sow is fixed; changes ripple forward toward harvest")
	fmt.Println("  (h)arvest date — harvest is fixed; changes ripple backward toward sow")
	fmt.Println()
	fmt.Print("Anchor: ")
	anchorInput, _ := reader.ReadString('\n')
	anchorInput = strings.ToLower(strings.TrimSpace(anchorInput))

	if anchorInput != "s" && anchorInput != "h" {
		fmt.Println("Invalid choice — cancelled.")
		return
	}

	// anchorSow = true means we keep the sow date fixed.
	// anchorSow = false means we keep the harvest date fixed.
	anchorSow := anchorInput == "s"

	// ─── Adjustment loop ──────────────────────────────────────────────────────
	//
	// We loop here so the grower can make multiple adjustments in one session
	// (for example: add a blackout day to see the effect, then trim a light
	// day to balance it). The anchor stays the same throughout.
	//
	// At the top of every loop iteration we reload from disk. This makes sure
	// the dates shown and the menu options always reflect the current state —
	// including any changes made in a previous iteration.

	for {
		cycles, err = farm.LoadCycles()
		if err != nil {
			fmt.Printf("Error reloading cycles: %v\n", err)
			return
		}
		tasks, err = store.Load()
		if err != nil {
			fmt.Printf("Error reloading tasks: %v\n", err)
			return
		}

		// Re-find the chosen cycle by its ID in the freshly loaded list.
		chosen, ok := findCycleByID(cycles, chosenID)
		if !ok {
			// The cycle was cancelled in a previous iteration — nothing left to do.
			return
		}

		sow, _ := time.Parse(task.DateFormat, chosen.SowDate)
		mtl, _ := time.Parse(task.DateFormat, chosen.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)

		// inBlackout is true if the crop has not yet moved to the lit rack.
		// Once it has moved to light, the blackout stage is historical and
		// cannot be adjusted from the future-tasks side.
		inBlackout := today.Before(mtl)

		anchorLabel := "harvest"
		if anchorSow {
			anchorLabel = "sow"
		}

		// Print the current state of the cycle at the top of each loop.
		fmt.Println()
		fmt.Printf("─── %s %dx  (anchor: %s) ───\n",
			task.Capitalize(chosen.CropName), chosen.Trays, anchorLabel)
		fmt.Printf("Sown %s  ·  Move to light %s  ·  Harvest %s\n",
			sow.Format("Mon Jan 02"),
			mtl.Format("Mon Jan 02"),
			harv.Format("Mon Jan 02"),
		)
		fmt.Println()

		// Build the menu. The (b)lackout option is hidden once the crop is
		// already on the lit rack — that stage is already in the past.
		fmt.Println("What would you like to adjust?")
		if inBlackout {
			fmt.Println("  (b)lackout days")
		}
		fmt.Println("  (l)ight days")
		fmt.Println("  (r)etray   — change tray count")
		fmt.Println("  (c)ancel   — abandon this cycle from today forward")
		fmt.Println("  (d)one     — finished adjusting")
		fmt.Println()
		fmt.Print("Choice: ")

		choice, _ := reader.ReadString('\n')
		choice = strings.ToLower(strings.TrimSpace(choice))

		switch choice {
		case "b":
			if !inBlackout {
				fmt.Println("This crop has already moved to light — the blackout stage is done.")
				fmt.Println("Use (l) to adjust light days instead.")
				continue
			}
			doAdjustBlackout(reader, chosen, sow, mtl, harv, anchorSow, today, tasks, cycles, farmEnvs)

		case "l":
			doAdjustLight(reader, chosen, sow, mtl, harv, anchorSow, today, tasks, cycles, farmEnvs)

		case "r":
			adjustRetray(reader, chosen, sow, today, tasks, cycles, farmEnvs)

		case "c":
			cancelCycle(reader, chosen, today, tasks, cycles)
			return // cycle is gone — end the session

		case "d", "":
			fmt.Println("Done.")
			return

		default:
			fmt.Println("Unrecognised choice.")
		}
	}
}

// ─── Branch A: Adjust blackout days ──────────────────────────────────────────
//
// Blackout adjustment changes how many days the crop spends in the dark stage.
//
// Anchor sow (sow is fixed):
//   Adding N days pushes both move-to-light and harvest N days later.
//   Removing N days pulls both N days earlier.
//   Future tasks are deleted and regenerated with a shifted "phantom sow" date —
//   this makes every remaining task's calendar date shift by exactly N days.
//   All regenerated tasks are tagged "adjusted - be mindful".
//
// Anchor harvest (harvest is fixed):
//   Adding N days shifts the sow date N days earlier.
//   Removing N days shifts the sow date N days later.
//   Move-to-light and harvest do not change, so no calendar tasks move.
//   This is "metadata only" — the snapshot will show a different Day N number
//   because Day N is calculated from the sow date, but the calendar is untouched.
func doAdjustBlackout(reader *bufio.Reader, c farm.Cycle, sow, mtl, harv time.Time,
	anchorSow bool, today time.Time, tasks []task.Task, cycles []farm.Cycle, envs []farm.Environment) {

	fmt.Println()
	fmt.Println("Blackout stage adjustment.")
	if anchorSow {
		fmt.Println("Anchor: sow.     Adding days pushes move-to-light and harvest later.")
		fmt.Println("                 Removing days pulls them earlier.")
	} else {
		fmt.Println("Anchor: harvest. Adding days moves the sow date earlier (metadata only).")
		fmt.Println("                 Removing days moves the sow date later.")
		fmt.Println("                 Move-to-light and harvest dates do not change.")
	}
	fmt.Println()

	n, ok := askDays(reader)
	if !ok {
		return
	}

	var newSow, newMTL, newHarv time.Time

	if anchorSow {
		// Sow is fixed. Both MTL and harvest shift by n.
		newSow = sow
		newMTL = mtl.AddDate(0, 0, n)
		newHarv = harv.AddDate(0, 0, n)

		// Validation: the new MTL must still be in the future.
		// If we shift too far back, the move-to-light date would become today
		// or earlier — meaning the transition has already happened, which
		// contradicts the current state of the crop.
		if !newMTL.After(today) {
			maxBack := int(mtl.Sub(today).Hours()/24) - 1
			fmt.Printf("Cannot do that: move-to-light would land on %s, which is today or earlier.\n",
				newMTL.Format("Mon Jan 02"))
			if maxBack > 0 {
				fmt.Printf("Maximum you can remove: %d day(s).\n", maxBack)
			}
			return
		}
		if newHarv.Before(today) {
			fmt.Printf("Cannot do that: harvest would land on %s, which has already passed.\n",
				newHarv.Format("Mon Jan 02"))
			return
		}

	} else {
		// Harvest is fixed. Sow shifts by −n.
		// Adding N blackout days (n > 0): sow moves N earlier (sow − N).
		// Removing N blackout days (n < 0): sow moves N later (sow + N, since −(−N) = +N).
		newSow = sow.AddDate(0, 0, -n)
		newMTL = mtl
		newHarv = harv

		// Validation: if removing blackout days, the sow date moves later.
		// For a crop already in the ground (sow ≤ today), the new sow date
		// cannot move past today — you cannot un-sow a crop that is already
		// growing. For an upcoming crop (sow > today), moving the sow date
		// later is fine — nothing has been planted yet.
		if n < 0 && !sow.After(today) && newSow.After(today) {
			fmt.Printf("Cannot do that: sow date would move to %s, which is in the future.\n",
				newSow.Format("Mon Jan 02"))
			fmt.Println("You cannot un-sow a crop that is already growing.")
			return
		}
	}

	// Show the before/after preview so the grower can see exactly what will change.
	before := buildCycleView(sow, mtl, harv)
	after := buildCycleView(newSow, newMTL, newHarv)
	fmt.Println()
	printAdjustPreview(before, after, today)
	fmt.Println()

	fmt.Print("Confirm? (yes to apply): ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(confirm) != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	if anchorSow {
		// Re-generate future tasks using a phantom sow date shifted by n.
		// "fromDayNum" is tomorrow's day number — we leave today's tasks alone
		// and regenerate everything from tomorrow onward.
		cropDef, ok := loadCropByName(c.CropName)
		if !ok {
			printCropNotFoundError(c.CropName)
			return
		}

		fromDayNum := int(today.Sub(sow).Hours()/24) + 2

		tasks, _ = removeFutureTasks(tasks, c.CycleID, today)

		if fromDayNum <= cropDef.CycleDays {
			shiftedSow := sow.AddDate(0, 0, n)
			newTasks, err := scheduler.ScheduleFromDay(cropDef, shiftedSow.Format(task.DateFormat), fromDayNum, c.Trays, c.CycleID)
			if err != nil {
				fmt.Printf("Error regenerating tasks: %v\n", err)
				os.Exit(1)
			}
			// Every regenerated task has a shifted date — tag them all.
			newTasks = tagAdjusted(newTasks)
			tasks = append(tasks, newTasks...)
		}

		cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
			cy.MoveToLightDate = newMTL.Format(task.DateFormat)
			cy.HarvestDate = newHarv.Format(task.DateFormat)
		})

	} else {
		// Metadata only — update the sow date on the cycle record.
		// No calendar tasks change, so no "adjusted" tags are applied.
		cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
			cy.SowDate = newSow.Format(task.DateFormat)
		})
	}

	saveAndCheck(tasks, cycles, envs)
	fmt.Println()
	if anchorSow {
		fmt.Printf("Updated — move to light: %s → %s  ·  harvest: %s → %s\n",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"),
			harv.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))
	} else {
		fmt.Printf("Updated — sow date: %s → %s  (no task dates changed)\n",
			sow.Format("Mon Jan 02"), newSow.Format("Mon Jan 02"))
	}

	// Offer to apply the same change to other cycles, then offer to update
	// the crops.csv template. Both prompts appear regardless of anchor.
	offerCascade(reader, c, "dark", n, anchorSow, today, envs)
	offerCropsUpdate(reader, c.CropName, "dark", n)
}

// ─── Branch B: Adjust light days ─────────────────────────────────────────────
//
// Light adjustment changes how many days the crop spends on the lit rack.
//
// Anchor sow (sow is fixed):
//   Adding N days pushes only the harvest date N days later.
//   Removing N days pulls harvest N days earlier.
//   Existing light-stage tasks are date-shifted in place.
//   Only tasks whose date actually changes are tagged "adjusted - be mindful".
//
// Anchor harvest (harvest is fixed):
//   Adding N days moves the move-to-light date N days earlier (the crop gets
//   more time on the lit rack, but harvest stays fixed).
//   Removing N days moves move-to-light N days later.
//   Sow shifts by the same amount to preserve the blackout length.
//
//   If the crop is still in blackout (MTL is in the future): tasks are
//   regenerated with a shifted phantom sow so their dates adjust correctly.
//
//   If the crop is already on the lit rack (MTL is in the past): this is
//   "metadata only" — records the corrected move-to-light date without
//   changing any task dates.
func doAdjustLight(reader *bufio.Reader, c farm.Cycle, sow, mtl, harv time.Time,
	anchorSow bool, today time.Time, tasks []task.Task, cycles []farm.Cycle, envs []farm.Environment) {

	fmt.Println()
	fmt.Println("Light stage adjustment.")

	inBlackout := today.Before(mtl)

	if anchorSow {
		fmt.Println("Anchor: sow.     Adding days pushes harvest later; move-to-light stays fixed.")
		fmt.Println("                 Removing days pulls harvest earlier.")
	} else if inBlackout {
		fmt.Println("Anchor: harvest. Adding days moves move-to-light earlier (more light time).")
		fmt.Println("                 Removing days moves move-to-light later.")
		fmt.Println("                 Crop is still in blackout — future tasks will be regenerated.")
	} else {
		fmt.Println("Anchor: harvest. Crop is already on the lit rack.")
		fmt.Println("                 This corrects the recorded move-to-light date.")
		fmt.Println("                 No task dates will change.")
	}
	fmt.Println()

	n, ok := askDays(reader)
	if !ok {
		return
	}

	var newSow, newMTL, newHarv time.Time

	if anchorSow {
		// Sow and MTL stay fixed. Only harvest moves.
		newSow = sow
		newMTL = mtl
		newHarv = harv.AddDate(0, 0, n)

		if newHarv.Before(today) {
			fmt.Printf("Cannot do that: harvest would land on %s, which has already passed.\n",
				newHarv.Format("Mon Jan 02"))
			return
		}

	} else {
		// Harvest stays fixed. MTL moves by −n.
		//   Adding N light days (n > 0):  MTL moves N earlier (MTL − N).
		//   Removing N light days (n < 0): MTL moves N later  (MTL + N, since −(−N) = +N).
		// Sow shifts by the same amount to preserve the blackout length.
		shiftN := -n
		newSow = sow.AddDate(0, 0, shiftN)
		newMTL = mtl.AddDate(0, 0, shiftN)
		newHarv = harv

		if inBlackout {
			// MTL is still in the future — make sure it stays usable.
			if !newMTL.After(today) {
				maxAdd := int(mtl.Sub(today).Hours()/24) - 1
				fmt.Printf("Cannot do that: move-to-light would land on %s, which is today or earlier.\n",
					newMTL.Format("Mon Jan 02"))
				if maxAdd > 0 {
					fmt.Printf("Maximum you can add: %d day(s).\n", maxAdd)
				}
				return
			}
		}
	}

	// Show the before/after preview.
	before := buildCycleView(sow, mtl, harv)
	after := buildCycleView(newSow, newMTL, newHarv)
	fmt.Println()
	printAdjustPreview(before, after, today)
	fmt.Println()

	fmt.Print("Confirm? (yes to apply): ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(confirm) != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	if anchorSow {
		// Date-shift existing light-stage future tasks only.
		// A task is in the light stage if its date is on or after the MTL date.
		// Blackout tasks that are still in the future are left untouched.
		mtlStr := mtl.Format(task.DateFormat)
		todayStr := today.Format(task.DateFormat)
		for i := range tasks {
			t := &tasks[i]
			if t.CycleID != c.CycleID {
				continue
			}
			if t.Date <= todayStr {
				continue // leave today's and past tasks alone
			}
			if t.Date < mtlStr {
				continue // leave blackout tasks alone
			}
			// This is a future light-stage task — shift its date.
			d, parseErr := time.Parse(task.DateFormat, t.Date)
			if parseErr != nil {
				continue
			}
			newDate := d.AddDate(0, 0, n).Format(task.DateFormat)
			if newDate != t.Date {
				// The date moved — add the reminder note.
				t.Date = newDate
				if !strings.Contains(t.Notes, "adjusted - be mindful") {
					t.Notes += "\nadjusted - be mindful"
				}
			}
		}
		cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
			cy.HarvestDate = newHarv.Format(task.DateFormat)
		})

	} else if inBlackout {
		// Anchor harvest, crop still in blackout — re-generate future tasks
		// using the shifted phantom sow so task dates move to the right positions.
		cropDef, ok := loadCropByName(c.CropName)
		if !ok {
			printCropNotFoundError(c.CropName)
			return
		}

		shiftN := -n
		shiftedSow := sow.AddDate(0, 0, shiftN)
		fromDayNum := int(today.Sub(sow).Hours()/24) + 2

		tasks, _ = removeFutureTasks(tasks, c.CycleID, today)

		if fromDayNum <= cropDef.CycleDays {
			newTasks, err := scheduler.ScheduleFromDay(cropDef, shiftedSow.Format(task.DateFormat), fromDayNum, c.Trays, c.CycleID)
			if err != nil {
				fmt.Printf("Error regenerating tasks: %v\n", err)
				os.Exit(1)
			}
			newTasks = tagAdjusted(newTasks)
			tasks = append(tasks, newTasks...)
		}

		cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
			cy.SowDate = newSow.Format(task.DateFormat)
			cy.MoveToLightDate = newMTL.Format(task.DateFormat)
		})

	} else {
		// Anchor harvest, already in light — metadata only.
		// Records the corrected move-to-light date. No tasks change.
		cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
			cy.MoveToLightDate = newMTL.Format(task.DateFormat)
		})
	}

	saveAndCheck(tasks, cycles, envs)
	fmt.Println()
	if anchorSow {
		fmt.Printf("Updated — harvest: %s → %s\n",
			harv.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))
		offerCascade(reader, c, "light", n, anchorSow, today, envs)
		offerCropsUpdate(reader, c.CropName, "light", n)
	} else if inBlackout {
		fmt.Printf("Updated — move to light: %s → %s  (tasks regenerated)\n",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"))
		offerCascade(reader, c, "light", n, anchorSow, today, envs)
		offerCropsUpdate(reader, c.CropName, "light", n)
	} else {
		// Metadata-only correction of the recorded move-to-light date.
		// No real schedule change happened, so no cascade or template update.
		fmt.Printf("Updated — move-to-light corrected: %s → %s  (no task dates changed)\n",
			mtl.Format("Mon Jan 02"), newMTL.Format("Mon Jan 02"))
	}
}

// ─── Branch C: Retray ─────────────────────────────────────────────────────────
//
// Changing the tray count means every future task title and notes line needs
// to show the new number. That requires regenerating the tasks from the crop
// template. Dates stay the same — only the content changes.
func adjustRetray(reader *bufio.Reader, c farm.Cycle, sow time.Time,
	today time.Time, tasks []task.Task, cycles []farm.Cycle, envs []farm.Environment) {

	fmt.Println()
	fmt.Printf("New tray count (currently %d): ", c.Trays)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	newTrays, err := strconv.Atoi(input)
	if err != nil || newTrays < 1 {
		fmt.Println("Invalid number — must be at least 1. Cancelled.")
		return
	}

	if newTrays > c.Trays {
		fmt.Printf("You are increasing trays from %d to %d.\n", c.Trays, newTrays)
		fmt.Println("Make sure you have the physical trays available.")
	}

	fmt.Printf("\nChange tray count %d → %d.\n", c.Trays, newTrays)
	fmt.Print("Confirm? (yes to apply): ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(confirm) != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	cropDef, ok := loadCropByName(c.CropName)
	if !ok {
		printCropNotFoundError(c.CropName)
		return
	}

	// fromDayNum is tomorrow's day number — we regenerate everything from
	// tomorrow onward with the new tray count.
	fromDayNum := int(today.Sub(sow).Hours()/24) + 2

	tasks, removedCount := removeFutureTasks(tasks, c.CycleID, today)

	if fromDayNum > cropDef.CycleDays {
		fmt.Printf("Removed %d future task(s). This cycle is on its last day — no tasks to regenerate.\n", removedCount)
	} else {
		newTasks, err := scheduler.ScheduleFromDay(cropDef, c.SowDate, fromDayNum, newTrays, c.CycleID)
		if err != nil {
			fmt.Printf("Error regenerating tasks: %v\n", err)
			os.Exit(1)
		}
		tasks = append(tasks, newTasks...)
	}

	// Update tray count and expected yield on the cycle record.
	cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
		cy.Trays = newTrays
		cy.ExpectedGrams = cropDef.YieldGrams * newTrays
	})

	saveAndCheck(tasks, cycles, envs)
	fmt.Printf("\nDone. %s updated: %d → %d trays.\n",
		task.Capitalize(c.CropName), c.Trays, newTrays)
}

// ─── Branch D: Cancel ─────────────────────────────────────────────────────────
//
// Cancelling removes all future tasks for this cycle and removes the Cycle
// record from cycles.json (so the snapshot stops showing it). Past tasks are
// kept as a calendar record of what was done up to the point of cancellation.
func cancelCycle(reader *bufio.Reader, c farm.Cycle, today time.Time,
	tasks []task.Task, cycles []farm.Cycle) {

	harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
	sow, _ := time.Parse(task.DateFormat, c.SowDate)

	fmt.Println()
	fmt.Printf("Cancel %s %dx (sown %s, harvest %s)?\n",
		task.Capitalize(c.CropName), c.Trays,
		sow.Format("Mon Jan 02"), harv.Format("Mon Jan 02"))
	fmt.Println("Future tasks will be deleted. Past tasks are kept as calendar history.")
	fmt.Print("Type \"yes\" to confirm: ")

	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(confirm) != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	tasks, removedCount := removeFutureTasks(tasks, c.CycleID, today)

	// Remove the Cycle record so the snapshot no longer shows this batch.
	var keptCycles []farm.Cycle
	for _, cy := range cycles {
		if cy.CycleID != c.CycleID {
			keptCycles = append(keptCycles, cy)
		}
	}

	if err := store.Save(tasks); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}
	if err := farm.SaveCycles(keptCycles); err != nil {
		fmt.Printf("Error saving cycles: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n%s %dx cycle cancelled. %d future task(s) deleted.\n",
		task.Capitalize(c.CropName), c.Trays, removedCount)
	// No conflict check needed: removing a cycle can only reduce conflicts.
}

// ─── Preview helpers ──────────────────────────────────────────────────────────

// buildCycleView returns a day-by-day timeline from sow to harvest (inclusive),
// labelling each day by its role in the cycle.
//
// It only needs the three boundary dates (sow, mtl, harvest) — it does not
// need the full crop template. This makes it fast and usable for both
// "before" and "after" states.
func buildCycleView(sow, mtl, harvest time.Time) []scheduleView {
	var result []scheduleView
	for d := sow; !d.After(harvest); d = d.AddDate(0, 0, 1) {
		// Day number: sow = Day 1, so each subsequent day adds 1.
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

		result = append(result, scheduleView{Date: d, Label: label})
	}
	return result
}

// printAdjustPreview prints a side-by-side table comparing the before and
// after schedules, aligned by calendar date. Today is marked with "◄ today".
// Dates that exist in one schedule but not the other show a dashed placeholder.
//
// Example output:
//
//	                   Before                   After
//	                   ──────────────────────   ──────────────────────
//	  Sun Mar 08       ───────────────────      Day 1 (sow)
//	  Mon Mar 09       Day 1 (sow)              Day 2 (dark)
//	  Tue Mar 11       Day 3 (dark)             Day 4 (dark)  ◄ today
func printAdjustPreview(before, after []scheduleView, today time.Time) {
	// Build lookup maps from date string → label for fast access.
	beforeMap := make(map[string]string)
	for _, sv := range before {
		beforeMap[sv.Date.Format(task.DateFormat)] = sv.Label
	}
	afterMap := make(map[string]string)
	for _, sv := range after {
		afterMap[sv.Date.Format(task.DateFormat)] = sv.Label
	}

	// Find the full date range to display — the union of both schedules.
	startDate := before[0].Date
	if after[0].Date.Before(startDate) {
		startDate = after[0].Date
	}
	endDate := before[len(before)-1].Date
	if after[len(after)-1].Date.After(endDate) {
		endDate = after[len(after)-1].Date
	}

	// Column widths. The label column is wide enough for typical day labels.
	const colW = 22
	const empty = "───────────────────"

	fmt.Printf("  %-14s  %-*s  %-*s\n", "", colW, "Before", colW, "After")
	fmt.Printf("  %-14s  %-*s  %-*s\n", "",
		colW, strings.Repeat("─", colW),
		colW, strings.Repeat("─", colW))

	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		key := d.Format(task.DateFormat)

		bLabel, bOk := beforeMap[key]
		aLabel, aOk := afterMap[key]

		bStr := empty
		if bOk {
			bStr = bLabel
		}
		aStr := empty
		if aOk {
			aStr = aLabel
		}

		todayMark := ""
		if d.Equal(today) {
			todayMark = "  ◄ today"
		}

		fmt.Printf("  %-14s  %-*s  %-*s%s\n",
			d.Format("Mon Jan 02"),
			colW, bStr,
			colW, aStr,
			todayMark,
		)
	}
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

// askDays prompts the grower to choose (a)dd or (r)emove, then asks how many
// days. Returns the net shift as a signed integer (positive = add, negative =
// remove) and true on success, or 0 and false if the input was invalid.
func askDays(reader *bufio.Reader) (int, bool) {
	fmt.Print("(a)dd or (r)emove days? ")
	dirInput, _ := reader.ReadString('\n')
	dir := strings.ToLower(strings.TrimSpace(dirInput))
	if dir != "a" && dir != "r" {
		fmt.Println("Invalid — cancelled.")
		return 0, false
	}

	fmt.Print("How many days? ")
	numInput, _ := reader.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(numInput))
	if err != nil || n <= 0 {
		fmt.Println("Please enter a positive whole number — cancelled.")
		return 0, false
	}

	if dir == "r" {
		n = -n // remove = negative shift
	}
	return n, true
}

// tagAdjusted appends "adjusted - be mindful" to the Notes of every task in
// the slice. Used after regenerating tasks whose dates have all shifted, so
// the grower knows these entries were touched by an adjustment.
func tagAdjusted(tasks []task.Task) []task.Task {
	for i := range tasks {
		if !strings.Contains(tasks[i].Notes, "adjusted - be mindful") {
			tasks[i].Notes += "\nadjusted - be mindful"
		}
	}
	return tasks
}

// findCycleByID looks up a cycle by its CycleID in the list.
// Returns the cycle and true if found; an empty Cycle and false if not.
// Used at the top of the adjustment loop to re-find the chosen cycle
// after each operation reloads data from disk.
func findCycleByID(cycles []farm.Cycle, id string) (farm.Cycle, bool) {
	for _, c := range cycles {
		if c.CycleID == id {
			return c, true
		}
	}
	return farm.Cycle{}, false
}

// removeFutureTasks returns a new task list with all tasks for the given
// cycleID whose date is strictly after today removed.
// Also returns how many tasks were removed, for reporting to the user.
func removeFutureTasks(tasks []task.Task, cycleID string, today time.Time) ([]task.Task, int) {
	todayStr := today.Format(task.DateFormat)
	var kept []task.Task
	removed := 0
	for _, t := range tasks {
		// Keep the task if it belongs to a different cycle, OR if it is today
		// or in the past (we preserve today and all earlier calendar entries
		// as a record of what the grower has already done).
		if t.CycleID != cycleID || t.Date <= todayStr {
			kept = append(kept, t)
		} else {
			removed++
		}
	}
	return kept, removed
}

// updateCycle finds the cycle with the given cycleID in the list and applies
// the provided update function to it. Returns the updated list.
// If no matching cycle is found, the list is returned unchanged.
func updateCycle(cycles []farm.Cycle, cycleID string, update func(*farm.Cycle)) []farm.Cycle {
	for i := range cycles {
		if cycles[i].CycleID == cycleID {
			update(&cycles[i])
			return cycles
		}
	}
	return cycles
}

// saveAndCheck saves tasks and cycles to disk, then runs the conflict checker
// and prints any warnings. Called after every successful adjustment.
func saveAndCheck(tasks []task.Task, cycles []farm.Cycle, envs []farm.Environment) {
	if err := store.Save(tasks); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}
	if err := farm.SaveCycles(cycles); err != nil {
		fmt.Printf("Error saving cycles: %v\n", err)
		os.Exit(1)
	}

	// Re-run the conflict checker now that the schedule has changed.
	// This catches cases where an adjustment has created a new slot overlap.
	warnings := checker.Check(envs, cycles)
	if len(warnings) > 0 {
		fmt.Println("\nConflict warnings after adjustment:")
		for _, w := range warnings {
			fmt.Println(" !", w)
		}
	} else {
		fmt.Println("\nNo conflicts detected.")
	}
}

// loadCropByName looks up a crop variety by name in crops.csv.
// Returns the Crop value and true if found; an empty Crop and false if not.
func loadCropByName(name string) (crop.Crop, bool) {
	path, err := crop.CropsFilePath()
	if err != nil {
		return crop.Crop{}, false
	}
	crops, err := crop.CSVSource{Path: path}.LoadCrops()
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

// printCropNotFoundError prints a clear, friendly error when a cycle's crop
// name can no longer be found in crops.csv. This can happen if the crop
// library was edited after the cycle was planned.
func printCropNotFoundError(name string) {
	fmt.Printf("Error: crop %q not found in crops.csv.\n", name)
	fmt.Println("The crop library may have changed since this batch was planned.")
	fmt.Println("Adjustments that need the crop template (blackout days, retray) cannot run.")
	fmt.Println("You can still use (c)ancel to remove the cycle from the farm view.")
}

// ─── Propagation helpers ──────────────────────────────────────────────────────

// offerCascade asks the grower whether to apply the same adjustment to all
// other active or upcoming cycles of the same crop.
//
// "Active or upcoming" means harvest > today — cycles that haven't been
// fully harvested yet. The cycle that was just adjusted (chosenCycle) is
// excluded so we don't double-apply the shift.
//
// The anchor (anchorSow) carries through: if the original adjustment kept the
// sow date fixed, the cascade also keeps sow fixed on every target cycle.
// If the original kept harvest fixed, the cascade keeps harvest fixed.
//
// Only tasks with date > today are ever touched — past tasks are permanent
// calendar history and are never moved.
func offerCascade(reader *bufio.Reader, chosenCycle farm.Cycle,
	stage string, n int, anchorSow bool, today time.Time, envs []farm.Environment) {

	// Reload fresh data from disk. The main adjustment was already saved by
	// saveAndCheck before this function is called, so reloading guarantees
	// we are working from the correct current state.
	cycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Error reloading cycles for cascade: %v\n", err)
		return
	}
	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error reloading tasks for cascade: %v\n", err)
		return
	}

	// Find all other cycles of the same crop that have not yet been harvested.
	var targets []farm.Cycle
	for _, c := range cycles {
		if c.CycleID == chosenCycle.CycleID {
			continue // skip the cycle we just adjusted
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
		// Nothing to cascade to — skip the prompt entirely so the grower
		// isn't asked a question that has no effect.
		return
	}

	// Describe the adjustment in plain English for the confirmation prompt.
	absN := n
	if absN < 0 {
		absN = -absN
	}
	dirWord := "add"
	if n < 0 {
		dirWord = "remove"
	}
	stageWord := stage + " day"
	if absN != 1 {
		stageWord = stage + " days"
	}

	fmt.Println()
	fmt.Printf("Apply this adjustment (%s %d %s) to %d other %s cycle(s)?\n",
		dirWord, absN, stageWord, len(targets), task.Capitalize(chosenCycle.CropName))
	fmt.Println()
	for _, c := range targets {
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		fmt.Printf("  %dx  sown %s  harvest %s\n",
			c.Trays, sow.Format("Mon Jan 02"), harv.Format("Mon Jan 02"))
	}
	fmt.Println()
	fmt.Print("(yes to apply, anything else to skip): ")
	input, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(input)) != "yes" {
		fmt.Println("Skipped.")
		return
	}

	updated := 0
	skipped := 0
	todayStr := today.Format(task.DateFormat)

	for _, c := range targets {
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mtl, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		sowStr := sow.Format(task.DateFormat)
		mtlStr := mtl.Format(task.DateFormat)
		harvStr := harv.Format(task.DateFormat)

		// Compute new key dates using the same anchor-aware arithmetic as
		// the original adjustment. Whichever end was immovable for the live
		// batch stays immovable for every other batch too.
		var newSow, newMTL, newHarv time.Time
		switch {
		case anchorSow && stage == "dark":
			// Sow fixed. MTL and harvest both shift.
			newSow = sow
			newMTL = mtl.AddDate(0, 0, n)
			newHarv = harv.AddDate(0, 0, n)
		case anchorSow && stage == "light":
			// Sow and MTL fixed. Only harvest shifts.
			newSow = sow
			newMTL = mtl
			newHarv = harv.AddDate(0, 0, n)
		case !anchorSow && stage == "dark":
			// Harvest and MTL fixed. Only sow shifts (metadata for past sow dates).
			newSow = sow.AddDate(0, 0, -n)
			newMTL = mtl
			newHarv = harv
		case !anchorSow && stage == "light":
			// Harvest fixed. MTL and sow both shift earlier.
			newSow = sow.AddDate(0, 0, -n)
			newMTL = mtl.AddDate(0, 0, -n)
			newHarv = harv
		}

		// Skip this cycle if the adjustment would push harvest into the past —
		// that would leave the grower with an impossible schedule.
		if !newHarv.After(today) {
			fmt.Printf("  Skipped %s %dx (sown %s) — adjustment would push harvest to %s.\n",
				task.Capitalize(c.CropName), c.Trays,
				sow.Format("Mon Jan 02"), newHarv.Format("Mon Jan 02"))
			skipped++
			continue
		}

		// Shift the relevant future tasks in-memory.
		//
		// Which tasks shift and in which direction depends on the anchor and
		// stage, mirroring the four cases in the table above.
		for i := range tasks {
			t := &tasks[i]
			if t.CycleID != c.CycleID {
				continue
			}
			if t.Date <= todayStr {
				continue // never touch today's or past tasks
			}

			var shouldShift bool
			shift := n

			switch {
			case anchorSow && stage == "dark":
				// All post-sow tasks shift (dark, light, harvest all move).
				shouldShift = t.Date > sowStr
				shift = n
			case anchorSow && stage == "light":
				// Only tasks after MTL shift (light days and harvest move).
				shouldShift = t.Date > mtlStr
				shift = n
			case !anchorSow && stage == "dark":
				// For cycles already in blackout: shifting dark tasks backward
				// would push them into the past — the newDate.Before(today) guard
				// below silently skips those, making this effectively metadata-only
				// for in-progress batches (matching the single-cycle behaviour).
				// For upcoming cycles: all dark tasks are in the future, so they
				// do shift to stay consistent with the new (earlier) sow date.
				shouldShift = t.Date < mtlStr
				shift = -n
			case !anchorSow && stage == "light":
				// All non-harvest tasks shift (sow, dark, light move; harvest stays).
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

			// Only apply the shift if the new date is still today or later.
			// A large removal could theoretically push a task into the past.
			if newDate.Before(today) {
				continue
			}

			t.Date = newDate.Format(task.DateFormat)
			if !strings.Contains(t.Notes, "adjusted - be mindful") {
				t.Notes += "\nadjusted - be mindful"
			}
		}

		// Update the cycle record with the new key dates.
		cycles = updateCycle(cycles, c.CycleID, func(cy *farm.Cycle) {
			cy.SowDate = newSow.Format(task.DateFormat)
			cy.MoveToLightDate = newMTL.Format(task.DateFormat)
			cy.HarvestDate = newHarv.Format(task.DateFormat)
		})

		updated++
	}

	// Save all changes and re-run the conflict checker so the grower can
	// see immediately whether the cascade created any new slot conflicts.
	saveAndCheck(tasks, cycles, envs)
	fmt.Println()
	if skipped > 0 {
		fmt.Printf("%d %s cycle(s) updated, %d skipped.\n",
			updated, task.Capitalize(chosenCycle.CropName), skipped)
	} else {
		fmt.Printf("%d %s cycle(s) updated.\n", updated, task.Capitalize(chosenCycle.CropName))
	}
}

// offerCropsUpdate asks the grower whether to update the crop's template in
// crops.csv to permanently reflect the adjusted stage length.
//
// If they say yes, it calls ModifyCropDays to insert or remove the appropriate
// day rows from the file. The change takes effect immediately: any future
// "greenies plan" run for this crop variety will use the new day count.
func offerCropsUpdate(reader *bufio.Reader, cropName, stage string, n int) {
	fmt.Println()
	fmt.Printf("Update the %s template in crops.csv? ", task.Capitalize(cropName))
	fmt.Print("(yes to apply, anything else to skip): ")
	input, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(input)) != "yes" {
		fmt.Println("Skipped.")
		return
	}

	path, err := crop.CropsFilePath()
	if err != nil {
		fmt.Printf("Error locating crops.csv: %v\n", err)
		return
	}

	// Load the crop before modifying so we can show an old → new count in
	// the confirmation message.
	oldCropDef, found := loadCropByName(cropName)
	var oldCount int
	if found {
		if stage == "dark" {
			oldCount = oldCropDef.DarkDays
		} else {
			oldCount = oldCropDef.LightDays
		}
	}

	if err := crop.ModifyCropDays(path, cropName, stage, n); err != nil {
		fmt.Printf("Error updating crops.csv: %v\n", err)
		return
	}

	colName := "dark_days"
	if stage == "light" {
		colName = "light_days"
	}
	fmt.Printf("%s template updated — %s: %d → %d\n",
		task.Capitalize(cropName), colName, oldCount, oldCount+n)
	fmt.Println("Open crops.csv in Google Sheets to review the new day rows.")
}
