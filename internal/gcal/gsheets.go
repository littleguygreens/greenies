// This file handles all communication with the Google Sheets API.
//
// It lets the program create, read, and write a Google Spreadsheet that
// mirrors the farm's local data files:
//
//   "Crops" — one row per crop variety with its parameters (seed weight,
//   soak hours, dark days, light days, etc.). Two-way sync: edits in the
//   Sheet are pulled down; local changes are pushed up.
//
//   "Cycle" — the day-by-day schedule for every crop. Two-way with Crops.
//
//   "Farm" — the physical farm layout (environments, slot capacities, and
//   inventory counts). Two-way sync, same as Crops.
//
//   "Trials" — trial run data (push-only).
//
//   "Schedule Tasks" — every calendar task from tasks.json (push-only).
//
//   "Schedule Cycles" — every planned crop cycle from cycles.json (push-only).
//
//   "Harvests" — the harvest log from harvests.json (push-only).
//
// The Google Sheet is the "source of truth" for crops and farm layout —
// when you edit them in Google Sheets, running "greenies sync" pulls those
// changes down to the local CSV. When the program modifies data locally
// (e.g. adjusting stages or promoting a trial), it pushes changes back up.
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
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
	"github.com/littleguygreens/greenies/internal/trial"
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
// Push and pull functions look up columns by name, so reordering is safe.
var cropsHeaders = []interface{}{
	"name", "overnight_soak", "soak_hours", "seed_grams",
	"dirt_litres", "dark_days", "light_days", "yield_grams",
}

// cycleHeaders are the column headings for the Cycle tab.
var cycleHeaders = []interface{}{
	"name", "day", "stage", "tasks",
}

// farmTab is the name of the spreadsheet tab that holds the physical farm
// layout — environments, slot capacities, and inventory counts.
const farmTab = "Farm"

// farmHeaders are the column headings for the Farm tab.
// Matches the columns in ~/.greenies/farm.csv exactly.
var farmHeaders = []interface{}{
	"name", "type", "capacity",
}

// trialsTab is the name of the spreadsheet tab that holds trial run data.
// This tab is push-only — the program writes to it, but never reads from it.
const trialsTab = "Trials"

// trialsHeaders are the column headings for the Trials tab.
// One row per trial-day, with trial metadata on the first row of each block
// (same layout as trials.csv, but not sparse — every row has the crop name).
var trialsHeaders = []interface{}{
	"name", "trial_variable", "status", "sow_date",
	"day", "stage", "tasks",
	"overnight_soak", "soak_hours", "seed_grams", "dirt_litres",
	"dark_days", "light_days", "yield_grams",
}

// scheduleTab is the name of the spreadsheet tab that holds every task from
// the calendar (tasks.json). Push-only — the program writes here so the
// grower can view their full schedule on their phone, but edits in the
// Sheet are not pulled back. Named "Schedule Tasks" to distinguish it from
// the "Schedule Cycles" tab.
const scheduleTab = "Schedule Tasks"

// scheduleHeaders are the column headings for the Schedule Tasks tab.
// One row per calendar task: date, title, notes, and the cycle ID that
// groups tasks from the same planning run.
var scheduleHeaders = []interface{}{
	"date", "title", "notes", "cycle_id",
}

// batchesTab is the name of the spreadsheet tab that holds planned crop
// cycle records (cycles.json). Push-only — view on your phone, edit
// through the CLI/GUI.
//
// Called "Schedule Cycles" rather than "Cycles" because the "Cycle" tab
// already exists (it holds the day-by-day crop schedule template). Each
// row here represents one run of "greenies plan" — one planned cycle.
const batchesTab = "Schedule Cycles"

// batchesHeaders are the column headings for the Schedule Cycles tab.
var batchesHeaders = []interface{}{
	"crop", "trays", "sow_date", "harvest_date",
	"move_to_light_date", "lit_environment", "expected_grams",
}

// harvestsTab is the name of the spreadsheet tab that holds the harvest
// log (harvests.json). Push-only — view your harvest history on any device.
const harvestsTab = "Harvests"

// harvestsHeaders are the column headings for the Harvests tab.
var harvestsHeaders = []interface{}{
	"crop", "harvest_date",
	"expected_trays", "actual_trays",
	"expected_grams", "actual_grams",
	"notes",
}

// sheetRange formats a tab name (and optional cell suffix like "!A1") into a
// range string that the Google Sheets API can parse. Tab names that contain
// spaces must be wrapped in single quotes — e.g. 'Schedule Tasks' instead of
// Schedule Tasks — otherwise the API returns a "Unable to parse range" error.
// Tab names without spaces are returned unchanged.
func sheetRange(tab string, suffix ...string) string {
	name := tab
	if strings.Contains(tab, " ") {
		name = "'" + tab + "'"
	}
	if len(suffix) > 0 {
		return name + suffix[0]
	}
	return name
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

// ensureTab checks that a tab with the given name exists on the spreadsheet.
// If it doesn't, it creates the tab automatically. This handles the case where
// the user's sheet was created before newer tabs (like Schedule Tasks) were
// added — instead of crashing with "Unable to parse range", the program
// quietly adds the missing tab and carries on.
func (sc *SheetsClient) ensureTab(tabName string) error {
	// Fetch the spreadsheet metadata to see which tabs already exist.
	spreadsheet, err := sc.service.Spreadsheets.Get(sc.sheetID).Do()
	if err != nil {
		return fmt.Errorf("could not read spreadsheet metadata: %w", err)
	}

	// Check if the tab already exists — if so, nothing to do.
	for _, s := range spreadsheet.Sheets {
		if s.Properties.Title == tabName {
			return nil
		}
	}

	// Tab doesn't exist — create it.
	_, err = sc.service.Spreadsheets.BatchUpdate(sc.sheetID, &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{AddSheet: &sheets.AddSheetRequest{
				Properties: &sheets.SheetProperties{Title: tabName},
			}},
		},
	}).Do()
	if err != nil {
		return fmt.Errorf("could not create tab %q: %w", tabName, err)
	}

	time.Sleep(apiPause)
	return nil
}

// ─── Sheet creation ─────────────────────────────────────────────────────────

// CreateSheet creates a brand-new Google Spreadsheet with all seven tabs
// and their header rows already in place.
//
// This is called only once — the very first time the user runs "greenies sync"
// and agrees to link Google Sheets. After creation, the spreadsheet ID is
// saved to config.json and reused on every future sync.
//
// Returns the new spreadsheet's ID (the string needed to access it via the API).
func CreateSheet(ctx context.Context) (string, error) {
	// Get the shared OAuth HTTP client.
	client, err := AuthorizeClient(ctx)
	if err != nil {
		return "", fmt.Errorf("Google authorisation failed: %w", err)
	}

	svc, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return "", fmt.Errorf("could not create Google Sheets service: %w", err)
	}

	// Define the spreadsheet structure: seven tabs with specific names.
	// Google always creates a default "Sheet1" tab, but by specifying our
	// own tabs here it only creates the ones we ask for.
	spreadsheet := &sheets.Spreadsheet{
		Properties: &sheets.SpreadsheetProperties{
			Title: "greenies",
		},
		Sheets: []*sheets.Sheet{
			{Properties: &sheets.SheetProperties{Title: cropsTab}},
			{Properties: &sheets.SheetProperties{Title: cycleTab}},
			{Properties: &sheets.SheetProperties{Title: farmTab}},
			{Properties: &sheets.SheetProperties{Title: trialsTab}},
			{Properties: &sheets.SheetProperties{Title: scheduleTab}},
			{Properties: &sheets.SheetProperties{Title: batchesTab}},
			{Properties: &sheets.SheetProperties{Title: harvestsTab}},
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

	time.Sleep(apiPause)

	// Farm tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, farmTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{farmHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Farm header: %w", err)
	}

	time.Sleep(apiPause)

	// Trials tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, trialsTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{trialsHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Trials header: %w", err)
	}

	time.Sleep(apiPause)

	// Schedule tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, scheduleTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{scheduleHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Schedule header: %w", err)
	}

	time.Sleep(apiPause)

	// Batches tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, batchesTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{batchesHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Batches header: %w", err)
	}

	time.Sleep(apiPause)

	// Harvests tab header.
	_, err = svc.Spreadsheets.Values.Update(sheetID, harvestsTab+"!A1", &sheets.ValueRange{
		Values: [][]interface{}{harvestsHeaders},
	}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return sheetID, fmt.Errorf("created sheet but could not write Harvests header: %w", err)
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
	// Read all columns (not a fixed range like A:H) so that newly added
	// parameter columns are automatically picked up without code changes.
	cropsResp, err := sc.service.Spreadsheets.Values.Get(
		sc.sheetID, cropsTab,
	).Do()
	if err != nil {
		return nil, fmt.Errorf("could not read Crops tab: %w", err)
	}

	time.Sleep(apiPause)

	// ── Read the Cycle tab ──────────────────────────────────────────────
	// Read all columns — same approach as the Crops tab above.
	cycleResp, err := sc.service.Spreadsheets.Values.Get(
		sc.sheetID, cycleTab,
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

	// Build a lookup map from the header row so we find columns by name
	// instead of by position number. If someone reorders or adds columns
	// in the Google Sheet, the code still picks up the right values.
	cropsCol := make(map[string]int)
	if len(cropsResp.Values) > 0 {
		for i, cell := range cropsResp.Values[0] {
			cropsCol[strings.TrimSpace(strings.ToLower(fmt.Sprintf("%v", cell)))] = i
		}
	}

	for i, row := range cropsResp.Values {
		if i == 0 {
			continue // skip header row
		}
		if len(row) == 0 {
			continue // blank row
		}

		name := strings.TrimSpace(cellString(row, cropsCol["name"]))
		if name == "" {
			continue
		}

		p := &cropParams{
			OvernightSoak: parseBoolCell(row, cropsCol["overnight_soak"]),
			SoakHours:     parseIntCell(row, cropsCol["soak_hours"]),
			SeedGrams:     parseIntCell(row, cropsCol["seed_grams"]),
			DirtLitres:    parseFloatCell(row, cropsCol["dirt_litres"]),
			DarkDays:      parseIntCell(row, cropsCol["dark_days"]),
			LightDays:     parseIntCell(row, cropsCol["light_days"]),
			YieldGrams:    parseIntCell(row, cropsCol["yield_grams"]),
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

	// Build a header lookup for the Cycle tab — same approach as Crops.
	cycleCol := make(map[string]int)
	if len(cycleResp.Values) > 0 {
		for i, cell := range cycleResp.Values[0] {
			cycleCol[strings.TrimSpace(strings.ToLower(fmt.Sprintf("%v", cell)))] = i
		}
	}

	dayMap := make(map[string][]crop.CropDay)

	for i, row := range cycleResp.Values {
		if i == 0 {
			continue // skip header row
		}
		if len(row) == 0 {
			continue
		}

		name := strings.TrimSpace(cellString(row, cycleCol["name"]))
		if name == "" {
			continue
		}

		dayNum := parseIntCell(row, cycleCol["day"])
		stage := strings.TrimSpace(cellString(row, cycleCol["stage"]))
		tasks := strings.TrimSpace(cellString(row, cycleCol["tasks"]))

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
	//
	// We build a column-name → position map from cropsHeaders so that
	// adding a new parameter column means updating the header list and
	// the row-building code below — the positions are always derived
	// automatically. No counting required.
	cropCol := make(map[string]int, len(cropsHeaders))
	for i, h := range cropsHeaders {
		cropCol[h.(string)] = i
	}

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

		row := make([]interface{}, len(cropsHeaders))
		row[cropCol["name"]] = c.Name
		row[cropCol["overnight_soak"]] = soakStr
		row[cropCol["soak_hours"]] = c.SoakHours
		row[cropCol["seed_grams"]] = c.SeedGrams
		row[cropCol["dirt_litres"]] = dirtStr
		row[cropCol["dark_days"]] = c.DarkDays
		row[cropCol["light_days"]] = c.LightDays
		row[cropCol["yield_grams"]] = c.YieldGrams

		cropsRows = append(cropsRows, row)
	}

	// ── Build the Cycle tab data ────────────────────────────────────────
	//
	// One row per crop-day: name, day number, stage, tasks.
	// Every row has the crop name filled in (not sparse) so it reads
	// naturally in a spreadsheet.
	//
	// Same header-lookup approach as the Crops tab.
	cyCol := make(map[string]int, len(cycleHeaders))
	for i, h := range cycleHeaders {
		cyCol[h.(string)] = i
	}

	cycleRows := [][]interface{}{cycleHeaders}

	for _, c := range crops {
		for _, d := range c.Days {
			row := make([]interface{}, len(cycleHeaders))
			row[cyCol["name"]] = c.Name
			row[cyCol["day"]] = d.Day
			row[cyCol["stage"]] = d.Stage
			row[cyCol["tasks"]] = d.Tasks

			cycleRows = append(cycleRows, row)
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

// ─── Farm layout: pull and push ─────────────────────────────────────────────

// PullFarm reads the "Farm" tab from the Google Sheet and returns the data as
// []farm.Environment — the same type used everywhere else in the program.
//
// If the tab is empty (just headers, no data rows), it returns an empty slice
// and no error — same behaviour as PullCrops for an empty sheet.
func (sc *SheetsClient) PullFarm() ([]farm.Environment, error) {
	// Read all columns so newly added columns are picked up automatically.
	resp, err := sc.service.Spreadsheets.Values.Get(
		sc.sheetID, farmTab,
	).Do()
	if err != nil {
		return nil, fmt.Errorf("could not read Farm tab: %w", err)
	}

	// Build a header lookup so columns are found by name, not position.
	farmCol := make(map[string]int)
	if len(resp.Values) > 0 {
		for i, cell := range resp.Values[0] {
			farmCol[strings.TrimSpace(strings.ToLower(fmt.Sprintf("%v", cell)))] = i
		}
	}

	var envs []farm.Environment

	for i, row := range resp.Values {
		if i == 0 {
			continue // skip header row
		}
		if len(row) == 0 {
			continue // blank row
		}

		name := strings.TrimSpace(cellString(row, farmCol["name"]))
		if name == "" {
			continue
		}

		envType := strings.TrimSpace(cellString(row, farmCol["type"]))
		capacity := parseIntCell(row, farmCol["capacity"])

		envs = append(envs, farm.Environment{
			Name:     name,
			Type:     envType,
			Capacity: capacity,
		})
	}

	return envs, nil
}

// PushFarm writes the given farm environments to the "Farm" tab, completely
// replacing any existing data. Same clear-and-rewrite approach as PushCrops.
func (sc *SheetsClient) PushFarm(envs []farm.Environment) error {
	// Build a column-name → position map so we place values by name,
	// not by counting positions. Same approach as PushCrops.
	fCol := make(map[string]int, len(farmHeaders))
	for i, h := range farmHeaders {
		fCol[h.(string)] = i
	}

	// Build the rows: header + one row per environment.
	rows := [][]interface{}{farmHeaders}

	for _, env := range envs {
		row := make([]interface{}, len(farmHeaders))
		row[fCol["name"]] = env.Name
		row[fCol["type"]] = env.Type
		row[fCol["capacity"]] = env.Capacity

		rows = append(rows, row)
	}

	// Clear the tab and rewrite from scratch.
	_, err := sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, farmTab, &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Farm tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, farmTab+"!A1", &sheets.ValueRange{Values: rows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Farm tab: %w", err)
	}

	return nil
}

// ─── Trials: push-only ─────────────────────────────────────────────────────

// PushTrials writes trial data to the "Trials" tab. This is push-only —
// the program never reads trial data back from the Sheet. The tab exists
// so the grower can view trial progress in Google Sheets from any device.
//
// Only trials with at least one confirmed day are included — a brand-new
// trial with no confirmed parameters has nothing useful to show yet.
func (sc *SheetsClient) PushTrials(records []trial.TrialRecord) error {
	// Build the rows: header + one row per trial-day.
	rows := [][]interface{}{trialsHeaders}

	for _, tr := range records {
		// Skip trials with no confirmed day data — nothing to display.
		if len(tr.ConfirmedDays) == 0 {
			continue
		}

		// Count dark and light days from the confirmed parameters.
		darkDays := 0
		lightDays := 0
		for _, d := range tr.ConfirmedDays {
			switch d.Stage {
			case "dark":
				darkDays++
			case "light":
				lightDays++
			}
		}

		// Format parameter fields. Empty string for zero/unknown values
		// so the Sheet cell stays blank rather than showing a misleading "0".
		soakStr := "FALSE"
		if tr.OvernightSoak {
			soakStr = "TRUE"
		}
		soakHoursStr := ""
		if tr.SoakHours > 0 {
			soakHoursStr = strconv.FormatFloat(tr.SoakHours, 'f', -1, 64)
		}
		seedStr := ""
		if tr.SeedGrams > 0 {
			seedStr = strconv.FormatFloat(tr.SeedGrams, 'f', -1, 64)
		}
		dirtStr := ""
		if tr.DirtLitres > 0 {
			dirtStr = strconv.FormatFloat(tr.DirtLitres, 'f', -1, 64)
		}
		yieldStr := ""
		if tr.ActualYieldGrams > 0 {
			yieldStr = strconv.Itoa(tr.ActualYieldGrams)
		}

		// One row per confirmed day. Every row has the crop name and
		// trial variable filled in (not sparse) so it reads cleanly.
		// Parameters only appear on the first row of each trial block.
		for i, d := range tr.ConfirmedDays {
			row := []interface{}{
				tr.CropName,
				tr.TrialVariable,
				tr.Status,
				tr.SowDate,
				d.Day,
				d.Stage,
				d.Tasks,
			}

			if i == 0 {
				// First row: include all parameters.
				row = append(row,
					soakStr, soakHoursStr, seedStr, dirtStr,
					darkDays, lightDays, yieldStr,
				)
			} else {
				// Subsequent rows: leave parameter columns empty.
				row = append(row,
					"", "", "", "",
					"", "", "",
				)
			}

			rows = append(rows, row)
		}
	}

	// Clear the tab and rewrite from scratch.
	_, err := sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, trialsTab, &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Trials tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, trialsTab+"!A1", &sheets.ValueRange{Values: rows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Trials tab: %w", err)
	}

	return nil
}

// ─── Schedule (tasks.json): push-only ────────────────────────────────────────

// PushSchedule writes every calendar task to the "Schedule" tab. Push-only —
// the program never reads tasks back from the Sheet. The tab exists so the
// grower can view their full schedule on any device.
//
// Tasks are sorted by date (earliest first) so the spreadsheet reads like
// a chronological list. "No tasks today" placeholder tasks are excluded —
// they exist only for slot tracking and would clutter the view.
func (sc *SheetsClient) PushSchedule(tasks []task.Task) error {
	// Make sure the tab exists — it may be missing if the user's sheet
	// was created before this tab was added.
	if err := sc.ensureTab(scheduleTab); err != nil {
		return err
	}

	// Build the rows: header + one row per task.
	rows := [][]interface{}{scheduleHeaders}

	for _, t := range tasks {
		// Skip placeholder tasks — they have no real actions and would
		// just clutter the spreadsheet.
		if t.Notes == task.NoTasksNote {
			continue
		}

		rows = append(rows, []interface{}{
			t.Date,
			t.Title,
			t.Notes,
			t.CycleID,
		})
	}

	// Clear the tab and rewrite from scratch.
	_, err := sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, sheetRange(scheduleTab), &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Schedule tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, sheetRange(scheduleTab, "!A1"), &sheets.ValueRange{Values: rows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Schedule tab: %w", err)
	}

	return nil
}

// ─── Batches (cycles.json): push-only ───────────────────────────────────────

// PushBatches writes every planned crop cycle to the "Batches" tab. Push-only
// — the program never reads cycle records back from the Sheet. The tab exists
// so the grower can see all their planned batches on any device.
func (sc *SheetsClient) PushBatches(cycles []farm.Cycle) error {
	// Make sure the tab exists — it may be missing if the user's sheet
	// was created before this tab was added.
	if err := sc.ensureTab(batchesTab); err != nil {
		return err
	}

	// Build the rows: header + one row per cycle.
	rows := [][]interface{}{batchesHeaders}

	for _, c := range cycles {
		// Format expected grams: empty string for zero so the cell stays
		// blank rather than showing a misleading "0".
		gramsStr := ""
		if c.ExpectedGrams > 0 {
			gramsStr = strconv.Itoa(c.ExpectedGrams)
		}

		rows = append(rows, []interface{}{
			c.CropName,
			c.Trays,
			c.SowDate,
			c.HarvestDate,
			c.MoveToLightDate,
			c.LitEnvironment,
			gramsStr,
		})
	}

	// Clear the tab and rewrite from scratch.
	_, err := sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, sheetRange(batchesTab), &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Batches tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, sheetRange(batchesTab, "!A1"), &sheets.ValueRange{Values: rows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Batches tab: %w", err)
	}

	return nil
}

// ─── Harvests (harvests.json): push-only ────────────────────────────────────

// PushHarvests writes the harvest log to the "Harvests" tab. Push-only —
// the program never reads harvest records back from the Sheet. The tab
// exists so the grower can review their harvest history on any device.
func (sc *SheetsClient) PushHarvests(records []farm.HarvestRecord) error {
	// Make sure the tab exists — it may be missing if the user's sheet
	// was created before this tab was added.
	if err := sc.ensureTab(harvestsTab); err != nil {
		return err
	}

	// Build the rows: header + one row per harvest record.
	rows := [][]interface{}{harvestsHeaders}

	for _, h := range records {
		// Format expected grams: empty string for zero (means "unknown").
		expectedStr := ""
		if h.ExpectedGrams > 0 {
			expectedStr = strconv.Itoa(h.ExpectedGrams)
		}

		rows = append(rows, []interface{}{
			h.CropName,
			h.HarvestDate,
			h.ExpectedTrays,
			h.ActualTrays,
			expectedStr,
			h.ActualGrams,
			h.Notes,
		})
	}

	// Clear the tab and rewrite from scratch.
	_, err := sc.service.Spreadsheets.Values.Clear(
		sc.sheetID, sheetRange(harvestsTab), &sheets.ClearValuesRequest{},
	).Do()
	if err != nil {
		return fmt.Errorf("could not clear Harvests tab: %w", err)
	}

	time.Sleep(apiPause)

	_, err = sc.service.Spreadsheets.Values.Update(
		sc.sheetID, sheetRange(harvestsTab, "!A1"), &sheets.ValueRange{Values: rows},
	).ValueInputOption("RAW").Do()
	if err != nil {
		return fmt.Errorf("could not write Harvests tab: %w", err)
	}

	return nil
}

// ─── Fire-and-forget sync helper ────────────────────────────────────────────

// SyncLocalToSheet reads all local data files and pushes their contents to
// the configured Google Sheet. This is called after local modifications
// (adjust stages, promote trial, plan a batch, log a harvest, etc.) to
// keep the Sheet in sync.
//
// Files pushed:
//   - crops.csv     → Crops + Cycle tabs (two-way source of truth)
//   - farm.csv      → Farm tab (two-way source of truth)
//   - trials.json   → Trials tab (push-only)
//   - tasks.json    → Schedule Tasks tab (push-only)
//   - cycles.json   → Schedule Cycles tab (push-only)
//   - harvests.json → Harvests tab (push-only)
//
// It is "fire-and-forget" — if Sheets is not enabled, or the push fails
// (e.g. no internet), it prints a warning and returns without error.
// The local files are always the immediate write target; this is a
// best-effort mirror to keep the Sheet up to date.
func SyncLocalToSheet(ctx context.Context) {
	// Check if Sheets integration is enabled.
	cfg, err := config.Load()
	if err != nil || !cfg.SheetsEnabled || cfg.SheetID == "" {
		return // Sheets not configured — nothing to do.
	}

	// Create a Sheets client — shared for all three pushes.
	sc, err := NewSheetsClient(ctx, cfg.SheetID)
	if err != nil {
		fmt.Println("  ⚠ Could not connect to Google Sheets — Sheet not updated.")
		return
	}

	// ── Push crops ──────────────────────────────────────────────────────
	cropsPath, err := crop.CropsFilePath()
	if err == nil {
		source := crop.CSVSource{Path: cropsPath}
		if crops, loadErr := source.LoadCrops(); loadErr == nil {
			if pushErr := sc.PushCrops(crops); pushErr != nil {
				fmt.Printf("  ⚠ Could not push crops to Google Sheet: %v\n", pushErr)
			}
			time.Sleep(apiPause)
		}
	}

	// ── Push farm layout ────────────────────────────────────────────────
	if envs, loadErr := farm.LoadConfig(); loadErr == nil {
		if pushErr := sc.PushFarm(envs); pushErr != nil {
			fmt.Printf("  ⚠ Could not push farm layout to Google Sheet: %v\n", pushErr)
		}
		time.Sleep(apiPause)
	}

	// ── Push trials ─────────────────────────────────────────────────────
	if records, loadErr := trial.LoadTrials(); loadErr == nil && len(records) > 0 {
		if pushErr := sc.PushTrials(records); pushErr != nil {
			fmt.Printf("  ⚠ Could not push trials to Google Sheet: %v\n", pushErr)
		}
		time.Sleep(apiPause)
	}

	// ── Push schedule (tasks.json) ──────────────────────────────────────
	if tasks, loadErr := store.Load(); loadErr == nil {
		if pushErr := sc.PushSchedule(tasks); pushErr != nil {
			fmt.Printf("  ⚠ Could not push schedule to Google Sheet: %v\n", pushErr)
		}
		time.Sleep(apiPause)
	}

	// ── Push batches (cycles.json) ──────────────────────────────────────
	if cycles, loadErr := farm.LoadCycles(); loadErr == nil {
		if pushErr := sc.PushBatches(cycles); pushErr != nil {
			fmt.Printf("  ⚠ Could not push batches to Google Sheet: %v\n", pushErr)
		}
		time.Sleep(apiPause)
	}

	// ── Push harvests (harvests.json) ────────────────────────────────────
	if harvests, loadErr := farm.LoadHarvests(); loadErr == nil {
		if pushErr := sc.PushHarvests(harvests); pushErr != nil {
			fmt.Printf("  ⚠ Could not push harvests to Google Sheet: %v\n", pushErr)
		}
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
