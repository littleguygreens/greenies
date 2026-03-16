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
//	greenies sync
//	greenies harvest
//	greenies harvestlog
//	greenies trial
//	greenies adjust
//	greenies gui
//	greenies install-desktop
package main

import (
	"fmt"
	"os"      // for os.Exit and reading command-line arguments
	"strings" // for string utilities used throughout
	"time"

	"github.com/littleguygreens/greenies/internal/task"
)

func main() {
	// os.Args is the list of words the user typed. os.Args[0] is always the
	// program name itself ("greenies"). The subcommand (list, delete, etc.) is
	// os.Args[1] if it exists.
	// If no subcommand is given, launch the GUI — the grower just double-clicked
	// the program or typed "greenies" with nothing after it. This makes the GUI
	// the default experience, while all CLI commands still work exactly as before.
	if len(os.Args) < 2 {
		runGui()
		return
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
	case "adjust":
		runAdjust()
	case "gui":
		runGui()
	case "install-desktop":
		runInstallDesktop()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Printf("Unknown command: %q\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

// printUsage prints a friendly summary of available commands.
// This is shown when the user types "greenies" with no arguments, or types
// an unrecognised command.
func printUsage() {
	fmt.Println(`Greenies — microgreens farm scheduler

Usage:
  greenies                Launch the browser-based GUI (default)
  greenies <command>      Run a specific CLI command

Commands:
  gui              Open the browser-based dashboard
  list             Show upcoming tasks (week or month view)
  plan             Schedule a new crop cycle
  snapshot         Live farm view — slots, active cycles, conflicts
  crops            List all crop varieties from the crop library
  harvest          Log a completed harvest (actual trays + grams)
  harvestlog       View full harvest history
  trial            Start, manage, or compare crop trials
  adjust           Mid-cycle tray adjustments (add/remove days)
  sync             Push schedule to Google Calendar + Tasks
  delete           Remove a task or entire crop cycle
  clear            Wipe all tasks (asks for confirmation)
  install-desktop  Create a desktop shortcut (Linux only)
  help             Show this help message

Examples:
  greenies                Open the GUI in your browser
  greenies list           View this week's tasks
  greenies plan           Schedule a new crop
  greenies snapshot       See today's farm status`)
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
