package main

import (
	"bufio" // for reading a full line of user input from the terminal
	"fmt"
	"math"    // for math.Ceil, which rounds a decimal up to the next whole number
	"os"      // for os.Exit
	"strconv" // for converting text like "2" into the number 2
	"strings" // for string utilities used throughout
	"time"

	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// runPlan handles the "greenies plan" command.
// It asks the user a series of questions interactively, then shows a full
// preview and asks for confirmation before saving anything.
func runPlan() {
	// Load the farm layout config so we can offer environment choices when the
	// user confirms their plan. We load it early — at the start of the function
	// — so any config error surfaces before the user types all their answers.
	farmEnvs, err := farm.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading farm config: %v\n", err)
		os.Exit(1)
	}

	// Extract only the lit environments (blackout is automatic and not a choice).
	var litEnvs []farm.Environment
	for _, e := range farmEnvs {
		if e.Type == "lit" {
			litEnvs = append(litEnvs, e)
		}
	}

	// Load the crop library up front so we can show the available varieties
	// before asking which one the user wants.
	source, err := crop.GetSource()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	crops, err := source.LoadCrops()
	if err != nil {
		fmt.Printf("Error loading crop library: %v\n", err)
		os.Exit(1)
	}

	// bufio.NewReader wraps os.Stdin (the keyboard) so we can read a full
	// line of input at once, including any spaces in the crop name.
	reader := bufio.NewReader(os.Stdin)

	// Helper that prints a prompt and returns whatever the user typed,
	// with leading/trailing whitespace stripped.
	//
	// If the user types "cancel" (in any capitalisation) at any prompt,
	// the program exits immediately without saving anything. This lets the
	// grower bail out of a multi-step plan at any point without having to
	// finish the whole flow first.
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		v := strings.TrimSpace(line)
		if strings.EqualFold(v, "cancel") {
			fmt.Println("\nPlan cancelled — nothing was saved.")
			os.Exit(0)
		}
		return v
	}

	// --- Upfront choice: single cycle or a harvest batch? ---
	// A harvest batch lets the grower plan multiple crop varieties that all
	// share the same harvest date — ideal for planning a farmers market day.
	// A single cycle is the existing flow for planning one crop at a time.
	planType := ask("Plan a (c)ycle or a (b)atch harvest day? [c]: ")
	if strings.ToLower(planType) == "b" || strings.EqualFold(planType, "batch") {
		runBatchPlan(reader, ask, farmEnvs, litEnvs, crops)
		return
	}

	// --- Question 1: which crop? ---
	// Show a numbered list so the user can pick by number, by full name, or by
	// any unique prefix — whichever is quickest to type.
	fmt.Println("Available crops:")
	for i, c := range crops {
		// Wrap the first letter in parentheses to hint that it can be used
		// as a shortcut — e.g. "(s)unnies" means typing "s" selects sunnies.
		fmt.Printf("  %d. (%c)%s\n", i+1, c.Name[0], c.Name[1:])
	}
	fmt.Println()

	cropInput := ask("Which crop? (name, number, or unique prefix): ")
	if cropInput == "" {
		fmt.Println("No crop entered — cancelled.")
		return
	}

	// findCrop resolves the user's input to one crop, or explains the problem.
	// It accepts:
	//   - a number ("1", "2", …) — picks the crop at that position in the list
	//   - a full name or any prefix ("sun", "p", "daikon") — matches by prefix
	//     and succeeds only if exactly one crop matches
	found := func() *crop.Crop {
		// Try it as a number first.
		if n, err := strconv.Atoi(cropInput); err == nil {
			if n >= 1 && n <= len(crops) {
				return &crops[n-1]
			}
			fmt.Printf("Number %d is out of range — pick 1 to %d.\n", n, len(crops))
			return nil
		}

		// Otherwise match by prefix (case-insensitive).
		// Collect every crop whose name starts with what the user typed.
		var matches []*crop.Crop
		lower := strings.ToLower(cropInput)
		for i := range crops {
			if strings.HasPrefix(strings.ToLower(crops[i].Name), lower) {
				matches = append(matches, &crops[i])
			}
		}

		switch len(matches) {
		case 0:
			fmt.Printf("No crop found matching %q. Check the spelling and try again.\n", cropInput)
			return nil
		case 1:
			return matches[0]
		default:
			// Two or more crops share this prefix — the user needs to be more specific.
			fmt.Printf("%q matches more than one crop:\n", cropInput)
			for _, m := range matches {
				fmt.Printf("  %s\n", m.Name)
			}
			fmt.Println("Please type more letters, or use the number from the list.")
			return nil
		}
	}()

	if found == nil {
		os.Exit(1)
	}

	// --- Question 2: plan by tray count or desired yield? ---
	// "Tray count" mode is straightforward — the grower knows how many trays
	// they want to plant. "Yield" mode works backwards: the grower says how
	// many grams they need and the program figures out how many trays to plant.
	planMode := ask("Plan by (t)ray count or (y)ield target? [t/y]: ")

	var trays int

	switch strings.ToLower(planMode) {
	case "y", "yield":
		// Make sure this crop has yield data before trying to use it.
		if found.YieldGrams == 0 {
			fmt.Printf("No yield data found for %s in the crop library.\n", task.Capitalize(found.Name))
			fmt.Println("Add a yield_grams value to crops.csv, or plan by tray count instead.")
			os.Exit(1)
		}

		pcfg, _ := config.Load()
		pwl := pcfg.WeightLabel()
		yieldStr := ask(fmt.Sprintf("Desired yield in %s? (%s yields ~%d%s per tray): ",
			pwl, task.Capitalize(found.Name), found.YieldGrams, pwl))
		desiredYield, err := strconv.Atoi(yieldStr)
		if err != nil || desiredYield < 1 {
			fmt.Println("Please enter a whole number greater than zero (e.g. 500).")
			os.Exit(1)
		}

		// Divide the target yield by the per-tray yield and round up.
		// Rounding up ensures the grower meets or exceeds their target —
		// for example, 700g ÷ 500g/tray = 1.4, which rounds up to 2 trays
		// (yielding ~1000g, which covers the 700g need with some surplus).
		trays = int(math.Ceil(float64(desiredYield) / float64(found.YieldGrams)))

		fmt.Printf("→ %d %s needed to yield ~%d%s (target: %d%s)\n",
			trays, task.TrayWord(trays), trays*found.YieldGrams, pwl, desiredYield, pwl)

	default:
		// "t", "tray", blank, or anything unrecognised — default to tray count.
		traysStr := ask("How many trays? ")
		n, err := strconv.Atoi(traysStr)
		if err != nil || n < 1 {
			fmt.Println("Please enter a whole number greater than zero (e.g. 2).")
			os.Exit(1)
		}
		trays = n
	}

	// --- Question 3: plan direction — from harvest date or sow date? ---
	// Backward (harvest date) is useful when you have a market day or deadline
	// and need to work out when to sow. Forward (sow date) is useful when you
	// know you have trays free on a specific day and want to know when to expect
	// the harvest.
	directionInput := ask("Plan from (h)arvest date or (s)ow date? [h/s]: ")

	// fromHarvest is true if the grower wants to plan backward from a harvest date.
	// It is false if they want to plan forward from a sow date.
	// Blank input or anything unrecognised defaults to harvest (backward) mode.
	fromHarvest := strings.ToLower(directionInput) != "s" && strings.ToLower(directionInput) != "sow"

	var preview []scheduler.ScheduledDay
	var newTasks []task.Task
	var displayDate string // the date shown in the preview header

	if !fromHarvest {
		// Forward scheduling: the grower enters the sow date (Day 1).
		// For overnight-soak crops, Day 0 is automatically placed the day before.
		sowDate, err := parseDate(ask("Sow date (MM-DD or YYYY-MM-DD): "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		displayDate = sowDate

		preview, newTasks, err = scheduler.ScheduleForward(*found, sowDate, trays)
		if err != nil {
			fmt.Printf("Error generating schedule: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Backward scheduling: the grower enters the harvest date and the program
		// counts backward to find the sow date and every day in between.
		harvestDate, err := parseDate(ask("Harvest date (MM-DD or YYYY-MM-DD): "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		displayDate = harvestDate

		preview, newTasks, err = scheduler.Schedule(*found, harvestDate, trays)
		if err != nil {
			fmt.Printf("Error generating schedule: %v\n", err)
			os.Exit(1)
		}
	}

	// Show the full preview so the user can check everything looks right.
	trayWord := task.TrayWord(trays)
	// The header shows the anchor date — harvest date for backward scheduling,
	// sow date for forward scheduling — so the user knows what they entered.
	anchorLabel := "harvest"
	if !fromHarvest {
		anchorLabel = "sow"
	}
	fmt.Printf("\n%s — %d %s — %s %s\n\n",
		task.Capitalize(found.Name), trays, trayWord, anchorLabel, displayDate)

	for _, d := range preview {
		tasks := d.CropDay.Tasks
		if tasks == "" {
			tasks = "(no tasks)"
		}
		fmt.Printf("  %s  Day %-2d  %-8s  %s\n",
			d.Date, d.CropDay.Day, d.CropDay.Stage, tasks)
	}

	fmt.Println()

	// Ask for confirmation before writing anything to disk.
	response := ask("Add these tasks to the calendar? Type \"yes\" to confirm: ")
	if response != "yes" {
		fmt.Println("Cancelled — nothing was saved.")
		return
	}

	// --- Extract key dates from the preview ---
	// The sow date (Day 1) and harvest date (last day) are needed to create the
	// cycle record that powers "greenies snapshot". We read them from the preview
	// slice rather than asking the user again.
	//
	// For crops with an overnight soak, the preview starts with Day 0 (the soak
	// reminder) followed by Day 1. We skip Day 0 and find Day 1.
	var sowDateStr string
	for _, d := range preview {
		if d.CropDay.Day == 1 {
			sowDateStr = d.Date
			break
		}
	}
	harvestDateStr := preview[len(preview)-1].Date

	// Parse the sow date so we can do date arithmetic for the cycle record.
	// Ignoring the error here is safe: sowDateStr was produced by the scheduler,
	// which already validated and formatted it correctly.
	baseSow, _ := time.Parse(task.DateFormat, sowDateStr)
	baseHarvest, _ := time.Parse(task.DateFormat, harvestDateStr)

	// Compute the move-to-light date: the first day trays spend on a lit rack.
	//
	// Day 1 is the sow day. Days 2 through DarkDays+1 are dark days.
	// Day DarkDays+2 is the first light day.
	// Days from sow to first light = DarkDays + 1.
	//
	// Example: sunnies DarkDays=4 → first light = Day 6 = sow + 5 days ✓
	// Example: peas    DarkDays=3 → first light = Day 5 = sow + 4 days ✓
	//
	// NOTE: This formula has been verified on paper. Test it carefully with
	// greenies snapshot after planning real cycles.
	baseMoveToLight := baseSow.AddDate(0, 0, found.DarkDays+1)

	// --- Question 4: which lit environment? ---
	// Every tray goes through the blackout room first — that's automatic.
	// This question asks where the trays are headed once they move to light.
	var litEnv string

	if len(litEnvs) == 0 {
		// No lit environments are configured in farm.csv.
		// Default to "any" so the cycle record is still saved.
		litEnv = "any"
	} else {
		// Build a compact prompt from the list of lit environments in the config.
		// Example: "Which lit environment? (1) main tent / (2) test tent / (a) any [a]: "
		var promptParts []string
		for i, e := range litEnvs {
			promptParts = append(promptParts, fmt.Sprintf("(%d) %s", i+1, farm.DisplayName(e.Name)))
		}
		promptParts = append(promptParts, "(a) any")
		envInput := ask("Which lit environment? " + strings.Join(promptParts, " / ") + " [a]: ")
		litEnv = resolveLitEnv(envInput, litEnvs)
	}

	// Create the cycle record for this base planning session.
	// This is what "greenies snapshot" reads to know what is on the farm.
	var newCycleRecords []farm.Cycle
	newCycleRecords = append(newCycleRecords, farm.Cycle{
		CycleID:         newTasks[0].CycleID,
		CropName:        found.Name,
		Trays:           trays,
		SowDate:         sowDateStr,
		HarvestDate:     harvestDateStr,
		MoveToLightDate: baseMoveToLight.Format(task.DateFormat),
		LitEnvironment:  litEnv,
		// Store the expected yield at plan time so the harvest log can show
		// expected vs actual without needing to re-read the crop library later.
		ExpectedGrams: found.YieldGrams * trays,
	})

	// --- Optional: weekly repetition ---
	// Many growers sow the same crop every week to maintain a steady harvest
	// rhythm. If the user wants repeats, we generate additional copies of the
	// schedule, each shifted forward by one week (7 days) at a time.
	// Blank input defaults to 1 additional week; type 0 to skip repeating.
	allNewTasks := newTasks // start with the first cycle

	weeksStr := ask("Repeat weekly? Additional weeks (0 to skip) [1]: ")
	additionalWeeks := 1 // default: one additional week
	if weeksStr != "" {
		w, convErr := strconv.Atoi(weeksStr)
		if convErr != nil || w < 0 {
			fmt.Println("Please enter a whole number (e.g. 3), or 0 to skip.")
			os.Exit(1)
		}
		additionalWeeks = w
	}

	if additionalWeeks > 0 {

		// Parse the anchor date (sow or harvest) so we can shift it by weeks.
		// Safe to ignore the error here — displayDate was already validated by
		// parseDate earlier in this function, so we know it's a valid date.
		baseDate, _ := time.Parse(task.DateFormat, displayDate)

		for week := 1; week <= additionalWeeks; week++ {
			// Shift the anchor date forward by 7 days per additional week.
			weeklyDate := baseDate.AddDate(0, 0, week*7).Format(task.DateFormat)

			// Use the same scheduling direction as the first cycle.
			var weekTasks []task.Task
			if !fromHarvest {
				_, weekTasks, err = scheduler.ScheduleForward(*found, weeklyDate, trays)
			} else {
				_, weekTasks, err = scheduler.Schedule(*found, weeklyDate, trays)
			}
			if err != nil {
				fmt.Printf("Error generating week %d schedule: %v\n", week, err)
				os.Exit(1)
			}
			allNewTasks = append(allNewTasks, weekTasks...)

			// Create a cycle record for this week's repeat.
			// All dates for week i are shifted by exactly i×7 days from the base.
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
				LitEnvironment:  litEnv,
				ExpectedGrams:   found.YieldGrams * trays,
			})
		}

		fmt.Printf("%d cycles scheduled (%d weeks total).\n",
			additionalWeeks+1, additionalWeeks+1)
	}

	// ── Conflict check ───────────────────────────────────────────────────────
	//
	// Before saving, combine the newly created cycle records with any cycles
	// already on file and run the conflict checker. If problems are found we
	// show them as a warning and give the grower a chance to bail out.
	//
	// We load cycles here (before saving) so we can check the combined picture.
	existingCyclesForCheck, checkErr := farm.LoadCycles()
	if checkErr == nil {
		// Combine existing cycles with all the new ones (including repeats).
		allCyclesForCheck := append(existingCyclesForCheck, newCycleRecords...)
		conflicts := checker.Check(farmEnvs, allCyclesForCheck)

		if len(conflicts) > 0 {
			fmt.Println("\nWARNING — this plan creates capacity conflicts:")
			fmt.Println()
			for _, w := range conflicts {
				fmt.Printf("  %s\n", w)
			}
			fmt.Println()

			// Check whether the overflow can be cleanly resolved by splitting
			// this crop across two lit environments. For example, if main tent
			// has 4 slots left and you're adding 8 trays, a split would put 4
			// trays in main tent and 4 in test tent — two separate cycles, each
			// tracked independently by the snapshot.
			sp, canSplit := computeSingleCycleSplit(litEnvs, existingCyclesForCheck, newCycleRecords)
			if canSplit {
				fmt.Printf("  → (s)plit: %d trays → %s + %d trays → %s\n\n",
					sp.splitA, farm.DisplayName(sp.envA),
					sp.splitB, farm.DisplayName(sp.envB))
			}

			// Build the prompt based on whether a split is available.
			var choice string
			if canSplit {
				choice = ask("Type \"yes\" to save, \"s\" to split, or \"cancel\" to abort: ")
			} else {
				choice = ask("Save anyway? Type \"yes\" to save or \"cancel\" to abort: ")
			}

			c := strings.ToLower(strings.TrimSpace(choice))
			switch c {
			case "s", "split":
				// Apply the split: regenerate the cycle records and task lists
				// so that every original cycle becomes two halves — one in
				// envA, one in envB. Each half gets its own CycleID so it can
				// be tracked and deleted independently.
				var splitErr error
				newCycleRecords, allNewTasks, splitErr = executeSingleCycleSplit(
					found, fromHarvest, newCycleRecords, sp)
				if splitErr != nil {
					fmt.Printf("Error applying split: %v\n", splitErr)
					os.Exit(1)
				}
				fmt.Printf("\nSplit applied — %d cycles (%d+%d trays across %s and %s).\n",
					len(newCycleRecords),
					sp.splitA, sp.splitB,
					farm.DisplayName(sp.envA), farm.DisplayName(sp.envB))
			case "yes":
				// Save as-is despite the conflict — grower is aware.
			default:
				fmt.Println("Cancelled — nothing was saved.")
				return
			}
		}
	}
	// (If loading existing cycles failed, we skip the conflict check and save
	// anyway — a warning from the cycle-save step below will surface the error.)

	// Load existing tasks, append all the new ones, and save.
	existing, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading existing tasks: %v\n", err)
		os.Exit(1)
	}

	all := append(existing, allNewTasks...)
	if err := store.Save(all); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	// Also save the cycle records so "greenies snapshot" can track this batch.
	// We use a warning rather than a fatal error here because the calendar tasks
	// are already saved — the core scheduling functionality is intact. The
	// snapshot just won't show this cycle until the records are re-created.
	existingCycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Warning: could not load existing cycle records: %v\n", err)
	} else {
		allCycles := append(existingCycles, newCycleRecords...)
		if err := farm.SaveCycles(allCycles); err != nil {
			fmt.Printf("Warning: could not save cycle records: %v\n", err)
		}
	}

	fmt.Printf("%d tasks added to the calendar.\n", len(allNewTasks))
	fmt.Println("Run \"greenies list\" to see the schedule.")
	fmt.Println("Run \"greenies snapshot\" to see the farm view.")
}

// runBatchPlan handles the "batch harvest day" planning flow.
//
// A batch is a group of crop cycles that all share the same harvest date —
// for example, everything being harvested for a Saturday farmers market.
// The grower enters the harvest date once, then adds as many crop varieties
// as they need. At the end, they can repeat the whole batch weekly (e.g.
// "the same market mix every Saturday for the next four weeks").
//
// Nothing is written to disk until the grower types "yes" at the final
// confirmation prompt — cancelling at any point saves nothing.
//
// Parameters passed in from runPlan so we don't have to reload them:
//   - reader   — the keyboard reader (bufio.Reader wrapping os.Stdin)
//   - ask      — the shared prompt helper that handles "cancel" automatically
//   - farmEnvs — all environments from farm.csv (needed for conflict checking)
//   - litEnvs  — only the lit environments (for the environment choice prompt)
//   - crops    — the full crop library loaded from crops.csv
func runBatchPlan(
	reader *bufio.Reader,
	ask func(string) string,
	farmEnvs []farm.Environment,
	litEnvs []farm.Environment,
	crops []crop.Crop,
) {
	// Load unit label for yield prompts.
	pcfg2, _ := config.Load()
	pwl := pcfg2.WeightLabel()

	// --- Step 1: harvest date ---
	// Every crop in the batch counts backward from this single date.
	harvestDate, err := parseDate(ask("Harvest date for this batch (MM-DD or YYYY-MM-DD): "))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// batchEntry holds everything we need for one crop cycle within the batch.
	// We collect these in a slice and only save them all at the very end.
	type batchEntry struct {
		found     *crop.Crop  // the crop variety
		trays     int         // how many trays
		litEnv    string      // which lit environment
		tasks     []task.Task // the generated calendar tasks
		sowDate   time.Time   // Day 1 date (for weekly repeat arithmetic)
		harvest   time.Time   // harvest date (same for all, kept for arithmetic)
		moveLight time.Time   // first day on a lit rack
	}

	var entries []batchEntry

	// --- Step 2: crop loop ---
	// Keep asking for crops until the grower says they're done.
	for {
		fmt.Println()

		// Show the crop list before each iteration so the grower doesn't
		// have to remember the numbers.
		fmt.Println("Available crops:")
		for i, c := range crops {
			fmt.Printf("  %d. (%c)%s\n", i+1, c.Name[0], c.Name[1:])
		}
		fmt.Println()

		cropInput := ask("Which crop? (name, number, or unique prefix): ")
		if cropInput == "" {
			fmt.Println("No crop entered — skipping.")
			break
		}

		// Resolve the user's input to exactly one crop (same logic as single
		// cycle planning — number, full name, or unique prefix).
		var found *crop.Crop
		if n, convErr := strconv.Atoi(cropInput); convErr == nil {
			if n >= 1 && n <= len(crops) {
				found = &crops[n-1]
			} else {
				fmt.Printf("Number %d is out of range — pick 1 to %d.\n", n, len(crops))
				continue
			}
		} else {
			lower := strings.ToLower(cropInput)
			var matches []*crop.Crop
			for i := range crops {
				if strings.HasPrefix(strings.ToLower(crops[i].Name), lower) {
					matches = append(matches, &crops[i])
				}
			}
			switch len(matches) {
			case 0:
				fmt.Printf("No crop found matching %q. Check the spelling and try again.\n", cropInput)
				continue
			case 1:
				found = matches[0]
			default:
				fmt.Printf("%q matches more than one crop:\n", cropInput)
				for _, m := range matches {
					fmt.Printf("  %s\n", m.Name)
				}
				fmt.Println("Please type more letters, or use the number from the list.")
				continue
			}
		}

		// --- Tray count or yield target? ---
		planMode := ask("Plan by (t)ray count or (y)ield target? [t/y]: ")
		var trays int

		switch strings.ToLower(planMode) {
		case "y", "yield":
			if found.YieldGrams == 0 {
				fmt.Printf("No yield data found for %s in the crop library.\n", task.Capitalize(found.Name))
				fmt.Println("Add a yield_grams value to crops.csv, or plan by tray count instead.")
				continue
			}
			yieldStr := ask(fmt.Sprintf("Desired yield in %s? (%s yields ~%d%s per tray): ",
				pwl, task.Capitalize(found.Name), found.YieldGrams, pwl))
			desiredYield, convErr := strconv.Atoi(yieldStr)
			if convErr != nil || desiredYield < 1 {
				fmt.Println("Please enter a whole number greater than zero (e.g. 500).")
				continue
			}
			trays = int(math.Ceil(float64(desiredYield) / float64(found.YieldGrams)))
			fmt.Printf("→ %d %s needed to yield ~%d%s (target: %d%s)\n",
				trays, task.TrayWord(trays), trays*found.YieldGrams, pwl, desiredYield, pwl)
		default:
			traysStr := ask("How many trays? ")
			n, convErr := strconv.Atoi(traysStr)
			if convErr != nil || n < 1 {
				fmt.Println("Please enter a whole number greater than zero (e.g. 2).")
				continue
			}
			trays = n
		}

		// --- Generate the schedule (always backward from harvest date) ---
		preview, newTasks, schedErr := scheduler.Schedule(*found, harvestDate, trays)
		if schedErr != nil {
			fmt.Printf("Error generating schedule for %s: %v\n", found.Name, schedErr)
			continue
		}

		// Show a preview so the grower can see when this crop needs to be sown.
		fmt.Printf("\n%s — %d %s — harvest %s\n\n",
			task.Capitalize(found.Name), trays, task.TrayWord(trays), harvestDate)
		for _, d := range preview {
			tasks := d.CropDay.Tasks
			if tasks == "" {
				tasks = "(no tasks)"
			}
			fmt.Printf("  %s  Day %-2d  %-8s  %s\n",
				d.Date, d.CropDay.Day, d.CropDay.Stage, tasks)
		}
		fmt.Println()

		// --- Which lit environment? ---
		var litEnv string
		if len(litEnvs) == 0 {
			litEnv = "any"
		} else {
			var promptParts []string
			for i, e := range litEnvs {
				promptParts = append(promptParts, fmt.Sprintf("(%d) %s", i+1, farm.DisplayName(e.Name)))
			}
			promptParts = append(promptParts, "(a) any")
			envInput := ask("Which lit environment? " + strings.Join(promptParts, " / ") + " [a]: ")
			litEnv = resolveLitEnv(envInput, litEnvs)
		}

		// --- Extract the sow date and move-to-light date from the preview ---
		// (same logic as the single-cycle flow)
		var sowDateStr string
		for _, d := range preview {
			if d.CropDay.Day == 1 {
				sowDateStr = d.Date
				break
			}
		}
		baseSow, _ := time.Parse(task.DateFormat, sowDateStr)
		baseHarvest, _ := time.Parse(task.DateFormat, harvestDate)
		baseMoveToLight := baseSow.AddDate(0, 0, found.DarkDays+1)

		entries = append(entries, batchEntry{
			found:     found,
			trays:     trays,
			litEnv:    litEnv,
			tasks:     newTasks,
			sowDate:   baseSow,
			harvest:   baseHarvest,
			moveLight: baseMoveToLight,
		})

		// --- Add another crop to this batch? ---
		another := ask("Add another crop to this batch? [y/n]: ")
		if strings.ToLower(another) != "y" && !strings.EqualFold(another, "yes") {
			break
		}
	}

	// If the grower added no crops (e.g. typed cancel on the first crop
	// prompt, which exits, or entered nothing), there's nothing to save.
	if len(entries) == 0 {
		fmt.Println("No crops added — nothing was saved.")
		return
	}

	// --- Step 3: weekly repeat ---
	// Ask if the grower wants to repeat this entire batch on subsequent weeks.
	// Each week shifts the harvest date (and therefore every sow date) by 7 days.
	allTasks := make([]task.Task, 0)
	var allCycleRecords []farm.Cycle

	// Build the base week's tasks and cycle records first.
	for _, e := range entries {
		allTasks = append(allTasks, e.tasks...)
		allCycleRecords = append(allCycleRecords, farm.Cycle{
			CycleID:         e.tasks[0].CycleID,
			CropName:        e.found.Name,
			Trays:           e.trays,
			SowDate:         e.sowDate.Format(task.DateFormat),
			HarvestDate:     e.harvest.Format(task.DateFormat),
			MoveToLightDate: e.moveLight.Format(task.DateFormat),
			LitEnvironment:  e.litEnv,
			ExpectedGrams:   e.found.YieldGrams * e.trays,
		})
	}

	// Blank input defaults to 1 additional week; type 0 to skip repeating.
	batchWeeksStr := ask("Repeat weekly? Additional weeks (0 to skip) [1]: ")
	additionalWeeks := 1 // default: one additional week
	if batchWeeksStr != "" {
		w, convErr := strconv.Atoi(batchWeeksStr)
		if convErr != nil || w < 0 {
			fmt.Println("Please enter a whole number (e.g. 3), or 0 to skip.")
			os.Exit(1)
		}
		additionalWeeks = w
	}

	if additionalWeeks > 0 {

		// For each additional week, regenerate every crop's schedule shifted
		// forward by week×7 days, using a new harvest date per week.
		baseHarvest, _ := time.Parse(task.DateFormat, harvestDate)

		for week := 1; week <= additionalWeeks; week++ {
			weeklyHarvest := baseHarvest.AddDate(0, 0, week*7).Format(task.DateFormat)

			for _, e := range entries {
				_, weekTasks, schedErr := scheduler.Schedule(*e.found, weeklyHarvest, e.trays)
				if schedErr != nil {
					fmt.Printf("Error generating week %d schedule for %s: %v\n", week, e.found.Name, schedErr)
					os.Exit(1)
				}
				allTasks = append(allTasks, weekTasks...)

				// Shift all three key dates forward by the same number of days.
				weekSow := e.sowDate.AddDate(0, 0, week*7)
				weekHarvest := e.harvest.AddDate(0, 0, week*7)
				weekMoveToLight := e.moveLight.AddDate(0, 0, week*7)

				allCycleRecords = append(allCycleRecords, farm.Cycle{
					CycleID:         weekTasks[0].CycleID,
					CropName:        e.found.Name,
					Trays:           e.trays,
					SowDate:         weekSow.Format(task.DateFormat),
					HarvestDate:     weekHarvest.Format(task.DateFormat),
					MoveToLightDate: weekMoveToLight.Format(task.DateFormat),
					LitEnvironment:  e.litEnv,
					ExpectedGrams:   e.found.YieldGrams * e.trays,
				})
			}
		}

		fmt.Printf("%d crops × %d weeks = %d cycles total.\n",
			len(entries), additionalWeeks+1, len(entries)*(additionalWeeks+1))
	}

	// --- Step 4: combined summary ---
	fmt.Println()
	fmt.Println("Batch summary:")
	fmt.Println()
	for _, c := range allCycleRecords {
		fmt.Printf("  %s  %d %s  sow %s  harvest %s  [%s]\n",
			task.Capitalize(c.CropName), c.Trays, task.TrayWord(c.Trays),
			c.SowDate, c.HarvestDate, farm.DisplayName(c.LitEnvironment))
	}
	fmt.Println()

	// --- Step 5: conflict check ---
	existingCyclesForCheck, checkErr := farm.LoadCycles()
	if checkErr == nil {
		allCyclesForCheck := append(existingCyclesForCheck, allCycleRecords...)
		conflicts := checker.Check(farmEnvs, allCyclesForCheck)
		if len(conflicts) > 0 {
			fmt.Println("WARNING — this batch creates capacity conflicts:")
			fmt.Println()
			for _, w := range conflicts {
				fmt.Printf("  %s\n", w)
			}
			fmt.Println()

			// --- Batch split logic ---
			//
			// A split is possible when every crop in the batch targets the
			// same lit environment (or all chose "any") and the combined
			// total overflows that environment's capacity.
			//
			// The split uses a "greedy fill" approach: crops are assigned to
			// the primary environment in the order they were added until it is
			// full; remaining crops (and any crop that straddles the boundary)
			// go to the next environment.
			targetEnvForSplit := ""
			allSameBatchEnv := true
			for _, e := range entries {
				env := e.litEnv
				if env == "any" && len(litEnvs) > 0 {
					env = litEnvs[0].Name
				}
				if targetEnvForSplit == "" {
					targetEnvForSplit = env
				} else if targetEnvForSplit != env {
					allSameBatchEnv = false
					break
				}
			}

			// batchAssignments[i] = {traysInEnvA, traysInEnvB} for each entry.
			var batchAssignments [][2]int
			var batchEnvA, batchEnvB string
			canBatchSplit := false

			if allSameBatchEnv && len(litEnvs) >= 2 && targetEnvForSplit != "" {
				// Find the capacity of the primary environment.
				envACap := 0
				envAIdx := -1
				for i, e := range litEnvs {
					if e.Name == targetEnvForSplit {
						envACap = e.Capacity
						envAIdx = i
						break
					}
				}
				if envAIdx >= 0 && envACap > 0 && envAIdx+1 < len(litEnvs) {
					batchEnvA = targetEnvForSplit
					batchEnvB = litEnvs[envAIdx+1].Name

					// How many of these slots are already spoken for?
					// Use the first entry's light window as a proxy — all
					// crops in the batch share the same harvest date, so their
					// light phases are close enough to use one measurement.
					peakInA := computePeakLitUsage(batchEnvA, existingCyclesForCheck,
						entries[0].moveLight, entries[0].harvest)
					remaining := envACap - peakInA
					if remaining < 0 {
						remaining = 0
					}

					// Count the total new trays targeting envA.
					total := 0
					for _, e := range entries {
						total += e.trays
					}

					if total > remaining {
						// Greedy fill: assign entries to envA until full.
						batchAssignments = make([][2]int, len(entries))
						filled := 0
						for i, e := range entries {
							switch {
							case filled >= remaining:
								// envA is already full — this crop goes entirely to envB.
								batchAssignments[i] = [2]int{0, e.trays}
							case filled+e.trays <= remaining:
								// This crop fits entirely in envA.
								batchAssignments[i] = [2]int{e.trays, 0}
								filled += e.trays
							default:
								// This crop straddles the boundary — split it.
								inA := remaining - filled
								batchAssignments[i] = [2]int{inA, e.trays - inA}
								filled += inA
							}
						}
						canBatchSplit = true
					}
				}
			}

			// Show the split preview if one is available.
			if canBatchSplit {
				fmt.Printf("  → (s)plit across environments:\n")
				for i, e := range entries {
					a := batchAssignments[i][0]
					b := batchAssignments[i][1]
					switch {
					case a > 0 && b > 0:
						fmt.Printf("     %s: %d → %s  +  %d → %s\n",
							task.Capitalize(e.found.Name),
							a, farm.DisplayName(batchEnvA),
							b, farm.DisplayName(batchEnvB))
					case a > 0:
						fmt.Printf("     %s: %d → %s\n",
							task.Capitalize(e.found.Name), a, farm.DisplayName(batchEnvA))
					default:
						fmt.Printf("     %s: %d → %s\n",
							task.Capitalize(e.found.Name), b, farm.DisplayName(batchEnvB))
					}
				}
				fmt.Println()
			}

			var batchChoice string
			if canBatchSplit {
				batchChoice = ask("Type \"yes\" to save, \"s\" to split, or \"cancel\" to abort: ")
			} else {
				batchChoice = ask("Save anyway? Type \"yes\" to save or \"cancel\" to abort: ")
			}

			bc := strings.ToLower(strings.TrimSpace(batchChoice))
			switch bc {
			case "s", "split":
				// Rebuild allTasks and allCycleRecords from scratch, applying
				// the greedy-fill assignment to each week's entries.
				allTasks = make([]task.Task, 0)
				allCycleRecords = nil

				for week := 0; week <= additionalWeeks; week++ {
					shift := week * 7
					for i, e := range entries {
						aTrays := batchAssignments[i][0]
						bTrays := batchAssignments[i][1]

						weeklyHarv := e.harvest.AddDate(0, 0, shift).Format(task.DateFormat)
						sowStr := e.sowDate.AddDate(0, 0, shift).Format(task.DateFormat)
						mltStr := e.moveLight.AddDate(0, 0, shift).Format(task.DateFormat)

						if aTrays > 0 {
							_, aTasks, schedErr := scheduler.Schedule(*e.found, weeklyHarv, aTrays)
							if schedErr != nil {
								fmt.Printf("Error generating split schedule: %v\n", schedErr)
								os.Exit(1)
							}
							// Tag each task title with the destination environment
							// so the grower can tell the two halves apart on the
							// calendar (e.g. "Sunnies 4x dark (Main tent)").
							for j := range aTasks {
								aTasks[j].Title += " (" + farm.DisplayName(batchEnvA) + ")"
							}
							allTasks = append(allTasks, aTasks...)
							allCycleRecords = append(allCycleRecords, farm.Cycle{
								CycleID:         aTasks[0].CycleID,
								CropName:        e.found.Name,
								Trays:           aTrays,
								SowDate:         sowStr,
								HarvestDate:     weeklyHarv,
								MoveToLightDate: mltStr,
								LitEnvironment:  batchEnvA,
								ExpectedGrams:   e.found.YieldGrams * aTrays,
							})
						}

						if bTrays > 0 {
							_, bTaskSet, schedErr := scheduler.Schedule(*e.found, weeklyHarv, bTrays)
							if schedErr != nil {
								fmt.Printf("Error generating split schedule: %v\n", schedErr)
								os.Exit(1)
							}
							for j := range bTaskSet {
								bTaskSet[j].Title += " (" + farm.DisplayName(batchEnvB) + ")"
							}
							allTasks = append(allTasks, bTaskSet...)
							allCycleRecords = append(allCycleRecords, farm.Cycle{
								CycleID:         bTaskSet[0].CycleID,
								CropName:        e.found.Name,
								Trays:           bTrays,
								SowDate:         sowStr,
								HarvestDate:     weeklyHarv,
								MoveToLightDate: mltStr,
								LitEnvironment:  batchEnvB,
								ExpectedGrams:   e.found.YieldGrams * bTrays,
							})
						}
					}
				}
				fmt.Printf("\nSplit applied — %d cycles across %s and %s.\n",
					len(allCycleRecords),
					farm.DisplayName(batchEnvA), farm.DisplayName(batchEnvB))
			case "yes":
				// Save as-is despite the conflict.
			default:
				fmt.Println("Cancelled — nothing was saved.")
				return
			}
		}
	}

	// --- Step 6: final confirmation ---
	response := ask("Add this batch to the calendar? Type \"yes\" to confirm: ")
	if response != "yes" {
		fmt.Println("Cancelled — nothing was saved.")
		return
	}

	// --- Step 7: save everything at once ---
	existing, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading existing tasks: %v\n", err)
		os.Exit(1)
	}
	all := append(existing, allTasks...)
	if err := store.Save(all); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	existingCycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Warning: could not load existing cycle records: %v\n", err)
	} else {
		allCycles := append(existingCycles, allCycleRecords...)
		if err := farm.SaveCycles(allCycles); err != nil {
			fmt.Printf("Warning: could not save cycle records: %v\n", err)
		}
	}

	fmt.Printf("%d tasks added to the calendar.\n", len(allTasks))
	fmt.Println("Run \"greenies list\" to see the schedule.")
	fmt.Println("Run \"greenies snapshot\" to see the farm view.")
}

// resolveLitEnv maps what the user typed to a lit environment name.
//
// Accepts any of:
//   - a number ("1", "2") — picks the environment at that position
//   - a full name or unique prefix ("main", "test") — matched case-insensitively
//   - "a" or "any" — leaves the environment unassigned until snapshot time
//   - blank — defaults to "any" (auto-assign at snapshot time)
//
// If nothing matches, defaults to the first lit environment.
func resolveLitEnv(input string, litEnvs []farm.Environment) string {
	if len(litEnvs) == 0 {
		return "any"
	}

	input = strings.TrimSpace(input)

	// Blank → "any", which auto-assigns to the first lit environment with
	// enough free slots at snapshot time, spilling to the next if needed.
	// This is the safest lazy default: it never locks a cycle to a specific
	// tent before the grower knows which one has room.
	if input == "" {
		return "any"
	}

	lower := strings.ToLower(input)

	// "a" or "any" → explicitly unassigned; resolved at snapshot time.
	if lower == "a" || lower == "any" {
		return "any"
	}

	// Number → pick by 1-based index.
	if n, err := strconv.Atoi(input); err == nil {
		if n >= 1 && n <= len(litEnvs) {
			return litEnvs[n-1].Name
		}
		// Out of range — default to first.
		return litEnvs[0].Name
	}

	// Prefix match (case-insensitive) — e.g. "main" matches "main_tent".
	for _, e := range litEnvs {
		if strings.HasPrefix(strings.ToLower(e.Name), lower) {
			return e.Name
		}
	}

	// No match at all — default to first.
	return litEnvs[0].Name
}

// ─────────────────────────────────────────────────────────────────────────────
// Lit-environment split helpers
// ─────────────────────────────────────────────────────────────────────────────

// litSplit describes how to divide one overflowing crop cycle across two lit
// environments. It is produced by computeSingleCycleSplit and consumed by
// executeSingleCycleSplit.
type litSplit struct {
	splitA int    // number of trays to place in the primary environment
	splitB int    // number of trays to place in the secondary environment
	envA   string // name of the primary (first) lit environment
	envB   string // name of the secondary (overflow) lit environment
}

// computePeakLitUsage returns the maximum number of trays from the given
// cycles that are simultaneously occupying lit-rack slots in `envName`
// during the window [windowStart, windowEnd).
//
// A cycle occupies lit slots from its MoveToLightDate (inclusive) up to —
// but not including — its HarvestDate. On harvest morning those slots are
// freed.
//
// We use this to find out how much capacity is ALREADY spoken for in a lit
// environment during the period when a new cycle would be on the racks.
// Knowing that, we can calculate how many slots are still free and offer a
// sensible split to the grower.
func computePeakLitUsage(envName string, cycles []farm.Cycle, windowStart, windowEnd time.Time) int {
	if !windowStart.Before(windowEnd) {
		return 0
	}

	// Collect every "change point" date that falls inside the window.
	// Usage only changes on move-to-light or harvest dates, so checking
	// these boundary dates is enough to find the true peak.
	dateSet := map[time.Time]bool{windowStart: true}
	for _, c := range cycles {
		if c.LitEnvironment != envName {
			continue
		}
		mlt, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		// Only add change points that land inside the window — outside
		// points would give us usage counts that belong to other periods.
		if !mlt.Before(windowStart) && mlt.Before(windowEnd) {
			dateSet[mlt] = true
		}
		if !harv.Before(windowStart) && harv.Before(windowEnd) {
			dateSet[harv] = true
		}
	}

	// Check every change-point date and keep the highest usage count we see.
	peak := 0
	for date := range dateSet {
		if date.Before(windowStart) || !date.Before(windowEnd) {
			continue
		}
		usage := 0
		for _, c := range cycles {
			if c.LitEnvironment != envName {
				continue
			}
			mlt, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
			harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
			// This cycle is in lit slots on `date` if:
			//   date >= moveToLight  AND  date < harvest
			if !date.Before(mlt) && date.Before(harv) {
				usage += c.Trays
			}
		}
		if usage > peak {
			peak = usage
		}
	}
	return peak
}

// computeSingleCycleSplit checks whether a set of new cycle records (one
// crop variety, possibly with weekly repeats) can be split across two lit
// environments to resolve a slot overflow.
//
// A split is possible when:
//   - All new cycles target the same lit environment (or all use "any").
//   - That environment will overflow once the new cycles are added.
//   - There is a second lit environment configured to absorb the surplus.
//   - Both halves of the split are non-zero (otherwise it's a move, not a split).
//
// Returns (plan, true) if a meaningful split is possible, (litSplit{}, false)
// otherwise.
func computeSingleCycleSplit(
	litEnvs []farm.Environment,
	existingCycles []farm.Cycle,
	newCycles []farm.Cycle,
) (litSplit, bool) {
	if len(litEnvs) < 2 || len(newCycles) == 0 {
		return litSplit{}, false
	}

	// All new cycles must have the same tray count and target the same lit
	// environment. In single-crop mode with weekly repeats they always do.
	trays := newCycles[0].Trays
	targetEnv := newCycles[0].LitEnvironment

	for _, c := range newCycles {
		if c.Trays != trays {
			// Mixed tray counts — can't offer a uniform per-cycle split.
			return litSplit{}, false
		}
		env := c.LitEnvironment
		if env == "any" && len(litEnvs) > 0 {
			env = litEnvs[0].Name
		}
		if targetEnv == "any" && len(litEnvs) > 0 {
			targetEnv = litEnvs[0].Name
		}
		if env != targetEnv {
			// Different target environments — no simple uniform split.
			return litSplit{}, false
		}
	}

	// Resolve "any" to the first lit environment.
	if targetEnv == "any" && len(litEnvs) > 0 {
		targetEnv = litEnvs[0].Name
	}

	// Find the capacity of the primary environment (envA) and the name of
	// the secondary environment (envB) that will absorb the overflow.
	envACap := 0
	envAIdx := -1
	for i, e := range litEnvs {
		if e.Name == targetEnv {
			envACap = e.Capacity
			envAIdx = i
			break
		}
	}
	if envAIdx < 0 || envACap == 0 || envAIdx+1 >= len(litEnvs) {
		return litSplit{}, false
	}
	envBName := litEnvs[envAIdx+1].Name

	// Calculate how many slots are already spoken for in envA during the
	// first new cycle's light phase. All repeats share the same tray count,
	// so the first cycle is a representative proxy.
	mlt, _ := time.Parse(task.DateFormat, newCycles[0].MoveToLightDate)
	harv, _ := time.Parse(task.DateFormat, newCycles[0].HarvestDate)
	peakExisting := computePeakLitUsage(targetEnv, existingCycles, mlt, harv)

	remaining := envACap - peakExisting
	if remaining < 0 {
		remaining = 0
	}
	if remaining >= trays {
		// There is no actual overflow for this cycle — split not needed.
		return litSplit{}, false
	}

	splitA := remaining
	splitB := trays - remaining

	// Both sides must be non-zero for a meaningful split. If the environment
	// is completely full (splitA == 0) the grower would need to pick a
	// different environment entirely rather than splitting.
	if splitA <= 0 || splitB <= 0 {
		return litSplit{}, false
	}

	return litSplit{splitA: splitA, splitB: splitB, envA: targetEnv, envB: envBName}, true
}

// executeSingleCycleSplit takes the list of new cycle records (base week +
// any repeats) and a split plan, and returns two new lists:
//
//   - A new cycle-records list twice as long: each original cycle becomes
//     two — one in envA (splitA trays) and one in envB (splitB trays).
//   - A new task list: fresh task sets are generated for each half so each
//     half gets its own CycleID and can be deleted independently.
//
// Tasks for each half are annotated with the environment name in the title —
// e.g. "Sunnies 4x dark (Main tent)" — so the grower can tell them apart
// on the calendar.
func executeSingleCycleSplit(
	found *crop.Crop,
	fromHarvest bool,
	origCycles []farm.Cycle,
	sp litSplit,
) ([]farm.Cycle, []task.Task, error) {
	var newCycles []farm.Cycle
	var newTasks []task.Task

	envALabel := farm.DisplayName(sp.envA)
	envBLabel := farm.DisplayName(sp.envB)

	for _, cr := range origCycles {
		// Generate a brand-new task set for each half. Re-running the
		// scheduler gives each half its own unique CycleID, so the grower
		// can later delete one half without touching the other.
		var aTasks, bTasks []task.Task
		var err error

		if fromHarvest {
			_, aTasks, err = scheduler.Schedule(*found, cr.HarvestDate, sp.splitA)
		} else {
			_, aTasks, err = scheduler.ScheduleForward(*found, cr.SowDate, sp.splitA)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("schedule error for %s split: %w", sp.envA, err)
		}

		if fromHarvest {
			_, bTasks, err = scheduler.Schedule(*found, cr.HarvestDate, sp.splitB)
		} else {
			_, bTasks, err = scheduler.ScheduleForward(*found, cr.SowDate, sp.splitB)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("schedule error for %s split: %w", sp.envB, err)
		}

		// Tag each task's title with its destination environment so the two
		// halves are distinguishable on the calendar.
		// Before: "Sunnies 4x dark"
		// After:  "Sunnies 4x dark (Main tent)"
		for i := range aTasks {
			aTasks[i].Title += " (" + envALabel + ")"
		}
		for i := range bTasks {
			bTasks[i].Title += " (" + envBLabel + ")"
		}

		newTasks = append(newTasks, aTasks...)
		newTasks = append(newTasks, bTasks...)

		// The move-to-light date is derived from the sow date — DarkDays + 1
		// days after Day 1 is the first day trays are on a lit rack.
		sowTime, _ := time.Parse(task.DateFormat, cr.SowDate)
		moveToLightStr := sowTime.AddDate(0, 0, found.DarkDays+1).Format(task.DateFormat)

		// Cycle A: the portion going into the primary lit environment.
		newCycles = append(newCycles, farm.Cycle{
			CycleID:         aTasks[0].CycleID,
			CropName:        cr.CropName,
			Trays:           sp.splitA,
			SowDate:         cr.SowDate,
			HarvestDate:     cr.HarvestDate,
			MoveToLightDate: moveToLightStr,
			LitEnvironment:  sp.envA,
			ExpectedGrams:   found.YieldGrams * sp.splitA,
		})

		// Cycle B: the portion going into the overflow lit environment.
		newCycles = append(newCycles, farm.Cycle{
			CycleID:         bTasks[0].CycleID,
			CropName:        cr.CropName,
			Trays:           sp.splitB,
			SowDate:         cr.SowDate,
			HarvestDate:     cr.HarvestDate,
			MoveToLightDate: moveToLightStr,
			LitEnvironment:  sp.envB,
			ExpectedGrams:   found.YieldGrams * sp.splitB,
		})
	}

	return newCycles, newTasks, nil
}
