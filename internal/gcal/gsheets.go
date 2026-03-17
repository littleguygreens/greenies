// This file handles all communication with the Google Sheets API.
//
// It lets the program create, read, and write a Google Spreadsheet that
// contains the crop library — the same data that lives locally in
// ~/.greenies/crops.csv.
//
// The spreadsheet has two tabs (sheets):
//
//   "Crops" — one row per crop variety with its parameters (seed weight,
//   soak hours, dark days, light days, etc.). This is the "settings" tab
//   that a grower scans to see all their varieties at a glance.
//
//   "Cycle" — the day-by-day schedule for every crop. Each row is one day
//   in one crop's growing cycle, with the stage (sow/dark/light/harvest)
//   and the specific tasks for that day.
//
// Why two tabs instead of one?
//   The old crops.csv crammed everything into one file using a "sparse"
//   format where parameters only appeared on the first row of each crop
//   block. That's hard to read in a spreadsheet. Two tabs are cleaner:
//   each tab does one job, and every row makes sense on its own.
//
// The Google Sheet is the "source of truth" — when you edit crops in
// Google Sheets, running "greenies sync" pulls those changes down to the
// local CSV. When the program modifies crops (e.g. adjusting stages or
// promoting a trial), it pushes the changes back up to the Sheet.
package gcal

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
)

// ─── Sheet layout constants ─────────────────────────────────────────────────
//
// These define the tab names and header rows used in the Google Sheet.
// Keeping them as constants means they are easy to find and change if the
// format ever needs to evolve.

// cropsTab is the name of the spreadsheet tab that holds one row per crop
// variety with all its parameters (seed weight, soak hours, etc.).
const cropsTab = "Crops"

// cycleTab is the name of the spreadsheet tab that holds the day-by-day
// schedule — every day of every crop's growing cycle with stage and tasks.
const cycleTab = "Cycle"

// cropsHeaders are the column headings for the Crops tab.
// Order matters — the push and pull functions rely on this exact sequence.
var cropsHeaders = []interface{}{
	"name", "overnight_soak", "soak_hours", "seed_grams",
	"dirt_litres", "dark_days", "light_days", "yield_grams",
}

// cycleHeaders are the column headings for the Cycle tab.
var cycleHeaders = []interface{}{
	"name", "day", "stage", "tasks",
}

// ─── SheetsClient ───────────────────────────────────────────────────────────

// SheetsClient wraps the Google Sheets API service with helper methods
// specific to the Greenies crop library format.
type SheetsClient struct {
	// service is the authorised Google Sheets API client — created from the
	// same OAuth token used for Calendar and Tasks.
	service *sheets.Service

	// sheetID is the unique identifier of the Google Spreadsheet.
	// This is the long string in the middle of a Google Sheets URL:
	//   https://docs.google.com/spreadsheets/d/<sheetID>/edit
	sheetID string
}

// NewSheetsClient creates a SheetsClient that can read and write the crop
// library spreadsheet. It uses the same OAuth login as Calendar and Tasks —
// no extra sign-in needed.
//
// sheetID is the Google-assigned spreadsheet identifier, stored in
// ~/.greenies/config.json after the first sync.
func NewSheetsClient(ctx context.Context, sheetID string) (*SheetsClient, error) {
	// Get the shared OAuth HTTP client — same one Calendar and Tasks use.
	client, err := AuthorizeClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("Google authorisation failed: %w", err)
	}

	// Build the Sheets API service. option.WithHTTPClient passes in our
	// authorised client so every request automatically includes the token.
	svc, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("could not create Google Sheets service: %w", err)
	}

	return &SheetsClient{
		service: svc,
		sheetID: sheetID,
	}, nil
}

// ─── Sheet creation ─────────────────────────────────────────────────────────

// CreateCropSheet creates a brand-new Google Spreadsheet with the two tabs
// ("Crops" and "Cycle") and their header rows already in place.
//
// This is called only once — the very first time the user runs "greenies sync"
// and agrees to link Google Sheets. After creation, the spreadsheet ID is
// saved to config.json and reused on every future sync.
//
// Returns the new spreadsheet's ID (the string needed to access it via the API).
func CreateCropSheet(ctx context.Context) (string, error) {
	// Get the shared OAuth HTTP client.
	client, err := AuthorizeClient(ctx)
	if err != nil {
		return "", fmt.Errorf("Google authorisation failed: %w", err)
	}

	svc, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return "", fmt.Errorf("could not create Google Sheets service: %w", err)
	}

	// Define the spreadsheet structure: two tabs with specific names.
	// Google always creates a default "Sheet1" tab, but by specifying our
	// own tabs here it only creates the ones we ask for.
	spreadsheet := &sheets.Spreadsheet{
		Properties: &sheets.SpreadsheetProperties{
			Title: "greenies",
		},
		Sheets: []*sheets.Sheet{
			{Properties: &sheets.SheetProperties{Title: cropsTab}},
			{Properties: &sheets.SheetProperties{Title: cycleTab}},
		},
	}

	created, err := svc.Spreadsheets.Create(spreadsheet).Context(ctx).Do()
	if err != nil {
		// If the error mentions "insufficient authentication scopes", the
		// user's saved token was created before the Sheets scope was added.
		if isInsufficientScopeError(err) {
			return "", fmt.Errorf(
				"Google Sheets permission not granted.\n\n" +
					"Your saved login token was created before Sheets support was added.\n" +
					"To fix this:\n" +
					"  1. Delete the file: ~/.greenies/token.json\n" +
					"  2. Run \"greenies sync\" again\n" +
					"  3. Approve the new permission in your browser\n\n" +
					"Original error: %w", err)
		}
		return "", fmt.Errorf("could not create Google Sheet: %w", err)
	}

	// Write the header rows to both tabs so the grower sees column labels
	// even before any crop data is pushed.
	sheetID := created.SpreadsheetId

	// Crops tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, cropsTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{cropsHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Crops header: %w", err)
	}

	time.Sleep(apiPause)

	// Cycle tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, cycleTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{cycleHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Cycle header: %w", err)
	}

	return sheetID, nil
}

// ─── Pull (download from Google Sheets → in-memory Crop structs) ────────────

// PullCrops reads both tabs of the Google Sheet and assembles them into
// []crop.Crop — the same data structure that the rest of the program uses.
//
// It reads the "Crops" tab for parameters (seed weight, soak hours, etc.)
// and the "Cycle" tab for the day-by-day schedule, then merges them together
// by crop name.
//
// If the sheet is empty (just headers, no data rows), it returns an empty
// slice and no error — this is valid for a brand-new sheet that the grower
// hasn't populated yet.
func (sc *SheetsClient) PullCrops() ([]crop.Crop, error) {
	// ── Read the Crops tab ──────────────────────────────────────────────
	cropsResp, err := sc.service.Spreadsheets.Values.Get(
		sc.sheetID, cropsTab+"!A:H",
	).Do()
	if err != nil {
		return nil, fmt.Errorf("could not read Crops tab: %w", err)
	}

	time.Sleep(apiPause)

	// ── Read the Cycle tab ──────────────────────────────────────────────
	cycleResp, err := sc.service.Spreadsheets.Values.Get(
		sc.sheetID, cycleTab+"!A:D",
	).Do()
	if err != nil {
		return nil, fmt.Errorf("could not read Cycle tab: %w", err)
	}

	// ── Parse the Crops tab into a map of crop parameters ───────────────
	//
	// Each row after the header becomes one entry keyed by crop name.
	// The map preserves insertion order via a separate name slice so the
	// final output is in the same order the grower arranged their rows.
	type cropParams struct {
		OvernightSoak bool
		SoakHours     int
		SeedGrams     int
		DirtLitres    float64
		DarkDays      int
		LightDays     int
		YieldGrams    int
	}

	paramMap := make(map[string]*cropParams)
	var nameOrder []string // preserves the row order from the Sheet

	for i, row := range cropsResp.Values {
		if i == 0 {
			continue // skip header row
		}
		if len(row) == 0 {
			continue // blank row
		}

		name := strings.TrimSpace(cellString(row, 0))
		if name == "" {
			continue
		}

		p := &cropParams{
			OvernightSoak: parseBoolCell(row, 1),
			SoakHours:     parseIntCell(row, 2),
			SeedGrams:     parseIntCell(row, 3),
			DirtLitres:    parseFloatCell(row, 4),
			DarkDays:      parseIntCell(row, 5),
			LightDays:     parseIntCell(row, 6),
			YieldGrams:    parseIntCell(row, 7),
		}

		// Default dirt to 1 litre if the cell is empty or zero.
		if p.DirtLitres == 0 {
			p.DirtLitres = 1.0
		}

		paramMap[strings.ToLower(name)] = p
		nameOrder = append(nameOrder, name)
	}

	// ── Parse the Cycle tab into day lists keyed by crop name ───────────
	//
	// dayMap collects all CropDay entries for each crop, in the order they
	// appear in the sheet. The crop name is lowercased for matching but
	// the original casing from the Crops tab is used in the final output.
	dayMap := make(map[string][]crop.CropDay)

	for i, row := range cycleResp.Values {
		if i == 0 {
			continue // skip header row
		}
		if len(row) == 0 {
			continue
		}

		name := strings.TrimSpace(cellString(row, 0))
		if name == "" {
			continue
		}

		dayNum := parseIntCell(row, 1)
		stage := strings.TrimSpace(cellString(row, 2))
		tasks := strings.TrimSpace(cellString(row, 3))

		key := strings.ToLower(name)
		dayMap[key] = append(dayMap[key], crop.CropDay{
			Day:   dayNum,
			Stage: stage,
			Tasks: tasks,
		})
	}

	// ── Merge parameters and days into complete Crop structs ────────────
	var crops []crop.Crop

	for _, name := range nameOrder {
		key := strings.ToLower(name)
		p := paramMap[key]
		days := dayMap[key]

		// Derive CycleDays from the last day row — same logic as CSVSource.
		cycleDays := 0
		if len(days) > 0 {
			cycleDays = days[len(days)-1].Day
		}

		crops = append(crops, crop.Crop{
			Name:          name,
			CycleDays:     cycleDays,
			OvernightSoak: p.OvernightSoak,
			SoakHours:     p.SoakHours,
			SeedGrams:     p.SeedGrams,
			DirtLitres:    p.DirtLitres,
			DarkDays:      p.DarkDays,
			LightDays:     p.LightDays,
			YieldGrams:    p.YieldGrams,
			Days:          days,
		})
	}

	return crops, nil
}

// ─── Push (upload in-memory Crop structs → Google Sheets) ───────────────────

// PushCrops writes the given crops to both tabs of the Google Sheet,
// completely replacing any existing data (except the header rows).
//
// This is the "upload" direction — used when:
//   - First-time setup: populating the new sheet from existing crops.csv
//   - After a local modification (adjust stages, promote trial): mirroring
//     the change back to the Sheet so it stays in sync
func (sc *SheetsClient) PushCrops(crops []crop.Crop) error {
	// ── Build the Crops tab data ────────────────────────────────────────
	//
	// One row per variety: name, overnight_soak, soak_hours, seed_grams,
	// dirt_litres, dark_days, light_days, yield_grams.
	cropsRows := [][]interface{}{cropsHeaders}

	for _, c := range crops {
		soakStr := "FALSE"
		if c.OvernightSoak {
			soakStr = "TRUE"
		}

		// Format dirt_litres nicely: "1" not "1.0", "1.5" stays "1.5".
		var dirtStr string
		if c.DirtLitres == float64(int(c.DirtLitres)) {
			dirtStr = strconv.Itoa(int(c.DirtLitres))
		} else {
			dirtStr = strconv.FormatFloat(c.DirtLitres, 'f', -1, 64)
		}

		cropsRows = append(cropsRows, []interface{}{
			c.Name,
			soakStr,
			c.SoakHours,
			c.SeedGrams,
			dirtStr,
			c.DarkDays,
			c.LightDays,
			c.YieldGrams,
		})
	}

	// ── Build the Cycle tab data ────────────────────────────────────────
	//
	// One row per crop-day: name, day number, stage, tasks.
	// Every row has the crop name filled in (not sparse) so it reads
	// naturally in a spreadsheet.
	cycleRows := [][]interface{}{cycleHeaders}

	for _, c := range crops {
		for _, d := range c.Days {
			cycleRows = append(cycleRows, []interface{}{
				c.Name,
				d.Day,
				d.Stage,
				d.Tasks,
			})
		}
	}

	// ── Clear existing data from both tabs ──────────────────────────────
	//
	// We clear everything (including headers) and rewrite from scratch.
	// This is simpler than trying to figure out which rows changed.
	_, err := sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, cropsTab, &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Crops tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, cycleTab, &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Cycle tab: %w", err)
	}

	time.Sleep(apiPause)

	// ── Write fresh data to both tabs ───────────────────────────────────

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, cropsTab+"!A1", &sheets.ValueRange{Values: cropsRows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Crops tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, cycleTab+"!A1", &sheets.ValueRange{Values: cycleRows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Cycle tab: %w", err)
	}

	return nil
}

// ─── Fire-and-forget sync helper ────────────────────────────────────────────

// SyncLocalToSheet reads the local crops.csv and pushes its contents to
// the configured Google Sheet. This is called after local modifications
// (adjust stages, promote trial) to keep the Sheet in sync.
//
// It is "fire-and-forget" — if Sheets is not enabled, or the push fails
// (e.g. no internet), it prints a warning and returns without error.
// The local CSV is always the immediate write target; this is a best-effort
// mirror to keep the Sheet up to date.
func SyncLocalToSheet(ctx context.Context) {
	// Check if Sheets integration is enabled.
	cfg, err := config.Load()
	if err != nil || !cfg.SheetsEnabled || cfg.SheetID == "" {
		return // Sheets not configured — nothing to do.
	}

	// Load the local crops.csv.
	cropsPath, err := crop.CropsFilePath()
	if err != nil {
		fmt.Println("  ⚠ Could not find crops.csv path — Sheet not updated.")
		return
	}

	source := crop.CSVSource{Path: cropsPath}
	crops, err := source.LoadCrops()
	if err != nil {
		fmt.Println("  ⚠ Could not read local crops.csv — Sheet not updated.")
		return
	}

	// Create a Sheets client and push.
	sc, err := NewSheetsClient(ctx, cfg.SheetID)
	if err != nil {
		fmt.Println("  ⚠ Could not connect to Google Sheets — Sheet not updated.")
		return
	}

	if err := sc.PushCrops(crops); err != nil {
		fmt.Printf("  ⚠ Could not push to Google Sheet: %v\n", err)
		fmt.Println("  Your local crops.csv is up to date. Run \"greenies sync\" later to update the Sheet.")
		return
	}

	fmt.Println("  ✓ Google Sheet updated.")
}

// ─── Cell parsing helpers ───────────────────────────────────────────────────
//
// Google Sheets API returns cell values as interface{} (could be string,
// float64, bool, or nil). These helpers safely extract the value we need.

// cellString returns the cell at the given index as a string, or "" if the
// index is out of range or the cell is nil.
func cellString(row []interface{}, idx int) string {
	if idx >= len(row) || row[idx] == nil {
		return ""
	}
	return fmt.Sprintf("%v", row[idx])
}

// parseIntCell reads a cell as an integer. Returns 0 for empty/unparseable cells.
func parseIntCell(row []interface{}, idx int) int {
	s := cellString(row, idx)
	if s == "" {
		return 0
	}
	// Google Sheets often returns numbers as floats (e.g. "150" comes back
	// as the float 150.0). Parse as float first, then convert to int.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f)
}

// parseFloatCell reads a cell as a decimal number. Returns 0 for empty cells.
func parseFloatCell(row []interface{}, idx int) float64 {
	s := cellString(row, idx)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// parseBoolCell reads a cell as a boolean. Accepts "TRUE", "true", "yes" —
// anything else (including empty) is false.
func parseBoolCell(row []interface{}, idx int) bool {
	s := strings.ToLower(strings.TrimSpace(cellString(row, idx)))
	return s == "true" || s == "yes"
}

// isInsufficientScopeError checks if a Google API error is about missing
// OAuth scopes. This happens when a user's saved token was created before
// the Sheets scope was added — they need to delete token.json and re-login.
func isInsufficientScopeError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "insufficient") ||
		strings.Contains(msg, "PERMISSION_DENIED") ||
		strings.Contains(msg, "403")
}
