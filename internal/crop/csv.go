// This file implements the CropSource interface by reading crop data from a
// local CSV file. It is the only part of the program that knows anything
// about the CSV format — all other code works with Crop and CropDay values
// and never needs to care where they came from.
package crop

import (
	"encoding/csv" // Go's built-in CSV parser — no external library needed
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CropsFilePath returns the full path to the crops CSV file.
// It lives in the same ~/.greenies/ folder as the tasks file, so everything
// the program needs is in one predictable place.
func CropsFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find your home directory: %w", err)
	}
	return filepath.Join(home, ".greenies", "crops.csv"), nil
}

// CSVSource is a CropSource that reads crop data from a local CSV file.
// The Path field is the full path to the file on disk.
type CSVSource struct {
	Path string
}

// LoadCrops reads the CSV file and returns all crop varieties found in it.
//
// The CSV uses a "sparse" format: only the first row of each crop block has
// the crop name and parameters. Subsequent rows for the same crop leave those
// cells empty and only fill in the day number, stage, and tasks.
//
// This function stitches those sparse rows back together into complete Crop
// values, each with a full list of daily tasks attached.
func (s CSVSource) LoadCrops() ([]Crop, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("could not open crops file at %s\n"+
			"Tip: copy crops.csv from your project folder to ~/.greenies/crops.csv", s.Path)
	}
	defer f.Close()

	reader := csv.NewReader(f)

	// Allow rows to have fewer fields than the header — this handles the
	// sparse rows where trailing empty cells may be omitted by spreadsheet apps.
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("could not read crops file: %w", err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("crops file appears to be empty (no crop rows found)")
	}

	// Build a lookup map from column name → column index.
	// This means we can refer to cells by name ("day", "stage", etc.) rather
	// than by position — so reordering columns in the spreadsheet won't break anything.
	header := rows[0]
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[strings.TrimSpace(name)] = i
	}

	// get safely retrieves a cell value by column name from a row.
	// Returns an empty string if the column doesn't exist or the row is too short.
	get := func(row []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	var crops []Crop
	var current *Crop // points to the crop we are currently building up

	for lineNum, row := range rows[1:] { // skip the header row; lineNum is 0-based
		name := get(row, "name")

		if name != "" {
			// A non-empty name signals the start of a new crop block.
			// Before moving on, set the previous crop's CycleDays from its
			// last day row — this is more reliable than asking the grower to
			// type it manually, because it can never get out of sync.
			if current != nil {
				if len(current.Days) > 0 {
					current.CycleDays = current.Days[len(current.Days)-1].Day
				}
				crops = append(crops, *current)
			}

			c, err := parseCropParams(row, get)
			if err != nil {
				return nil, fmt.Errorf("line %d (crop %q): %w", lineNum+2, name, err)
			}
			current = &c
		}

		if current == nil {
			// A day row appeared before any crop name row — skip it.
			continue
		}

		// Parse the day entry and attach it to the crop we are building.
		day, err := parseCropDay(row, get)
		if err != nil {
			return nil, fmt.Errorf("line %d (crop %q, day row): %w", lineNum+2, current.Name, err)
		}
		current.Days = append(current.Days, day)
	}

	// The loop only appends a crop when a new name is found, so the very last
	// crop in the file never gets appended inside the loop — do it here.
	// Also set its CycleDays from the last day row, same as above.
	if current != nil {
		if len(current.Days) > 0 {
			current.CycleDays = current.Days[len(current.Days)-1].Day
		}
		crops = append(crops, *current)
	}

	if len(crops) == 0 {
		return nil, fmt.Errorf("no crops found in %s — is the file empty?", s.Path)
	}

	return crops, nil
}

// parseCropParams reads the crop-level parameters from the first row of a
// crop block and returns a Crop with those values populated (but no Days yet).
func parseCropParams(row []string, get func([]string, string) string) (Crop, error) {
	// parseInt reads a named column as a whole number.
	// An empty cell is treated as zero — callers can decide if that is valid.
	parseInt := func(colName string) (int, error) {
		v := get(row, colName)
		if v == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("column %q: %q is not a whole number", colName, v)
		}
		return n, nil
	}

	// parseFloat reads a named column as a decimal number.
	// An empty cell defaults to 1.0 (the standard medium amount per tray).
	parseFloat := func(colName string) (float64, error) {
		v := get(row, colName)
		if v == "" {
			return 1.0, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("column %q: %q is not a number", colName, v)
		}
		return f, nil
	}

	// parseBool reads a named column as true/false.
	// Accepts "true", "TRUE", "yes", "YES" — anything else is false.
	parseBool := func(colName string) bool {
		v := strings.ToLower(get(row, colName))
		return v == "true" || v == "yes"
	}

	soakHours, err := parseInt("soak_hours")
	if err != nil {
		return Crop{}, err
	}
	seedGrams, err := parseInt("seed_grams")
	if err != nil {
		return Crop{}, err
	}
	mediumLitres, err := parseFloat("medium_litres")
	if err != nil {
		return Crop{}, err
	}
	saturateMl, err := parseInt("saturate_ml")
	if err != nil {
		return Crop{}, err
	}
	darkDays, err := parseInt("dark_days")
	if err != nil {
		return Crop{}, err
	}
	lightDays, err := parseInt("light_days")
	if err != nil {
		return Crop{}, err
	}
	yieldGrams, err := parseInt("yield_grams")
	if err != nil {
		return Crop{}, err
	}

	// ── Business / costing fields ────────────────────────────────────────
	seedCost, err := parseFloat("seed_cost")
	if err != nil {
		return Crop{}, err
	}
	// parseFloat defaults empty cells to 1.0 (for medium), but seed_cost
	// should default to 0 (meaning "not set yet"). Override here.
	if get(row, "seed_cost") == "" {
		seedCost = 0
	}

	seedPurchaseWeight, err := parseFloat("seed_purchase_weight")
	if err != nil {
		return Crop{}, err
	}
	// parseFloat defaults empty cells to 1.0 (for medium), but seed purchase
	// weight should default to 0 (meaning "not set yet"). Override here.
	if get(row, "seed_purchase_weight") == "" {
		seedPurchaseWeight = 0
	}

	unitWeight, err := parseFloat("unit_weight")
	if err != nil {
		return Crop{}, err
	}
	// Default to 100 g per unit if not specified — a common clamshell size.
	if unitWeight == 0 {
		unitWeight = 100
	}

	unitSellPrice, err := parseFloat("unit_sell_price")
	if err != nil {
		return Crop{}, err
	}
	if get(row, "unit_sell_price") == "" {
		unitSellPrice = 0
	}

	return Crop{
		Name: get(row, "name"),
		// CycleDays is not set here — it is derived from the last day row
		// after all day entries for this crop have been loaded. See LoadCrops.
		OvernightSoak: parseBool("overnight_soak"),
		SoakHours:     soakHours,
		SeedGrams:     seedGrams,
		MediumLitres:  mediumLitres,
		SaturateMl:    saturateMl,
		DarkDays:      darkDays,
		LightDays:     lightDays,
		YieldGrams:    yieldGrams,

		// Business fields — zero means "not configured yet".
		SeedCost:           seedCost,
		SeedPurchaseWeight: seedPurchaseWeight,
		UnitWeight:         unitWeight,
		UnitSellPrice:      unitSellPrice,
	}, nil
}

// parseCropDay reads the day number, stage, and tasks from a single CSV row.
func parseCropDay(row []string, get func([]string, string) string) (CropDay, error) {
	dayStr := get(row, "day")
	if dayStr == "" {
		return CropDay{}, fmt.Errorf("column \"day\" is empty")
	}
	day, err := strconv.Atoi(dayStr)
	if err != nil {
		return CropDay{}, fmt.Errorf("column \"day\": %q is not a whole number", dayStr)
	}

	return CropDay{
		Day:   day,
		Stage: get(row, "stage"),
		Tasks: get(row, "tasks"),
	}, nil
}

// ─── Crop template modification ───────────────────────────────────────────────

// ModifyCropDays adds or removes day rows from a named stage within a crop's
// block in crops.csv.
//
// stage must be "dark" or "light". delta is signed: positive adds rows,
// negative removes them. The minimum remaining days per stage is always 1 —
// you cannot reduce a stage to zero days.
//
// When adding days, the new row's task list is copied from the last existing
// row of that stage (e.g. the dark day just before move-to-light, or the
// light day just before harvest) with "adjusted - be mindful" appended as a
// reminder to review the file in Google Sheets. This gives the grower a
// sensible starting point without requiring them to edit the CSV immediately.
//
// When removing days, the last day row(s) of the stage are deleted.
//
// In both cases, all subsequent day numbers in the crop block are renumbered
// to stay consecutive, and the dark_days or light_days count in the crop's
// header row is updated to match.
func ModifyCropDays(cropsPath, cropName, stage string, delta int) error {
	if delta == 0 {
		return nil
	}

	// ── Read the raw file ─────────────────────────────────────────────────
	f, err := os.Open(cropsPath)
	if err != nil {
		return fmt.Errorf("could not open crops.csv: %w", err)
	}
	r := csv.NewReader(f)
	// Allow rows with fewer fields than the header — spreadsheet apps sometimes
	// strip trailing empty cells when exporting CSV.
	r.FieldsPerRecord = -1
	rawRows, err := r.ReadAll()
	f.Close()
	if err != nil {
		return fmt.Errorf("could not read crops.csv: %w", err)
	}
	if len(rawRows) < 2 {
		return fmt.Errorf("crops.csv appears to be empty")
	}

	// ── Build a column-index map from the header row ──────────────────────
	//
	// Looking up column positions by name (rather than hardcoded numbers)
	// keeps the code correct even if the grower reorders columns in Sheets.
	header := rawRows[0]
	numCols := len(header)
	colIdx := make(map[string]int, numCols)
	for i, name := range header {
		colIdx[strings.TrimSpace(name)] = i
	}

	// getCol returns the index of a required column, or a clear error.
	getCol := func(name string) (int, error) {
		i, ok := colIdx[name]
		if !ok {
			return 0, fmt.Errorf("required column %q not found in crops.csv header", name)
		}
		return i, nil
	}

	nameCol, err := getCol("name")
	if err != nil {
		return err
	}
	dayCol, err := getCol("day")
	if err != nil {
		return err
	}
	stageCol, err := getCol("stage")
	if err != nil {
		return err
	}
	tasksCol, err := getCol("tasks")
	if err != nil {
		return err
	}
	// The count column is dark_days when adjusting the dark stage,
	// light_days when adjusting the light stage.
	countColName := "dark_days"
	if stage == "light" {
		countColName = "light_days"
	}
	countCol, err := getCol(countColName)
	if err != nil {
		return err
	}

	// ── Pad all rows to numCols ───────────────────────────────────────────
	//
	// We rebuild rows as a uniform grid so csv.Writer always produces
	// well-formed output, regardless of how many trailing cells the original
	// spreadsheet export included.
	rows := make([][]string, len(rawRows))
	for i, row := range rawRows {
		padded := make([]string, numCols)
		copy(padded, row)
		rows[i] = padded
	}

	// ── Find the crop block ───────────────────────────────────────────────
	//
	// The block starts at the row whose name cell matches cropName and ends
	// where the next crop's name row appears (or at the end of the file).
	cropHeaderIdx := -1
	cropBlockEnd := len(rows) // exclusive upper bound for this crop's rows

	for i := 1; i < len(rows); i++ {
		rowName := strings.TrimSpace(rows[i][nameCol])
		if strings.EqualFold(rowName, cropName) {
			cropHeaderIdx = i
		} else if cropHeaderIdx != -1 && rowName != "" {
			// A new crop's header row begins here — our block ends just before it.
			cropBlockEnd = i
			break
		}
	}

	if cropHeaderIdx == -1 {
		return fmt.Errorf("crop %q not found in crops.csv", cropName)
	}

	// ── Collect indices of all rows in the target stage ───────────────────
	var stageRowIdxs []int
	for i := cropHeaderIdx; i < cropBlockEnd; i++ {
		if strings.EqualFold(strings.TrimSpace(rows[i][stageCol]), stage) {
			stageRowIdxs = append(stageRowIdxs, i)
		}
	}

	if len(stageRowIdxs) == 0 {
		return fmt.Errorf("no %q stage rows found for crop %q in crops.csv", stage, cropName)
	}

	// Never reduce a stage below 1 day.
	if delta < 0 && len(stageRowIdxs)+delta < 1 {
		return fmt.Errorf("cannot remove %d %s day(s) — crop %q only has %d",
			-delta, stage, cropName, len(stageRowIdxs))
	}

	// ── Apply the modification ────────────────────────────────────────────

	if delta > 0 {
		// Adding days: insert new rows immediately after the last stage row.

		lastStageIdx := stageRowIdxs[len(stageRowIdxs)-1]

		// Default task list for the new row: copy from the last existing stage
		// day (e.g. the dark day just before move-to-light, or the light day
		// just before harvest). This is the closest thing to a sensible default
		// without knowing the crop's real biology. The grower should review and
		// adjust in Google Sheets.
		baseTasks := strings.TrimSpace(rows[lastStageIdx][tasksCol])
		var newTaskStr string
		if baseTasks != "" {
			newTaskStr = baseTasks + ", adjusted - be mindful"
		} else {
			newTaskStr = "adjusted - be mindful"
		}

		lastDayNum, err := strconv.Atoi(strings.TrimSpace(rows[lastStageIdx][dayCol]))
		if err != nil {
			return fmt.Errorf("bad day number on row %d of crops.csv: %w", lastStageIdx+1, err)
		}

		// Build the rows to insert — one new row per delta.
		newRows := make([][]string, delta)
		for i := range newRows {
			r := make([]string, numCols)
			r[dayCol] = strconv.Itoa(lastDayNum + i + 1)
			r[stageCol] = stage
			r[tasksCol] = newTaskStr
			newRows[i] = r
		}

		// Insert the new rows into the main slice.
		rows = insertCSVRows(rows, lastStageIdx+1, newRows)
		cropBlockEnd += delta

		// Renumber every day row that comes after the inserted block.
		// Adding delta rows means all subsequent day numbers go up by delta.
		for i := lastStageIdx + 1 + delta; i < cropBlockEnd; i++ {
			dayStr := strings.TrimSpace(rows[i][dayCol])
			if dayStr == "" {
				continue
			}
			n, err := strconv.Atoi(dayStr)
			if err != nil {
				continue
			}
			rows[i][dayCol] = strconv.Itoa(n + delta)
		}

	} else {
		// Removing days: delete the last |delta| rows of the target stage.

		removeCount := -delta
		toRemove := stageRowIdxs[len(stageRowIdxs)-removeCount:]

		// Remove in reverse order (highest index first) so that earlier
		// indices remain valid as each removal shifts the slice.
		for j := len(toRemove) - 1; j >= 0; j-- {
			idx := toRemove[j]
			rows = append(rows[:idx], rows[idx+1:]...)
			cropBlockEnd--
		}

		// Renumber every surviving day row that follows the removed block.
		// delta is negative here, so adding it decrements the day numbers.
		renumberFrom := toRemove[0]
		for i := renumberFrom; i < cropBlockEnd; i++ {
			dayStr := strings.TrimSpace(rows[i][dayCol])
			if dayStr == "" {
				continue
			}
			n, err := strconv.Atoi(dayStr)
			if err != nil {
				continue
			}
			rows[i][dayCol] = strconv.Itoa(n + delta)
		}
	}

	// ── Update the day-count column in the crop's header row ─────────────
	//
	// cropHeaderIdx has not moved — it is above the insertion/removal point.
	existingCount, err := strconv.Atoi(strings.TrimSpace(rows[cropHeaderIdx][countCol]))
	if err != nil {
		return fmt.Errorf("bad %s value in %q header row: %w", countColName, cropName, err)
	}
	rows[cropHeaderIdx][countCol] = strconv.Itoa(existingCount + delta)

	// ── Write the modified rows back to disk ──────────────────────────────
	out, err := os.Create(cropsPath)
	if err != nil {
		return fmt.Errorf("could not overwrite crops.csv: %w", err)
	}
	defer out.Close()

	w := csv.NewWriter(out)
	if err := w.WriteAll(rows); err != nil {
		return fmt.Errorf("error writing crops.csv: %w", err)
	}
	w.Flush()
	return w.Error()
}

// WriteCrops writes a complete list of crops to a CSV file in the standard
// sparse format — the same format that LoadCrops expects.
//
// This is used to update the local crops.csv cache after pulling fresh data
// from Google Sheets. It completely replaces the file contents.
//
// The "sparse format" means:
//   - The first row of each crop block has all columns filled in (name,
//     parameters, day, stage, tasks).
//   - Subsequent day rows for the same crop only have day, stage, and tasks
//     filled in — the name and parameter columns are left empty.
//
// This round-trips cleanly: WriteCrops(path, crops) followed by
// CSVSource{Path: path}.LoadCrops() returns the same data.
func WriteCrops(path string, crops []Crop) error {
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("could not create crops file: %w", err)
	}
	defer out.Close()

	w := csv.NewWriter(out)

	// Write the header row — same column order as the original crops.csv.
	// New columns can be added anywhere in this list — the name-based
	// lookup map below ensures values always land in the right column.
	header := []string{
		"name", "day", "stage", "tasks",
		"overnight_soak", "soak_hours", "seed_grams",
		"medium_litres", "saturate_ml", "dark_days", "light_days", "yield_grams",
		"seed_cost", "seed_purchase_weight", "unit_weight", "unit_sell_price",
	}
	if err := w.Write(header); err != nil {
		return fmt.Errorf("could not write header: %w", err)
	}

	// Build a lookup map from column name → position in the header.
	// This way we refer to columns by name ("seed_grams") instead of by
	// number (6). If we ever add, remove, or reorder columns, we only
	// need to update the header list above — the rest adjusts automatically.
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}

	for _, c := range crops {
		for i, d := range c.Days {
			row := make([]string, len(header))

			// Day, stage, and tasks are always filled in.
			row[col["day"]] = strconv.Itoa(d.Day)
			row[col["stage"]] = d.Stage
			row[col["tasks"]] = d.Tasks

			if i == 0 {
				// First row of the crop block: fill in name + all parameters.
				row[col["name"]] = c.Name

				// Format overnight_soak as TRUE/FALSE (matches Google Sheets).
				if c.OvernightSoak {
					row[col["overnight_soak"]] = "TRUE"
				} else {
					row[col["overnight_soak"]] = "FALSE"
				}

				row[col["soak_hours"]] = strconv.Itoa(c.SoakHours)
				row[col["seed_grams"]] = strconv.Itoa(c.SeedGrams)

				row[col["medium_litres"]] = FormatFloat(c.MediumLitres)
				row[col["saturate_ml"]] = strconv.Itoa(c.SaturateMl)

				row[col["dark_days"]] = strconv.Itoa(c.DarkDays)
				row[col["light_days"]] = strconv.Itoa(c.LightDays)
				row[col["yield_grams"]] = strconv.Itoa(c.YieldGrams)

				// Business / costing fields.
				// Format money as clean decimals: "15" not "15.00", "4.5" stays "4.5".
				row[col["seed_cost"]] = FormatFloat(c.SeedCost)
				row[col["seed_purchase_weight"]] = FormatFloat(c.SeedPurchaseWeight)
				row[col["unit_weight"]] = FormatFloat(c.UnitWeight)
				row[col["unit_sell_price"]] = FormatFloat(c.UnitSellPrice)
			}

			// Subsequent rows: name and parameter columns stay empty (sparse).
			if err := w.Write(row); err != nil {
				return fmt.Errorf("could not write row for %s day %d: %w", c.Name, d.Day, err)
			}
		}
	}

	w.Flush()
	return w.Error()
}

// AppendCrop adds a single new crop to the end of the local crops.csv file.
//
// It loads all existing crops, appends the new one, and writes the whole file
// back out in the standard sparse CSV format. This is the safe way to add a
// crop — it preserves everything already in the file and guarantees the result
// is a valid, round-trippable CSV.
//
// Returns an error if the file cannot be read or written, or if a crop with
// the same name already exists (case-insensitive).
func AppendCrop(newCrop Crop) error {
	path, err := CropsFilePath()
	if err != nil {
		return err
	}

	// Load whatever is already in the file. If the file does not exist yet,
	// start with an empty list — this handles first-time setup gracefully.
	var existing []Crop
	source := CSVSource{Path: path}
	loaded, loadErr := source.LoadCrops()
	if loadErr == nil {
		existing = loaded
	}

	// Check for duplicate names — two crops with the same name would confuse
	// the scheduler (it wouldn't know which one to use).
	for _, c := range existing {
		if strings.EqualFold(c.Name, newCrop.Name) {
			return fmt.Errorf("a crop named %q already exists in your library", c.Name)
		}
	}

	existing = append(existing, newCrop)
	return WriteCrops(path, existing)
}

// ReplaceCrop updates an existing crop in crops.csv by replacing it with a
// new version. The crop to replace is identified by originalName (case-
// insensitive). This lets the grower change anything about a crop — even its
// name — without leaving orphan data behind.
//
// Returns an error if the file can't be read/written or if no crop with
// originalName exists. If the crop is being renamed (newCrop.Name differs
// from originalName), it also checks that the new name isn't already taken.
func ReplaceCrop(originalName string, newCrop Crop) error {
	path, err := CropsFilePath()
	if err != nil {
		return err
	}

	// Load all existing crops from the file.
	source := CSVSource{Path: path}
	existing, err := source.LoadCrops()
	if err != nil {
		return fmt.Errorf("could not load crop library: %w", err)
	}

	// Find the crop we are replacing. Keep track of its position in the
	// list so the replacement ends up in the same spot (not moved to the end).
	found := -1
	for i, c := range existing {
		if strings.EqualFold(c.Name, originalName) {
			found = i
			break
		}
	}
	if found == -1 {
		return fmt.Errorf("crop %q not found in your library", originalName)
	}

	// If the crop is being renamed, make sure the new name isn't already
	// taken by a different crop.
	if !strings.EqualFold(originalName, newCrop.Name) {
		for _, c := range existing {
			if strings.EqualFold(c.Name, newCrop.Name) {
				return fmt.Errorf("a crop named %q already exists in your library", newCrop.Name)
			}
		}
	}

	// Swap in the new version at the same position.
	existing[found] = newCrop

	return WriteCrops(path, existing)
}

// FormatFloat formats a float64 for CSV output. Whole numbers are written
// without a decimal point ("15" not "15.000000"), while fractional values
// keep their natural precision ("4.5", "1.25"). This keeps the CSV clean
// and readable in a spreadsheet.
//
// Exported so the supply package (and any future CSV writer) can reuse the
// same formatting logic without duplicating code.
func FormatFloat(f float64) string {
	if f == float64(int(f)) {
		return strconv.Itoa(int(f))
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// insertCSVRows inserts a slice of new rows into rows at position idx,
// returning the extended slice. All rows from idx onwards are shifted right
// to make room. Uses a fresh slice to avoid overlapping-copy issues.
func insertCSVRows(rows [][]string, idx int, newRows [][]string) [][]string {
	result := make([][]string, 0, len(rows)+len(newRows))
	result = append(result, rows[:idx]...)
	result = append(result, newRows...)
	result = append(result, rows[idx:]...)
	return result
}
