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
	// An empty cell defaults to 1.0 (the standard dirt amount per tray).
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
	dirtLitres, err := parseFloat("dirt_litres")
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
	return Crop{
		Name:          get(row, "name"),
		// CycleDays is not set here — it is derived from the last day row
		// after all day entries for this crop have been loaded. See LoadCrops.
		OvernightSoak: parseBool("overnight_soak"),
		SoakHours:     soakHours,
		SeedGrams:     seedGrams,
		DirtLitres:    dirtLitres,
		DarkDays:      darkDays,
		LightDays:     lightDays,
		YieldGrams:    yieldGrams,
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
