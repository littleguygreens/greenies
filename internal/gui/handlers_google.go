// handlers_google.go — Google Calendar and Sheets integration handlers.
//
// These handle the /sync page (pull from Sheets + push to Calendar/Tasks),
// the Sheets setup flow, and the Google OAuth sign-in process.
package gui

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/trial"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sync
// ─────────────────────────────────────────────────────────────────────────────

// handleSyncPage renders the sync page (GET /sync).
//
// It checks whether Google Calendar credentials are set up and whether
// Google Sheets has been linked. The template uses these flags to decide
// what to show:
//   - No credentials at all → setup instructions
//   - Credentials exist but Sheets not linked → "Link Google Sheets?" section
//   - Everything linked → "Sync Now" button
func handleSyncPage(w http.ResponseWriter, r *http.Request) {
	cfg, _ := config.Load()
	renderPage(w, "sync.html", map[string]any{
		"CredentialsExist": gcal.CredentialsExist(),
		"TokenExists":      gcal.TokenExists(),
		"SheetsEnabled":    cfg.SheetsEnabled,
		"SheetID":          cfg.SheetID,
	})
}

// handleSyncAction performs the full Google sync (POST /sync).
//
// This does the same thing as "greenies sync" in the terminal:
//  1. Google Sheets sync — pulls crops and farm layout from the Sheet,
//     pushes trials to the Sheet (same two-way + one-way pattern as CLI)
//  2. Google Calendar + Tasks sync — pushes the schedule to Google
//
// The response is an htmx fragment (just a piece of HTML, not a full page)
// that replaces the button area with a success or error message.
func handleSyncAction(w http.ResponseWriter, r *http.Request) {
	// context.Background() means "no deadline, no cancellation" — fine for
	// a user-initiated action they're waiting on.
	ctx := context.Background()
	syncStart := time.Now()

	// ── Google Sheets sync ──────────────────────────────────────────────
	//
	// Pull crops and farm layout from the Sheet (two-way), push trials
	// (one-way). This runs BEFORE Calendar/Tasks so that any changes
	// from the Sheet are reflected in the local data first.
	//
	// Sheets sync is best-effort — if it fails, we continue with the
	// Calendar/Tasks sync using whatever local data we have.
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		sc, err := gcal.NewSheetsClient(ctx, cfg.SheetID)
		if err == nil {
			// Pull crops (two-way).
			if pulledCrops, pullErr := sc.PullCrops(); pullErr == nil && len(pulledCrops) > 0 {
				if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
					_ = crop.WriteCrops(cropsPath, pulledCrops)
				}
			}

			// Pull farm layout (two-way).
			if pulledFarm, pullErr := sc.PullFarm(); pullErr == nil && len(pulledFarm) > 0 {
				if farmPath, pathErr := farm.FarmConfigPath(); pathErr == nil {
					_ = farm.WriteConfig(farmPath, pulledFarm)
				}
			}

			// Push trials (one-way to Sheet).
			if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
				_ = sc.PushTrials(records)
			}

			// Push schedule (one-way to Sheet).
			if allTasks, loadErr := store.Load(); loadErr == nil {
				_ = sc.PushSchedule(allTasks)
			}

			// Push batches (one-way to Sheet).
			if allCycles, loadErr := farm.LoadCycles(); loadErr == nil {
				_ = sc.PushBatches(allCycles)
			}

			// Push harvests (one-way to Sheet).
			if allHarvests, loadErr := farm.LoadHarvests(); loadErr == nil {
				_ = sc.PushHarvests(allHarvests)
			}
		}
	}

	// ── Load local data (freshly synced from Sheets if available) ───────
	tasks, err := store.Load()
	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not load tasks: %v", err),
		})
		return
	}

	envs, _ := farm.LoadConfig()
	cycles, _ := farm.LoadCycles()

	// ── Google Calendar + Tasks sync ────────────────────────────────────
	exporter, err := gcal.NewExporter(ctx)
	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not connect to Google: %v", err),
		})
		return
	}

	err = exporter.Sync(tasks, envs, cycles)
	elapsed := time.Since(syncStart).Round(time.Second)

	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("%v", err),
		})
		return
	}

	renderFragment(w, "sync_result.html", map[string]any{
		"Elapsed": elapsed.String(),
	})
}

// handleSheetsSetup creates the Google Sheet for the first time (POST /sheets-setup).
//
// This is the GUI equivalent of the "Link Google Sheets?" prompt that appears
// in the terminal during the first "greenies sync". When the user clicks the
// "Link Google Sheets" button on the sync page, this handler:
//
//  1. Creates a new Google Spreadsheet with all seven tabs
//     (Crops, Cycle, Farm, Trials, Schedule, Batches, Harvests)
//  2. Saves the spreadsheet ID to config.json
//  3. Pushes all existing local data to the new Sheet
//  4. Returns an htmx fragment showing the Sheet URL and a "Sync Now" button
//
// The response replaces the setup section with a success message, so the user
// can immediately start syncing without reloading the page.
func handleSheetsSetup(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Create the Google Sheet with all seven tabs and headers.
	sheetID, err := gcal.CreateSheet(ctx)
	if err != nil {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not create Google Sheet: %v", err),
		})
		return
	}

	// Save the sheet ID to config so we never have to ask again.
	cfg, _ := config.Load()
	cfg.SheetsEnabled = true
	cfg.SheetID = sheetID
	if saveErr := config.Save(cfg); saveErr != nil {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": fmt.Sprintf("Created Sheet but could not save config: %v", saveErr),
		})
		return
	}

	// Push existing local data to the new Sheet so the user doesn't have
	// to re-enter everything by hand.
	sc, clientErr := gcal.NewSheetsClient(ctx, sheetID)
	if clientErr == nil {
		// Push crops.
		if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
			localSource := crop.CSVSource{Path: cropsPath}
			if localCrops, loadErr := localSource.LoadCrops(); loadErr == nil && len(localCrops) > 0 {
				_ = sc.PushCrops(localCrops)
			}
		}

		// Push farm layout.
		if envs, loadErr := farm.LoadConfig(); loadErr == nil && len(envs) > 0 {
			_ = sc.PushFarm(envs)
		}

		// Push trials.
		if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
			_ = sc.PushTrials(records)
		}

		// Push schedule (tasks.json).
		if allTasks, loadErr := store.Load(); loadErr == nil {
			_ = sc.PushSchedule(allTasks)
		}

		// Push batches (cycles.json).
		if allCycles, loadErr := farm.LoadCycles(); loadErr == nil {
			_ = sc.PushBatches(allCycles)
		}

		// Push harvests (harvests.json).
		if allHarvests, loadErr := farm.LoadHarvests(); loadErr == nil {
			_ = sc.PushHarvests(allHarvests)
		}
	}

	renderFragment(w, "sheets_setup_result.html", map[string]any{
		"SheetID": sheetID,
	})
}

// handleGoogleSignIn runs just the Google OAuth browser sign-in flow
// (POST /google-signin). This is the GUI equivalent of the browser popup
// that normally triggers the first time you run "greenies sync" in the
// terminal.
//
// It does NOT create a Sheet or sync anything — just authenticates. Once
// signed in, the sync page reloads to show the next step (Link Google
// Sheets / Sync Now).
//
// How it works:
//  1. Calls AuthorizeClient, which opens the user's browser to Google's
//     sign-in page if no saved token exists
//  2. The user approves in their browser — the token is saved to token.json
//  3. Returns an htmx fragment confirming success (or showing the error)
func handleGoogleSignIn(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// AuthorizeClient handles everything: opens the browser, waits for
	// approval, saves the token. If a token already exists, it returns
	// immediately without opening the browser.
	_, err := gcal.AuthorizeClient(ctx)
	if err != nil {
		renderFragment(w, "google_signin_result.html", map[string]any{
			"Error": fmt.Sprintf("%v", err),
		})
		return
	}

	renderFragment(w, "google_signin_result.html", map[string]any{})
}
