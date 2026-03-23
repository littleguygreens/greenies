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
	"github.com/littleguygreens/greenies/internal/supply"
	"github.com/littleguygreens/greenies/internal/trial"
)

// runSync handles the "greenies sync" command.
//
// Two modes:
//   - Pull from Google — downloads all data from the Google Sheet and
//     overwrites the local files. Use this on a second device (phone,
//     laptop) to get a copy of the schedule from the desktop.
//   - Push to Google — uploads all local data to the Google Sheet,
//     Calendar, and Tasks. Use this after making changes locally.
//
// On first run, offers to link Google Sheets (create new or link existing).
// On subsequent runs, asks which direction: pull or push.
func runSync() {
	// context.Background() means "no deadline, no cancellation" — fine for
	// a short interactive command that the user is actively waiting on.
	ctx := context.Background()

	// ── First-time setup (if Sheets not yet linked) ─────────────────────
	cfg, _ := config.Load()
	if !cfg.SheetsEnabled {
		setupSheets(ctx)
		return
	}

	if cfg.SheetID == "" {
		// Sheets is "enabled" but has no sheet ID — shouldn't happen, but
		// handle it gracefully.
		fmt.Println("Google Sheets is enabled but no Sheet ID is saved.")
		fmt.Println("Run \"greenies sync\" again to set it up.")
		return
	}

	// ── Ask direction: pull or push ─────────────────────────────────────
	fmt.Println()
	fmt.Println("  (pull) Pull from Google — download Sheet data to this device")
	fmt.Println("  (push) Push to Google — upload this device's data to Google")
	fmt.Print("(pull/push) [push]: ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))

	// Default to push if the user just presses Enter.
	if answer == "" {
		answer = "push"
	}

	if answer == "pull" || answer == "p" {
		syncPull(ctx, cfg.SheetID)
	} else {
		syncPush(ctx, cfg.SheetID)
	}
}

// setupSheets handles the first-time Google Sheets setup. Asks the user
// whether to create a new Sheet or link an existing one, then does the
// initial data transfer (push for new, pull for existing).
func setupSheets(ctx context.Context) {
	cfg, _ := config.Load()
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("Link Google Sheets for your farm data?")
	fmt.Println("This lets you view and edit your crop library and farm layout")
	fmt.Println("in Google Sheets from any device.")
	fmt.Println()
	fmt.Println("  (n) New Sheet — create a fresh Sheet and push local data up")
	fmt.Println("  (l) Link existing — paste a Sheet URL to pull data down")
	fmt.Println("  (s) Skip — no Sheets, use local files only")
	fmt.Print("(n/l/s) [n]: ")

	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))

	// Default to "new" if the user just presses Enter.
	if answer == "" {
		answer = "n"
	}

	// ── Link existing Sheet ──────────────────────────────────────────
	if answer == "l" || answer == "link" {
		fmt.Print("Paste the Google Sheet URL: ")
		urlLine, _ := reader.ReadString('\n')
		urlLine = strings.TrimSpace(urlLine)

		// Extract the Sheet ID from the URL.
		sheetID := gcal.ExtractSheetID(urlLine)
		if sheetID == "" {
			fmt.Println("Could not find a Sheet ID in that URL.")
			fmt.Println("Make sure you paste the full URL from your browser's address bar.")
			fmt.Println()
			return
		}

		// Verify we can access this Sheet.
		_, clientErr := gcal.NewSheetsClient(ctx, sheetID)
		if clientErr != nil {
			fmt.Printf("Could not access that Sheet: %v\n", clientErr)
			fmt.Println()
			return
		}

		// Save the sheet ID to config.
		cfg.SheetsEnabled = true
		cfg.SheetID = sheetID
		if saveErr := config.Save(cfg); saveErr != nil {
			fmt.Printf("Warning: could not save config: %v\n", saveErr)
		}

		fmt.Println("Sheet linked! Pulling data...")
		syncPull(ctx, sheetID)
		return
	}

	// ── Skip ─────────────────────────────────────────────────────────
	if answer == "s" || answer == "skip" || answer == "no" {
		fmt.Println("Skipping Google Sheets — using local files only.")
		fmt.Println()
		return
	}

	// ── Create new Sheet (default) ───────────────────────────────────
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

	// Print the URL so the user can bookmark it.
	fmt.Println()
	fmt.Println("Google Sheet created!")
	fmt.Printf("  URL: https://docs.google.com/spreadsheets/d/%s/edit\n", sheetID)
	fmt.Println()
	fmt.Println("Bookmark that URL — it's where you'll view and edit your farm data.")

	// Push existing local data to the new Sheet.
	fmt.Println("Pushing local data to the new Sheet...")
	syncPush(ctx, sheetID)
}

// syncPull downloads everything from the Google Sheet into local files.
// Google → local. This overwrites whatever is on this device with whatever
// is in the Sheet.
func syncPull(ctx context.Context, sheetID string) {
	fmt.Println()
	fmt.Println("Pulling from Google Sheets...")

	sc, err := gcal.NewSheetsClient(ctx, sheetID)
	if err != nil {
		fmt.Printf("  ⚠ Could not connect to Google Sheets: %v\n", err)
		return
	}

	// Pull crops.
	if pulledCrops, pullErr := sc.PullCrops(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Crops tab: %v\n", pullErr)
	} else if len(pulledCrops) > 0 {
		if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
			if writeErr := crop.WriteCrops(cropsPath, pulledCrops); writeErr == nil {
				fmt.Printf("  Crops pulled (%d varieties).\n", len(pulledCrops))
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Pull farm layout.
	if pulledFarm, pullErr := sc.PullFarm(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Farm tab: %v\n", pullErr)
	} else if len(pulledFarm) > 0 {
		if farmPath, pathErr := farm.FarmConfigPath(); pathErr == nil {
			if writeErr := farm.WriteConfig(farmPath, pulledFarm); writeErr == nil {
				fmt.Printf("  Farm layout pulled (%d environments).\n", len(pulledFarm))
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Pull supplies.
	if pulledSupplies, pullErr := sc.PullSupplies(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Supplies tab: %v\n", pullErr)
	} else if len(pulledSupplies) > 0 {
		if writeErr := supply.Save(pulledSupplies); writeErr == nil {
			fmt.Printf("  Supplies pulled (%d items).\n", len(pulledSupplies))
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Pull schedule (tasks).
	if pulledTasks, pullErr := sc.PullSchedule(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Schedule Tasks tab: %v\n", pullErr)
	} else if len(pulledTasks) > 0 {
		if writeErr := store.Save(pulledTasks); writeErr == nil {
			fmt.Printf("  Schedule pulled (%d tasks).\n", len(pulledTasks))
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Pull batches (cycles).
	if pulledCycles, pullErr := sc.PullBatches(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Schedule Cycles tab: %v\n", pullErr)
	} else if len(pulledCycles) > 0 {
		if writeErr := farm.SaveCycles(pulledCycles); writeErr == nil {
			fmt.Printf("  Batches pulled (%d cycles).\n", len(pulledCycles))
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Pull harvests.
	if pulledHarvests, pullErr := sc.PullHarvests(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Harvests tab: %v\n", pullErr)
	} else if len(pulledHarvests) > 0 {
		if writeErr := farm.SaveHarvests(pulledHarvests); writeErr == nil {
			fmt.Printf("  Harvests pulled (%d records).\n", len(pulledHarvests))
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Pull trials (lossy — observations don't transfer between devices).
	if pulledTrials, pullErr := sc.PullTrials(); pullErr != nil {
		fmt.Printf("  ⚠ Could not read Trials tab: %v\n", pullErr)
	} else if len(pulledTrials) > 0 {
		if writeErr := trial.SaveTrials(pulledTrials); writeErr == nil {
			fmt.Printf("  Trials pulled (%d records).\n", len(pulledTrials))
		}
	}

	fmt.Println()
	fmt.Println("Pull complete! Local files updated from Google.")
	fmt.Println()
}

// syncPush uploads everything from local files to Google. Local → Google.
// This overwrites the Sheet with whatever is on this device, then syncs
// the schedule to Google Calendar and Tasks.
func syncPush(ctx context.Context, sheetID string) {
	fmt.Println()

	// ── Push to Google Sheets ───────────────────────────────────────────
	fmt.Println("Pushing to Google Sheets...")
	sc, err := gcal.NewSheetsClient(ctx, sheetID)
	if err != nil {
		fmt.Printf("  ⚠ Could not connect to Google Sheets: %v\n", err)
		fmt.Println("  Skipping Sheets push.")
	} else {
		// Push crops.
		if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
			localSource := crop.CSVSource{Path: cropsPath}
			if localCrops, loadErr := localSource.LoadCrops(); loadErr == nil && len(localCrops) > 0 {
				if pushErr := sc.PushCrops(localCrops); pushErr != nil {
					fmt.Printf("  ⚠ Could not push crops: %v\n", pushErr)
				} else {
					fmt.Printf("  Crops pushed (%d varieties).\n", len(localCrops))
				}
				time.Sleep(100 * time.Millisecond)
			}
		}

		// Push farm layout.
		if envs, loadErr := farm.LoadConfig(); loadErr == nil && len(envs) > 0 {
			if pushErr := sc.PushFarm(envs); pushErr != nil {
				fmt.Printf("  ⚠ Could not push farm layout: %v\n", pushErr)
			} else {
				fmt.Printf("  Farm layout pushed (%d environments).\n", len(envs))
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push supplies.
		if supplies, loadErr := supply.Load(); loadErr == nil && len(supplies) > 0 {
			if pushErr := sc.PushSupplies(supplies); pushErr != nil {
				fmt.Printf("  ⚠ Could not push supplies: %v\n", pushErr)
			} else {
				fmt.Printf("  Supplies pushed (%d items).\n", len(supplies))
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push trials.
		if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
			if pushErr := sc.PushTrials(records); pushErr != nil {
				fmt.Printf("  ⚠ Could not push trials: %v\n", pushErr)
			} else {
				fmt.Println("  Trials pushed.")
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push schedule (tasks.json).
		if allTasks, loadErr := store.Load(); loadErr == nil {
			if pushErr := sc.PushSchedule(allTasks); pushErr != nil {
				fmt.Printf("  ⚠ Could not push schedule: %v\n", pushErr)
			} else {
				fmt.Println("  Schedule pushed.")
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push batches (cycles.json).
		if allCycles, loadErr := farm.LoadCycles(); loadErr == nil {
			if pushErr := sc.PushBatches(allCycles); pushErr != nil {
				fmt.Printf("  ⚠ Could not push batches: %v\n", pushErr)
			} else {
				fmt.Println("  Batches pushed.")
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Push harvests (harvests.json).
		if allHarvests, loadErr := farm.LoadHarvests(); loadErr == nil {
			if pushErr := sc.PushHarvests(allHarvests); pushErr != nil {
				fmt.Printf("  ⚠ Could not push harvests: %v\n", pushErr)
			} else {
				fmt.Println("  Harvests pushed.")
			}
		}

		fmt.Println("  ✓ Google Sheet updated.")
	}

	// ── Google Calendar + Tasks sync ────────────────────────────────────
	fmt.Println()
	fmt.Println("Syncing to Google Calendar and Tasks...")

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	envs, _ := farm.LoadConfig()
	cycles, _ := farm.LoadCycles()

	exporter, err := gcal.NewExporter(ctx)
	if err != nil {
		fmt.Printf("Error connecting to Google Calendar: %v\n", err)
		os.Exit(1)
	}

	// Record when the Calendar/Tasks sync starts so we can show elapsed time.
	// This part makes many individual API calls, so it can take a minute or two.
	syncStart := time.Now()

	// Run the sync in the background (a "goroutine" — a lightweight task that
	// runs alongside the rest of the program). This frees up the main thread
	// to run the live timer below.
	done := make(chan error, 1)
	go func() {
		done <- exporter.Sync(tasks, envs, cycles)
	}()

	// Tick once per second and show the current elapsed time.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		case err := <-done:
			ticker.Stop()
			fmt.Println()
			if err != nil {
				fmt.Printf("Calendar sync failed: %v\n", err)
				os.Exit(1)
			}
			break loop

		case <-ticker.C:
			fmt.Printf("\r⏱  %s ", time.Since(syncStart).Round(time.Second))
		}
	}

	elapsed := time.Since(syncStart).Round(time.Second)
	fmt.Println()
	fmt.Printf("Push complete! Finished in %s.\n", elapsed)
	fmt.Println()
}
