package main

import (
	"fmt"
	"os"      // for os.Args and os.Exit
	"strings" // for strings.Repeat
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/trial"
	"github.com/littleguygreens/greenies/internal/visualizer"
)

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
				sow, _ := time.Parse(task.DateFormat, tr.SowDate)
				fmt.Printf("%s %dx — Day %d (trial)\n", tr.DisplayName(), tr.Trays, dayNum)
				fmt.Printf("sown %s\n", sow.Format("Mon Jan 02"))
				// Show the tentative harvest date if one was given when the
				// trial started — labelled "(tentative)" so it is clearly not
				// a confirmed date from the main crop schedule.
				if hStr := tr.TentativeHarvestDate(); hStr != "" {
					hDate, _ := time.Parse(task.DateFormat, hStr)
					fmt.Printf("harvest %s (tentative)\n", hDate.Format("Mon Jan 02"))
				}
				fmt.Println()
			}
		}
	}
}
