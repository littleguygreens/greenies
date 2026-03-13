package main

import (
	"context" // for context.Background(), used when calling Google Calendar
	"fmt"
	"os"  // for os.Exit
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/store"
)

// runSync handles the "greenies sync" command.
//
// It pushes the current schedule to Google in two ways:
//  1. Google Tasks — one checkable to-do entry per day, listing that day's work.
//  2. Google Calendar — one all-day event per day, with the full farm snapshot
//     in the description so the grower can see the live farm state when they
//     tap on any day in their phone calendar.
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
