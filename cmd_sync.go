package main

import (
	"bufio"   // for reading user input (yes/no prompt)
	"context" // for context.Background(), used when calling Google APIs
	"fmt"
	"os"      // for os.Exit, os.Stdin
	"strings" // for strings.TrimSpace, strings.ToLower
	"time"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/store"
)

// runSync handles the "greenies sync" command.
//
// It does two things:
//
//  1. Google Sheets sync (crop library) — pulls the latest crop data from
//     a Google Sheet and saves it to the local crops.csv cache. On first run,
//     it offers to create the Sheet and populate it from existing data.
//
//  2. Google Calendar + Tasks sync — pushes the current schedule to Google
//     so the grower can see tasks and farm snapshots on their phone calendar.
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

	// context.Background() means "no deadline, no cancellation" — fine for
	// a short interactive command that the user is actively waiting on.
	ctx := context.Background()

	// ── Google Sheets sync (crop library) ────────────────────────────────
	//
	// This runs BEFORE Calendar/Tasks sync so that any crop data changes
	// from the Sheet are reflected in the local cache before we build
	// calendar events.
	syncSheets(ctx)

	// ── Load local data for Calendar/Tasks sync ──────────────────────────

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

// syncSheets handles the Google Sheets portion of the sync — pulling the
// latest crop library from the Google Sheet and saving it to the local
// crops.csv cache.
//
// On first run: asks the user if they want to link Google Sheets, creates
// the spreadsheet, and populates it from existing crops.csv data.
//
// On subsequent runs: pulls from the Sheet and overwrites the local cache.
//
// If anything goes wrong (no internet, API error), it prints a warning and
// continues — the local crops.csv is left untouched and the Calendar/Tasks
// sync proceeds as normal.
func syncSheets(ctx context.Context) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("  Warning: could not load config: %v\n", err)
		return
	}

	// ── First-time setup ────────────────────────────────────────────────
	//
	// If Sheets sync has never been enabled, ask the user if they want it.
	// This only happens once — after they say yes (or no), the config file
	// remembers their choice.
	if !cfg.SheetsEnabled {
		fmt.Println()
		fmt.Println("Link Google Sheets for your crop library?")
		fmt.Println("This lets you edit crop parameters in Google Sheets from any device.")
		fmt.Print("(y/n): ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))

		if answer != "y" && answer != "yes" {
			fmt.Println("Skipping Google Sheets — using local crops.csv only.")
			fmt.Println()
			return
		}

		// Create the Google Sheet with the two tabs and headers.
		fmt.Println("Creating Google Sheet...")
		sheetID, err := gcal.CreateCropSheet(ctx)
		if err != nil {
			fmt.Printf("  Could not create Google Sheet: %v\n", err)
			fmt.Println("  Continuing with local crops.csv only.")
			fmt.Println()
			return
		}

		// Save the sheet ID to config so we never have to ask again.
		cfg.SheetsEnabled = true
		cfg.SheetID = sheetID
		if err := config.Save(cfg); err != nil {
			fmt.Printf("  Warning: created Sheet but could not save config: %v\n", err)
		}

		// Print the URL so the user can bookmark it and open it on any device.
		fmt.Println()
		fmt.Println("Google Sheet created!")
		fmt.Printf("  URL: https://docs.google.com/spreadsheets/d/%s/edit\n", sheetID)
		fmt.Println()
		fmt.Println("Bookmark that URL — it's where you'll edit your crop library.")

		// If the user already has crop data in their local CSV, push it to
		// the new Sheet so they don't have to re-enter everything by hand.
		cropsPath, pathErr := crop.CropsFilePath()
		if pathErr == nil {
			localSource := crop.CSVSource{Path: cropsPath}
			if localCrops, loadErr := localSource.LoadCrops(); loadErr == nil && len(localCrops) > 0 {
				fmt.Printf("Uploading %d crop varieties to Google Sheet...\n", len(localCrops))

				sc, clientErr := gcal.NewSheetsClient(ctx, sheetID)
				if clientErr == nil {
					if pushErr := sc.PushCrops(localCrops); pushErr == nil {
						fmt.Println("Crop library uploaded successfully.")
					} else {
						fmt.Printf("  Warning: could not upload crops: %v\n", pushErr)
					}
				}
			}
		}

		fmt.Println()
		return
	}

	// ── Subsequent sync: pull from Sheet → update local CSV ─────────────

	if cfg.SheetID == "" {
		// Sheets is "enabled" but has no sheet ID — shouldn't happen, but
		// handle it gracefully by skipping.
		return
	}

	fmt.Println("Syncing crop library from Google Sheets...")
	sc, err := gcal.NewSheetsClient(ctx, cfg.SheetID)
	if err != nil {
		fmt.Printf("  ⚠ Could not connect to Google Sheets: %v\n", err)
		fmt.Println("  Using local crops.csv (may be out of date).")
		fmt.Println()
		return
	}

	// Pull the latest crop data from the Sheet.
	pulledCrops, err := sc.PullCrops()
	if err != nil {
		fmt.Printf("  ⚠ Could not read Google Sheet: %v\n", err)
		fmt.Println("  Using local crops.csv (may be out of date).")
		fmt.Println()
		return
	}

	// If the Sheet is empty (just headers, no crop data), don't overwrite
	// the local CSV — the user might not have populated the Sheet yet.
	if len(pulledCrops) == 0 {
		fmt.Println("  Google Sheet is empty — keeping local crops.csv as-is.")
		fmt.Println("  Add crops to your Google Sheet and sync again.")
		fmt.Println()
		return
	}

	// Write the pulled data to the local CSV cache.
	cropsPath, err := crop.CropsFilePath()
	if err != nil {
		fmt.Printf("  Warning: could not find crops.csv path: %v\n", err)
		fmt.Println()
		return
	}

	if err := crop.WriteCrops(cropsPath, pulledCrops); err != nil {
		fmt.Printf("  Warning: could not update local crops.csv: %v\n", err)
		fmt.Println()
		return
	}

	fmt.Printf("  Crop library synced (%d varieties).\n", len(pulledCrops))
	fmt.Println()
}
