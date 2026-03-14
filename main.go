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
	case "adjust":
		runAdjust()
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
  greenies list
  greenies delete
  greenies clear
  greenies crops
  greenies plan
  greenies snapshot
  greenies sync
  greenies harvest
  greenies harvestlog
  greenies trial
  greenies adjust

Examples:
  greenies list
  greenies crops
  greenies plan
  greenies snapshot
  greenies sync
  greenies harvest
  greenies harvestlog
  greenies trial
  greenies adjust`)
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
