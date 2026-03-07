// Greenies — a microgreens farm scheduling tool.
//
// This is the entry point of the program — the first thing Go runs when you
// type "./greenies" in the terminal. Its job is to read the command you typed
// and hand off to the right piece of code.
//
// Commands:
//
//	greenies add      --date YYYY-MM-DD --title "label" [--notes "text"]
//	greenies list
//	greenies edit     <id> [--title "new title"] [--notes "new notes"] [--date YYYY-MM-DD]
//	greenies delete   <id>
//	greenies clear
//	greenies crops
//	greenies plan
package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"flag"    // Go's built-in package for reading command-line flags (the --date, --title parts)
	"fmt"
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
	case "add":
		runAdd(os.Args[2:])
	case "list":
		runList()
	case "edit":
		runEdit(os.Args[2:])
	case "delete":
		runDelete(os.Args[2:])
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

// runAdd handles the "greenies add" command.
// It reads the flags the user typed, creates a new task, and saves it.
func runAdd(args []string) {
	// Create a new set of flags specific to the "add" subcommand.
	// Each flag.String call defines one flag: its name, its default value,
	// and a description shown in help text.
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	dateFlag  := fs.String("date",  "", "The date for this task, in YYYY-MM-DD format (e.g. 2026-03-05)")
	titleFlag := fs.String("title", "", "A short label for the task (e.g. \"Sow sunflowers\")")
	notesFlag := fs.String("notes", "", "Optional extra detail (e.g. \"2 trays, main tent\")")
	fs.Parse(args)

	// Validate that the required flags were provided.
	if *dateFlag == "" {
		fmt.Println("Error: --date is required. Example: --date 2026-03-05")
		os.Exit(1)
	}
	if *titleFlag == "" {
		fmt.Println("Error: --title is required. Example: --title \"Sow sunflowers\"")
		os.Exit(1)
	}

	// Validate the date format before saving — give a clear error if the
	// user typed something like "March 5" instead of "2026-03-05".
	if _, err := time.Parse(task.DateFormat, *dateFlag); err != nil {
		fmt.Printf("Error: date %q is not in the right format. Use YYYY-MM-DD (e.g. 2026-03-05)\n", *dateFlag)
		os.Exit(1)
	}

	// Create the new task.
	t, err := task.New(*titleFlag, *dateFlag, *notesFlag)
	if err != nil {
		fmt.Printf("Error creating task: %v\n", err)
		os.Exit(1)
	}

	// Load the existing tasks from disk so we can append to them.
	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Append the new task to the list and save everything back to disk.
	tasks = append(tasks, t)
	if err := store.Save(tasks); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Task added (ID: %s)\n", t.ID)

	// Show the day view for the task's date so the user can immediately
	// see it on the calendar.
	calendar.PrintDay(*dateFlag, tasks)
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

// runEdit handles the "greenies edit <id>" command.
// It finds the task with the given ID and updates whichever fields were provided.
func runEdit(args []string) {
	if len(args) == 0 {
		fmt.Println("Error: edit requires a task ID. Example: greenies edit a3f2c81b9d047e56")
		os.Exit(1)
	}

	// The task ID is the first argument, before any flags.
	id := args[0]

	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	titleFlag := fs.String("title", "", "New title for the task")
	notesFlag := fs.String("notes", "", "New notes for the task")
	dateFlag  := fs.String("date",  "", "New date for the task (YYYY-MM-DD)")
	fs.Parse(args[1:])

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Find the task with the matching ID and update it.
	// We use a pointer (the & symbol) to edit the task in-place inside the
	// slice rather than working on a copy that would be thrown away.
	found := false
	for i := range tasks {
		if tasks[i].ID == id {
			found = true

			// Only update a field if the user actually provided a new value
			// for it — leaving a flag blank means "keep the existing value".
			if *titleFlag != "" {
				tasks[i].Title = *titleFlag
			}
			if *notesFlag != "" {
				tasks[i].Notes = *notesFlag
			}
			if *dateFlag != "" {
				if _, err := time.Parse(task.DateFormat, *dateFlag); err != nil {
					fmt.Printf("Error: date %q is not valid. Use YYYY-MM-DD (e.g. 2026-03-05)\n", *dateFlag)
					os.Exit(1)
				}
				tasks[i].Date = *dateFlag
			}

			// Stamp the updated time so we have an accurate record of when
			// this task was last changed.
			tasks[i].UpdatedAt = time.Now()

			fmt.Printf("Task %s updated.\n", id)
			break
		}
	}

	if !found {
		fmt.Printf("No task found with ID %q. Use \"greenies list\" to see task IDs.\n", id)
		os.Exit(1)
	}

	if err := store.Save(tasks); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}
}

// runDelete handles the "greenies delete <id>" command.
// It removes the task with the given ID permanently.
func runDelete(args []string) {
	if len(args) == 0 {
		fmt.Println("Error: delete requires a task ID. Example: greenies delete a3f2c81b9d047e56")
		os.Exit(1)
	}

	id := args[0]

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Build a new list that contains every task except the one being deleted.
	// This is a common Go pattern for removing an item from a list.
	var updated []task.Task
	found := false
	for _, t := range tasks {
		if t.ID == id {
			found = true
			// Skip this task — it is being deleted.
			continue
		}
		updated = append(updated, t)
	}

	if !found {
		fmt.Printf("No task found with ID %q. Use \"greenies list\" to see task IDs.\n", id)
		os.Exit(1)
	}

	if err := store.Save(updated); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Task %s deleted.\n", id)
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
	fmt.Println("Available crops:")
	for _, c := range crops {
		fmt.Printf("  %s\n", c.Name)
	}
	fmt.Println()

	cropName := ask("Which crop? ")
	if cropName == "" {
		fmt.Println("No crop entered — cancelled.")
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
		fmt.Printf("Crop %q not found in the library. Check the spelling and try again.\n", cropName)
		os.Exit(1)
	}

	// --- Question 2: how many trays? ---
	traysStr := ask("How many trays? ")
	trays, err := strconv.Atoi(traysStr)
	if err != nil || trays < 1 {
		fmt.Println("Please enter a whole number greater than zero (e.g. 2).")
		os.Exit(1)
	}

	// --- Question 3: harvest date? ---
	harvestDate, err := parseHarvestDate(ask("Harvest date (MM-DD or YYYY-MM-DD): "))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Generate the schedule — a preview of every day plus the saveable tasks.
	preview, newTasks, err := scheduler.Schedule(*found, harvestDate, trays)
	if err != nil {
		fmt.Printf("Error generating schedule: %v\n", err)
		os.Exit(1)
	}

	// Show the full preview so the user can check everything looks right.
	trayWord := "tray"
	if trays != 1 {
		trayWord = "trays"
	}
	fmt.Printf("\n%s — %d %s — harvest %s\n\n",
		capitalize(found.Name), trays, trayWord, harvestDate)

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

		// Parse the original harvest date so we can add weeks to it.
		baseHarvest, _ := time.Parse(task.DateFormat, harvestDate)

		for week := 1; week <= additionalWeeks; week++ {
			// Shift the harvest date forward by 7 days per additional week.
			weeklyHarvest := baseHarvest.AddDate(0, 0, week*7).Format(task.DateFormat)

			_, weekTasks, err := scheduler.Schedule(*found, weeklyHarvest, trays)
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
  greenies add      --date YYYY-MM-DD --title "label" [--notes "text"]
  greenies list
  greenies edit     <id> [--title "new"] [--notes "new"] [--date YYYY-MM-DD]
  greenies delete   <id>
  greenies clear
  greenies crops
  greenies plan

Examples:
  greenies add --date 2026-03-05 --title "Sow sunflowers" --notes "2 trays"
  greenies list
  greenies edit a3f2c81b --title "Sow peas"
  greenies delete a3f2c81b
  greenies crops
  greenies plan`)
}
