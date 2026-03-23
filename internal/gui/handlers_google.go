// handlers_google.go — Google Calendar and Sheets integration handlers.
//
// These handle the /sync page (pull from Sheets + push to Calendar/Tasks),
// the Sheets setup flow, and the Google OAuth sign-in process.
package gui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/gcal"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/supply"
	"github.com/littleguygreens/greenies/internal/trial"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared pull/push helpers
// ─────────────────────────────────────────────────────────────────────────────
//
// These two functions contain the actual "download everything" and "upload
// everything" logic for Google Sheets. They're used by multiple handlers:
//
//   - pullAllFromSheet is called by handleSyncPull AND handleSheetsLink
//   - pushAllToSheet  is called by handleSyncPush AND handleSheetsSetup
//
// By keeping the logic in one place, both handlers always stay in sync.

// pullAllFromSheet downloads every data type from the Google Sheet into
// the matching local files: crops.csv, farm.csv, supplies.csv, tasks.json,
// cycles.json, harvests.json, and trials.json.
//
// Each pull is independent — if one fails, the others still run. This is
// "best effort" because a partially-synced state is better than no sync.
func pullAllFromSheet(sc *gcal.SheetsClient) {
	// Pull crops.
	if pulledCrops, err := sc.PullCrops(); err == nil && len(pulledCrops) > 0 {
		if cropsPath, pathErr := crop.CropsFilePath(); pathErr == nil {
			_ = crop.WriteCrops(cropsPath, pulledCrops)
		}
	}

	// Pull farm layout.
	if pulledFarm, err := sc.PullFarm(); err == nil && len(pulledFarm) > 0 {
		if farmPath, pathErr := farm.FarmConfigPath(); pathErr == nil {
			_ = farm.WriteConfig(farmPath, pulledFarm)
		}
	}

	// Pull supplies.
	if pulledSupplies, err := sc.PullSupplies(); err == nil && len(pulledSupplies) > 0 {
		_ = supply.Save(pulledSupplies)
	}

	// Pull schedule (tasks).
	if pulledTasks, err := sc.PullSchedule(); err == nil && len(pulledTasks) > 0 {
		_ = store.Save(pulledTasks)
	}

	// Pull batches (cycles).
	if pulledCycles, err := sc.PullBatches(); err == nil && len(pulledCycles) > 0 {
		_ = farm.SaveCycles(pulledCycles)
	}

	// Pull harvests.
	if pulledHarvests, err := sc.PullHarvests(); err == nil && len(pulledHarvests) > 0 {
		_ = farm.SaveHarvests(pulledHarvests)
	}

	// Pull trials (lossy — observations don't transfer via Sheets).
	if pulledTrials, err := sc.PullTrials(); err == nil && len(pulledTrials) > 0 {
		_ = trial.SaveTrials(pulledTrials)
	}
}

// pushAllToSheet uploads every data type from local files to the Google
// Sheet: crops.csv, farm.csv, supplies.csv, trials.json, tasks.json,
// cycles.json, and harvests.json.
//
// Like pullAllFromSheet, each push is independent and best-effort.
func pushAllToSheet(sc *gcal.SheetsClient) {
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

	// Push supplies.
	if supplies, loadErr := supply.Load(); loadErr == nil && len(supplies) > 0 {
		_ = sc.PushSupplies(supplies)
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

// handleSyncPull downloads everything from Google to the local device
// (POST /sync-pull).
//
// This reads every tab in the Google Sheet and overwrites the matching
// local files: crops.csv, farm.csv, supplies.csv, tasks.json, cycles.json,
// harvests.json, and trials.json.
//
// Use this when the desktop has pushed fresh data and you want to bring
// this device up to date. "Pull" = Google → local.
func handleSyncPull(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	syncStart := time.Now()

	cfg, _ := config.Load()
	if !cfg.SheetsEnabled || cfg.SheetID == "" {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": "Google Sheets is not linked yet.",
		})
		return
	}

	sc, err := gcal.NewSheetsClient(ctx, cfg.SheetID)
	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not connect to Google Sheets: %v", err),
		})
		return
	}

	pullAllFromSheet(sc)

	elapsed := time.Since(syncStart).Round(time.Second)
	renderFragment(w, "sync_result.html", map[string]any{
		"Elapsed":   elapsed.String(),
		"Direction": "pull",
	})
}

// handleSyncPush uploads everything from the local device to Google
// (POST /sync-push).
//
// This pushes all local data to the Google Sheet, then syncs the schedule
// to Google Calendar and Google Tasks. "Push" = local → Google.
func handleSyncPush(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	syncStart := time.Now()

	// ── Push to Google Sheets ───────────────────────────────────────────
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		sc, err := gcal.NewSheetsClient(ctx, cfg.SheetID)
		if err == nil {
			pushAllToSheet(sc)
		}
	}

	// ── Google Calendar + Tasks sync ────────────────────────────────────
	tasks, err := store.Load()
	if err != nil {
		renderFragment(w, "sync_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not load tasks: %v", err),
		})
		return
	}

	envs, _ := farm.LoadConfig()
	cycles, _ := farm.LoadCycles()

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
		"Elapsed":   elapsed.String(),
		"Direction": "push",
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
		pushAllToSheet(sc)
	}

	renderFragment(w, "sheets_setup_result.html", map[string]any{
		"SheetID": sheetID,
	})
}

// handleSheetsLink links an existing Google Sheet by URL (POST /sheets-link).
//
// This is for users who already have a greenies Sheet (e.g. set up on their
// desktop) and want to link a second device to the same Sheet instead of
// creating a duplicate. They paste the Sheet URL and we extract the ID.
//
// A Google Sheets URL looks like:
//   https://docs.google.com/spreadsheets/d/SHEET_ID_HERE/edit
// We extract the SHEET_ID_HERE part.
func handleSheetsLink(w http.ResponseWriter, r *http.Request) {
	sheetURL := strings.TrimSpace(r.FormValue("sheet_url"))

	if sheetURL == "" {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": "Please paste a Google Sheet URL.",
		})
		return
	}

	// Extract the Sheet ID from the URL.
	// The ID is between "/d/" and the next "/" (or end of string).
	sheetID := gcal.ExtractSheetID(sheetURL)
	if sheetID == "" {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": "Could not find a Sheet ID in that URL. Make sure you paste the full URL from your browser's address bar (it should contain /spreadsheets/d/).",
		})
		return
	}

	// Verify we can actually access this Sheet by trying to read from it.
	ctx := context.Background()
	_, clientErr := gcal.NewSheetsClient(ctx, sheetID)
	if clientErr != nil {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not access that Sheet: %v. Make sure the Sheet belongs to the Google account you signed in with.", clientErr),
		})
		return
	}

	// Save the sheet ID to config.
	cfg, _ := config.Load()
	cfg.SheetsEnabled = true
	cfg.SheetID = sheetID
	if saveErr := config.Save(cfg); saveErr != nil {
		renderFragment(w, "sheets_setup_result.html", map[string]any{
			"Error": fmt.Sprintf("Could not save config: %v", saveErr),
		})
		return
	}

	// Pull ALL data FROM the Sheet into local files. This is the key
	// difference from "Create New Sheet" — we're joining an existing Sheet,
	// so we want its data, not the other way around.
	//
	// This is the main use case for a fresh Android install: the desktop
	// has been pushing data to the Sheet, and the phone needs all of it.
	sc, pullErr := gcal.NewSheetsClient(ctx, sheetID)
	if pullErr == nil {
		pullAllFromSheet(sc)
	}

	renderFragment(w, "sheets_setup_result.html", map[string]any{
		"SheetID": sheetID,
	})
}

// handleGoogleSignIn redirects the browser/WebView to Google's sign-in page
// (POST /google-signin). Uses an in-page redirect so it works everywhere:
// Android WebView, desktop browsers, and Chromium --app mode.
//
// The flow:
//  1. This handler returns an HTML fragment with a JavaScript redirect
//     to /auth/start (which builds the Google URL and redirects there)
//  2. The user signs in on Google's page (inside the same window)
//  3. Google redirects to /auth/callback with a one-time code
//  4. /auth/callback exchanges the code for a token, saves it, and
//     redirects to /sync — where the user now sees the next step
//
// This works everywhere: Android WebView, desktop browser, Chromium --app.
func handleGoogleSignIn(w http.ResponseWriter, r *http.Request) {
	// Return a tiny HTML fragment that redirects the whole page to
	// /auth/start. We use JavaScript because htmx swaps only replace
	// part of the page — we need to navigate away entirely so Google's
	// sign-in page can take over the full window.
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<p>Redirecting to Google sign-in...</p><script>window.location.href="/auth/start";</script>`)
}

// handleAuthStart builds the Google sign-in URL and redirects the browser
// there (GET /auth/start). After the user approves on Google's page,
// Google will redirect back to /auth/callback on this same server.
func handleAuthStart(w http.ResponseWriter, r *http.Request) {
	// Build the callback URL pointing back to our own server.
	// We use the Host header from the request so it works regardless
	// of what port the server is on.
	callbackURL := "http://" + r.Host + "/auth/callback"

	authURL, err := gcal.BuildAuthURL(callbackURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Could not build auth URL: %v", err), http.StatusInternalServerError)
		return
	}

	// Redirect the browser to Google's sign-in page.
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleAuthCallback receives the one-time code from Google after the user
// approves (GET /auth/callback?code=...). It exchanges the code for a
// long-lived token, saves it, and redirects to the sync page.
func handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		// Google sends an "error" parameter if the user denied access
		// or something went wrong on Google's end.
		errMsg := r.URL.Query().Get("error")
		if errMsg == "" {
			errMsg = "no authorisation code received"
		}
		// Show a simple error page with a link back to sync.
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<h2>Sign-in failed</h2><p>%s</p><p><a href="/sync">Back to sync</a></p>`, errMsg)
		return
	}

	// The callback URL must match exactly what BuildAuthURL used.
	callbackURL := "http://" + r.Host + "/auth/callback"

	ctx := context.Background()
	if err := gcal.HandleAuthCallback(ctx, code, callbackURL); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<h2>Sign-in failed</h2><p>%v</p><p><a href="/sync">Back to sync</a></p>`, err)
		return
	}

	// Success! Now we need to get the user back to the app.
	//
	// On desktop, the browser IS the app — redirect to /sync and done.
	// On Android, Chrome handled the sign-in but the app is in the WebView.
	// We show a "success, go back" message so the user knows to switch back.
	//
	// How we detect Android: the User-Agent header from an Android Chrome
	// browser will contain "Android". The WebView would too, but the WebView
	// never reaches this handler (it opened Chrome for Google sign-in).
	ua := r.Header.Get("User-Agent")
	if containsAndroid(ua) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head>
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<style>
  body { background: #1a1a1a; color: #c8e6c9; font-family: sans-serif;
         display: flex; align-items: center; justify-content: center;
         min-height: 100vh; margin: 0; text-align: center; padding: 2rem; }
  h2 { color: #8bc34a; }
</style>
</head><body>
<div>
  <h2>Signed in!</h2>
  <p>You can close this tab and go back to Greenies.</p>
</div>
</body></html>`)
		return
	}

	// Desktop — redirect straight to the sync page.
	http.Redirect(w, r, "/sync", http.StatusFound)
}

// containsAndroid checks if a User-Agent string contains "Android".
// Used to detect when the auth callback is being handled by Chrome on
// an Android phone (as opposed to a desktop browser).
func containsAndroid(ua string) bool {
	return strings.Contains(ua, "Android")
}
