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
package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"fmt"
	"math"    // for math.Ceil, which rounds a decimal up to the next whole number
	"os"      // for os.Exit and reading command-line arguments
	"strconv" // for converting text like "2" into the number 2
	"strings" // for string utilities used in the crops and schedule commands
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/scheduler"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)


func main() {
	// os.Args is the list of words the user typed. os.Args[0] is always the
	// program name itself ("greenies"). The subcommand (add, list, etc.) is
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
	default:
		fmt.Printf("Unknown command: %q\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

// runList handles the "greenies list" command.
// It immediately shows the current 7-day week, then optionally lets the user
// view a custom date range or the full current month.
func runList() {
	// Load all tasks from disk.
	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Always show the current week first — today through 6 days from now.
	now := time.Now()
	weekEnd := now.AddDate(0, 0, 6)
	if err := calendar.PrintRange(
		now.Format(task.DateFormat),
		weekEnd.Format(task.DateFormat),
		tasks,
	); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Ask if they want to see something else.
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
	}

	choice := ask("View something else? (range / month): ")

	switch strings.ToLower(choice) {
	case "range":
		// Ask for a start and end date using the same flexible format as plan.
		startDate, err := parseHarvestDate(ask("Start date (MM-DD or YYYY-MM-DD): "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		endDate, err := parseHarvestDate(ask("End date (MM-DD or YYYY-MM-DD): "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if err := calendar.PrintRange(startDate, endDate, tasks); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "month":
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

	default:
		// Empty input or anything unrecognised — just exit quietly.
		// The user has already seen the week view, so nothing more is needed.
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

	removed := len(tasks) - len(updated)
	if removed == 1 {
		fmt.Printf("1 task deleted.\n")
	} else {
		fmt.Printf("%d tasks deleted.\n", removed)
	}
}

// runClear deletes every task in the store after asking for confirmation.
// Useful during development and testing. The confirmation step is a safety net
// so a mistyped command cannot accidentally wipe the whole schedule.
func runClear() {
	fmt.Print("This will delete ALL tasks. Type \"yes\" to confirm: ")

	var response string
	fmt.Scanln(&response)

	if response != "yes" {
		fmt.Println("Cancelled — nothing was deleted.")
		return
	}

	// Save an empty list, which overwrites the existing file.
	if err := store.Save([]task.Task{}); err != nil {
		fmt.Printf("Error clearing tasks: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("All tasks deleted.")
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

// runPlan handles the "greenies plan" command.
// Instead of flags, it asks the user three questions interactively, then
// shows a full preview and asks for confirmation before saving anything.
func runPlan() {
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
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
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
			fmt.Printf("No yield data found for %s in the crop library.\n", capitalize(found.Name))
			fmt.Println("Add a yield_grams value to crops.csv, or plan by tray count instead.")
			os.Exit(1)
		}

		yieldStr := ask(fmt.Sprintf("Desired yield in grams? (%s yields ~%dg per tray): ",
			capitalize(found.Name), found.YieldGrams))
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
		sowDate, err := parseHarvestDate(ask("Sow date (MM-DD or YYYY-MM-DD): "))
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
		harvestDate, err := parseHarvestDate(ask("Harvest date (MM-DD or YYYY-MM-DD): "))
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
		capitalize(found.Name), trays, trayWord, anchorLabel, displayDate)

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

	// --- Optional: weekly repetition ---
	// Many growers sow the same crop every week to maintain a steady harvest
	// rhythm. If the user wants repeats, we generate additional copies of the
	// schedule, each shifted forward by one week (7 days) at a time.
	allNewTasks := newTasks // start with the first cycle

	repeatStr := ask("Repeat this cycle weekly? Type \"yes\" to add more weeks: ")
	if repeatStr == "yes" {
		weeksStr := ask("How many additional weeks? ")
		additionalWeeks, err := strconv.Atoi(weeksStr)
		if err != nil || additionalWeeks < 1 {
			fmt.Println("Please enter a whole number greater than zero (e.g. 3).")
			os.Exit(1)
		}

		// Parse the anchor date (sow or harvest) so we can shift it by weeks.
		// Safe to ignore the error here — displayDate was already validated by
		// parseHarvestDate earlier in this function, so we know it's a valid date.
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
		}

		fmt.Printf("%d cycles scheduled (%d weeks total).\n",
			additionalWeeks+1, additionalWeeks+1)
	}

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

	fmt.Printf("%d tasks added to the calendar.\n", len(allNewTasks))
	fmt.Println("Run \"greenies list\" to see the schedule.")
}

// parseHarvestDate accepts a harvest date in either MM-DD or YYYY-MM-DD format
// and always returns a full YYYY-MM-DD string.
//
// MM-DD is the convenient shorthand for dates in the current year.
// YYYY-MM-DD lets the user cross a year boundary — e.g. scheduling in December
// for a January harvest.
func parseHarvestDate(input string) (string, error) {
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

// capitalize uppercases the first letter of a string and leaves the rest alone.
// Used to display crop names from the CSV (which are lowercase) with a capital
// at the start of a sentence or title.
//
// Note: an identical copy of this function exists in internal/scheduler/scheduler.go.
// Go does not allow sharing unexported (lowercase) helpers across packages, so both
// packages keep their own copy. If you change one, change the other too.
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

Examples:
  greenies list
  greenies crops
  greenies plan`)
}
