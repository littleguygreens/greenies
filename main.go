// Greenies — a microgreens farm scheduling tool.
//
// This is the entry point of the program — the first thing Go runs when you
// type "./greenies" in the terminal. Its job is to read the command you typed
// and hand off to the right piece of code.
//
// Commands:
//
//	greenies list
//	greenies delete
//	greenies clear
//	greenies crops
//	greenies plan
//	greenies snapshot
package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"context" // for context.Background(), used when calling Google Calendar
	"fmt"
	"math"    // for math.Ceil, which rounds a decimal up to the next whole number
	"os"      // for os.Exit and reading command-line arguments
	"sort"    // for sorting the harvest log by date
	"strconv" // for converting text like "2" into the number 2
	"strings" // for string utilities used throughout
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/trial"
	"github.com/littleguygreens/greenies/internal/visualizer"
)


func main() {
	// os.Args is the list of words the user typed. os.Args[0] is always the
	// program name itself ("greenies"). The subcommand (list, delete, etc.) is
	// os.Args[1] if it exists.
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Route to the correct function based on the first word after "greenies".
	subcommand := os.Args[1]
	switch subcommand {
	case "list":
		runList()
	case "delete":
		runDelete()
	case "clear":
		runClear()
	case "crops":
		runCrops()
	case "plan":
		runPlan()
	case "snapshot":
		runSnapshot()
	case "sync":
		runSync()
	case "harvest":
		runHarvest()
	case "harvestlog":
		runHarvestLog()
	case "trial":
		runTrial()
	default:
		fmt.Printf("Unknown command: %q\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

// runList handles the "greenies list" command.
// It asks upfront what the user wants to see, defaulting to the current week
// on a blank Enter press. This avoids printing the week view when the user
// already knows they want something else.
func runList() {
	// Load all tasks from disk.
	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	now := time.Now()

	// bufio.NewReader lets us read a full line of input including spaces.
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
	}

	// Ask which view they want before printing anything.
	// Blank input or anything unrecognised defaults to the current week.
	choice := ask("View (w)eek / (m)onth / (r)ange [w]: ")

	switch strings.ToLower(choice) {
	case "m", "month":
		// Show every day of the current calendar month.
		// The first day is always the 1st; the last day is found by going to
		// the 1st of next month and stepping back one day.
		firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		firstOfNext := firstOfMonth.AddDate(0, 1, 0)
		lastOfMonth := firstOfNext.AddDate(0, 0, -1)
		if err := calendar.PrintRange(
			firstOfMonth.Format(task.DateFormat),
			lastOfMonth.Format(task.DateFormat),
			tasks,
		); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "r", "range":
		// Ask for a start and end date using the same flexible format as plan.
		startDate, err := parseDate(ask("Start date (MM-DD or YYYY-MM-DD): "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		endDate, err := parseDate(ask("End date (MM-DD or YYYY-MM-DD): "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if err := calendar.PrintRange(startDate, endDate, tasks); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	default:
		// Blank input, "w", "week", or anything unrecognised — show the current
		// 7-day week (today through 6 days from now).
		weekEnd := now.AddDate(0, 0, 6)
		if err := calendar.PrintRange(
			now.Format(task.DateFormat),
			weekEnd.Format(task.DateFormat),
			tasks,
		); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	}
}

// runDelete handles the "greenies delete" command.
// It removes a task by ID. If the task belongs to a planned crop cycle
// (i.e. it has a CycleID), the user is offered the choice to delete just
// that one task or the entire cycle at once.
func runDelete() {
	fmt.Print("Task ID to delete: ")
	var id string
	fmt.Scanln(&id)
	if id == "" {
		fmt.Println("No ID entered — cancelled.")
		return
	}

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Find the target task so we can check whether it has a CycleID.
	var target *task.Task
	for i := range tasks {
		if tasks[i].ID == id {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		fmt.Printf("No task found with ID %q. Use \"greenies list\" to see task IDs.\n", id)
		os.Exit(1)
	}

	// Decide whether to delete just this one task or the whole cycle.
	// deleteByID is the set of task IDs we will actually remove.
	deleteByID := map[string]bool{id: true}

	if target.CycleID != "" {
		// Count how many tasks share this cycle so we can tell the user.
		cycleCount := 0
		for _, t := range tasks {
			if t.CycleID == target.CycleID {
				cycleCount++
			}
		}

		fmt.Printf("Task: %q (%s)\n", target.Title, target.Date)
		fmt.Printf("This task belongs to a planned cycle (%d tasks total).\n", cycleCount)
		fmt.Print("Delete just this task, or the whole cycle? [t/c]: ")

		var choice string
		fmt.Scanln(&choice)

		if strings.ToLower(choice) == "c" {
			// Mark every task in this cycle for deletion.
			for _, t := range tasks {
				if t.CycleID == target.CycleID {
					deleteByID[t.ID] = true
				}
			}
		}
	}

	// Build the kept list by skipping anything in the delete set.
	var updated []task.Task
	for _, t := range tasks {
		if !deleteByID[t.ID] {
			updated = append(updated, t)
		}
	}

	if err := store.Save(updated); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	// If a whole cycle was deleted, also remove its record from cycles.json
	// so that "greenies snapshot" no longer shows the deleted batch.
	// A task-only deletion (not a full cycle) leaves cycles.json untouched
	// because the rest of the cycle is still active.
	if target.CycleID != "" && len(deleteByID) > 1 {
		// len(deleteByID) > 1 means we deleted more than one task, which
		// only happens when the user chose to delete the whole cycle ("c").
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			var keptCycles []farm.Cycle
			for _, c := range cycles {
				if c.CycleID != target.CycleID {
					keptCycles = append(keptCycles, c)
				}
			}
			if err := farm.SaveCycles(keptCycles); err != nil {
				fmt.Printf("Warning: tasks deleted but could not update cycle records: %v\n", err)
			}
		}
	}

	removed := len(tasks) - len(updated)
	if removed == 1 {
		fmt.Printf("1 task deleted.\n")
	} else {
		fmt.Printf("%d tasks deleted.\n", removed)
	}
}

// runClear deletes every task and cycle record after asking for confirmation.
// This resets both the calendar (tasks.json) and the snapshot (cycles.json)
// so the two data files stay in sync. The harvest log (harvests.json) is
// permanent history and is NOT cleared — use a text editor to edit that file
// directly if you ever need to remove a harvest record.
func runClear() {
	fmt.Print("This will delete ALL tasks and cycle records. Type \"yes\" to confirm: ")

	var response string
	fmt.Scanln(&response)

	if response != "yes" {
		fmt.Println("Cancelled — nothing was deleted.")
		return
	}

	// Clear the calendar tasks.
	if err := store.Save([]task.Task{}); err != nil {
		fmt.Printf("Error clearing tasks: %v\n", err)
		os.Exit(1)
	}

	// Also clear the cycle records so the snapshot doesn't show stale data.
	// Tasks and cycles are always created together (by "greenies plan"), so
	// wiping one without the other leaves the snapshot showing batches that
	// have no calendar tasks behind them.
	if err := farm.SaveCycles([]farm.Cycle{}); err != nil {
		fmt.Printf("Error clearing cycle records: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("All tasks and cycle records deleted.")
	fmt.Println("(Harvest log preserved — run \"greenies harvestlog\" to see it.)")
}

// runCrops handles the "greenies crops" command.
// It reads the crop library and prints a summary of every available variety.
func runCrops() {
	// Find the crops file and load it.
	path, err := crop.CropsFilePath()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	source := crop.CSVSource{Path: path}
	crops, err := source.LoadCrops()
	if err != nil {
		fmt.Printf("Error loading crop library: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Crop library (%d varieties)\n", len(crops))
	fmt.Println(strings.Repeat("─", 60))

	for _, c := range crops {
		// Show the key numbers at a glance: cycle length, seed and yield weights.
		fmt.Printf("  %-12s  %d days   seed: %dg/tray   yield: %dg/tray\n",
			c.Name, c.CycleDays, c.SeedGrams, c.YieldGrams)
	}

	fmt.Println()
	fmt.Println("Run \"greenies plan\" to plan a crop cycle.")
}

// runSnapshot handles the "greenies snapshot" command.
// It reads the farm layout config and the saved cycle records, then prints a
// picture of the farm showing what is growing in each space.
//
// An optional date argument lets you see a past or future snapshot:
//
//	greenies snapshot           → today's snapshot
//	greenies snapshot 03-15     → snapshot for March 15 of the current year
//	greenies snapshot 2026-03-15 → snapshot for a specific date
func runSnapshot() {
	// Default to today. If the user passed a date after the command, use that instead.
	snapshotTime := time.Now()
	if len(os.Args) >= 3 {
		parsed, err := parseDate(os.Args[2])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		snapshotTime, err = time.Parse(task.DateFormat, parsed)
		if err != nil {
			fmt.Printf("Error parsing date: %v\n", err)
			os.Exit(1)
		}
	}

	envs, err := farm.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading farm config: %v\n", err)
		os.Exit(1)
	}

	cycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Error loading cycle records: %v\n", err)
		os.Exit(1)
	}

	visualizer.PrintSnapshot(envs, cycles, snapshotTime)

	// ── Active trials section ─────────────────────────────────────────────────
	//
	// Trials are experimental runs that live outside the confirmed crop
	// schedule. They appear below the main snapshot so the grower can see
	// what is being tested at a glance alongside the regular production view.
	//
	// Trials are only shown here in the terminal — they are not included in
	// the Google Calendar description (which uses SnapshotText), because
	// trials are a local-only experimental feature.

	trials, trialErr := trial.LoadTrials()
	if trialErr == nil {
		active := trial.ActiveTrials(trials)
		if len(active) > 0 {
			fmt.Println(strings.Repeat("─", 21))
			fmt.Println("Active trials")
			fmt.Println()
			for _, tr := range active {
				dayNum := tr.DayNumber(snapshotTime)
				sow, _ := time.Parse(trial.DateFormat, tr.SowDate)
				fmt.Printf("%s %dx — Day %d (trial)\n", tr.DisplayName(), tr.Trays, dayNum)
				fmt.Printf("sown %s\n", sow.Format("Mon Jan 02"))
				// Show the tentative harvest date if one was given when the
				// trial started — labelled "(tentative)" so it is clearly not
				// a confirmed date from the main crop schedule.
				if hStr := tr.TentativeHarvestDate(); hStr != "" {
					hDate, _ := time.Parse(trial.DateFormat, hStr)
					fmt.Printf("harvest %s (tentative)\n", hDate.Format("Mon Jan 02"))
				}
				fmt.Println()
			}
		}
	}
}

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
	path, err := crop.CropsFilePath()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	source := crop.CSVSource{Path: path}
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

		yieldStr := ask(fmt.Sprintf("Desired yield in grams? (%s yields ~%dg per tray): ",
			task.Capitalize(found.Name), found.YieldGrams))
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

		trayWord := "tray"
		if trays != 1 {
			trayWord = "trays"
		}
		fmt.Printf("→ %d %s needed to yield ~%dg (target: %dg)\n",
			trays, trayWord, trays*found.YieldGrams, desiredYield)

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
	trayWord := "tray"
	if trays != 1 {
		trayWord = "trays"
	}
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
		// Default to "either" so the cycle record is still saved.
		litEnv = "either"
	} else {
		// Build a compact prompt from the list of lit environments in the config.
		// Example: "Which lit environment? (1) main tent / (2) test tent / (e) either [1]: "
		var promptParts []string
		for i, e := range litEnvs {
			promptParts = append(promptParts, fmt.Sprintf("(%d) %s", i+1, farm.DisplayName(e.Name)))
		}
		promptParts = append(promptParts, "(e) either")
		envInput := ask("Which lit environment? " + strings.Join(promptParts, " / ") + " [e]: ")
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
		found     *crop.Crop        // the crop variety
		trays     int               // how many trays
		litEnv    string            // which lit environment
		tasks     []task.Task       // the generated calendar tasks
		sowDate   time.Time         // Day 1 date (for weekly repeat arithmetic)
		harvest   time.Time         // harvest date (same for all, kept for arithmetic)
		moveLight time.Time         // first day on a lit rack
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
			yieldStr := ask(fmt.Sprintf("Desired yield in grams? (%s yields ~%dg per tray): ",
				task.Capitalize(found.Name), found.YieldGrams))
			desiredYield, convErr := strconv.Atoi(yieldStr)
			if convErr != nil || desiredYield < 1 {
				fmt.Println("Please enter a whole number greater than zero (e.g. 500).")
				continue
			}
			trays = int(math.Ceil(float64(desiredYield) / float64(found.YieldGrams)))
			trayWord := "tray"
			if trays != 1 {
				trayWord = "trays"
			}
			fmt.Printf("→ %d %s needed to yield ~%dg (target: %dg)\n",
				trays, trayWord, trays*found.YieldGrams, desiredYield)
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
		trayWord := "tray"
		if trays != 1 {
			trayWord = "trays"
		}
		fmt.Printf("\n%s — %d %s — harvest %s\n\n",
			task.Capitalize(found.Name), trays, trayWord, harvestDate)
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
			litEnv = "either"
		} else {
			var promptParts []string
			for i, e := range litEnvs {
				promptParts = append(promptParts, fmt.Sprintf("(%d) %s", i+1, farm.DisplayName(e.Name)))
			}
			promptParts = append(promptParts, "(e) either")
			envInput := ask("Which lit environment? " + strings.Join(promptParts, " / ") + " [e]: ")
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
		trayWord := "tray"
		if c.Trays != 1 {
			trayWord = "trays"
		}
		fmt.Printf("  %s  %d %s  sow %s  harvest %s  [%s]\n",
			task.Capitalize(c.CropName), c.Trays, trayWord,
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
			// same lit environment (or all chose "either") and the combined
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
				if env == "either" && len(litEnvs) > 0 {
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
//   - "e" or "either" — leaves the environment unassigned until snapshot time
//   - blank — defaults to the first lit environment in the config
//
// If nothing matches, defaults to the first lit environment.
func resolveLitEnv(input string, litEnvs []farm.Environment) string {
	if len(litEnvs) == 0 {
		return "either"
	}

	input = strings.TrimSpace(input)

	// Blank → "either", which auto-assigns to the first lit environment with
	// enough free slots at snapshot time, spilling to the next if needed.
	// This is the safest lazy default: it never locks a cycle to a specific
	// tent before the grower knows which one has room.
	if input == "" {
		return "either"
	}

	lower := strings.ToLower(input)

	// "e" or "either" → explicitly unassigned; resolved at snapshot time.
	if lower == "e" || lower == "either" {
		return "either"
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
//   - All new cycles target the same lit environment (or all use "either").
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
		if env == "either" && len(litEnvs) > 0 {
			env = litEnvs[0].Name
		}
		if targetEnv == "either" && len(litEnvs) > 0 {
			targetEnv = litEnvs[0].Name
		}
		if env != targetEnv {
			// Different target environments — no simple uniform split.
			return litSplit{}, false
		}
	}

	// Resolve "either" to the first lit environment.
	if targetEnv == "either" && len(litEnvs) > 0 {
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

// parseDate parses a date entered by the user and always returns a full
// YYYY-MM-DD string. Used for both harvest dates and sow dates — wherever
// the user needs to enter a date.
//
// MM-DD is the convenient shorthand for dates in the current year.
// YYYY-MM-DD lets the user cross a year boundary — e.g. scheduling in December
// for a January harvest.
func parseDate(input string) (string, error) {
	input = strings.TrimSpace(input)

	// MM-DD: 5 characters, dash in the middle (e.g. "03-15").
	// Prepend the current year to make a full date.
	if len(input) == 5 && input[2] == '-' {
		full := fmt.Sprintf("%d-%s", time.Now().Year(), input)
		if _, err := time.Parse(task.DateFormat, full); err != nil {
			return "", fmt.Errorf("%q is not a valid date — use MM-DD (e.g. 03-15) or YYYY-MM-DD", input)
		}
		return full, nil
	}

	// YYYY-MM-DD: full date including year.
	if _, err := time.Parse(task.DateFormat, input); err != nil {
		return "", fmt.Errorf("%q is not a valid date — use MM-DD (e.g. 03-15) or YYYY-MM-DD (e.g. 2026-03-15)", input)
	}
	return input, nil
}

// runHarvest handles the "greenies harvest" command.
//
// It shows all crop cycles whose harvest date is within the last 30 days and
// that have not yet been logged, lets the grower pick one, and records the
// actual tray count and gram weight alongside the planned expectations.
//
// Each logged batch gets its own record — a day where two crops were both
// harvested would produce two separate records, one per cycle.
func runHarvest() {
	// Load cycles and the existing harvest log.
	cycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Error loading cycle records: %v\n", err)
		os.Exit(1)
	}

	harvests, err := farm.LoadHarvests()
	if err != nil {
		fmt.Printf("Error loading harvest log: %v\n", err)
		os.Exit(1)
	}

	// Build a set of CycleIDs that have already been logged, so we don't
	// offer the same batch twice.
	logged := map[string]bool{}
	for _, h := range harvests {
		logged[h.CycleID] = true
	}

	// "Today" in UTC midnight, matching the format used in stored date strings.
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// The log window: any cycle harvested in the last 30 days.
	// 30 days gives the grower plenty of time to log without nagging, but
	// keeps the list short — batches older than a month fall off the list.
	cutoff := today.AddDate(0, 0, -30)

	// Collect cycles eligible for logging: harvest date is past (or today)
	// and within the 30-day window, and not yet in the log.
	var eligible []farm.Cycle
	for _, c := range cycles {
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		// harv <= today  →  !today.Before(harv)
		// harv >= cutoff →  !harv.Before(cutoff)
		if !today.Before(harv) && !harv.Before(cutoff) && !logged[c.CycleID] {
			eligible = append(eligible, c)
		}
	}

	if len(eligible) == 0 {
		fmt.Println("No recent harvests to log.")
		fmt.Println("Cycles are eligible to log for 30 days after their harvest date.")
		fmt.Println("Use \"greenies harvestlog\" to see previously logged harvests.")
		return
	}

	// Show the list sorted by harvest date, most recent first.
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].HarvestDate > eligible[j].HarvestDate
	})

	fmt.Println("Recent harvests ready to log:")
	fmt.Println()
	for i, c := range eligible {
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		trayWord := "tray"
		if c.Trays != 1 {
			trayWord = "trays"
		}
		// Show the expected grams if we have them; skip if unknown (older cycles).
		expectedLabel := ""
		if c.ExpectedGrams > 0 {
			expectedLabel = fmt.Sprintf("   expected: %dg", c.ExpectedGrams)
		}
		fmt.Printf("  %d.  %-12s  %d %s   harvest %s%s\n",
			i+1, task.Capitalize(c.CropName), c.Trays, trayWord,
			harv.Format("Jan 02"), expectedLabel)
	}
	fmt.Println()
	fmt.Println("  (Type \"cancel\" at any prompt to exit without saving.)")
	fmt.Println()

	// Set up the ask helper with cancel support — same pattern as runPlan().
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		v := strings.TrimSpace(line)
		if strings.EqualFold(v, "cancel") {
			fmt.Println("Cancelled.")
			os.Exit(0)
		}
		return v
	}

	// Ask which cycle to log.
	choiceStr := ask(fmt.Sprintf("Which cycle to log? (1-%d): ", len(eligible)))
	n, err := strconv.Atoi(choiceStr)
	if err != nil || n < 1 || n > len(eligible) {
		fmt.Printf("Please enter a number between 1 and %d.\n", len(eligible))
		os.Exit(1)
	}
	chosen := eligible[n-1]

	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	chosenTrayWord := "tray"
	if chosen.Trays != 1 {
		chosenTrayWord = "trays"
	}
	fmt.Printf("\nLogging harvest: %s — %d %s — harvest %s\n\n",
		task.Capitalize(chosen.CropName), chosen.Trays, chosenTrayWord,
		harv.Format("Jan 02"))

	// Ask actual trays. The default is the planned tray count — press Enter
	// to accept it. The grower only needs to type a different number if they
	// lost a tray (e.g. mould, accident).
	actualTraysStr := ask(fmt.Sprintf("Actual trays harvested [%d]: ", chosen.Trays))
	actualTrays := chosen.Trays // default
	if actualTraysStr != "" {
		t, err := strconv.Atoi(actualTraysStr)
		if err != nil || t < 0 {
			fmt.Println("Please enter a whole number (e.g. 3). Type 0 if no usable crop was cut.")
			os.Exit(1)
		}
		actualTrays = t
	}

	// Ask actual grams. The default is the expected yield if we have it,
	// or blank (must type a number) if the cycle pre-dates the ExpectedGrams field.
	var gramsPrompt string
	defaultGrams := chosen.ExpectedGrams
	if defaultGrams > 0 {
		gramsPrompt = fmt.Sprintf("Actual grams harvested [%d]: ", defaultGrams)
	} else {
		gramsPrompt = "Actual grams harvested: "
	}
	actualGramsStr := ask(gramsPrompt)

	// Parse actual grams.
	actualGrams := defaultGrams // default (may be 0 if unknown)
	if actualGramsStr != "" {
		g, err := strconv.Atoi(actualGramsStr)
		if err != nil || g < 0 {
			fmt.Println("Please enter a whole number in grams (e.g. 1400).")
			os.Exit(1)
		}
		actualGrams = g
	} else if defaultGrams == 0 {
		// No default and user pressed Enter — we need a number.
		fmt.Println("Please enter the actual grams harvested (e.g. 1400).")
		os.Exit(1)
	}

	// Optional notes — pressing Enter skips this field.
	notes := ask("Notes (optional — press Enter to skip): ")

	// Build the record and save.
	record := farm.HarvestRecord{
		CycleID:       chosen.CycleID,
		CropName:      chosen.CropName,
		HarvestDate:   chosen.HarvestDate,
		ExpectedTrays: chosen.Trays,
		ActualTrays:   actualTrays,
		ExpectedGrams: chosen.ExpectedGrams,
		ActualGrams:   actualGrams,
		Notes:         notes,
	}

	harvests = append(harvests, record)
	if err := farm.SaveHarvests(harvests); err != nil {
		fmt.Printf("Error saving harvest record: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nHarvest logged — %s, %d trays, %dg.\n",
		task.Capitalize(chosen.CropName), actualTrays, actualGrams)
	fmt.Println("Run \"greenies harvestlog\" to see your full history.")
}

// runHarvestLog handles the "greenies harvestlog" command.
//
// It prints all saved harvest records sorted most-recent-first, showing the
// planned yield alongside what was actually cut — so the grower can spot
// trends over time (e.g. a crop that consistently under-yields).
func runHarvestLog() {
	harvests, err := farm.LoadHarvests()
	if err != nil {
		fmt.Printf("Error loading harvest log: %v\n", err)
		os.Exit(1)
	}

	if len(harvests) == 0 {
		fmt.Println("No harvests logged yet.")
		fmt.Println("Run \"greenies harvest\" after each harvest to build your log.")
		return
	}

	// Sort most recent first. HarvestDate is YYYY-MM-DD, so string comparison
	// gives the same result as date comparison — later dates sort higher.
	sort.Slice(harvests, func(i, j int) bool {
		return harvests[i].HarvestDate > harvests[j].HarvestDate
	})

	fmt.Printf("Harvest log — %d records\n", len(harvests))
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println()

	for _, h := range harvests {
		harv, _ := time.Parse(task.DateFormat, h.HarvestDate)

		// Tray column: "3/3 trays" or "2/3 trays" (actual/expected).
		// This immediately shows whether any trays were lost.
		trayCol := fmt.Sprintf("%d/%d trays", h.ActualTrays, h.ExpectedTrays)

		// Gram column: show actual alongside expected (if we have expected data).
		// Old cycles logged before ExpectedGrams was added will show just actual.
		var gramCol string
		if h.ExpectedGrams > 0 {
			gramCol = fmt.Sprintf("%dg / %dg expected", h.ActualGrams, h.ExpectedGrams)
		} else {
			gramCol = fmt.Sprintf("%dg", h.ActualGrams)
		}

		fmt.Printf("  %s   %-12s  %-10s  %s\n",
			harv.Format("Jan 02"),
			task.Capitalize(h.CropName),
			trayCol,
			gramCol)

		// Notes appear on their own line, indented to align under the crop name.
		if h.Notes != "" {
			fmt.Printf("               %s\n", h.Notes)
		}
	}

	fmt.Println()
}


// runSync handles the "greenies sync" command.
//
// It pushes the current schedule to Google in two ways:
//   1. Google Tasks — one checkable to-do entry per day, listing that day's work.
//   2. Google Calendar — one all-day event per day, with the full farm snapshot
//      in the description so the grower can see the live farm state when they
//      tap on any day in their phone calendar.
//
// Running it multiple times is safe — you always end up with exactly one copy
// of each entry, no duplicates.
//
// Only tasks from today forward are synced. Past tasks are history and don't
// need to appear in Google.
func runSync() {
	if !gcal.CredentialsExist() {
		fmt.Println("Google Calendar is not set up.")
		fmt.Println("Place credentials.json in ~/.greenies/ to enable calendar sync.")
		fmt.Println("See the Phase 5 setup notes for instructions.")
		return
	}

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Load the farm layout and cycle records. These are passed directly into
	// Sync so it can compute a fresh snapshot for each specific calendar day —
	// every event shows what the farm will look like on that date, not what
	// it looks like right now at the moment the sync runs.
	// If either file is missing or unreadable we still proceed — the sync will
	// just embed empty farm snapshots rather than failing entirely.
	envs, _ := farm.LoadConfig()
	cycles, _ := farm.LoadCycles()

	// context.Background() means "no deadline, no cancellation" — fine for
	// a short interactive command that the user is actively waiting on.
	ctx := context.Background()

	exporter, err := gcal.NewExporter(ctx)
	if err != nil {
		fmt.Printf("Error connecting to Google Calendar: %v\n", err)
		os.Exit(1)
	}

	// Record when the sync starts so we can show elapsed time at the end.
	// Syncing makes many individual API calls to Google, so it can take a
	// minute or two — it's reassuring to see how long it actually took.
	syncStart := time.Now()

	// Run the sync in the background (a "goroutine" — a lightweight task that
	// runs alongside the rest of the program). This frees up the main thread
	// to run the live timer below. The goroutine sends its result (nil for
	// success, or an error message) into the "done" channel when it finishes.
	// A channel is like a pipe: one end sends a value, the other end receives it.
	done := make(chan error, 1)
	go func() {
		done <- exporter.Sync(tasks, envs, cycles)
	}()

	// Tick once per second and show the current elapsed time.
	// \r (carriage return) moves the cursor to the start of the current line
	// without adding a new line — so each update overwrites the timer in place.
	// The sync's own progress messages ("Finding calendar...", "3 removed.", etc.)
	// will still appear as they happen — the timer just ticks between them.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		// The sync goroutine finished — break out of the timer loop.
		case err := <-done:
			ticker.Stop()
			// End the current line. If the timer ticked last (and left the
			// cursor sitting after "⏱  Xs "), this puts "Done in Xs." on its
			// own clean line. If sync's last print already ended with \n, this
			// just adds one harmless blank line.
			fmt.Println()
			if err != nil {
				fmt.Printf("Sync failed: %v\n", err)
				os.Exit(1)
			}
			break loop

		// One second has ticked — rewrite the timer on the current line.
		// \r goes to position 0, then we print the elapsed time with a trailing
		// space. The next thing to print (sync result or another tick) will
		// appear right after the space — giving "⏱  3s 105 removed." format.
		case <-ticker.C:
			fmt.Printf("\r⏱  %s ", time.Since(syncStart).Round(time.Second))
		}
	}

	elapsed := time.Since(syncStart).Round(time.Second)
	fmt.Printf("Done in %s.\n", elapsed)
	fmt.Println("Run \"greenies list\" to see your local schedule.")
}

// printUsage prints a friendly summary of available commands.
// This is shown when the user types "greenies" with no arguments, or types
// an unrecognised command.
func printUsage() {
	fmt.Println(`Greenies — microgreens farm scheduler

Usage:
  greenies list
  greenies delete
  greenies clear
  greenies crops
  greenies plan
  greenies snapshot
  greenies sync
  greenies harvest
  greenies harvestlog

Examples:
  greenies list
  greenies crops
  greenies plan
  greenies snapshot
  greenies sync
  greenies harvest
  greenies harvestlog
  greenies trial`)
}

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
			sow, _ := time.Parse(trial.DateFormat, tr.SowDate)
			fmt.Printf("  %d. %s %dx — Day %d (sown %s)\n",
				i+1, tr.DisplayName(), tr.Trays, dayNum, sow.Format("Mon Jan 02"))
		}
		fmt.Println()
	}

	// Route to the right flow. If there are no active trials, go straight to
	// starting a new one — no need to ask.
	var choice string
	if len(active) == 0 {
		choice = "n"
	} else {
		choice = strings.ToLower(strings.TrimSpace(
			ask("(m)anage a trial / (n)ew trial [n]: ")))
		if choice == "" {
			choice = "n"
		}
	}

	switch choice {
	case "n", "new":
		startNewTrial(ask, trials)
	default: // "m" or "manage" or any other input routes to the manage flow
		manageTrial(ask, trials, active)
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
			sow, _ := time.Parse(trial.DateFormat, pt.SowDate)

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
					d, _ := time.Parse(trial.DateFormat, obs.Date)
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
	sowTime, _ := time.Parse(trial.DateFormat, sowDate)

	// LastManaged is set to the day before the sow date so the very first
	// manage session always starts at Day 1 — the sow date itself.
	lastManaged := sowTime.AddDate(0, 0, -1).Format(trial.DateFormat)

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
		mtlDateStr := sowTime.AddDate(0, 0, moveToLightDay-1).Format(trial.DateFormat)
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
		harvDateStr := sowTime.AddDate(0, 0, harvestDay-1).Format(trial.DateFormat)
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

	// ── Step 9: confirmation summary ─────────────────────────────────────────

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

	sow, _ := time.Parse(trial.DateFormat, tr.SowDate)
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
	lastManaged, err := time.Parse(trial.DateFormat, tr.LastManaged)
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
					Date:  d.Format(trial.DateFormat),
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
	tr.LastManaged = t.Format(trial.DateFormat)

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
	source := crop.CSVSource{Path: cropsPath}
	existingCrops, _ := source.LoadCrops()
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
				mtlDate, parseErr := time.Parse(trial.DateFormat, mtlDateStr)
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
				harvDate, parseErr := time.Parse(trial.DateFormat, harvDateStr)
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
