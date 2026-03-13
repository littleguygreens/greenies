package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"fmt"
	"os"      // for os.Exit and reading command-line arguments
	"strings" // for string utilities used throughout
	"time"

	"github.com/littleguygreens/greenies/internal/calendar"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

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
