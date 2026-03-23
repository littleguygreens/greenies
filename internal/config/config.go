// Package config manages program-wide settings that persist between runs.
//
// Settings are stored in a small JSON file at ~/.greenies/config.json.
// This file is internal — the user never needs to open or edit it by hand.
// The program reads and writes it automatically.
//
// JSON is used here (instead of CSV) because this is a machine-managed file,
// not a human-edited one. The CLAUDE.md rule "no JSON for human-edited files"
// does not apply.
package config

import (
	"encoding/json" // for reading/writing the config file in JSON format
	"fmt"
	"os"            // for file operations (read, write, check existence)
	"path/filepath" // for building file paths that work on any operating system
)

// Config holds program-wide settings. Right now it only stores Google Sheets
// information, but more fields can be added here later without breaking
// anything.
type Config struct {
	// SheetsEnabled is true once the user has linked a Google Sheet for
	// their crop library. When false, the program ignores Sheets entirely
	// and uses the local crops.csv file as before.
	SheetsEnabled bool `json:"sheets_enabled"`

	// SheetID is the unique identifier Google assigns to the spreadsheet.
	// It's the long string of characters you see in a Google Sheets URL:
	//   https://docs.google.com/spreadsheets/d/<THIS PART>/edit
	// Stored here so the program knows which spreadsheet to talk to.
	SheetID string `json:"sheet_id"`

	// Lowercase is a cosmetic preference. When true, the GUI renders all
	// text in lowercase. Some growers prefer this aesthetic — it's purely
	// visual and has no effect on the data or the CLI.
	Lowercase bool `json:"lowercase"`

	// WeekStart controls which day the calendar week begins on.
	// Either "sun" (Sunday, the default) or "mon" (Monday).
	// Used by the swim-lane calendar and the snapshot week view.
	WeekStart string `json:"week_start"`

	// Theme controls the GUI colour scheme. Either "dark" (the default) or
	// "light". The CSS uses this to swap between two colour palettes.
	Theme string `json:"theme"`

	// Units controls whether the program displays measurements in metric
	// (grams, litres — the default) or imperial (ounces, gallons).
	//
	// IMPORTANT: this setting only changes the *labels* shown next to
	// numbers. It does NOT convert any values. When a grower switches
	// from metric to imperial, they need to manually update their crop
	// numbers (seed weight, yield, medium volume, etc.) to match the new
	// unit system. The program shows a warning about this on the
	// settings page.
	//
	// Valid values: "metric" (default) or "imperial".
	Units string `json:"units"`

	// IrrigationMode controls how the program counts bottom tray usage.
	//
	// "flood" (the default) — the lit environment has automated watering
	//   (flood tables, drip lines, etc.), so bottom trays are only needed
	//   during blackout. They are returned to inventory the moment trays
	//   move to light.
	//
	// "tray_pairs" — every grow tray sits in a bottom tray for the entire
	//   cycle, from sow through harvest. Both trays are returned together
	//   on harvest day. This is typical for growers who hand-water on open
	//   shelving or wire racks.
	//
	// Valid values: "flood" (default) or "tray_pairs".
	IrrigationMode string `json:"irrigation_mode"`
}

// IsImperial returns true when the grower has chosen the imperial unit
// system. Use this instead of comparing cfg.Units == "imperial" directly
// — it keeps the check in one place and handles the default (empty string
// = metric) automatically.
func (c Config) IsImperial() bool {
	return c.Units == "imperial"
}

// IsTrayPairs returns true when the grower uses tray pairs for the full
// cycle (bottom trays stay paired from sow to harvest). When false
// (the default "flood" mode), bottom trays are returned at move-to-light.
func (c Config) IsTrayPairs() bool {
	return c.IrrigationMode == "tray_pairs"
}

// WeightLabel returns the small-weight unit label: "g" for metric, "oz"
// for imperial. Used everywhere seed weight and yield are displayed.
func (c Config) WeightLabel() string {
	if c.IsImperial() {
		return "oz"
	}
	return "g"
}

// LargeWeightLabel returns the large-weight unit label: "kg" for metric,
// "lb" for imperial. Used in the seed bag weight dropdown.
func (c Config) LargeWeightLabel() string {
	if c.IsImperial() {
		return "lb"
	}
	return "kg"
}

// VolumeLabel returns the volume unit label: "L" for metric (litres),
// "gal" for imperial (gallons). Used for growing medium amounts.
func (c Config) VolumeLabel() string {
	if c.IsImperial() {
		return "gal"
	}
	return "L"
}

// LargeWeightMultiplier returns how many small units make one large unit.
// Metric: 1 kg = 1000 g. Imperial: 1 lb = 16 oz.
func (c Config) LargeWeightMultiplier() int {
	if c.IsImperial() {
		return 16
	}
	return 1000
}

// Path returns the full file path to the config file (~/.greenies/config.json).
// The ~/.greenies/ directory is the same place where tasks.json, crops.csv,
// and other data files live.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find home directory: %w", err)
	}
	return filepath.Join(home, ".greenies", "config.json"), nil
}

// Load reads the config file from disk and returns its contents.
//
// If the file does not exist yet (first run, or user never enabled Sheets),
// it returns a zero-value Config with SheetsEnabled=false and SheetID="".
// This is not an error — it just means "no settings configured yet."
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist yet — that's fine, return defaults.
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("could not read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config file is corrupted: %w", err)
	}
	return cfg, nil
}

// Save writes the config to disk, creating the file if it doesn't exist.
// The file is written with permissions 0644 (owner can read/write, others
// can only read) — the same permissions used for tasks.json and other data.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}

	// json.MarshalIndent makes the file human-readable (with indentation)
	// even though humans shouldn't need to edit it — it's nice for
	// debugging if something ever goes wrong.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("could not write config file: %w", err)
	}
	return nil
}
