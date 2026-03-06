// Greenies — a microgreens farm scheduling tool.
//
// This is the entry point of the program — the first thing Go runs when you
// type "./greenies" in the terminal. Its job is to read the command you typed
// and hand off to the right piece of code.
//
// Commands:
//
//	greenies add   --date YYYY-MM-DD --title "label" [--notes "text"]
//	greenies list  --date YYYY-MM-DD
//	greenies list  --week YYYY-MM-DD
//	greenies edit  <id> [--title "new title"] [--notes "new notes"] [--date YYYY-MM-DD]
//	greenies delete <id>
//	greenies clear
package main

import (
	"flag"  // Go's built-in package for reading command-line flags (the --date, --title parts)
	"fmt"
	"os"    // for os.Exit and reading command-line arguments
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
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
		runList(os.Args[2:])
	case "edit":
		runEdit(os.Args[2:])
	case "delete":
		runDelete(os.Args[2:])
	case "clear":
		runClear()
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
// It shows tasks for a single date (--date) or a 7-day range (--week).
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dateFlag := fs.String("date", "", "Show tasks for one specific date (YYYY-MM-DD)")
	weekFlag := fs.String("week", "", "Show tasks for 7 days starting from this date (YYYY-MM-DD)")
	fs.Parse(args)

	// Load all tasks from disk.
	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	switch {
	case *dateFlag != "":
		// Show a single day.
		calendar.PrintDay(*dateFlag, tasks)

	case *weekFlag != "":
		// Show 7 days starting from the given date.
		start, err := time.Parse(task.DateFormat, *weekFlag)
		if err != nil {
			fmt.Printf("Error: date %q is not in the right format. Use YYYY-MM-DD (e.g. 2026-03-05)\n", *weekFlag)
			os.Exit(1)
		}
		// AddDate(0, 0, 6) adds 6 days to the start date, giving a 7-day window.
		end := start.AddDate(0, 0, 6)
		if err := calendar.PrintRange(*weekFlag, end.Format(task.DateFormat), tasks); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	default:
		// If no flag was given, default to showing today.
		today := time.Now().Format(task.DateFormat)
		calendar.PrintDay(today, tasks)
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

// printUsage prints a friendly summary of available commands.
// This is shown when the user types "greenies" with no arguments, or types
// an unrecognised command.
func printUsage() {
	fmt.Println(`Greenies — microgreens farm scheduler

Usage:
  greenies add    --date YYYY-MM-DD --title "label" [--notes "text"]
  greenies list   [--date YYYY-MM-DD | --week YYYY-MM-DD]
  greenies edit   <id> [--title "new"] [--notes "new"] [--date YYYY-MM-DD]
  greenies delete <id>
  greenies clear

Examples:
  greenies add --date 2026-03-05 --title "Sow sunflowers" --notes "2 trays"
  greenies list --date 2026-03-05
  greenies list --week 2026-03-03
  greenies edit a3f2c81b --title "Sow peas"
  greenies delete a3f2c81b`)
}
