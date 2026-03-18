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
	"github.com/littleguygreens/greenies/internal/trial"
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
		fmt.Println("Google sync is not set up yet.")
		fmt.Println()
		fmt.Println("To connect greenies to your Google account, you need a credentials")
		fmt.Println("file from Google. This is a one-time setup that takes about 5 minutes:")
		fmt.Println()
		fmt.Println("  1. Go to https://console.cloud.google.com/")
		fmt.Println("     Sign in with the Google account you want greenies to use.")
		fmt.Println()
		fmt.Println("  2. Create a new project (click the project dropdown at the top,")
		fmt.Println("     then \"New Project\"). Name it anything — \"greenies\" works.")
		fmt.Println()
		fmt.Println("  3. Enable three APIs for your project. Go to \"APIs & Services\" →")
		fmt.Println("     \"Library\" and search for each of these, then click \"Enable\":")
		fmt.Println("       • Google Calendar API")
		fmt.Println("       • Google Tasks API")
		fmt.Println("       • Google Sheets API")
		fmt.Println()
		fmt.Println("  4. Set up the consent screen. Go to \"APIs & Services\" →")
		fmt.Println("     \"OAuth consent screen\". Choose \"External\", fill in the app")
		fmt.Println("     name (\"greenies\"), your email, and save. Add yourself as a")
		fmt.Println("     test user under \"Test users\".")
		fmt.Println()
		fmt.Println("  5. Create credentials. Go to \"APIs & Services\" → \"Credentials\",")
		fmt.Println("     click \"+ Create Credentials\" → \"OAuth client ID\".")
		fmt.Println("     Application type: \"Desktop app\". Name: \"greenies\".")
		fmt.Println("     Click \"Create\", then \"Download JSON\".")
		fmt.Println()
		fmt.Println("  6. Move the downloaded file to ~/.greenies/credentials.json")
		fmt.Println("     (you may need to rename it — the download has a long name).")
		fmt.Println()
		fmt.Println("  7. Run \"greenies sync\" again. You'll be asked to sign in once.")
		fmt.Println()
		fmt.Println("After this one-time setup, syncing is just one click.")
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
// latest crop library and farm layout from the Google Sheet and saving
// them to local CSV files, then pushing trials data to the Sheet.
//
// On first run: asks the user if they want to link Google Sheets, creates
// the spreadsheet, and populates it from existing local data.
//
// On subsequent runs: pulls crops and farm from the Sheet and overwrites
// local caches; pushes trials to the Sheet (push-only).
//
// If anything goes wrong (no internet, API error), it prints a warning and
// continues — local files are left untouched and the Calendar/Tasks sync
// proceeds as normal.
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
		fmt.Println("Link Google Sheets for your farm data?")
		fmt.Println("This lets you view and edit your crop library and farm layout")
		fmt.Println("in Google Sheets from any device.")
		fmt.Print("(y/n): ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))

		if answer != "y" && answer != "yes" {
			fmt.Println("Skipping Google Sheets — using local files only.")
			fmt.Println()
			return
		}

		// Create the Google Sheet with all seven tabs and headers.
		fmt.Println("Creating Google Sheet...")
		sheetID, err := gcal.CreateSheet(ctx)
		if err != nil {
			fmt.Printf("  Could not create Google Sheet: %v\n", err)
			fmt.Println("  Continuing with local files only.")
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
		fmt.Println("Bookmark that URL — it's where you'll view and edit your farm data.")

		// Push existing local data to the new Sheet so the user doesn't
		// have to re-enter everything by hand.
		sc, clientErr := gcal.NewSheetsClient(ctx, sheetID)
		if clientErr != nil {
			fmt.Printf("  Warning: could not connect to push data: %v\n", clientErr)
			fmt.Println()
			return
		}

		// Push crops.
		cropsPath, pathErr := crop.CropsFilePath()
		if pathErr == nil {
			localSource := crop.CSVSource{Path: cropsPath}
			if localCrops, loadErr := localSource.LoadCrops(); loadErr == nil && len(localCrops) > 0 {
				fmt.Printf("Uploading %d crop varieties...\n", len(localCrops))
				if pushErr := sc.PushCrops(localCrops); pushErr != nil {
					fmt.Printf("  Warning: could not upload crops: %v\n", pushErr)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}

		// Push farm layout.
		if envs, loadErr := farm.LoadConfig(); loadErr == nil && len(envs) > 0 {
			fmt.Printf("Uploading farm layout (%d environments)...\n", len(envs))
			if pushErr := sc.PushFarm(envs); pushErr != nil {
				fmt.Printf("  Warning: could not upload farm layout: %v\n", pushErr)
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push trials.
		if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
			fmt.Println("Uploading trial data...")
			if pushErr := sc.PushTrials(records); pushErr != nil {
				fmt.Printf("  Warning: could not upload trials: %v\n", pushErr)
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push schedule (tasks.json).
		if tasks, loadErr := store.Load(); loadErr == nil && len(tasks) > 0 {
			fmt.Printf("Uploading schedule (%d tasks)...\n", len(tasks))
			if pushErr := sc.PushSchedule(tasks); pushErr != nil {
				fmt.Printf("  Warning: could not upload schedule: %v\n", pushErr)
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push batches (cycles.json).
		if cycles, loadErr := farm.LoadCycles(); loadErr == nil && len(cycles) > 0 {
			fmt.Printf("Uploading batches (%d cycles)...\n", len(cycles))
			if pushErr := sc.PushBatches(cycles); pushErr != nil {
				fmt.Printf("  Warning: could not upload batches: %v\n", pushErr)
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push harvests (harvests.json).
		if harvests, loadErr := farm.LoadHarvests(); loadErr == nil && len(harvests) > 0 {
			fmt.Printf("Uploading harvest log (%d records)...\n", len(harvests))
			if pushErr := sc.PushHarvests(harvests); pushErr != nil {
				fmt.Printf("  Warning: could not upload harvest log: %v\n", pushErr)
			}
		}

		fmt.Println("Data uploaded successfully.")
		fmt.Println()
		return
	}

	// ── Subsequent sync: pull from Sheet → update local files ───────────

	if cfg.SheetID == "" {
		// Sheets is "enabled" but has no sheet ID — shouldn't happen, but
		// handle it gracefully by skipping.
		return
	}

	fmt.Println("Syncing from Google Sheets...")
	sc, err := gcal.NewSheetsClient(ctx, cfg.SheetID)
	if err != nil {
		fmt.Printf("  ⚠ Could not connect to Google Sheets: %v\n", err)
		fmt.Println("  Using local files (may be out of date).")
		fmt.Println()
		return
	}

	// ── Pull crops (two-way) ────────────────────────────────────────────
	pulledCrops, err := sc.PullCrops()
	if err != nil {
		fmt.Printf("  ⚠ Could not read Crops tab: %v\n", err)
		fmt.Println("  Keeping local crops.csv as-is.")
	} else if len(pulledCrops) == 0 {
		fmt.Println("  Crops tab is empty — keeping local crops.csv as-is.")
	} else {
		cropsPath, pathErr := crop.CropsFilePath()
		if pathErr != nil {
			fmt.Printf("  Warning: could not find crops.csv path: %v\n", pathErr)
		} else if writeErr := crop.WriteCrops(cropsPath, pulledCrops); writeErr != nil {
			fmt.Printf("  Warning: could not update local crops.csv: %v\n", writeErr)
		} else {
			fmt.Printf("  Crop library synced (%d varieties).\n", len(pulledCrops))
		}
	}

	time.Sleep(100 * time.Millisecond)

	// ── Pull farm layout (two-way) ──────────────────────────────────────
	pulledFarm, err := sc.PullFarm()
	if err != nil {
		fmt.Printf("  ⚠ Could not read Farm tab: %v\n", err)
		fmt.Println("  Keeping local farm.csv as-is.")
	} else if len(pulledFarm) == 0 {
		fmt.Println("  Farm tab is empty — keeping local farm.csv as-is.")
	} else {
		farmPath, pathErr := farm.FarmConfigPath()
		if pathErr != nil {
			fmt.Printf("  Warning: could not find farm.csv path: %v\n", pathErr)
		} else if writeErr := farm.WriteConfig(farmPath, pulledFarm); writeErr != nil {
			fmt.Printf("  Warning: could not update local farm.csv: %v\n", writeErr)
		} else {
			fmt.Printf("  Farm layout synced (%d environments).\n", len(pulledFarm))
		}
	}

	time.Sleep(100 * time.Millisecond)

	// ── Push trials (one-way to Sheet) ──────────────────────────────────
	if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
		if pushErr := sc.PushTrials(records); pushErr != nil {
			fmt.Printf("  ⚠ Could not push trials to Sheet: %v\n", pushErr)
		} else {
			fmt.Println("  Trials pushed to Sheet.")
		}
	}

	time.Sleep(100 * time.Millisecond)

	// ── Push schedule (one-way to Sheet) ────────────────────────────────
	if tasks, loadErr := store.Load(); loadErr == nil {
		if pushErr := sc.PushSchedule(tasks); pushErr != nil {
			fmt.Printf("  ⚠ Could not push schedule to Sheet: %v\n", pushErr)
		} else {
			fmt.Println("  Schedule pushed to Sheet.")
		}
	}

	time.Sleep(100 * time.Millisecond)

	// ── Push batches (one-way to Sheet) ─────────────────────────────────
	if cycles, loadErr := farm.LoadCycles(); loadErr == nil {
		if pushErr := sc.PushBatches(cycles); pushErr != nil {
			fmt.Printf("  ⚠ Could not push batches to Sheet: %v\n", pushErr)
		} else {
			fmt.Println("  Batches pushed to Sheet.")
		}
	}

	time.Sleep(100 * time.Millisecond)

	// ── Push harvests (one-way to Sheet) ────────────────────────────────
	if harvests, loadErr := farm.LoadHarvests(); loadErr == nil {
		if pushErr := sc.PushHarvests(harvests); pushErr != nil {
			fmt.Printf("  ⚠ Could not push harvests to Sheet: %v\n", pushErr)
		} else {
			fmt.Println("  Harvests pushed to Sheet.")
		}
	}

	fmt.Println()
}
